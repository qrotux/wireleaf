package apidoc

// emit.go — graph-derived OpenAPI 3.1 component emission, over the typed IR.
//
// A cyclic include graph cannot be handed to a reflector in one piece (a
// reflector inlines nested struct types and has no lazy-reference escape), so
// emission runs in three passes:
//
//   (a) reflect every reachable node's SCALAR-only wire struct into a base
//       object node (acyclic — edge fields do not exist on wire structs);
//   (b) STITCH each INCLUDABLE or DEFAULT edge into that node's Props as a
//       $ref-shaped value — the cycle is expressed in refs the reflector never
//       traverses (defaults regardless of Includable: the engine materializes
//       Defaults() ∪ client keys, so a default key is part of the wire shape);
//   (c) INLINE auxiliary components by fixpoint substitution, so a nested
//       struct that exists only as an implementation detail of one wire type
//       does not surface as a document-level component.
//
// Everything happens on *IRNode: no step goes through map[string]any, which is
// what keeps property order and Opaque bytes intact end to end.
//
// Nullable idiom: a nullable scalar is the OAS-3.1 type-array
// ({"type":["T","null"]}) the Reflector contract guarantees; an edge value uses
// anyOf[…, {"type":"null"}] instead, since a $ref cannot carry a sibling
// type-array.

import (
	"fmt"
	"maps"
	"reflect"
	"slices"
	"sort"
	"strings"

	"github.com/qrotux/wireleaf/include"
)

// WireProvider is the optional Resource seam the doc layer and adapters read
// to learn a node's wire type: graph.Compile's nodes implement it.
type WireProvider interface {
	WireSample() any
}

// docExternaler is the capability collectReachable checks to skip
// externally-owned components (kept off include.Resource for the same reason).
type docExternaler interface{ IsDocExternal() bool }

// EmitComponent emits ONE node's component fragment: its wire struct reflected
// under the node's Name(), its includable edges stitched in, and every
// auxiliary struct component inlined where the rules allow (see inlineAux).
//
// Two restrictions, both because it returns ONE schema and knows ONE node name:
//
//   - an auxiliary that survives inlining (sole-ref, Opaque or cyclic — see
//     inlineAux) has nowhere to go and would dangle, so it is an ERROR naming
//     the components. A $ref to another graph node or a doc-external name is
//     not a survivor and stays legal.
//   - every other component the reflector emits is classified as an auxiliary,
//     so a wire struct that embeds ANOTHER NODE's wire type gets that sibling
//     INLINED where EmitComponents would keep it a $ref.
//
// Use EmitComponents, with every node as a root, for anything beyond plain,
// acyclic, non-Opaque helper structs.
func EmitComponent(r Reflector, node include.Resource) (string, Schema, error) {
	name := node.Name()
	out, err := emitFragments(r, []include.Resource{node})
	if err != nil {
		return "", Schema{}, err
	}
	n, ok := out[name]
	if !ok {
		return "", Schema{}, fmt.Errorf("apidoc: reflector did not emit a component for node %q", name)
	}
	if len(out) > 1 {
		// Auxiliaries survived inlining (sole-ref / Opaque / cyclic): the
		// singular form has no way to return them, and the schema above $refs
		// them — a guaranteed dangling reference. Fail loudly at the call
		// site instead of at some later Components.Verify.
		extra := make([]string, 0, len(out)-1)
		for k := range out {
			if k != name {
				extra = append(extra, k)
			}
		}
		sort.Strings(extra)
		return "", Schema{}, fmt.Errorf(
			"apidoc: EmitComponent(%q): auxiliary component(s) %s survive inlining and cannot be returned by the singular form; use EmitComponents",
			name, strings.Join(extra, ", "))
	}
	return name, Schema{n: n}, nil
}

// EmitComponents walks every node reachable from roots (following includable
// and default edges' targets, deduped by Name() so cycles terminate), reflects
// each node's scalar wire struct, stitches its includable/default edges, inlines
// auxiliary components, and returns the component map keyed by node Name() plus
// whatever auxiliaries survived inlining.
func EmitComponents(r Reflector, roots []include.Resource) (map[string]Schema, error) {
	reachable, err := collectReachable(roots)
	if err != nil {
		return nil, err
	}
	out, err := emitFragments(r, reachable)
	if err != nil {
		return nil, err
	}
	schemas := make(map[string]Schema, len(out))
	for name, n := range out {
		schemas[name] = Schema{n: n}
	}
	return schemas, nil
}

// emitFragments is the shared reflect → stitch → inline pipeline. nodes must
// already be deduped (collectReachable does that for EmitComponents).
func emitFragments(r Reflector, nodes []include.Resource) (map[string]*IRNode, error) {
	// A reflector names a component after its Go TYPE (e.g. BookWire), but the
	// graph identifies nodes by Name() (Book) and the stitched $refs point at
	// Name(). Overriding each top wire type → Name() makes the emitted component
	// land where those refs resolve; auxiliary types keep the default naming.
	types := make([]reflect.Type, 0, len(nodes))
	overrides := make(map[reflect.Type]string, len(nodes))
	nodeNames := make(map[string]include.Resource, len(nodes))
	for _, node := range nodes {
		t, err := wireType(node)
		if err != nil {
			return nil, err
		}
		if prev, ok := overrides[t]; ok && prev != node.Name() {
			return nil, fmt.Errorf("apidoc: wire type %s maps to two node names %q and %q", t, prev, node.Name())
		}
		types = append(types, t)
		overrides[t] = node.Name()
		nodeNames[node.Name()] = node
	}

	// One reflector call for every node at once: auxiliary components a wire
	// struct references (a nested struct field) are emitted alongside the tops
	// and deduped by name, so every internal $ref resolves.
	out, err := r.ReflectComponents(types, overrides)
	if err != nil {
		return nil, err
	}

	// Stitch onto graph-NODE components only; auxiliary sub-schemas are not
	// graph nodes and get no edges.
	for _, name := range slices.Sorted(maps.Keys(nodeNames)) {
		base, ok := out[name]
		if !ok || base == nil {
			return nil, fmt.Errorf("apidoc: reflector did not emit a component for node %q", name)
		}
		if err := stitchEdges(base, nodeNames[name]); err != nil {
			return nil, err
		}
	}
	return inlineAux(out, nodeNames)
}

// collectReachable returns the nodes reachable from roots through includable
// AND default edges (a non-includable default's target is still $ref'd by the
// stitched component, so it must be emitted or the reference dangles). Dedup is
// by Name(), which makes the walk cycle-safe; two distinct nodes sharing a name
// is a hard error. Traversal order is not deterministic (the DFS stack is fed by
// map iteration over Edges()), the resulting set is.
func collectReachable(roots []include.Resource) ([]include.Resource, error) {
	seen := make(map[string]include.Resource)
	var order []include.Resource
	var stack []include.Resource
	stack = append(stack, roots...)

	for len(stack) > 0 {
		node := stack[len(stack)-1]
		stack = stack[:len(stack)-1]
		name := node.Name()
		if prev, ok := seen[name]; ok {
			if prev != node {
				return nil, fmt.Errorf("apidoc: duplicate node name %q for two distinct resources", name)
			}
			continue
		}
		seen[name] = node
		order = append(order, node)

		inDefaults := defaultSet(node)
		for key, edge := range node.Edges() {
			if !edge.Includable && !inDefaults[key] {
				continue
			}
			// A COMPUTED edge has no destination resource at all — its Target is
			// a nil FUNC. Branch before touching it, or the walk panics.
			if edge.Computed {
				continue
			}
			if edge.Target == nil {
				return nil, fmt.Errorf("apidoc: node %q has an includable/default edge with a nil Target thunk", name)
			}
			target := edge.Target()
			if target == nil {
				return nil, fmt.Errorf("apidoc: node %q has an includable/default edge whose Target thunk returned nil", name)
			}
			if dx, ok := target.(docExternaler); ok && dx.IsDocExternal() {
				continue // externally-owned component: stitch $ref, don't emit its fragment here
			}
			stack = append(stack, target)
		}
	}
	return order, nil
}

// defaultSet returns node.Defaults() as a lookup set.
func defaultSet(node include.Resource) map[string]bool {
	d := node.Defaults()
	if len(d) == 0 {
		return nil
	}
	out := make(map[string]bool, len(d))
	for _, k := range d {
		out[k] = true
	}
	return out
}

// wireType returns the reflect.Type of a node's Wire struct (the dynamic type
// of its WireSample()). Errors if the node is not a WireProvider or its sample
// is a nil interface.
func wireType(node include.Resource) (reflect.Type, error) {
	wp, ok := node.(WireProvider)
	if !ok {
		return nil, fmt.Errorf("apidoc: node %q does not provide a WireSample()", node.Name())
	}
	sample := wp.WireSample()
	if sample == nil {
		return nil, fmt.Errorf("apidoc: node %q WireSample() returned a nil interface", node.Name())
	}
	return DerefType(reflect.TypeOf(sample)), nil
}

// stitchEdges inserts each INCLUDABLE or DEFAULT edge of node into base's Props
// using the edge-shape policy (edgeshape.go); other non-includable edges are
// omitted. An edge key is optional (the value shape encodes nullability, and
// ?exclude= can prune a default key) with ONE exception: a REQUIRED to-one edge
// is stitched as a bare $ref AND listed in `required`. Keys are processed in
// sorted order so repeated emission is byte-identical.
func stitchEdges(base *IRNode, node include.Resource) error {
	edges := node.Edges()
	if len(edges) == 0 {
		return nil
	}
	if base.Kind != KindObject {
		return fmt.Errorf("apidoc: node %q base schema is a %s, want an object to stitch edges into", node.Name(), base.Kind)
	}
	inDefaults := defaultSet(node)
	for _, key := range slices.Sorted(maps.Keys(edges)) {
		edge := edges[key]
		if !edge.Includable && !inDefaults[key] {
			continue
		}
		value, err := edgeValue(node.Name(), key, edge)
		if err != nil {
			return err
		}
		setProp(base, key, value, requiredEdge(edge))
	}
	return nil
}

// requiredEdge reports whether an edge key must be listed in the component's
// `required` set: a non-computed TO-ONE edge that declares Required. Every other
// kind ignores the flag (graph.Compile rejects it there, and a hand-built
// include.Edge must not widen the shape table through the back door).
func requiredEdge(e include.Edge) bool {
	return e.Required && !e.Computed && include.EdgeKind(e) == include.KindToOne
}

// edgeValue builds one includable edge's value schema, validating the edge's
// declaration on the way. A COMPUTED edge is handled FIRST: it carries a nil
// Target func, so every other branch would panic on it.
func edgeValue(nodeName, key string, edge include.Edge) (*IRNode, error) {
	if edge.Computed {
		n, err := computedSchemaIR(edge)
		if err != nil {
			return nil, fmt.Errorf("apidoc: node %q edge %q: %w", nodeName, key, err)
		}
		return n, nil
	}
	if edge.Target == nil {
		return nil, fmt.Errorf("apidoc: node %q edge %q has a nil Target thunk", nodeName, key)
	}
	target := edge.Target()
	if target == nil {
		return nil, fmt.Errorf("apidoc: node %q edge %q Target thunk returned nil", nodeName, key)
	}
	if include.EdgeKind(edge) == include.KindInArray && edge.SubField == "" {
		return nil, fmt.Errorf("apidoc: node %q edge %q: in-array edge requires SubField", nodeName, key)
	}
	return edgeShape(edge, newRef(target.Name())), nil
}

// setProp replaces the named property's schema in place, or appends it.
// required PROMOTES the property (a required to-one edge is always present);
// required == false leaves an existing property's Required flag alone — an edge
// key that collides with a wire field keeps the wire field's requiredness — and
// appends the new key as optional. Requiredness is never revoked here.
func setProp(base *IRNode, name string, value *IRNode, required bool) {
	for i := range base.Props {
		if base.Props[i].Name == name {
			base.Props[i].Schema = value
			if required {
				base.Props[i].Required = true
			}
			return
		}
	}
	base.Props = append(base.Props, Prop{Name: name, Schema: value, Required: required})
}

// ---------------------------------------------------------------------------
// auxiliary inlining
// ---------------------------------------------------------------------------

// inlineAux collapses AUXILIARY components into their referencing schemas by
// fixpoint substitution. An auxiliary is a component the reflector emitted that
// is not a graph node — a nested struct that exists as an implementation detail
// of some wire type. Graph nodes and doc-external names are never auxiliaries
// (a doc-external target is not in the reflector's output at all), so they
// always stay $refs.
//
// Three rules bound the substitution:
//
//   - the SOLE-REF rule: only a BARE $ref is inlined. A $ref carrying sibling
//     keywords (a description, a validation, an extension) is left alone —
//     inlining it would have to merge the annotation into the target, and the
//     merge has no single right answer.
//   - an OPAQUE auxiliary is never inlined: its bytes are held verbatim and are
//     the one thing the IR promises not to reshape, so it stays a $ref.
//   - a CYCLIC auxiliary is PINNED: a self-referential struct, or a mutually
//     referential pair, cannot be inlined (substitution would grow forever), so
//     it DEGRADES to its own component referenced by $ref — the same survival
//     path a sole-ref takes. That is a document that is merely less flat, not
//     an error; a self-referential struct is an ordinary Go shape.
//
// Cyclic auxiliaries are found by an SCC pre-pass (auxInCycle) BEFORE any
// substitution runs, so the remaining substitution set is provably acyclic and
// the round bound below is an assertion, not the cycle check. Auxiliaries still
// referenced after the fixpoint survive as their own components; unreferenced
// ones are dropped.
func inlineAux(out map[string]*IRNode, nodeNames map[string]include.Resource) (map[string]*IRNode, error) {
	aux := map[string]*IRNode{}
	for name, n := range out {
		if _, isNode := nodeNames[name]; !isNode {
			aux[name] = n
		}
	}
	if len(aux) == 0 {
		return out, nil
	}

	// SCC pre-pass: drop every cyclic auxiliary from the substitution SOURCE
	// set. It stays in `work` (its own body still gets its acyclic references
	// inlined) and stays a $ref everywhere it is mentioned.
	for _, name := range auxInCycle(aux) {
		delete(aux, name)
	}

	work := make(map[string]*IRNode, len(out))
	for name, n := range out {
		work[name] = n
	}

	// Bound/ASSERTION: the substitution set is acyclic after the pre-pass, so a
	// chain of depth d converges in d rounds and d cannot exceed len(aux). Still
	// changing past that means auxInCycle missed an edge — a bug here, not a
	// user's graph.
	for round := 0; ; round++ {
		changed := false
		next := make(map[string]*IRNode, len(work))
		for _, name := range slices.Sorted(maps.Keys(work)) {
			sub, ch := substituteAux(work[name], aux)
			next[name] = sub
			changed = changed || ch
		}
		work = next
		for name := range aux {
			aux[name] = work[name]
		}
		if !changed {
			break
		}
		if round >= len(aux) {
			return nil, fmt.Errorf("apidoc: internal invariant: auxiliary inlining did not converge over {%s} after the acyclic pre-pass", joinSorted(aux))
		}
	}

	referenced := map[string]bool{}
	for _, n := range work {
		for _, r := range refsOf(n) {
			referenced[r] = true
		}
	}
	result := make(map[string]*IRNode, len(work))
	for name, n := range work {
		if _, isNode := nodeNames[name]; isNode || referenced[name] {
			result[name] = n
		}
	}
	return result, nil
}

// auxInCycle returns, sorted, every auxiliary that lies on a cycle of the
// INLINABLE-$ref graph — i.e. every member of a non-trivial strongly connected
// component, plus every self-loop. Membership is decided by the defining
// property of an SCC: a node is in one exactly when it can reach ITSELF. With
// the handful of auxiliaries a wire struct produces, a reachability sweep is
// the clearest formulation of Tarjan's result, and it is exact.
//
// The edge set MUST match what substituteAux would actually follow (a bare $ref
// to a non-Opaque auxiliary), or the pre-pass and the fixpoint would disagree
// and the round bound would trip. Both sides descend through the child edges
// forEachChild (ir.go) defines, so a new *IRNode-carrying field extends them
// in lockstep — only substituteAux's rebuild needs a matching case by hand.
func auxInCycle(aux map[string]*IRNode) []string {
	edges := make(map[string][]string, len(aux))
	for name, n := range aux {
		edges[name] = inlinableRefTargets(n, aux)
	}
	var cyclic []string
	for _, start := range slices.Sorted(maps.Keys(aux)) {
		seen := map[string]bool{}
		var stack []string
		stack = append(stack, edges[start]...)
		for len(stack) > 0 {
			cur := stack[len(stack)-1]
			stack = stack[:len(stack)-1]
			if cur == start {
				cyclic = append(cyclic, start)
				break
			}
			if seen[cur] {
				continue
			}
			seen[cur] = true
			stack = append(stack, edges[cur]...)
		}
	}
	return cyclic
}

// inlinableRefTargets lists the auxiliary names n references through a $ref
// that substituteAux would actually inline: bare (sole-ref rule) and pointing
// at a non-Opaque auxiliary.
func inlinableRefTargets(n *IRNode, aux map[string]*IRNode) []string {
	var out []string
	seenNode := map[*IRNode]bool{}
	seenName := map[string]bool{}
	var walk func(*IRNode) error
	walk = func(cur *IRNode) error {
		if cur == nil || seenNode[cur] || cur.Kind == KindOpaque {
			return nil
		}
		seenNode[cur] = true
		if cur.Kind == KindRef {
			if isBareRef(cur) {
				if t, ok := aux[cur.Ref]; ok && t != nil && t.Kind != KindOpaque && !seenName[cur.Ref] {
					seenName[cur.Ref] = true
					out = append(out, cur.Ref)
				}
			}
			return nil
		}
		return cur.forEachChild(walk)
	}
	_ = walk(n) // walk never returns an error; the signature is forEachChild's
	return out
}

// substituteAux returns n with every inlinable $ref to an auxiliary replaced by
// that auxiliary's schema, plus whether anything changed. Nodes are never
// mutated: a changed subtree is rebuilt, an unchanged one is shared.
//
// INVARIANT — inlined subtrees are SHARED BY POINTER across (and within)
// emitted components, so every downstream consumer must treat IR nodes as
// IMMUTABLE and clone before mutating; Schema's delta methods mutate, which is
// why emitted components deliberately break the single-owner-per-node
// convention in exchange for not copying whole subtrees.
//
// It REBUILDS the tree rather than walking it, so the per-field cases below
// must cover exactly the child edge set forEachChild (ir.go) defines.
func substituteAux(n *IRNode, aux map[string]*IRNode) (*IRNode, bool) {
	if n == nil {
		return nil, false
	}
	if n.Kind == KindOpaque {
		// Opaque bytes are held verbatim; refs inside them are not rewritten.
		return n, false
	}
	if n.Kind == KindRef {
		if !isBareRef(n) {
			return n, false // sole-ref rule: a described ref stays a ref
		}
		target, ok := aux[n.Ref]
		if !ok || target == nil || target.Kind == KindOpaque {
			return n, false
		}
		return target, true
	}

	changed := false
	out := n.shallowClone()

	if len(n.Props) > 0 {
		props := make([]Prop, len(n.Props))
		copy(props, n.Props)
		for i := range props {
			sub, ch := substituteAux(props[i].Schema, aux)
			props[i].Schema = sub
			changed = changed || ch
		}
		out.Props = props
	}
	if n.Items != nil {
		sub, ch := substituteAux(n.Items, aux)
		out.Items = sub
		changed = changed || ch
	}
	if n.Not != nil {
		sub, ch := substituteAux(n.Not, aux)
		out.Not = sub
		changed = changed || ch
	}
	if ap, ok := n.AdditionalProperties.(*IRNode); ok {
		sub, ch := substituteAux(ap, aux)
		out.AdditionalProperties = sub
		changed = changed || ch
	}
	out.AnyOf, changed = substituteArms(n.AnyOf, aux, changed)
	out.OneOf, changed = substituteArms(n.OneOf, aux, changed)
	out.AllOf, changed = substituteArms(n.AllOf, aux, changed)

	if !changed {
		return n, false
	}
	return out, true
}

func substituteArms(arms []*IRNode, aux map[string]*IRNode, changed bool) ([]*IRNode, bool) {
	if len(arms) == 0 {
		return arms, changed
	}
	out := make([]*IRNode, len(arms))
	for i, a := range arms {
		sub, ch := substituteAux(a, aux)
		out[i] = sub
		changed = changed || ch
	}
	return out, changed
}

// isBareRef reports whether n is a $ref with NO sibling keywords — the only
// shape the sole-ref rule allows to be inlined. It compares against a canonical
// minimal ref via DeepEqual, which sees unexported fields too: a future IRNode
// field automatically TIGHTENS this check instead of silently weakening it.
func isBareRef(n *IRNode) bool {
	if n.Kind != KindRef || n.Ref == "" {
		return false
	}
	bare := &IRNode{Kind: KindRef, Ref: n.Ref}
	// DeepEqual distinguishes a nil collection from an empty one. Of the
	// keyword fields, only Types (fragTypes builds []string{} from "type":[])
	// and DependentRequired (Set takes the caller's map verbatim) can reach
	// here empty-but-non-nil; carry those over so a keyword-free ref stays
	// bare. Every other collection is nil whenever it is empty: the append
	// idiom yields nil, SetExtension never leaves Extensions empty, and Opaque
	// comes only from newOpaque, non-empty.
	if len(n.Types) == 0 {
		bare.Types = n.Types
	}
	if len(n.DependentRequired) == 0 {
		bare.DependentRequired = n.DependentRequired
	}
	return reflect.DeepEqual(n, bare)
}

// joinSorted renders a name set for an error message, deterministically.
func joinSorted[V any](m map[string]V) string {
	return strings.Join(slices.Sorted(maps.Keys(m)), ", ")
}
