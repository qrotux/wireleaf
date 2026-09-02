package include

import (
	"fmt"
	"maps"
	"sort"
	"strconv"
	"strings"
)

// ------------------------------------------------------------------ Limits

// Limits bounds the size and shape of a CLIENT-supplied include tree. Only
// client-introduced edges count toward these caps; a resource's own Defaults()
// expansion is uncapped (developer responsibility) but is still guaranteed to
// terminate via the default-cycle guard in ResolvePlan.
type Limits struct {
	// MaxDepth is the deepest client-introduced segment chain allowed.
	MaxDepth int
	// MaxNodes is the maximum number of client-introduced edges across the tree.
	MaxNodes int
	// MaxCost caps the STATIC cost estimate of a plan for one root document:
	// the sum over every plan node (defaults included) of the product of the
	// per-edge row limits on the path from the root. 0 means DefaultLimits.MaxCost.
	MaxCost int
	// MaxRows caps the rows ACTUALLY materialized by one hydrate call, root
	// documents included. 0 means DefaultLimits.MaxRows.
	MaxRows int
}

// DefaultLimits is the engine-wide default.
var DefaultLimits = Limits{MaxDepth: 4, MaxNodes: 50, MaxCost: 5000, MaxRows: 50000}

// ------------------------------------------------------------------ Options

// SortPolicy controls how an unknown client :sort() key is treated.
type SortPolicy int

const (
	// SortStrict fails the plan with INVALID_INCLUDE on an unknown sort key.
	SortStrict SortPolicy = iota
	// SortFallback silently falls back to the edge's default sort.
	SortFallback
)

// ArgPolicy controls how an undeclared client edge argument is treated.
type ArgPolicy int

const (
	// ArgsStrict fails the plan with INVALID_INCLUDE on an undeclared argument.
	ArgsStrict ArgPolicy = iota
	// ArgsTolerant passes undeclared arguments through to PlanNode.Args.
	ArgsTolerant
)

// ExcludeRequiredPolicy controls what an ?exclude= path naming a REQUIRED edge does.
// It concerns REQUIRED edges only — every other exclude path prunes the plan
// unchanged under both values.
type ExcludeRequiredPolicy int

const (
	// ExcludeRequiredTolerant silently keeps a required edge: the client asked for
	// less, the published component forces more, and the response wins. The
	// exclude is a no-op for that path; sub-paths beneath it still prune.
	ExcludeRequiredTolerant ExcludeRequiredPolicy = iota
	// ExcludeRequiredStrict fails the plan with INVALID_INCLUDE instead, so a client
	// that asks for a shape the contract forbids is told so rather than
	// silently served the key it tried to drop.
	ExcludeRequiredStrict
)

// MissingRequiredPolicy controls what the engine does when a REQUIRED to-one
// edge resolves to no target — an empty FK, a row the fetcher did not return,
// or a guard-false parent. It concerns REQUIRED edges only; a plain to-one
// edge nulls out under all three conditions regardless.
//
// Edge.MissingRequired overrides it per edge.
type MissingRequiredPolicy int

const (
	// MissingRequiredNull emits null under the required key, silently. The
	// document still says non-null: the contradiction is a server-side fault
	// the declaring application owns, and the doc layer does not soften the
	// published contract to hide it.
	MissingRequiredNull MissingRequiredPolicy = iota
	// MissingRequiredError fails the whole request instead: a required target
	// that is not there is a data-integrity fault (an orphaned FK) or a
	// fetcher quietly filtering a row the contract promised, and neither
	// should reach the client as a null.
	MissingRequiredError
)

// MissingForeignPolicy controls what the engine does when a NON-EMPTY foreign
// key read off a parent row resolves to no row — the fetcher was asked for the
// id and did not return it. That is a dangling reference: either the FK is
// orphaned, or the fetcher filtered out a row the parent still points at.
//
// It applies to the three kinds that read a parent-side FK — to-one
// (ForeignKey), forward-hasMany and in-array (ForeignKeys). A REVERSE edge has no
// parent-side FK (it fetches by the parent's own id, and no children simply
// means an empty collection), so the policy does not reach it, and neither
// does it reach a computed edge, which fetches nothing.
//
// An EMPTY fk is NOT a dangling reference — it is the absence of a reference,
// and on a required edge that case belongs to MissingRequiredPolicy.
//
// Edge.MissingForeign overrides it per edge.
type MissingForeignPolicy int

const (
	// MissingForeignNull keeps the v0 behaviour: a to-one edge yields null, a
	// to-many edge silently DROPS the unresolved item from its list, and an
	// in-array element's subField becomes null.
	MissingForeignNull MissingForeignPolicy = iota
	// MissingForeignError fails the whole request instead, naming the edge and
	// the id that resolved to nothing.
	MissingForeignError
)

// ------------------------------------------------------------------ edge overrides

// EdgePolicies is the per-edge override set carried on an Edge: a nil field
// inherits the engine-wide fallback (Options.ExcludeRequiredPolicy at plan
// time; Ctx.Policies at materialize time). graph.Policies builds it from
// declared values.
type EdgePolicies struct {
	ExcludeRequired *ExcludeRequiredPolicy
	MissingRequired *MissingRequiredPolicy
	MissingForeign  *MissingForeignPolicy
}

// EdgePolicy is one declarable policy VALUE. The interface is closed — its
// method is unexported, so only this package's three policy types implement it
// — which is what makes graph.Policies(...) accept exactly the policy
// constants and nothing else, checked by the compiler.
type EdgePolicy interface{ applyEdgePolicy(*EdgePolicies) }

func (p ExcludeRequiredPolicy) applyEdgePolicy(s *EdgePolicies) { s.ExcludeRequired = &p }
func (p MissingRequiredPolicy) applyEdgePolicy(s *EdgePolicies) { s.MissingRequired = &p }
func (p MissingForeignPolicy) applyEdgePolicy(s *EdgePolicies)  { s.MissingForeign = &p }

// EdgePoliciesOf folds declared policy values into an override set. Naming one
// policy twice is last-wins; a nil value is skipped. It is exported because the
// folding has to happen in graph, which cannot reach the unexported method.
func EdgePoliciesOf(ps ...EdgePolicy) EdgePolicies {
	var out EdgePolicies
	for _, p := range ps {
		if p != nil {
			p.applyEdgePolicy(&out)
		}
	}
	return out
}

// FetcherContractPolicy controls what the engine does when a FETCHER violates
// its loading contract — the conventions the type system cannot see:
//
//   - FetchByParents returned MORE than EdgeQuery.Limit rows for a parent
//     (the probe contract says "select limit+1, return at most limit");
//   - FetchByParents returned a parent id that was never requested;
//   - HasMore reported on a BARE edge (Limit 0 = fetch-all, nothing beyond);
//   - FetchIDs returned a row whose id was never requested.
//
// FetcherContractTolerant (default) keeps the historical behaviour: the engine
// corrects silently (truncates, drops). FetcherContractStrict fails the whole
// request instead, naming the edge and the violation — run it in tests and dev
// so a broken fetcher is loud on its FIRST execution; graph/loadertest remains
// the normative harness (it also checks what the engine cannot observe).
type FetcherContractPolicy int

const (
	// FetcherContractTolerant silently corrects violations (v0 behaviour).
	FetcherContractTolerant FetcherContractPolicy = iota
	// FetcherContractStrict fails the request on the first violation.
	FetcherContractStrict
)

// ------------------------------------------------------------------ Policies

// Policies is the engine-wide MATERIALIZE-time fallback set, carried on Ctx
// (the resolve-time policies stay on Options). The zero value is the
// permissive default for every field, so a zero Ctx behaves exactly as
// before the policies existed. A per-edge override on Edge wins where set
// (Edge.MissingRequired / Edge.MissingForeign); FetcherContract has no
// per-edge override — a contract violation is a server-side fetcher bug,
// not edge semantics.
type Policies struct {
	// MissingRequired is the fallback MissingRequiredPolicy for edges that
	// declare no override.
	MissingRequired MissingRequiredPolicy
	// MissingForeign is the fallback MissingForeignPolicy for edges that
	// declare no override.
	MissingForeign MissingForeignPolicy
	// FetcherContract applies engine-wide, never per edge.
	FetcherContract FetcherContractPolicy
}

// Options bundles the engine-wide RESOLVE-time configuration.
// ExcludeRequiredPolicy is the request default for the one policy an edge may
// override at plan time: Edge.ExcludeRequired wins where set. The
// materialize-time policies (missing-required, missing-foreign,
// fetcher-contract) live on Ctx.Policies instead — ResolvePlan never reads
// them.
type Options struct {
	Limits                Limits
	SortPolicy            SortPolicy
	ArgPolicy             ArgPolicy
	ExcludeRequiredPolicy ExcludeRequiredPolicy
}

// DefaultOptions is the zero-config default: DefaultLimits, strict sort,
// strict args, tolerant exclude.
var DefaultOptions = Options{Limits: DefaultLimits}

// effectiveExcludeRequired returns the exclude policy in force for one edge: its own
// override, else the request's.
func effectiveExcludeRequired(e *Edge, opts ExcludeRequiredPolicy) ExcludeRequiredPolicy {
	if e != nil && e.ExcludeRequired != nil {
		return *e.ExcludeRequired
	}
	return opts
}

// ------------------------------------------------------------------ PlanNode

// PlanNode is a validated, defaults-merged node in the resolved include plan.
// The tree is produced by ResolvePlan and consumed by the materialize step;
// it carries no fetched data — only what to fetch and how to shape it.
type PlanNode struct {
	// Path is the dotted path from the root: "" (root), "author", "author.avatar".
	Path string
	// Resource is the node this plan level serializes. It is nil on a COMPUTED
	// node, which has no target resource (see Computed).
	Resource Resource
	// Many reports whether the inbound edge produces a collection (root: false).
	Many bool
	// Args are the resolved client args with the ":" prefix stripped (root: nil).
	// Values are raw string / []string, EXCEPT the built-in "limit", which is
	// coerced to a positive int at plan time.
	Args map[string]any
	// Children are the resolved child plan nodes.
	Children []*PlanNode
	// EdgeKey is the include key of the inbound edge ("" for the root).
	EdgeKey string
	// Edge is the inbound edge definition (nil for the root). The
	// materialize-time policies are read off it directly (its overrides, else
	// the Ctx.Policies fallbacks) — the plan carries no policy state.
	Edge *Edge
	// Computed reports that the inbound edge is a COMPUTED edge: it has no
	// target Resource, it is always a plan LEAF (a child segment under it is
	// rejected at plan time), and the engine SKIPS it at materialize time —
	// the key is absent from the engine's bytes and the application splices
	// its own value in. Handlers find such a node with Get.
	Computed bool
	// Cost is the static row estimate of THIS node's subtree for one root
	// document: this level's estimated rows plus its children's. On the root
	// it is the whole plan's estimate, the number ResolvePlan compared to
	// Limits.MaxCost. Computed nodes cost 0.
	Cost int
	// MaxRows is the runtime row budget in force for this plan (set on the
	// ROOT node only; 0 elsewhere). Materialize reads it off the root.
	MaxRows int
}

// Get returns the DIRECT child of p with the given edge key, or nil when p has
// no such child (and when p itself is nil, so lookups chain safely). Handlers
// use it to read a resolved child node — its Args in particular — where
// HasInclude only answers presence.
func (p *PlanNode) Get(edgeKey string) *PlanNode {
	if p == nil {
		return nil
	}
	for _, child := range p.Children {
		if child.EdgeKey == edgeKey {
			return child
		}
	}
	return nil
}

// ------------------------------------------------------------------ ResolvePlan

// ResolvePlan turns rootNode + client IncludeTree + exclude + opts into a
// validated PlanNode tree — the "400-before-fetch" step.
//
// Validation/expansion rules (all enforced here, no data is fetched):
//
//   - MaxDepth is checked on ENTRY to each level (before iterating its
//     children); a client include deeper than MaxDepth → INCLUDE_TOO_DEEP.
//   - The children at each node are the union of node.Defaults() and the
//     client-supplied child keys.
//   - Per CLIENT-supplied child: an unknown edge → INVALID_INCLUDE; a known
//     but non-includable edge → INVALID_INCLUDE; a node counter is incremented
//     (client edges only) and, if it exceeds MaxNodes → INCLUDE_TOO_DEEP.
//   - Client args on an edge are checked against the edge's declaration and
//     the configured policies (see validateArgs).
//   - The DEFAULT expansion is cycle-guarded by seenDefaults (seeded with the
//     root resource Name(), extended only when descending a DEFAULT edge). A
//     default edge whose target Name() is already seen is SKIPPED (clean
//     termination, no error). Client edges are NOT cycle-guarded (bounded by
//     MaxDepth). A stale default (no matching edge) is silently skipped.
//   - A COMPUTED edge (no target Resource) plans like any other edge — same
//     namespace, Includable gate, arg validation, depth/node accounting — but
//     it is a LEAF: a client child segment under it → INVALID_INCLUDE at that
//     child path, and its PlanNode carries Computed=true with a nil Resource.
//   - exclude is applied LAST, in two passes: validate every path against the
//     resolved plan (unknown → INVALID_INCLUDE; a path naming a REQUIRED edge
//     → INVALID_INCLUDE under ExcludeRequiredStrict), then remove tolerantly. A
//     REQUIRED edge is never removed under either ExcludeRequiredPolicy.
func ResolvePlan(root Resource, tree IncludeTree, exclude [][]string, opts Options) (*PlanNode, error) {
	// nodeCount is shared across the whole recursion (client edges only).
	nodeCount := 0

	var build func(
		node Resource,
		clientTree IncludeTree,
		path string,
		edgeKey string,
		edge *Edge,
		many bool,
		clientDepth int,
		seenDefaults map[string]bool,
	) (*PlanNode, error)

	build = func(
		node Resource,
		clientTree IncludeTree,
		path string,
		edgeKey string,
		edge *Edge,
		many bool,
		clientDepth int,
		seenDefaults map[string]bool,
	) (*PlanNode, error) {
		// MaxDepth is checked BEFORE iterating this level's children.
		if clientDepth > opts.Limits.MaxDepth {
			return nil, NewError(INCLUDE_TOO_DEEP, path)
		}

		args, clientChildren := splitTreeNode(clientTree)

		// Root carries no edge → reject any root-level args. parse.go already
		// blocks this from an HTTP ?include= string; this closes the same
		// contract gap for programmatic IncludeTree inputs that hand-build a
		// ":"-prefixed root key.
		if edge == nil && len(args) > 0 {
			return nil, NewError(INVALID_INCLUDE, path)
		}

		// Client args are validated against the inbound edge's declaration.
		if edge != nil && len(args) > 0 {
			if err := validateArgs(edge, path, args, opts); err != nil {
				return nil, err
			}
		}

		n := &PlanNode{
			Path:    path,
			Many:    many,
			Args:    args,
			EdgeKey: edgeKey,
			Edge:    edge,
		}

		// A COMPUTED edge is a plan LEAF: it has no target Resource, so nothing
		// under it can be planned. It is otherwise an ordinary edge — the
		// Includable gate, arg validation and depth/node accounting above all
		// ran already. A client child segment under it is INVALID_INCLUDE at
		// the CHILD path (lowest key first, so the error is deterministic).
		if edge != nil && edge.Computed {
			if len(clientChildren) > 0 {
				keys := make([]string, 0, len(clientChildren))
				for k := range clientChildren {
					keys = append(keys, k)
				}
				sort.Strings(keys)
				return nil, NewError(INVALID_INCLUDE, joinPath(path, keys[0]))
			}
			n.Computed = true // Resource stays nil: no target
			return n, nil
		}

		// Merge: defaults ∪ client child keys. Defaults keep their declaration-order
		// slice position; client-only keys come after, SORTED, so PlanNode.Children
		// is deterministic run-to-run (Go map iteration is randomized, and the
		// downstream materialize/byte-assembly step asserts edge order on raw bytes).
		edges := node.Edges()
		seen := make(map[string]bool, len(node.Defaults())+len(clientChildren))
		ordered := make([]string, 0, len(node.Defaults())+len(clientChildren))
		for _, k := range node.Defaults() {
			if !seen[k] {
				seen[k] = true
				ordered = append(ordered, k)
			}
		}
		clientKeys := make([]string, 0, len(clientChildren))
		for k := range clientChildren {
			if !seen[k] {
				seen[k] = true
				clientKeys = append(clientKeys, k)
			}
		}
		sort.Strings(clientKeys)
		ordered = append(ordered, clientKeys...)

		children := make([]*PlanNode, 0, len(ordered))
		for _, key := range ordered {
			childEdge, edgeOK := edges[key]
			_, fromClient := clientChildren[key]

			if fromClient {
				if !edgeOK {
					return nil, NewError(INVALID_INCLUDE, joinPath(path, key))
				}
				if !childEdge.Includable {
					return nil, NewError(INVALID_INCLUDE, joinPath(path, key))
				}
				nodeCount++
				if nodeCount > opts.Limits.MaxNodes {
					return nil, NewError(INCLUDE_TOO_DEEP, joinPath(path, key))
				}
			}

			if !edgeOK {
				continue // stale default (no matching edge) — silently skipped
			}

			// A computed edge has NO Target — never dereference the thunk for
			// one, and it can never re-enter a resource, so the cycle guard
			// does not apply to it either.
			var childRes Resource
			childTargetName := ""
			if !childEdge.Computed {
				childRes = childEdge.Target()
				childTargetName = childRes.Name()

				// Cycle guard on the DEFAULT-only expansion path: a default re-entering
				// a resource already on the default-chain is skipped (clean termination).
				// Client edges are exempt (bounded by MaxDepth/MaxNodes), so a legitimate
				// diamond (same node via two sibling defaults) is unaffected.
				if !fromClient && seenDefaults[childTargetName] {
					continue
				}
			}

			childPath := joinPath(path, key)
			childClientTree := clientChildren[key]

			childDepth := clientDepth
			childSeen := seenDefaults
			if fromClient {
				childDepth = clientDepth + 1
				// client edge: do NOT extend the default-chain
			} else if !childEdge.Computed {
				// default edge: extend the default-chain with the target name
				childSeen = extendSet(seenDefaults, childTargetName)
			}

			// Pin the edge so &childEdge is a stable per-child pointer (range
			// reuses its loop variable; capturing its address would alias).
			ce := childEdge
			child, err := build(
				childRes, // nil for a computed edge (leaf, no target)
				childClientTree,
				childPath,
				key,
				&ce,
				childEdge.Many,
				childDepth,
				childSeen,
			)
			if err != nil {
				return nil, err
			}
			children = append(children, child)
		}

		n.Resource = node
		n.Children = children
		return n, nil
	}

	// Seed the default-chain with the root name so a default edge re-entering the
	// root is cut at the FIRST hop.
	plan, err := build(root, tree, "", "", nil, false, 0, map[string]bool{root.Name(): true})
	if err != nil {
		return nil, err
	}

	if err := applyExclude(plan, exclude, opts.ExcludeRequiredPolicy); err != nil {
		return nil, err
	}

	// Cost runs AFTER exclude so pruned children do not inflate the estimate.
	lim := opts.Limits
	if lim.MaxCost == 0 {
		lim.MaxCost = DefaultLimits.MaxCost
	}
	if lim.MaxRows == 0 {
		lim.MaxRows = DefaultLimits.MaxRows
	}
	estimateCost(plan, 1)
	if plan.Cost > lim.MaxCost {
		return nil, NewError(INCLUDE_TOO_EXPENSIVE, fmt.Sprintf("estimated %d rows per document, limit %d", plan.Cost, lim.MaxCost))
	}
	plan.MaxRows = lim.MaxRows
	return plan, nil
}

// ------------------------------------------------------------------ cost

// estimateCost fills n.Cost for n and its subtree. parentRows is the
// estimated number of rows at the PARENT level; this level's rows are
// parentRows × the inbound edge's multiplier.
func estimateCost(n *PlanNode, parentRows int) {
	if n.Computed {
		n.Cost = 0
		return
	}
	rows := parentRows * edgeMultiplier(n)
	total := rows
	for _, c := range n.Children {
		estimateCost(c, rows)
		total += c.Cost
	}
	n.Cost = total
}

// edgeMultiplier is the per-parent row estimate of the edge into n:
//   - root or to-one: 1
//   - in-array, or a Bare to-many: Edge.EstimatedRows, default 100
//   - enveloped to-many: the resolved limit (client :limit clamped to Edge.Limit)
func edgeMultiplier(n *PlanNode) int {
	e := n.Edge
	if e == nil || !e.Many {
		return 1
	}
	if e.Bare || e.ArrayPath != "" {
		if e.EstimatedRows > 0 {
			return e.EstimatedRows
		}
		return defaultUnboundedEstimate
	}
	def := defaultReverseLimit
	if e.Backref == "" {
		def = defaultHasManyLimit
	}
	return resolvedLimit(n, def)
}

// ------------------------------------------------------------------ args

// validateArgs enforces the edge-argument contract for one plan node: an
// undeclared name fails under ArgsStrict; a declared validator rejecting the
// raw value fails; a client sort key missing from SortCols fails under
// SortStrict; the built-in ":limit" is COERCED to a positive int in place. The
// built-ins are rejected outright on edge kinds whose loading contract cannot
// honour them (see the gate in the loop): ":limit" outside reverse /
// forward-hasMany or on a bare edge, ":sort" outside reverse. A DECLARED arg
// named "limit" does not shadow that coercion — its validator runs first, then
// the built-in applies. All failures are INVALID_INCLUDE at "path:arg".
func validateArgs(edge *Edge, path string, args map[string]any, opts Options) error {
	declared := make(map[string]*EdgeArg, len(edge.Args))
	for i := range edge.Args {
		declared[edge.Args[i].Name] = &edge.Args[i]
	}
	kind := EdgeKind(*edge)
	for name, raw := range args {
		// The built-ins ":limit" and ":sort" are legal ONLY where the loading
		// contract can honour them — an accepted-and-ignored built-in is a
		// contract error, never a silent no-op:
		//
		//	:limit — reverse + forward-hasMany (both resolve a per-parent
		//	         top-N), and never on a bare edge (fetch-all by
		//	         definition; checked below with the coercion);
		//	:sort  — reverse only (the one kind whose fetcher receives
		//	         EdgeQuery.Sort).
		//
		// Everything else (to-one, in-array, computed) rejects both. This runs
		// BEFORE the declared-arg short-circuit below, so a hand-built edge
		// that declares an arg of either name cannot make the rejection
		// evaporate (the builder path is compile-checked; this closes the
		// engine-level surface).
		if name == "limit" && kind != KindReverse && kind != KindForwardHasMany {
			return NewError(INVALID_INCLUDE, path+":"+name)
		}
		if name == "sort" && kind != KindReverse {
			return NewError(INVALID_INCLUDE, path+":"+name)
		}
		if spec, ok := declared[name]; ok {
			if spec.Validate != nil {
				if err := spec.Validate(raw); err != nil {
					return NewError(INVALID_INCLUDE, path+":"+name)
				}
			}
			// A DECLARED arg never shadows the built-in "limit": the declared
			// validator runs first (above), then the built-in coercion runs
			// anyway (below), so PlanNode.Args["limit"] is ALWAYS an int after
			// planning. graph.Compile rejects such a declaration outright; this
			// is the belt for hand-built edges outside the builder.
			if name != "limit" {
				continue
			}
		}
		switch name {
		case "limit":
			// Built-in: a bare edge has no top-N to cap, so a client limit on
			// one is a contract error, never a silent no-op. (The in-array
			// rejection already fired at the top of the loop.)
			if edge.Bare {
				return NewError(INVALID_INCLUDE, path+":"+name)
			}
			n, ok := parseLimit(raw)
			if !ok {
				return NewError(INVALID_INCLUDE, path+":"+name)
			}
			args[name] = n // stored as int; the materializer clamps it to Edge.Limit
		case "sort":
			if edge.SortCols == nil {
				if opts.ArgPolicy == ArgsStrict {
					return NewError(INVALID_INCLUDE, path+":"+name)
				}
				continue
			}
			if opts.SortPolicy == SortStrict {
				s, isStr := raw.(string)
				if !isStr {
					return NewError(INVALID_INCLUDE, path+":"+name)
				}
				if _, ok := lookupSortCol(s, edge.SortCols); !ok {
					return NewError(INVALID_INCLUDE, path+":"+name)
				}
			}
		default:
			if opts.ArgPolicy == ArgsStrict {
				return NewError(INVALID_INCLUDE, path+":"+name)
			}
		}
	}
	return nil
}

// parseLimit coerces a raw client ":limit" value to a positive int. Only the
// single-value string form is accepted: a bare arg (""), a multi-value
// []string, a non-numeric token and anything < 1 are all rejected (ok=false;
// the caller maps it to INVALID_INCLUDE at "path:limit").
func parseLimit(raw any) (int, bool) {
	s, ok := raw.(string)
	if !ok {
		return 0, false
	}
	n, err := strconv.Atoi(s)
	if err != nil || n < 1 {
		return 0, false
	}
	return n, true
}

// ------------------------------------------------------------------ helpers

// splitTreeNode splits a raw IncludeTree node into its args (":"-prefixed keys,
// prefix stripped) and its child subtrees (all other keys). A non-tree child
// value is treated as an empty subtree.
func splitTreeNode(tree IncludeTree) (args map[string]any, children map[string]IncludeTree) {
	args = nil
	children = make(map[string]IncludeTree)
	for k, v := range tree {
		if strings.HasPrefix(k, ":") {
			if args == nil {
				args = make(map[string]any)
			}
			args[k[1:]] = v // strip the ":" prefix; pass the raw value through
			continue
		}
		if sub, ok := v.(IncludeTree); ok {
			children[k] = sub
		} else {
			children[k] = IncludeTree{}
		}
	}
	return args, children
}

// joinPath joins a parent path and a child key with a dot ("" parent → key).
func joinPath(parent, key string) string {
	if parent == "" {
		return key
	}
	return parent + "." + key
}

// extendSet returns a NEW set = src ∪ {name}. The copy is required so sibling
// branches at the same level don't share a mutated default-chain. src is never
// nil (ResolvePlan seeds the chain with the root name).
func extendSet(src map[string]bool, name string) map[string]bool {
	out := maps.Clone(src)
	out[name] = true
	return out
}

// ------------------------------------------------------------------ exclude

// applyExclude removes subtrees named by exclude paths (after defaults+include).
//
// Two-pass strategy:
//
//	Pass 1 — validate EVERY exclude path against the resolved (unmutated) plan;
//	         a path whose segment chain does not exist → INVALID_INCLUDE, as is
//	         a path naming a REQUIRED edge under ExcludeRequiredStrict.
//	Pass 2 — apply removals tolerantly; a path whose ancestor was already removed
//	         by a prior exclude is a silent no-op.
//
// A REQUIRED edge is never REMOVED under either policy: the document declares
// its key always present, so honouring the exclude would make the response
// violate the published component. policy only decides whether that request is
// answered silently (ExcludeRequiredTolerant: no-op for that path, sub-paths beneath it
// still prune) or refused (ExcludeRequiredStrict: INVALID_INCLUDE).
func applyExclude(root *PlanNode, excludes [][]string, policy ExcludeRequiredPolicy) error {
	// Pass 1: validate all paths against the original tree (before any mutation).
	for _, segs := range excludes {
		node := nodeAtPath(root, segs)
		if node == nil {
			return NewError(INVALID_INCLUDE, strings.Join(segs, "."))
		}
		if node.Edge != nil && node.Edge.Required &&
			effectiveExcludeRequired(node.Edge, policy) == ExcludeRequiredStrict {
			return NewError(INVALID_INCLUDE, strings.Join(segs, "."))
		}
	}

	// Pass 2: apply removals tolerantly (already-removed ancestors → no-op).
	for _, segs := range excludes {
		cursor := root
		for i, seg := range segs {
			idx := indexOfChild(cursor, seg)
			if idx == -1 {
				break // ancestor already removed by a prior exclude — no-op
			}
			if i == len(segs)-1 {
				child := cursor.Children[idx]
				if child.Edge != nil && child.Edge.Required {
					break // required key: the exclude is silently suppressed
				}
				cursor.Children = append(cursor.Children[:idx], cursor.Children[idx+1:]...)
			} else {
				cursor = cursor.Children[idx]
			}
		}
	}
	return nil
}

// nodeAtPath returns the plan node the segment chain addresses in node's
// subtree, or nil when the chain does not exist.
func nodeAtPath(node *PlanNode, segs []string) *PlanNode {
	cursor := node
	for _, seg := range segs {
		idx := indexOfChild(cursor, seg)
		if idx == -1 {
			return nil
		}
		cursor = cursor.Children[idx]
	}
	return cursor
}

// indexOfChild returns the index of the direct child with the given edge key,
// or -1 if absent.
func indexOfChild(node *PlanNode, key string) int {
	for i, c := range node.Children {
		if c.EdgeKey == key {
			return i
		}
	}
	return -1
}
