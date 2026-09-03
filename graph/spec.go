package graph

import "github.com/qrotux/wireleaf/include"

// Spec is the DECLARATIVE form of one node: everything the chained
// NodeHandle/EdgeBuilder API records, as a single struct literal that can live
// next to the node's wire type — one file per node, the node's facts, edges
// and fetchers in one place. Add registers it on a Builder.
//
// It is sugar over the chained API, nothing more: Add replays the literal
// through Node, Edge, FetchIDs and FetchParents, so a Spec-declared node is
// indistinguishable from a chained one to Compile, and the two forms mix
// freely on one Builder. Nothing validates here either — a missing Wire or
// PrimaryKey is the same Compile finding it is on the chain.
//
//	var Author = graph.Spec[authorRow, AuthorWire]{
//	    Name:       "Author",
//	    Wire:       func(r authorRow, _ *include.Ctx) AuthorWire { return AuthorWire{ID: r.ID, Name: r.Name} },
//	    PrimaryKey: func(r authorRow) string { return r.ID },
//	    Edges: []graph.EdgeSpec[authorRow]{
//	        {Key: "books", Kind: graph.Reverse[BookWire]("authorId"), Inverse: "author", Limit: 10, Includable: true},
//	    },
//	    FetchIDs: fetchAuthors,
//	    Inputs:   &graph.Inputs{Sort: graph.SortInput{Enabled: true, Default: "name"}},
//	}
//
// The zero value of a field means "not declared" exactly as the corresponding
// chained method never being called: a nil closure is not bound (Compile
// reports the mandatory ones as missing), Limit 0 leaves the default top-N in
// place, a nil Envelope inherits the graph's. The pointer Envelope fields are
// the one place the distinction matters — a declared zero include.Envelope
// (plain) OVERRIDES a non-plain graph default, so it cannot be the zero value.
type Spec[Row any, Wire any] struct {
	// Name is the node's name (NodeHandle's Node argument); required.
	Name string
	// Slug is the optional DOC-layer key; "" lets Compile derive lowercase(Name).
	Slug string

	Wire       func(Row, *include.Ctx) Wire
	PrimaryKey func(Row) string
	Enrich     func([]Row, *include.Ctx) error

	// Defaults lists the edge keys included when the client asks for none.
	Defaults []string
	// DocExternal marks the OpenAPI component as externally owned.
	DocExternal bool
	// Envelope, when non-nil, fixes the wrapper style of every edge INTO this
	// node (NodeHandle.Envelope).
	Envelope *include.Envelope

	// Inputs, when non-nil, declares the node's list inputs (NodeHandle.Inputs).
	Inputs *Inputs

	// Edges are registered IN ORDER, as declaration order matters (Defaults,
	// finding order, doc emission).
	Edges []EdgeSpec[Row]

	// FetchIDs / FetchParents are the node's fetcher binds; nil leaves the
	// node unbound, exactly like never calling the bind function.
	FetchIDs     func(c *include.Ctx, ids []string) ([]Row, error)
	FetchParents FetchByParents[Row]
}

// EdgeSpec is one edge of a Spec: the fields of the EdgeBuilder chain as a
// struct literal, typed by the PARENT node's Row like the chain is. Key and
// Kind are what NodeHandle.Edge takes; every other field maps 1:1 onto the
// EdgeBuilder method of the same name, and a zero value is "not declared".
type EdgeSpec[Row any] struct {
	Key  string
	Kind EdgeKind

	ForeignKey  func(Row) string
	ForeignKeys func(Row) []string
	Guard       func(*include.Ctx, Row) bool

	Inverse    string
	Required   bool
	Includable bool
	Filterable bool
	Bare       bool

	// Limit is the per-edge top-N; 0 is "not declared" (the default applies).
	Limit int
	// EstimatedRows is the cost-model estimate of an unbounded edge; 0 is
	// "not declared".
	EstimatedRows int

	// Envelope, when non-nil, overrides the wrapper style for this edge alone.
	Envelope *include.Envelope
	Sort     string
	Args     []EdgeArgOpt
	Policies []include.EdgePolicy
}

// Add registers s on b and returns the node's handle — the same
// *NodeHandle the chained Node call returns, so Root, further chained
// configuration and the bind functions all still apply. It panics after
// Compile like every other builder call.
func Add[Row any, Wire any](b *Builder, s Spec[Row, Wire]) *NodeHandle[Row, Wire] {
	h := Node[Row, Wire](b, s.Name)
	if s.Slug != "" {
		h.Slug(s.Slug)
	}
	if s.Wire != nil {
		h.Wire(s.Wire)
	}
	if s.PrimaryKey != nil {
		h.PrimaryKey(s.PrimaryKey)
	}
	if s.Enrich != nil {
		h.Enrich(s.Enrich)
	}
	if len(s.Defaults) > 0 {
		h.Defaults(s.Defaults...)
	}
	if s.DocExternal {
		h.DocExternal()
	}
	if s.Envelope != nil {
		h.Envelope(*s.Envelope)
	}
	if s.Inputs != nil {
		h.Inputs(*s.Inputs)
	}
	for _, es := range s.Edges {
		es.apply(h.Edge(es.Key, es.Kind))
	}
	if s.FetchIDs != nil {
		FetchIDs(b, h, s.FetchIDs)
	}
	if s.FetchParents != nil {
		FetchParents(b, h, s.FetchParents)
	}
	return h
}

// apply replays one EdgeSpec through the EdgeBuilder chain, calling only the
// methods whose field was declared so the *Set flags Compile reads stay
// faithful to the literal.
func (es EdgeSpec[Row]) apply(e *EdgeBuilder[Row]) {
	if es.ForeignKey != nil {
		e.ForeignKey(es.ForeignKey)
	}
	if es.ForeignKeys != nil {
		e.ForeignKeys(es.ForeignKeys)
	}
	if es.Guard != nil {
		e.Guard(es.Guard)
	}
	if es.Inverse != "" {
		e.Inverse(es.Inverse)
	}
	if es.Required {
		e.Required()
	}
	if es.Includable {
		e.Includable()
	}
	if es.Filterable {
		e.Filterable()
	}
	if es.Bare {
		e.Bare()
	}
	if es.Limit != 0 {
		e.Limit(es.Limit)
	}
	if es.EstimatedRows != 0 {
		e.EstimatedRows(es.EstimatedRows)
	}
	if es.Envelope != nil {
		e.Envelope(*es.Envelope)
	}
	if es.Sort != "" {
		e.Sort(es.Sort)
	}
	if len(es.Args) > 0 {
		e.Args(es.Args...)
	}
	if len(es.Policies) > 0 {
		e.Policies(es.Policies...)
	}
}
