package apidoc

// schemafor.go — SchemaFor: include-aware response-schema recompute, over IR.
//
// SchemaFor MIRRORS THE PLANNER (include.ResolvePlan): the schema it returns
// describes exactly the bytes the engine emits for the given client tree. That
// means the children of every level are the EFFECTIVE set the planner merges —
// node.Defaults() ∪ the client's child keys — not the client keys alone:
//
//   - Defaults() keep their declaration-order position, client-only keys come
//     after, sorted (the same ordering PlanNode.Children gets, which is also
//     the byte order assembleObject emits);
//   - a default edge is expanded regardless of Includable (the engine
//     materializes it either way); a stale default (no matching edge) is
//     silently skipped; the default-only expansion is cycle-guarded by the
//     same target-name chain the planner threads (seeded with the root,
//     extended only when descending a DEFAULT edge — client edges are exempt);
//   - a CLIENT key must name an existing includable edge, or SchemaFor errors
//     (the planner's INVALID_INCLUDE analogue);
//   - every effective child is PRESENT in the response, so its key is
//     promoted to `required` — a graph.Required() edge is in Defaults() by
//     compile-time coupling, so it is always here and always required, in
//     agreement with the static component;
//   - a child whose own effective subtree is non-empty (client sub-includes,
//     or live defaults of its target) is INLINED in place of the $ref, since
//     that level carries a requiredness delta of its own. A leaf child reuses
//     the exact static edge shape (edgeshape.go), so the two arms cannot
//     drift.
//
// The client-tree LIMITS (MaxDepth/MaxNodes) are enforced up front
// (checkTreeLimits), so a tree the planner would reject is rejected here too.
// What SchemaFor does NOT mirror: a COMPUTED key's engine absence: the declared
// schema is spliced in as required, which is the handler's splice contract.
//
// AUXILIARY AGREEMENT: the base object runs the SAME auxiliary inlining
// EmitComponents runs — literally emit.go's inlineAux, over the same reflector
// output and the same node-name set. Without it, SchemaFor would keep $refs to
// nested-struct auxiliaries that EmitComponents inlines away and never emits,
// and every such reference would dangle against the component map.
//
// IMMUTABILITY: IR nodes reached from an edge shape or from a computed edge's
// declared ComputedSchema are SHARED BY POINTER (the same invariant components
// assembly relies on). SchemaFor therefore never writes through a node it did
// not build: promoting a key to required constructs a NEW object node with a
// fresh Prop slice, never flips Required on a Prop it borrowed.

import (
	"fmt"
	"reflect"
	"sort"
	"strings"

	"github.com/qrotux/wireleaf/include"
)

// SchemaFor recomputes root's response-node schema for a client include tree,
// mirroring the planner's defaults-merge (see the file comment). The result
// still carries $refs to sibling node components (and to whatever auxiliaries
// survive inlining), resolvable only against the component map from
// EmitComponents — and every ref it emits is one that map also contains,
// because the base object is inlined by the same rules.
//
// Children are emitted in plan order (defaults first, then client-only keys
// sorted), so a repeated recompute of the same tree serializes byte-identically
// (IR property order is significant).
//
// lim bounds the CLIENT tree exactly as include.ResolvePlan does: a chain of
// client keys deeper than MaxDepth, or more than MaxNodes client keys in
// total, is rejected before any schema work with an INCLUDE_TOO_DEEP error.
// A zero-value lim means include.DefaultLimits. Pass the same Limits you
// pass to the planner so the two agree on which trees are describable.
func SchemaFor(r Reflector, root include.Resource, tree include.IncludeTree, lim include.Limits) (Schema, error) {
	if lim == (include.Limits{}) {
		lim = include.DefaultLimits
	}
	if err := checkTreeLimits(tree, lim); err != nil {
		return Schema{}, err
	}
	// The default-chain is seeded with the root name, as the planner seeds it:
	// a default edge re-entering the root is cut at the first hop.
	n, err := schemaForNode(r, root, tree, map[string]bool{root.Name(): true})
	if err != nil {
		return Schema{}, err
	}
	return Schema{n: n}, nil
}

// checkTreeLimits walks the client tree counting non-arg keys. Depth is the
// number of nested client levels (the planner's clientDepth at entry to a
// level, checked `> MaxDepth`); nodes is the total number of client keys.
// Args (":"-prefixed keys) contribute to neither.
func checkTreeLimits(tree include.IncludeTree, lim include.Limits) error {
	nodes := 0
	var walk func(t include.IncludeTree, depth int) error
	walk = func(t include.IncludeTree, depth int) error {
		if depth > lim.MaxDepth {
			return fmt.Errorf("apidoc: SchemaFor: %s: client include deeper than MaxDepth=%d", include.INCLUDE_TOO_DEEP, lim.MaxDepth)
		}
		for key, sub := range t {
			if strings.HasPrefix(key, ":") {
				continue
			}
			nodes++
			if nodes > lim.MaxNodes {
				return fmt.Errorf("apidoc: SchemaFor: %s: more than MaxNodes=%d client keys", include.INCLUDE_TOO_DEEP, lim.MaxNodes)
			}
			child, _ := sub.(include.IncludeTree)
			if err := walk(child, depth+1); err != nil {
				return err
			}
		}
		return nil
	}
	return walk(tree, 0)
}

// schemaForNode recomputes ONE level: the node's base object plus its
// effective children (defaults ∪ client), each promoted to required.
// seenDefaults is the planner's default-only cycle chain.
func schemaForNode(r Reflector, node include.Resource, clientTree include.IncludeTree, seenDefaults map[string]bool) (*IRNode, error) {
	base, err := baseObjectIR(r, node)
	if err != nil {
		return nil, err
	}
	if base.Kind != KindObject {
		return nil, fmt.Errorf("apidoc: SchemaFor: node %q base schema is a %s, want an object", node.Name(), base.Kind)
	}
	// A shallow clone over a FRESH Prop slice. Cloning (rather than building a
	// bare newObject) is what KEEPS the base node's non-Props facts —
	// AdditionalProperties, Description/Title, Extensions and their extRefs, a
	// nullable Types widening — so the include-aware schema cannot contradict
	// the static component, and Verify still sees every reference. Only
	// out.Props is written below, so sharing the clone's other slices/maps with
	// base is safe.
	out := base.shallowClone()
	out.Props = append([]Prop(nil), base.Props...)

	edges := node.Edges()
	clientKeys := childEdges(clientTree)
	clientChildren := make(map[string]bool, len(clientKeys))
	for _, key := range clientKeys {
		clientChildren[key] = true
	}

	// Effective child order = the planner's: defaults in declaration order
	// (deduped), then client-only keys sorted (childEdges sorts already).
	seen := make(map[string]bool, len(node.Defaults())+len(clientChildren))
	ordered := make([]string, 0, len(node.Defaults())+len(clientChildren))
	for _, k := range node.Defaults() {
		if !seen[k] {
			seen[k] = true
			ordered = append(ordered, k)
		}
	}
	for _, k := range clientKeys {
		if !seen[k] {
			seen[k] = true
			ordered = append(ordered, k)
		}
	}

	for _, key := range ordered {
		edge, ok := edges[key]
		fromClient := clientChildren[key]
		if fromClient && (!ok || !edge.Includable) {
			return nil, fmt.Errorf("apidoc: SchemaFor: node %q has no includable edge %q", node.Name(), key)
		}
		if !ok {
			continue // stale default (no matching edge): the planner skips it too
		}

		childSeen := seenDefaults
		if !edge.Computed {
			if edge.Target == nil {
				return nil, fmt.Errorf("apidoc: SchemaFor: node %q edge %q has a nil Target thunk", node.Name(), key)
			}
			target := edge.Target()
			if target == nil {
				return nil, fmt.Errorf("apidoc: SchemaFor: node %q edge %q Target thunk returned nil", node.Name(), key)
			}
			// Default-only cycle guard: a default edge re-entering a node on
			// the chain is skipped (clean termination). Client edges are
			// exempt and do not extend the chain.
			if !fromClient {
				if seenDefaults[target.Name()] {
					continue
				}
				childSeen = extendSet(seenDefaults, target.Name())
			}
		}

		subtree, _ := clientTree[key].(include.IncludeTree)
		value, err := schemaForEdgeValue(r, node, key, edge, subtree, childSeen)
		if err != nil {
			return nil, err
		}
		setRequiredProp(out, key, value)
	}
	return out, nil
}

// schemaForEdgeValue builds one effective child's value schema. A COMPUTED
// edge is handled FIRST: it carries a nil Target func, so every other branch
// would reject (or panic on) it.
func schemaForEdgeValue(r Reflector, node include.Resource, key string, edge include.Edge, subtree include.IncludeTree, seenDefaults map[string]bool) (*IRNode, error) {
	subKeys := childEdges(subtree)
	if edge.Computed {
		// A computed edge has no target Resource, so it can have no children.
		// The engine's planner rejects such a tree; the doc layer must not
		// quietly accept it and emit a schema the client can never receive.
		if len(subKeys) > 0 {
			return nil, fmt.Errorf(
				"apidoc: SchemaFor: node %q computed edge %q takes no sub-includes, got %v",
				node.Name(), key, subKeys)
		}
		// The declared schema is spliced VERBATIM and by POINTER; nothing here
		// (or downstream) may mutate it.
		n, err := computedSchemaIR(edge)
		if err != nil {
			return nil, fmt.Errorf("apidoc: node %q edge %q: %w", node.Name(), key, err)
		}
		return n, nil
	}
	target := edge.Target() // nil-checked by the caller
	if include.EdgeKind(edge) == include.KindInArray && edge.SubField == "" {
		return nil, fmt.Errorf("apidoc: node %q edge %q: in-array edge requires SubField", node.Name(), key)
	}

	if len(subKeys) == 0 && !hasLiveDefaults(target, seenDefaults) {
		// Effective leaf: no client sub-includes AND every default of the
		// target is stale or cut by the chain — the response nests nothing
		// under it, so the exact static edge shape ($ref) applies.
		return edgeShape(edge, newRef(target.Name())), nil
	}
	// The response carries the target's own effective children too (client
	// sub-includes and/or live defaults): inline the recomputed target — it
	// carries its own requiredness delta — in place of the $ref. For an
	// in-array edge that lands in the subField position; edgeShape owns
	// that placement for every kind.
	inner, err := schemaForNode(r, target, subtree, seenDefaults)
	if err != nil {
		return nil, err
	}
	return edgeShape(edge, inner), nil
}

// hasLiveDefaults reports whether target would expand at least one of its own
// default edges under the given chain: an existing computed default, or an
// existing non-computed default whose target is not already on the chain. A
// nil-thunk default counts as live so the recursion surfaces the wiring error
// instead of silently $ref-ing past it.
func hasLiveDefaults(target include.Resource, seenDefaults map[string]bool) bool {
	edges := target.Edges()
	for _, k := range target.Defaults() {
		e, ok := edges[k]
		if !ok {
			continue // stale default
		}
		if e.Computed {
			return true
		}
		if e.Target == nil {
			return true // broken thunk: recurse so the error is reported
		}
		t := e.Target()
		if t == nil || !seenDefaults[t.Name()] {
			return true
		}
	}
	return false
}

// extendSet is the planner's chain-extension: a fresh set = src + name (src is
// shared up the recursion and must not be mutated).
func extendSet(src map[string]bool, name string) map[string]bool {
	out := make(map[string]bool, len(src)+1)
	for k := range src {
		out[k] = true
	}
	out[name] = true
	return out
}

// setRequiredProp writes an EFFECTIVE child key into base: every effective key
// is present in the response, so it is required. An existing property of that
// name is REPLACED by a fresh Prop value (never mutated in place through a
// borrowed slice element's fields beyond this owned copy).
func setRequiredProp(base *IRNode, name string, value *IRNode) {
	for i := range base.Props {
		if base.Props[i].Name == name {
			base.Props[i] = Prop{Name: name, Schema: value, Required: true}
			return
		}
	}
	base.Props = append(base.Props, Prop{Name: name, Schema: value, Required: true})
}

// baseObjectIR reflects ONE node's SCALAR wire struct into an IR object node
// WITHOUT stitching edges: the node's component lands under Name() via the same
// override EmitComponents uses.
//
// The reflector's AUXILIARY output is not discarded — it is fed straight into
// emit.go's inlineAux, the one implementation of the inlining rules (bare-ref /
// non-Opaque / not-a-node / not-in-a-cycle). That is what makes SchemaFor's refs
// a SUBSET of EmitComponents' component map: a nested struct that EmitComponents
// flattens away is flattened here too, and an auxiliary that survives inlining
// (sole-ref, Opaque, cyclic) survives in both, under the same name.
//
// The node-name set is everything reachable from root by includable edges — the
// same walk EmitComponents does, seeded with root alone. EmitComponents' roots
// can only make that set LARGER (reachability is transitive, and root's own
// reachable set is contained in it), and a larger node set only PROTECTS more
// names from inlining. The residual gap is emit.go's documented EmitComponent
// restriction: a wire struct referencing a node's wire type that is NOT
// reachable from root through includable edges.
func baseObjectIR(r Reflector, root include.Resource) (*IRNode, error) {
	t, err := wireType(root)
	if err != nil {
		return nil, err
	}
	out, err := r.ReflectComponents([]reflect.Type{t}, map[reflect.Type]string{t: root.Name()})
	if err != nil {
		return nil, err
	}
	if base, ok := out[root.Name()]; !ok || base == nil {
		return nil, fmt.Errorf("apidoc: baseObjectIR: reflector did not emit a component for node %q", root.Name())
	}
	nodes, err := collectReachable([]include.Resource{root})
	if err != nil {
		return nil, err
	}
	nodeNames := make(map[string]include.Resource, len(nodes))
	for _, n := range nodes {
		nodeNames[n.Name()] = n
	}
	inlined, err := inlineAux(out, nodeNames)
	if err != nil {
		return nil, err
	}
	base, ok := inlined[root.Name()]
	if !ok || base == nil {
		return nil, fmt.Errorf("apidoc: baseObjectIR: reflector did not emit a component for node %q", root.Name())
	}
	return base, nil
}

// childEdges returns the tree's non-arg child edge names (keys NOT prefixed
// ':'), SORTED so both the effective-child order and an error message quoting
// them are deterministic. A nil tree yields no children (range over a nil map
// is empty).
func childEdges(tree include.IncludeTree) []string {
	var out []string
	for k := range tree {
		if len(k) > 0 && k[0] == ':' {
			continue
		}
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}
