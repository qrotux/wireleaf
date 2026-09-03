package graph

import (
	"reflect"

	"github.com/qrotux/wireleaf/include"
)

// Builder accumulates declarations; single-goroutine, write-only, dead after
// Compile (spec §1). All validation happens in Compile, never at declaration.
type Builder struct {
	nodes []*nodeSpec
	roots []*nodeSpec
	dead  bool

	// Graph-wide default include.Envelope (see Envelope); envelopeSet tells
	// Compile whether to validate it.
	envelope    include.Envelope
	envelopeSet bool
}

// NewBuilder returns an empty, live Builder.
func NewBuilder() *Builder { return &Builder{} }

// Ready-made envelope styles. EnvelopePlain is the zero value — the shape
// wireleaf emits when nothing is declared; EnvelopeData wraps every node as
// {"data": …} and every enveloped list as {"data": […], "pagination": {…}}.
var (
	EnvelopePlain = include.Envelope{}
	EnvelopeData  = include.Envelope{Key: "data", Pagination: "pagination"}
)

// Envelope sets the GRAPH-wide default wrapper style of every edge value. A
// node's Envelope overrides it for the edges INTO that node, and an edge's
// Envelope overrides both; Compile resolves the three layers into
// include.Edge.Envelope and validates the declared value (Pagination without
// Key, colliding names, names needing JSON escaping are findings).
func (b *Builder) Envelope(e include.Envelope) *Builder {
	b.mustLive()
	b.envelope, b.envelopeSet = e, true
	return b
}

// nodeSpec is the untyped record behind a NodeHandle. rowT/wireT carry the
// type parameters for Compile-time checks and wire-tag derivation.
type nodeSpec struct {
	name, slug  string
	rowT, wireT reflect.Type

	wireFn       func(any, *include.Ctx) any     // boxed Wire
	primaryKeyFn func(any) string                // boxed PrimaryKey
	enrichFn     func([]any, *include.Ctx) error // boxed Enrich

	defaults    []string
	docExternal bool

	// envelope overrides the graph default for every edge TARGETING this node.
	envelope    include.Envelope
	envelopeSet bool

	// inputs is the node's list-input declaration; inputsSet distinguishes a
	// zero literal from no declaration.
	inputs    Inputs
	inputsSet bool

	// edges is ORDERED by declaration and APPEND-ONLY: a relation keeps the
	// *edgeDecl it appended and writes its backref through it later, so
	// nothing may reorder, filter or replace entries.
	edges []*edgeDecl

	fetchIDs     include.FetchByIDs     // boxed; nil until bound
	fetchParents include.FetchByParents // boxed; nil until bound

	// edgeFetch holds the reverse fetchers bound PER EDGE (FetchEdge, or a
	// relation's FetchParents), keyed by this node's edge key. Compile checks
	// each against the edge it names and copies it into the compiled node.
	edgeFetch map[string]*edgeFetchBind

	// set-flags let Compile distinguish "declared as empty/nil" from "not
	// declared". The chained methods are typed by the handle's own Row/Wire
	// parameters, so there are no recorded option types to cross-check — the
	// Go compiler already rejected a closure typed on another node's Row.
	slugSet, wireSet, primaryKeySet, enrichSet bool
}

// edgeFetchBind is one per-edge reverse fetcher as declared: the node the
// caller typed the rows by (Compile checks it is the edge's target) and the
// boxed fetcher.
type edgeFetchBind struct {
	target *nodeSpec
	fn     include.FetchByParents
}

// edgeDecl is one declared edge: its include key, its kind discriminant and
// the settings its EdgeBuilder chain writes (interpreted by Compile, never at
// declaration time).
type edgeDecl struct {
	key  string
	kind EdgeKind
	set  *edgeSettings

	// relation names the relation constructor that declared this edge
	// ("OneToMany" / "ManyToMany"), "" for a plain Edge call. Compile uses it
	// to report a relation's missing ForeignKey as ONE finding in the
	// relation's own vocabulary instead of two desugared ones.
	relation string
}

// NodeHandle is the typed façade over one nodeSpec — a chained configurator
// AND the value an application holds onto: node facts, edges, roots and binds
// all go through handles. Configuration methods record only; every check runs
// in Compile, and every method panics after Compile like the rest of the
// builder. Calling a method twice is last-wins.
type NodeHandle[Row any, Wire any] struct {
	b    *Builder
	spec *nodeSpec
}

// Node declares a node on b and returns its typed handle, configured by
// chaining. Row is the DB/query row type, Wire the serialized wire struct.
// Nothing is validated here.
//
//	book := graph.Node[bookRow, BookWire](b, "Book").
//	    Wire(func(r bookRow, _ *include.Ctx) BookWire { ... }).
//	    PrimaryKey(func(r bookRow) string { return r.ID })
func Node[Row any, Wire any](b *Builder, name string) *NodeHandle[Row, Wire] {
	b.mustLive()
	spec := &nodeSpec{
		name:  name,
		rowT:  reflect.TypeFor[Row](),
		wireT: reflect.TypeFor[Wire](),
	}
	b.nodes = append(b.nodes, spec)
	return &NodeHandle[Row, Wire]{b: b, spec: spec}
}

// Slug sets the node's short lowercase DOC-layer key (routes, operation ids,
// tags); include resolution never reads it. Absent, Compile derives it as
// lowercase(name).
func (h *NodeHandle[Row, W]) Slug(s string) *NodeHandle[Row, W] {
	h.b.mustLive()
	h.spec.slug, h.spec.slugSet = s, true
	return h
}

// Wire installs the node's Row → Wire mapper: the one place a DB row becomes
// the struct the client sees. It runs per document, AFTER Enrich has filled
// whatever side data the mapping needs, and its result is what
// include.Ctx.Marshal turns into bytes — so this returns a value, never JSON.
// It is the builder's half of include.Resource.Serialize.
//
// The closure is typed by THIS handle's Row and W, so a mismatched closure is
// a Go compile error. It is boxed once here; the single any→Row assertion
// lives inside it. A nil fn is recorded as "declared, but nil" so Compile
// reports it instead of the node panicking at hydrate time.
func (h *NodeHandle[Row, W]) Wire(fn func(Row, *include.Ctx) W) *NodeHandle[Row, W] {
	h.b.mustLive()
	h.spec.wireSet = true
	h.spec.wireFn = nil
	if fn != nil {
		h.spec.wireFn = func(doc any, c *include.Ctx) any { return fn(doc.(Row), c) }
	}
	return h
}

// PrimaryKey installs the extractor for a row's OWN id — the identity the
// engine batches and indexes by, and the value a ForeignKey on some other node
// points at. It is the builder's half of include.Resource.IDOf.
//
// The closure is typed by THIS handle's Row (a mismatch is a Go compile
// error). A nil fn is recorded as "declared, but nil" so Compile reports it.
func (h *NodeHandle[Row, W]) PrimaryKey(fn func(Row) string) *NodeHandle[Row, W] {
	h.b.mustLive()
	h.spec.primaryKeySet = true
	h.spec.primaryKeyFn = nil
	if fn != nil {
		h.spec.primaryKeyFn = func(doc any) string { return fn(doc.(Row)) }
	}
	return h
}

// Enrich installs the optional batch hook run over a WHOLE level's rows before
// Wire — the sanctioned place to side-load data the mapping needs (typically
// graph.Loader.Warm), once per level rather than once per row. It is the
// builder's half of include.Resource.Enrich.
//
// The closure is typed by THIS handle's Row (a mismatch is a Go compile
// error). A nil fn is recorded as "declared, but nil" so Compile reports it.
func (h *NodeHandle[Row, W]) Enrich(fn func([]Row, *include.Ctx) error) *NodeHandle[Row, W] {
	h.b.mustLive()
	h.spec.enrichSet = true
	h.spec.enrichFn = nil
	if fn != nil {
		h.spec.enrichFn = func(docs []any, c *include.Ctx) error {
			rows := make([]Row, len(docs))
			for i, doc := range docs {
				rows[i] = doc.(Row)
			}
			return fn(rows, c)
		}
	}
	return h
}

// Defaults lists the edge keys included when the client asks for none.
// Repeated calls APPEND (like Policies, not last-wins-whole-list), and every
// Required() edge key lands here automatically at Compile — after the explicit
// keys, in edge-declaration order — because a required key the plan never
// materializes would break the published component on every plain response.
func (h *NodeHandle[Row, W]) Defaults(keys ...string) *NodeHandle[Row, W] {
	h.b.mustLive()
	h.spec.defaults = append(h.spec.defaults, keys...)
	return h
}

// DocExternal marks a node whose OpenAPI component is owned externally:
// emission stitches a $ref to it but never emits its fragment.
func (h *NodeHandle[Row, W]) DocExternal() *NodeHandle[Row, W] {
	h.b.mustLive()
	h.spec.docExternal = true
	return h
}

// Envelope fixes the wrapper style of every edge whose TARGET is this node,
// overriding the Builder's default; an edge's own Envelope still wins. It is
// the way to keep one node's shape uniform wherever it is reached from.
func (h *NodeHandle[Row, W]) Envelope(e include.Envelope) *NodeHandle[Row, W] {
	h.b.mustLive()
	h.spec.envelope, h.spec.envelopeSet = e, true
	return h
}

// Edge declares an edge under key on this node and returns its chained
// configurator, typed by this node's Row. The target inside kind is addressed
// by its WIRE TYPE (resolved in Compile), so declaration order — and even
// declaration FILE — does not matter, and cycles need no forward references.
func (h *NodeHandle[Row, Wire]) Edge(key string, kind EdgeKind) *EdgeBuilder[Row] {
	h.b.mustLive()
	set := &edgeSettings{}
	h.spec.edges = append(h.spec.edges, &edgeDecl{key: key, kind: kind, set: set})
	return &EdgeBuilder[Row]{b: h.b, set: set}
}

// Root marks a node as an entry point of the graph.
func (b *Builder) Root(h handle) { b.mustLive(); b.roots = append(b.roots, h.nodeSpec()) }

func (b *Builder) mustLive() {
	if b.dead {
		panic("graph: builder is dead after Compile")
	}
}

// handle lets Root and the fetcher binds take any NodeHandle instantiation.
type handle interface{ nodeSpec() *nodeSpec }

func (h *NodeHandle[Row, Wire]) nodeSpec() *nodeSpec { return h.spec }
