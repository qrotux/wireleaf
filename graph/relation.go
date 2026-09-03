package graph

import "github.com/qrotux/wireleaf/include"

// ------------------------------------------------------------------ OneToMany

// OneToManyRelation is the chained configurator OneToMany returns: BOTH
// directions of a one-to-many link in one declaration. OneRow is the one-side
// node's Row (the reverse edge's parent), ManyRow the many-side node's Row
// (the to-one edge's parent, which carries the foreign key).
//
// Every setting is routed to the direction it belongs to: ForeignKey,
// Required and Policies to the to-one edge; Limit, Sort, Args, Bare and
// EstimatedRows to the reverse edge; Includable, Filterable and Envelope to
// both. One and Many expose each side's full EdgeBuilder for anything else
// (Guard lives there). Every method panics after Compile like the rest of
// the builder.
type OneToManyRelation[OneRow any, ManyRow any] struct {
	b        *Builder
	one      *EdgeBuilder[ManyRow] // to-one edge, on the MANY node
	many     *EdgeBuilder[OneRow]  // reverse edge, on the ONE node
	oneNode  *nodeSpec
	manyNode *nodeSpec
	manyDecl *edgeDecl // the reverse edge's declaration (ForeignKey sets its backref)
}

// OneToMany links one and many: many.<oneKey> is a to-one edge at one,
// one.<manyKey> a reverse edge at many, each the other's Inverse. It is sugar
// over NodeHandle.Edge — Compile sees two ordinary edges — for the assembly
// package that imports every domain package and links their handles: the
// targets and Row types are inferred from the handles, so no wire type is
// named and a wrong handle does not build.
//
// ForeignKey is mandatory (Compile reports a to-one edge without one): it
// declares the many-side foreign key by NAME and by reader in one call, and
// the name is what marks the reverse edge as reverse.
//
//	graph.OneToMany(author, book, "books", "author").
//	    ForeignKey("authorId", func(r bookRow) string { return r.AuthorID }).
//	    Includable().
//	    Limit(10)
func OneToMany[OneRow, OneWire, ManyRow, ManyWire any](
	one *NodeHandle[OneRow, OneWire], many *NodeHandle[ManyRow, ManyWire],
	manyKey, oneKey string,
) *OneToManyRelation[OneRow, ManyRow] {
	b := one.b
	b.mustLive()
	r := &OneToManyRelation[OneRow, ManyRow]{b: b, oneNode: one.spec, manyNode: many.spec}
	r.one = many.Edge(oneKey, ToOne[OneWire]()).Inverse(manyKey)
	lastDecl(many.spec).relation = "OneToMany"
	r.many = one.Edge(manyKey, Reverse[ManyWire]("")).Inverse(oneKey)
	r.manyDecl = lastDecl(one.spec)
	r.manyDecl.relation = "OneToMany"
	return r
}

// lastDecl returns the edge a NodeHandle.Edge call just appended.
func lastDecl(n *nodeSpec) *edgeDecl { return n.edges[len(n.edges)-1] }

// ForeignKey declares the many-side foreign key: field is its name on the
// many-side row as the doc layer knows it ("authorId"), fn reads it
// (EdgeBuilder.ForeignKey on the to-one edge). The name also becomes the
// reverse edge's backref — the discriminant that makes it a reverse edge,
// which Compile requires non-empty; no fetcher reads it. Last call wins.
func (r *OneToManyRelation[O, M]) ForeignKey(field string, fn func(M) string) *OneToManyRelation[O, M] {
	r.one.ForeignKey(fn)
	r.manyDecl.kind.backref = field
	return r
}

// Includable makes BOTH directions reachable from ?include=.
func (r *OneToManyRelation[O, M]) Includable() *OneToManyRelation[O, M] {
	r.one.Includable()
	r.many.Includable()
	return r
}

// Filterable lets filter conditions traverse BOTH directions
// (EdgeBuilder.Filterable; the reverse hop needs a client quantifier).
func (r *OneToManyRelation[O, M]) Filterable() *OneToManyRelation[O, M] {
	r.one.Filterable()
	r.many.Filterable()
	return r
}

// Envelope sets the wrapper style of BOTH edges.
func (r *OneToManyRelation[O, M]) Envelope(env include.Envelope) *OneToManyRelation[O, M] {
	r.one.Envelope(env)
	r.many.Envelope(env)
	return r
}

// Required marks the to-one edge never-null (EdgeBuilder.Required).
func (r *OneToManyRelation[O, M]) Required() *OneToManyRelation[O, M] {
	r.one.Required()
	return r
}

// Policies sets the to-one edge's policy overrides (EdgeBuilder.Policies).
func (r *OneToManyRelation[O, M]) Policies(ps ...include.EdgePolicy) *OneToManyRelation[O, M] {
	r.one.Policies(ps...)
	return r
}

// Limit sets the reverse edge's default top-N (EdgeBuilder.Limit).
func (r *OneToManyRelation[O, M]) Limit(n int) *OneToManyRelation[O, M] {
	r.many.Limit(n)
	return r
}

// Sort sets the reverse edge's default sort key (EdgeBuilder.Sort).
func (r *OneToManyRelation[O, M]) Sort(key string) *OneToManyRelation[O, M] {
	r.many.Sort(key)
	return r
}

// Args declares the reverse edge's client arguments (EdgeBuilder.Args).
func (r *OneToManyRelation[O, M]) Args(args ...EdgeArgOpt) *OneToManyRelation[O, M] {
	r.many.Args(args...)
	return r
}

// Bare makes the reverse edge a flat, fetch-all array (EdgeBuilder.Bare).
func (r *OneToManyRelation[O, M]) Bare() *OneToManyRelation[O, M] {
	r.many.Bare()
	return r
}

// EstimatedRows sets the reverse edge's cost estimate (EdgeBuilder.EstimatedRows).
func (r *OneToManyRelation[O, M]) EstimatedRows(n int) *OneToManyRelation[O, M] {
	r.many.EstimatedRows(n)
	return r
}

// One exposes the to-one edge's full EdgeBuilder (typed by the many-side Row)
// for settings the relation does not surface, such as Guard.
func (r *OneToManyRelation[O, M]) One(fn func(*EdgeBuilder[M])) *OneToManyRelation[O, M] {
	r.b.mustLive()
	fn(r.one)
	return r
}

// Many exposes the reverse edge's full EdgeBuilder (typed by the one-side Row).
func (r *OneToManyRelation[O, M]) Many(fn func(*EdgeBuilder[O])) *OneToManyRelation[O, M] {
	r.b.mustLive()
	fn(r.many)
	return r
}

// FetchParents binds the reverse fetcher of THIS relation's reverse edge
// (one.<manyKey>): many-side rows keyed by one-side id. It is FetchEdge on
// that edge, so the "how do I get an author's books" fact sits next to the
// link it serves, and other reverse edges into the many-side node keep their
// own fetchers. Re-binding is last-wins.
func (r *OneToManyRelation[O, M]) FetchParents(fn FetchByParents[M]) *OneToManyRelation[O, M] {
	r.b.mustLive()
	bindEdge(r.oneNode, r.manyDecl.key, r.manyNode, fn)
	return r
}

// ------------------------------------------------------------------ ManyToMany

// ManyToManyRelation is the chained configurator ManyToMany returns: BOTH
// directions of a many-to-many link whose id list lives on one side. LeftRow
// is the Row of the node that CARRIES the foreign-key list, RightRow the
// other node's Row.
//
// ForeignKeys and Policies go to the forward (left) edge; Includable,
// Filterable, Envelope and Limit to both (each side is an enveloped to-many
// edge). Left and Right expose each side's full EdgeBuilder (Sort and Args
// are reverse-only, so they live under Right). Every method panics after
// Compile like the rest of the builder.
type ManyToManyRelation[LeftRow any, RightRow any] struct {
	b         *Builder
	left      *EdgeBuilder[LeftRow]  // forward-hasMany edge, on the LEFT node
	right     *EdgeBuilder[RightRow] // reverse edge, on the RIGHT node
	leftNode  *nodeSpec
	rightNode *nodeSpec
	rightDecl *edgeDecl // the reverse edge's declaration (ForeignKeys sets its backref)
}

// ManyToMany links left and right where LEFT holds the id list:
// left.<leftKey> is a forward-hasMany edge at right, right.<rightKey> a
// reverse edge at left, each the other's Inverse. Like OneToMany it is sugar
// over NodeHandle.Edge with targets and Row types inferred from the handles.
//
// ForeignKeys is mandatory (Compile reports a forward-hasMany edge without
// one): it declares the left-side id list by NAME and by reader in one call,
// and the name is what marks the reverse edge as reverse.
//
//	graph.ManyToMany(book, tag, "tags", "tagged").
//	    ForeignKeys("tagIds", func(r bookRow) []string { return r.TagIDs }).
//	    Includable()
func ManyToMany[LeftRow, LeftWire, RightRow, RightWire any](
	left *NodeHandle[LeftRow, LeftWire], right *NodeHandle[RightRow, RightWire],
	leftKey, rightKey string,
) *ManyToManyRelation[LeftRow, RightRow] {
	b := left.b
	b.mustLive()
	r := &ManyToManyRelation[LeftRow, RightRow]{b: b, leftNode: left.spec, rightNode: right.spec}
	r.left = left.Edge(leftKey, ToMany[RightWire]()).Inverse(rightKey)
	lastDecl(left.spec).relation = "ManyToMany"
	r.right = right.Edge(rightKey, Reverse[LeftWire]("")).Inverse(leftKey)
	r.rightDecl = lastDecl(right.spec)
	r.rightDecl.relation = "ManyToMany"
	return r
}

// ForeignKeys declares the left-side id list: field is its name on the left
// row as the doc layer knows it ("tagIds"), fn reads it
// (EdgeBuilder.ForeignKeys on the forward edge). The name also becomes the
// reverse edge's backref — the discriminant that makes it a reverse edge,
// which Compile requires non-empty; no fetcher reads it. Last call wins.
func (r *ManyToManyRelation[L, R]) ForeignKeys(field string, fn func(L) []string) *ManyToManyRelation[L, R] {
	r.left.ForeignKeys(fn)
	r.rightDecl.kind.backref = field
	return r
}

// Includable makes BOTH directions reachable from ?include=.
func (r *ManyToManyRelation[L, R]) Includable() *ManyToManyRelation[L, R] {
	r.left.Includable()
	r.right.Includable()
	return r
}

// Filterable lets filter conditions traverse BOTH directions
// (EdgeBuilder.Filterable; each hop needs a client quantifier).
func (r *ManyToManyRelation[L, R]) Filterable() *ManyToManyRelation[L, R] {
	r.left.Filterable()
	r.right.Filterable()
	return r
}

// Envelope sets the wrapper style of BOTH edges.
func (r *ManyToManyRelation[L, R]) Envelope(env include.Envelope) *ManyToManyRelation[L, R] {
	r.left.Envelope(env)
	r.right.Envelope(env)
	return r
}

// Limit sets the default top-N of BOTH edges (each is enveloped to-many).
func (r *ManyToManyRelation[L, R]) Limit(n int) *ManyToManyRelation[L, R] {
	r.left.Limit(n)
	r.right.Limit(n)
	return r
}

// Policies sets the forward edge's policy overrides (MissingForeign is the
// one that applies to a forward-hasMany edge).
func (r *ManyToManyRelation[L, R]) Policies(ps ...include.EdgePolicy) *ManyToManyRelation[L, R] {
	r.left.Policies(ps...)
	return r
}

// Left exposes the forward edge's full EdgeBuilder (typed by the left Row).
func (r *ManyToManyRelation[L, R]) Left(fn func(*EdgeBuilder[L])) *ManyToManyRelation[L, R] {
	r.b.mustLive()
	fn(r.left)
	return r
}

// Right exposes the reverse edge's full EdgeBuilder (typed by the right Row):
// Sort and Args live there.
func (r *ManyToManyRelation[L, R]) Right(fn func(*EdgeBuilder[R])) *ManyToManyRelation[L, R] {
	r.b.mustLive()
	fn(r.right)
	return r
}

// FetchParents binds the reverse fetcher of THIS relation's reverse edge
// (right.<rightKey>): left rows keyed by right id. It is FetchEdge on that
// edge, so other reverse edges into the left node keep their own fetchers.
// Re-binding is last-wins.
func (r *ManyToManyRelation[L, R]) FetchParents(fn FetchByParents[L]) *ManyToManyRelation[L, R] {
	r.b.mustLive()
	bindEdge(r.rightNode, r.rightDecl.key, r.leftNode, fn)
	return r
}
