package graph

import (
	"maps"
	"reflect"

	"github.com/qrotux/wireleaf/apidoc"
	"github.com/qrotux/wireleaf/include"
)

// ------------------------------------------------------------------ compiledNode

// compiledNode is one validated node: the immutable include.Resource the
// engine hydrates through, plus the duck-typed seams the doc layer reads
// (WireSample/IsDocExternal/FieldVerdicts). It is produced ONLY by Compile;
// every field is frozen once Compile returns.
type compiledNode struct {
	name string
	slug string

	wireT reflect.Type

	// derived from the wire struct's tags (one reflect walk per node)
	fields   []string                  // json names, declaration order
	verdicts map[string]apidoc.Verdict // json name → nullability verdict
	sortCols map[string]string         // json name → SQL-side sort key
	cols     map[string]include.Column // json name → SQL-side binding (col tags)

	// inputs is the node's compiled list-input contract; hasInputs reports
	// whether the node declared one (false = include.DefaultInputs()).
	inputs    include.Inputs
	hasInputs bool

	defaults    []string
	docExternal bool

	// edgeKeys preserves declaration order (Edges() is an unordered map).
	edgeKeys []string
	edges    map[string]include.Edge

	wireFn       func(any, *include.Ctx) any
	primaryKeyFn func(any) string
	enrichFn     func([]any, *include.Ctx) error

	fetchIDs     include.FetchByIDs
	fetchParents include.FetchByParents
	edgeFetch    map[string]include.FetchByParents // per-edge reverse fetchers, by edge key
}

// Compile-time assertion that a compiled node is an include.Resource.
var _ include.Resource = (*compiledNode)(nil)

// Compile-time assertion that a compiled node exposes its column bindings.
var _ include.ColumnSource = (*compiledNode)(nil)

// Compile-time assertion that a compiled node exposes its list inputs.
var _ include.InputSource = (*compiledNode)(nil)

func (n *compiledNode) Name() string { return n.name }
func (n *compiledNode) Slug() string { return n.slug }

// ACCESSOR CONTRACT — Fields, Defaults and Edges return the LIVE backing
// slice/map of an immutable compiled graph, not a copy. They are on the engine's
// per-request hot path (the planner walks Edges for every include tree, the
// serializer walks Fields for every document), and copying there would cost an
// allocation per call for a structure that Compile froze and nothing may change.
//
// CALLERS MUST NOT MUTATE the returned value — not the slice elements, not the
// map entries, not the map itself. A write reaches every other request in the
// process. Copy first if you need to modify.
//
// (FieldVerdicts below DOES copy: it is doc-layer metadata, read once at
// emission time, so the allocation is free of consequence there.)

func (n *compiledNode) Fields() []string               { return n.fields }
func (n *compiledNode) Defaults() []string             { return n.defaults }
func (n *compiledNode) Edges() map[string]include.Edge { return n.edges }

// Columns returns the node's SQL-side column bindings keyed by wire json name
// — the include.ColumnSource seam, derived from the `col` / `sortCol` tags.
// LIVE map under the accessor contract above; nil when the wire declares no
// column at all.
func (n *compiledNode) Columns() map[string]include.Column { return n.cols }

// Inputs returns the node's compiled list-input contract and whether the node
// declared one (false = include.DefaultInputs()).
func (n *compiledNode) Inputs() (include.Inputs, bool) { return n.inputs, n.hasInputs }

// IDOf performs the single localized any→Row assertion and delegates to the
// boxed PrimaryKey closure (guaranteed non-nil by Compile).
func (n *compiledNode) IDOf(doc any) string { return n.primaryKeyFn(doc) }

// Serialize delegates to the boxed graph.Wire closure (guaranteed non-nil).
// The names differ on purpose: include.Resource fixes the METHOD name, while
// the builder option is named after what it produces.
func (n *compiledNode) Serialize(doc any, ctx *include.Ctx) any { return n.wireFn(doc, ctx) }

// Enrich delegates to the boxed Enrich closure; a node without one is a
// no-op.
func (n *compiledNode) Enrich(docs []any, ctx *include.Ctx) error {
	if n.enrichFn == nil {
		return nil
	}
	return n.enrichFn(docs, ctx)
}

// WireSample returns a zero value of the node's Wire type. The OpenAPI emitter
// reflects its dynamic type (duck-typed seam, apidoc/emit.go).
func (n *compiledNode) WireSample() any { return reflect.New(n.wireT).Elem().Interface() }

// IsDocExternal reports whether this node's component is owned externally
// (duck-typed seam, apidoc/emit.go).
func (n *compiledNode) IsDocExternal() bool { return n.docExternal }

// FieldVerdicts returns a COPY of the per-field nullability verdicts derived at
// compile time by the ONE policy (apidoc.DefaultNullability).
//
// It is EXPOSED METADATA, not the emission path. The emitted schema's
// nullability comes from the canonical reflector, which applies the same policy
// to the same wire type independently; nothing in apidoc reads this map. It is
// here so an application (or a test) can see the compiler's per-field decisions
// without re-deriving them, and because the two agreeing is a property worth
// being able to check.
func (n *compiledNode) FieldVerdicts() map[string]apidoc.Verdict {
	// n.verdicts is never nil (deriveShape always allocates it), so Clone
	// never returns nil here.
	return maps.Clone(n.verdicts)
}

// ------------------------------------------------------------------ Graph

// Graph is the immutable, validated result of Builder.Compile: the read-only
// runtime the engine and the OpenAPI emitter share. It doubles as the
// include.Registry, routing fetcher lookups to the binds recorded on the
// builder.
type Graph struct {
	byName map[string]*compiledNode
	roots  []*compiledNode // declaration order, deduped
}

// Compile-time assertion that *Graph is the engine's registry.
var _ include.Registry = (*Graph)(nil)

// Resource returns the compiled node named name. An unknown name is a wiring
// bug, not a runtime condition: it panics.
func (g *Graph) Resource(name string) include.Resource {
	n, ok := g.byName[name]
	if !ok {
		panic("graph: unknown resource " + name)
	}
	return n
}

// Roots returns the entry-point nodes in declaration order.
func (g *Graph) Roots() []include.Resource {
	out := make([]include.Resource, len(g.roots))
	for i, n := range g.roots {
		out[i] = n
	}
	return out
}

// Reachable returns res plus every node reachable from it by following ONLY
// includable edges. The walk is a BFS, edges in declaration order, deduped by
// Name() (so cycles terminate) — deterministic across runs.
//
// res must be a node of THIS graph (from Resource or Roots): every walked
// node is a *compiledNode — Edge.Target always closes over one — and a
// foreign include.Resource is a wiring bug that panics in orderedEdges.
func (g *Graph) Reachable(res include.Resource) []include.Resource {
	if res == nil {
		return nil
	}
	seen := map[string]bool{res.Name(): true}
	out := []include.Resource{res} // doubles as the BFS queue: out[i:] is unvisited

	for i := 0; i < len(out); i++ {
		for _, e := range orderedEdges(out[i]) {
			if !e.Includable || e.Target == nil {
				continue
			}
			t := e.Target()
			if t == nil || seen[t.Name()] {
				continue
			}
			seen[t.Name()] = true
			out = append(out, t)
		}
	}
	return out
}

// orderedEdges yields a compiled node's edges in declaration order. Reachable
// only ever hands it this graph's own nodes (see its contract), so any other
// include.Resource is a wiring bug, not a runtime condition: it panics.
func orderedEdges(res include.Resource) []include.Edge {
	cn, ok := res.(*compiledNode)
	if !ok {
		panic("graph: Reachable walked a resource not compiled by this graph")
	}
	out := make([]include.Edge, 0, len(cn.edgeKeys))
	for _, k := range cn.edgeKeys {
		out = append(out, cn.edges[k])
	}
	return out
}

// ------------------------------------------------------------------ include.Registry

// FetchByIDs returns the forward-batch fetcher bound to res, if any.
func (g *Graph) FetchByIDs(res include.Resource) (include.FetchByIDs, bool) {
	n, ok := g.byName[res.Name()]
	if !ok || n.fetchIDs == nil {
		return nil, false
	}
	return n.fetchIDs, true
}

// FetchByParents returns the batched reverse fetcher bound to res, if any.
func (g *Graph) FetchByParents(res include.Resource) (include.FetchByParents, bool) {
	n, ok := g.byName[res.Name()]
	if !ok || n.fetchParents == nil {
		return nil, false
	}
	return n.fetchParents, true
}

// FetchByEdge returns the reverse fetcher bound to the edge parent.<key>
// (FetchEdge, or a relation's FetchParents), if any. The engine asks here
// before FetchByParents(target).
func (g *Graph) FetchByEdge(parent include.Resource, key string) (include.FetchByParents, bool) {
	n, ok := g.byName[parent.Name()]
	if !ok {
		return nil, false
	}
	fn, ok := n.edgeFetch[key]
	return fn, ok
}
