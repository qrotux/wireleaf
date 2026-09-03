package include

import (
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"
)

// fRes is an inline Resource WITH columns (the ColumnSource seam) for the
// filter resolver tests. tRes (resolve_test.go) stays the column-less one.
type fRes struct {
	name  string
	edges map[string]Edge
	cols  map[string]Column
}

func (r *fRes) Name() string               { return r.name }
func (r *fRes) Slug() string               { return r.name }
func (r *fRes) Fields() []string           { return nil }
func (r *fRes) Defaults() []string         { return nil }
func (r *fRes) Edges() map[string]Edge     { return r.edges }
func (r *fRes) IDOf(any) string            { return "" }
func (r *fRes) Serialize(any, *Ctx) any    { return nil }
func (r *fRes) Enrich([]any, *Ctx) error   { return nil }
func (r *fRes) Columns() map[string]Column { return r.cols }

// steps builds a to-one-only path: every step has an empty quantifier.
func steps(keys ...string) []FilterStep {
	out := make([]FilterStep, len(keys))
	for i, k := range keys {
		out[i] = FilterStep{Key: k}
	}
	return out
}

// filterGraph: three hand-built nodes.
//
//	Book   -author(to-one, F)-> Author   -editor(to-one)-> Author
//	Book   -reviews(many, F)-> Review
//	Author -self(to-one, F)-> Author     -books(many)-> Book
//	Author -works(many, F)-> Book
//	Review -author(to-one, F)-> Author
//	Review -replies(many, F)-> Review    (self-referencing: four to-many hops)
//
// (F = Filterable.) `books` stays non-filterable so the "non-filterable edge
// deeper" case still exists; `works` is its filterable twin.
func filterGraph() (book, author, review *fRes) {
	str := reflect.TypeFor[string]()
	author = &fRes{name: "Author", cols: map[string]Column{
		"name":   {Col: "name", Type: str, Filterable: true},
		"email":  {Col: "email", Type: str}, // bound, NOT filterable
		"age":    {Col: "age", Type: reflect.TypeFor[int](), Filterable: true},
		"active": {Col: "active", Type: reflect.TypeFor[bool](), Filterable: true},
	}}
	book = &fRes{name: "Book", cols: map[string]Column{
		"title":     {Col: "title", Type: str, Filterable: true},
		"createdAt": {Col: "created_at", Type: reflect.TypeFor[time.Time](), Filterable: true},
	}}
	review = &fRes{name: "Review", cols: map[string]Column{
		"rating": {Col: "rating", Type: reflect.TypeFor[int](), Filterable: true},
		"text":   {Col: "text", Type: str, Filterable: true},
	}}
	book.edges = map[string]Edge{
		"author":  {Target: func() Resource { return author }, Filterable: true},
		"editor":  {Target: func() Resource { return author }},
		"reviews": {Target: func() Resource { return review }, Many: true, Backref: "bookId", Filterable: true},
	}
	author.edges = map[string]Edge{
		"self":  {Target: func() Resource { return author }, Filterable: true},
		"books": {Target: func() Resource { return book }, Many: true, Backref: "authorId"},
		"works": {Target: func() Resource { return book }, Many: true, Backref: "authorId", Filterable: true},
	}
	review.edges = map[string]Edge{
		"author":  {Target: func() Resource { return author }, Filterable: true},
		"replies": {Target: func() Resource { return review }, Many: true, Backref: "parentId", Filterable: true},
	}
	return book, author, review
}

// nilTargetRoot is a hand-built root whose filterable edge resolves to a nil
// Resource — graph.Compile never produces one, ResolveFilter must still refuse
// it rather than walk into a nil node.
func nilTargetRoot() Resource {
	return &fRes{name: "Nil", edges: map[string]Edge{
		"ghost": {Target: func() Resource { return nil }, Filterable: true},
	}}
}

func TestResolveFilterNil(t *testing.T) {
	book, _, _ := filterGraph()
	got, err := ResolveFilter(book, nil, DefaultOptions)
	if err != nil || got != nil {
		t.Fatalf("ResolveFilter(nil) = %v, %v; want nil, nil", got, err)
	}
}

func TestResolveFilterRootColumn(t *testing.T) {
	book, _, _ := filterGraph()
	got, err := ResolveFilter(book, FilterCond{Field: "title", Op: OpEq, Value: "Dune"}, DefaultOptions)
	if err != nil {
		t.Fatalf("ResolveFilter: %v", err)
	}
	c, ok := got.(ResolvedCond)
	if !ok {
		t.Fatalf("resolved = %T, want ResolvedCond", got)
	}
	if len(c.Hops) != 0 || c.Column.Col != "title" || c.Op != OpEq || c.Value != "Dune" {
		t.Errorf("resolved = %+v", c)
	}
	// A root column's Node is the root itself: the leaf names its own table
	// without the caller carrying the root in.
	if c.Node != Resource(book) {
		t.Errorf("node = %v, want the root", c.Node)
	}
}

func TestResolveFilterOneHop(t *testing.T) {
	book, author, _ := filterGraph()
	got, err := ResolveFilter(book, FilterCond{Path: steps("author"), Field: "age", Op: OpGte, Value: 18}, DefaultOptions)
	if err != nil {
		t.Fatalf("ResolveFilter: %v", err)
	}
	c := got.(ResolvedCond)
	if len(c.Hops) != 1 {
		t.Fatalf("hops = %d, want 1", len(c.Hops))
	}
	h := c.Hops[0]
	if h.Key != "author" || h.Quant != "" || h.From != Resource(book) || h.To != Resource(author) || !h.Edge.Filterable {
		t.Errorf("hop = %+v", h)
	}
	if c.Column.Col != "age" || c.Column.Type != reflect.TypeFor[int]() || c.Value != 18 {
		t.Errorf("resolved = %+v", c)
	}
	if c.Node != Resource(author) {
		t.Errorf("node = %v, want the last hop's target", c.Node)
	}
}

// One to-many hop with each quantifier: the quantifier lands on the hop, the
// column is the target's.
func TestResolveFilterQuantifiers(t *testing.T) {
	book, _, review := filterGraph()
	for _, q := range []Quant{QuantAny, QuantAll, QuantNone} {
		f := FilterCond{Path: []FilterStep{{Key: "reviews", Quant: q}}, Field: "rating", Op: OpGt, Value: 4}
		got, err := ResolveFilter(book, f, DefaultOptions)
		if err != nil {
			t.Fatalf("%s: %v", q, err)
		}
		c := got.(ResolvedCond)
		if len(c.Hops) != 1 || c.Hops[0].Key != "reviews" || c.Hops[0].Quant != q || c.Hops[0].To != Resource(review) {
			t.Errorf("%s: hops = %+v", q, c.Hops)
		}
		if c.Column.Col != "rating" || c.Op != OpGt || c.Value != 4 {
			t.Errorf("%s: resolved = %+v", q, c)
		}
	}
}

// The to-many hop need not be the last step: reviews(any).author.name is a
// quantified hop followed by a plain to-one hop.
func TestResolveFilterManyHopNotLast(t *testing.T) {
	book, _, _ := filterGraph()
	f := FilterCond{Path: []FilterStep{{Key: "reviews", Quant: QuantAny}, {Key: "author"}}, Field: "name", Op: OpEq, Value: "x"}
	got, err := ResolveFilter(book, f, DefaultOptions)
	if err != nil {
		t.Fatalf("ResolveFilter: %v", err)
	}
	c := got.(ResolvedCond)
	if len(c.Hops) != 2 || c.Hops[0].Quant != QuantAny || c.Hops[1].Quant != "" || c.Hops[1].Key != "author" {
		t.Errorf("hops = %+v", c.Hops)
	}
	if c.Column.Col != "name" {
		t.Errorf("column = %+v", c.Column)
	}
}

// Nested to-many hops each carry their own quantifier.
func TestResolveFilterNestedMany(t *testing.T) {
	book, _, _ := filterGraph()
	f := FilterCond{Path: []FilterStep{
		{Key: "reviews", Quant: QuantAll},
		{Key: "author"},
		{Key: "works", Quant: QuantNone},
	}, Field: "title", Op: OpEq, Value: "x"}
	got, err := ResolveFilter(book, f, DefaultOptions)
	if err != nil {
		t.Fatalf("ResolveFilter: %v", err)
	}
	c := got.(ResolvedCond)
	want := []Quant{QuantAll, "", QuantNone}
	if len(c.Hops) != 3 {
		t.Fatalf("hops = %d, want 3", len(c.Hops))
	}
	for i, h := range c.Hops {
		if h.Quant != want[i] {
			t.Errorf("hop %d (%s) quant = %q, want %q", i, h.Key, h.Quant, want[i])
		}
	}
	if c.Hops[2].To != Resource(book) || c.Column.Col != "title" {
		t.Errorf("leaf = %+v / %+v", c.Hops[2].To, c.Column)
	}
}

func TestResolveFilterPreservesGroups(t *testing.T) {
	book, _, _ := filterGraph()
	f := FilterOr{
		FilterCond{Field: "title", Op: OpEq, Value: "a"},
		FilterAnd{
			FilterCond{Path: steps("author"), Field: "name", Op: OpNe, Value: "b"},
			FilterCond{Field: "createdAt", Op: OpLt, Value: "2024-01-01"},
		},
	}
	got, err := ResolveFilter(book, f, DefaultOptions)
	if err != nil {
		t.Fatalf("ResolveFilter: %v", err)
	}
	or, ok := got.(ResolvedOr)
	if !ok || len(or) != 2 {
		t.Fatalf("resolved = %#v, want a 2-element ResolvedOr", got)
	}
	and, ok := or[1].(ResolvedAnd)
	if !ok || len(and) != 2 {
		t.Fatalf("or[1] = %#v, want a 2-element ResolvedAnd", or[1])
	}
	if c := and[1].(ResolvedCond); c.Column.Col != "created_at" || c.Op != OpLt || c.Value != "2024-01-01" {
		t.Errorf("and[1] = %+v (values must pass through verbatim)", c)
	}
}

// Ordering operators: numbers, strings and time.Time yes, bool no (the bool
// rejection lives in TestResolveFilterErrors).
func TestResolveFilterOrderingByType(t *testing.T) {
	book, _, _ := filterGraph()
	ok := []FilterCond{
		{Field: "createdAt", Op: OpLte, Value: "x"},
		{Field: "title", Op: OpGt, Value: "x"},
		{Path: steps("author"), Field: "age", Op: OpLt, Value: 3},
		{Path: steps("author"), Field: "active", Op: OpEq, Value: true},
		{Path: steps("author"), Field: "active", Op: OpIn, Value: []any{true}},
	}
	for _, c := range ok {
		if _, err := ResolveFilter(book, c, DefaultOptions); err != nil {
			t.Errorf("%+v: unexpected error %v", c, err)
		}
	}
}

func TestResolveFilterErrors(t *testing.T) {
	book, _, _ := filterGraph()
	threeMany := []FilterStep{
		{Key: "reviews", Quant: QuantAny}, {Key: "author"}, {Key: "works", Quant: QuantAny}, {Key: "reviews", Quant: QuantAny},
	}
	// longKey is an unbounded client key; cutKey is what clientEcho makes of it.
	longKey := strings.Repeat("x", 40)
	cutKey := strings.Repeat("x", 16) + "…"
	cases := []struct {
		name string
		root Resource
		f    Filter
		opts Options
		code Code
		path string
	}{
		{name: "unknown field", root: book, f: FilterCond{Field: "nope", Op: OpEq}, opts: DefaultOptions, code: INVALID_FILTER, path: "nope"},
		{name: "non-filterable column", root: book, f: FilterCond{Path: steps("author"), Field: "email", Op: OpEq}, opts: DefaultOptions, code: INVALID_FILTER, path: "author.email"},
		{name: "unknown edge", root: book, f: FilterCond{Path: steps("ghost"), Field: "name", Op: OpEq}, opts: DefaultOptions, code: INVALID_FILTER, path: "ghost"},
		{name: "overlong unknown edge key", root: book, f: FilterCond{Path: steps(strings.Repeat("x", 40)), Field: "name", Op: OpEq}, opts: DefaultOptions, code: INVALID_FILTER, path: strings.Repeat("x", 16) + "\u2026"},
		{name: "overlong unknown edge key deeper", root: book, f: FilterCond{Path: steps("author", strings.Repeat("x", 40)), Field: "name", Op: OpEq}, opts: DefaultOptions, code: INVALID_FILTER, path: "author." + strings.Repeat("x", 16) + "\u2026"},
		{name: "empty edge key", root: book, f: FilterCond{Path: steps(""), Field: "name", Op: OpEq}, opts: DefaultOptions, code: INVALID_FILTER, path: ""},
		{name: "overlong unknown field", root: book, f: FilterCond{Field: strings.Repeat("x", 40), Op: OpEq}, opts: DefaultOptions, code: INVALID_FILTER, path: strings.Repeat("x", 16) + "\u2026"},
		{name: "overlong unknown operator", root: book, f: FilterCond{Field: "title", Op: FilterOp(strings.Repeat("x", 40))}, opts: DefaultOptions, code: INVALID_FILTER, path: "title:" + strings.Repeat("x", 16) + "\u2026"},
		{name: "multibyte quantifier", root: book, f: FilterCond{Path: []FilterStep{{Key: "reviews", Quant: Quant(strings.Repeat("\u2192", 20))}}, Field: "rating", Op: OpEq}, opts: DefaultOptions, code: INVALID_FILTER, path: "reviews:" + strings.Repeat("\u2192", 5) + "\u2026"},
		{name: "filterable edge with a nil target result", root: nilTargetRoot(), f: FilterCond{Path: steps("ghost"), Field: "name", Op: OpEq}, opts: DefaultOptions, code: INVALID_FILTER, path: "ghost"},
		{name: "non-filterable edge", root: book, f: FilterCond{Path: steps("editor"), Field: "name", Op: OpEq}, opts: DefaultOptions, code: INVALID_FILTER, path: "editor"},
		{name: "non-filterable edge deeper", root: book, f: FilterCond{Path: []FilterStep{{Key: "author"}, {Key: "books", Quant: QuantAny}}, Field: "title", Op: OpEq}, opts: DefaultOptions, code: INVALID_FILTER, path: "author.books"},
		{name: "to-many hop without a quantifier", root: book, f: FilterCond{Path: steps("reviews"), Field: "rating", Op: OpEq}, opts: DefaultOptions, code: INVALID_FILTER, path: "reviews"},
		{name: "to-many hop without a quantifier, deeper", root: book, f: FilterCond{Path: steps("author", "works"), Field: "title", Op: OpEq}, opts: DefaultOptions, code: INVALID_FILTER, path: "author.works"},
		{name: "quantifier on a to-one hop", root: book, f: FilterCond{Path: []FilterStep{{Key: "author", Quant: QuantAny}}, Field: "name", Op: OpEq}, opts: DefaultOptions, code: INVALID_FILTER, path: "author:any"},
		{name: "unknown quantifier on a to-one hop", root: book, f: FilterCond{Path: []FilterStep{{Key: "author", Quant: Quant("bogus")}}, Field: "name", Op: OpEq}, opts: DefaultOptions, code: INVALID_FILTER, path: "author:bogus"},
		{name: "unknown quantifier", root: book, f: FilterCond{Path: []FilterStep{{Key: "reviews", Quant: Quant("some")}}, Field: "rating", Op: OpEq}, opts: DefaultOptions, code: INVALID_FILTER, path: "reviews:some"},
		{name: "overlong quantifier", root: book, f: FilterCond{Path: []FilterStep{{Key: "reviews", Quant: Quant(strings.Repeat("x", 40))}}, Field: "rating", Op: OpEq}, opts: DefaultOptions, code: INVALID_FILTER, path: "reviews:" + strings.Repeat("x", 16) + "\u2026"},
		{name: "column without a type", root: &fRes{name: "Typeless", cols: map[string]Column{"x": {Col: "x", Filterable: true}}}, f: FilterCond{Field: "x", Op: OpEq}, opts: DefaultOptions, code: INVALID_FILTER, path: "x:eq"},
		// A hand-built source may mark a column of any type filterable;
		// graph.Compile refuses this one at compile time, ResolveFilter at
		// resolve time — one FilterableType behind both.
		{name: "filterable column of an unsupported type", root: &fRes{name: "Tagged", cols: map[string]Column{"tags": {Col: "tags", Type: reflect.TypeFor[[]string](), Filterable: true}}}, f: FilterCond{Field: "tags", Op: OpEq}, opts: DefaultOptions, code: INVALID_FILTER, path: "tags:eq"},
		{name: "unknown operator", root: book, f: FilterCond{Field: "title", Op: FilterOp("like")}, opts: DefaultOptions, code: INVALID_FILTER, path: "title:like"},
		{name: "ordering on bool", root: book, f: FilterCond{Path: steps("author"), Field: "active", Op: OpGt}, opts: DefaultOptions, code: INVALID_FILTER, path: "author.active:gt"},
		{name: "root without columns", root: &tRes{name: "Bare"}, f: FilterCond{Field: "x", Op: OpEq}, opts: DefaultOptions, code: INVALID_FILTER, path: "x"},
		{name: "empty group", root: book, f: FilterAnd{}, opts: DefaultOptions, code: INVALID_FILTER, path: ""},
		// A structural fault has no field path, so it reports its POSITION in
		// the tree. The root itself has none — an empty root group stays the
		// one ambiguous "" — but everything below it is addressable.
		{name: "nil group element", root: book, f: FilterOr{nil}, opts: DefaultOptions, code: INVALID_FILTER, path: "or[0]"},
		{name: "empty nested group", root: book, f: FilterAnd{FilterCond{Field: "title", Op: OpEq}, FilterOr{}}, opts: DefaultOptions, code: INVALID_FILTER, path: "and[1]"},
		{name: "nil member of a nested group", root: book, f: FilterOr{FilterAnd{FilterCond{Field: "title", Op: OpEq}, nil}}, opts: DefaultOptions, code: INVALID_FILTER, path: "or[0].and[1]"},
		{name: "nil *FilterCond", root: book, f: (*FilterCond)(nil), opts: DefaultOptions, code: INVALID_FILTER, path: ""},
		{name: "nil *FilterAnd", root: book, f: (*FilterAnd)(nil), opts: DefaultOptions, code: INVALID_FILTER, path: ""},
		{name: "nil *FilterOr", root: book, f: (*FilterOr)(nil), opts: DefaultOptions, code: INVALID_FILTER, path: ""},
		{name: "too deep", root: book, f: FilterCond{Path: steps("author", "self"), Field: "name", Op: OpEq}, opts: Options{Limits: Limits{MaxFilterDepth: 1}}, code: FILTER_TOO_DEEP, path: "author.self"},
		{name: "too deep at the default of 4", root: book, f: FilterCond{Path: steps("author", "self", "self", "self", "self"), Field: "name", Op: OpEq}, opts: Options{}, code: FILTER_TOO_DEEP, path: "author.self.self.self.self"},
		// The trailing "…" marks segments DROPPED, so it appears only when the
		// path is longer than the MaxFilterDepth+1 window actually reported:
		// six hops at the default of 4 truncate, five do not.
		{name: "too deep past the reported window", root: book, f: FilterCond{Path: steps("author", "self", "self", "self", "self", "self"), Field: "name", Op: OpEq}, opts: Options{}, code: FILTER_TOO_DEEP, path: "author.self.self.self.self\u2026"},
		{name: "too many to-many hops", root: book, f: FilterCond{Path: threeMany, Field: "rating", Op: OpEq}, opts: Options{Limits: Limits{MaxFilterMany: 2}}, code: FILTER_TOO_DEEP, path: "reviews.author.works.reviews"},
		// A too-deep path is refused before any hop is looked up, so EVERY key
		// in it is raw client text: the count of segments is bounded and so is
		// each one.
		{name: "too deep with overlong keys", root: book, f: FilterCond{Path: steps(longKey, longKey), Field: "name", Op: OpEq}, opts: Options{Limits: Limits{MaxFilterDepth: 1}}, code: FILTER_TOO_DEEP, path: cutKey + "." + cutKey},
		// The unknown SECOND key fires before the many bound does: hop 0 is the
		// first to-many hop and sits exactly at MaxFilterMany 1, and hop 1 is
		// never resolved as an edge at all.
		{name: "overlong key at a many-bounded hop", root: book, f: FilterCond{Path: []FilterStep{{Key: "reviews", Quant: QuantAny}, {Key: longKey, Quant: QuantAny}}, Field: "rating", Op: OpEq}, opts: Options{Limits: Limits{MaxFilterMany: 1}}, code: INVALID_FILTER, path: "reviews." + cutKey},
		{name: "nil root", root: nil, f: FilterCond{Field: "title", Op: OpEq}, opts: DefaultOptions, code: INVALID_FILTER, path: ""},
		{name: "too many nodes", root: book, f: FilterAnd{FilterCond{Field: "title", Op: OpEq}, FilterCond{Field: "title", Op: OpEq}, FilterCond{Field: "title", Op: OpEq}}, opts: Options{Limits: Limits{MaxFilterNodes: 3}}, code: FILTER_TOO_DEEP, path: ""},
		// The two bounds meet on one path: MaxFilterMany is the per-PATH one and
		// is reached first, on the third to-many hop, so the client is told the
		// path is too deep — not that the tree is too expensive.
		{name: "per-path many bound beats the tree bound", root: book, f: FilterCond{Path: []FilterStep{{Key: "reviews", Quant: QuantAny}, {Key: "replies", Quant: QuantAny}, {Key: "replies", Quant: QuantAny}}, Field: "rating", Op: OpEq}, opts: Options{Limits: Limits{MaxFilterSubqueries: 1}}, code: FILTER_TOO_DEEP, path: "reviews.replies.replies"},
		// Two paths of two to-many hops each: neither breaks MaxFilterMany, the
		// four subqueries together break the tree bound. Tree-wide fault, no path.
		{name: "too many subqueries across conditions", root: book, f: FilterAnd{FilterCond{Path: []FilterStep{{Key: "reviews", Quant: QuantAny}, {Key: "replies", Quant: QuantAny}}, Field: "rating", Op: OpEq}, FilterCond{Path: []FilterStep{{Key: "reviews", Quant: QuantAny}, {Key: "replies", Quant: QuantAny}}, Field: "rating", Op: OpEq}}, opts: Options{Limits: Limits{MaxFilterSubqueries: 3}}, code: FILTER_TOO_EXPENSIVE, path: ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := ResolveFilter(tc.root, tc.f, tc.opts)
			var e *Error
			if !errors.As(err, &e) {
				t.Fatalf("err = %v, want *Error", err)
			}
			if e.Code != tc.code || e.Path != tc.path || e.Status != 400 {
				t.Errorf("err = %+v, want Code %s Path %q Status 400", e, tc.code, tc.path)
			}
		})
	}
}

// The default MaxFilterMany is 2 inside a depth of 4, so it bites on its own:
// a four-hop to-many chain is within MaxFilterDepth and still refused, at the
// THIRD to-many hop. The reported path is the client's WHOLE path (the fault
// is the shape of the path, not one hop of it) and carries NO trailing "…" —
// a many fault is bounded by MaxFilterDepth, so nothing was ever dropped.
func TestResolveFilterDefaultMany(t *testing.T) {
	book, _, _ := filterGraph()
	path := []FilterStep{
		{Key: "reviews", Quant: QuantAny},
		{Key: "replies", Quant: QuantAny},
		{Key: "replies", Quant: QuantAny},
		{Key: "replies", Quant: QuantAny},
	}
	f := FilterCond{Path: path, Field: "rating", Op: OpEq, Value: 5}

	_, err := ResolveFilter(book, f, Options{})
	var e *Error
	if !errors.As(err, &e) {
		t.Fatalf("four to-many hops under zero limits: err = %v, want *Error (0 must mean DefaultLimits.MaxFilterMany = 2)", err)
	}
	if e.Code != FILTER_TOO_DEEP || e.Path != "reviews.replies.replies.replies" {
		t.Errorf("zero limits: err = %+v, want FILTER_TOO_DEEP at the third to-many hop", e)
	}

	got, err := ResolveFilter(book, f, Options{Limits: Limits{MaxFilterMany: 4}})
	if err != nil {
		t.Fatalf("four to-many hops under MaxFilterMany 4: %v", err)
	}
	c, ok := got.(ResolvedCond)
	if !ok || len(c.Hops) != 4 {
		t.Fatalf("resolved = %#v, want a 4-hop ResolvedCond", got)
	}
	for i, h := range c.Hops {
		if h.Quant != QuantAny {
			t.Errorf("hop %d (%s) quant = %q, want %q", i, h.Key, h.Quant, QuantAny)
		}
	}

	_, err = ResolveFilter(book, f, Options{Limits: Limits{MaxFilterMany: 3}})
	if !errors.As(err, &e) {
		t.Fatalf("MaxFilterMany 3: err = %v, want *Error", err)
	}
	if e.Code != FILTER_TOO_DEEP || e.Path != "reviews.replies.replies.replies" {
		t.Errorf("MaxFilterMany 3: err = %+v, want FILTER_TOO_DEEP at the fourth to-many hop", e)
	}
}

// The pointer forms of the AST nodes resolve exactly like their value twins.
func TestResolveFilterPointerNodes(t *testing.T) {
	book, _, _ := filterGraph()

	got, err := ResolveFilter(book, &FilterCond{Field: "title", Op: OpEq, Value: "Dune"}, DefaultOptions)
	if err != nil {
		t.Fatalf("*FilterCond: %v", err)
	}
	c, ok := got.(ResolvedCond)
	if !ok {
		t.Fatalf("*FilterCond resolved = %T, want ResolvedCond", got)
	}
	if len(c.Hops) != 0 || c.Column.Col != "title" || c.Op != OpEq || c.Value != "Dune" {
		t.Errorf("*FilterCond resolved = %+v", c)
	}

	got, err = ResolveFilter(book, &FilterAnd{FilterCond{Field: "title", Op: OpEq, Value: "Dune"}}, DefaultOptions)
	if err != nil {
		t.Fatalf("*FilterAnd: %v", err)
	}
	and, ok := got.(ResolvedAnd)
	if !ok || len(and) != 1 {
		t.Fatalf("*FilterAnd resolved = %#v, want a 1-element ResolvedAnd", got)
	}

	got, err = ResolveFilter(book, &FilterOr{FilterCond{Field: "title", Op: OpEq, Value: "Dune"}}, DefaultOptions)
	if err != nil {
		t.Fatalf("*FilterOr: %v", err)
	}
	if or, ok := got.(ResolvedOr); !ok || len(or) != 1 {
		t.Fatalf("*FilterOr resolved = %#v, want a 1-element ResolvedOr", got)
	}
}

// Depth 4 is the default: author.self.self.self.name is exactly at the limit.
func TestResolveFilterDefaultDepth(t *testing.T) {
	book, _, _ := filterGraph()
	if _, err := ResolveFilter(book, FilterCond{Path: steps("author", "self", "self", "self"), Field: "name", Op: OpEq}, Options{}); err != nil {
		t.Errorf("depth 4 under zero limits: %v (0 must mean DefaultLimits.MaxFilterDepth = 4)", err)
	}
}

// ------------------------------------------------------------------ subqueries
//
// MaxFilterSubqueries bounds the to-many hops of the WHOLE tree, where
// MaxFilterMany bounds those of one path: four independent one-hop conditions
// are four correlated subqueries, and nothing else in Limits counts them.

func TestResolveFilterSubqueries(t *testing.T) {
	book, _, _ := filterGraph()
	oneHop := FilterCond{Path: []FilterStep{{Key: "reviews", Quant: QuantAny}}, Field: "rating", Op: OpEq, Value: 5}
	four := FilterAnd{oneHop, oneHop, oneHop, oneHop}

	got, err := ResolveFilter(book, four, Options{})
	if err != nil {
		t.Fatalf("ResolveFilter(4 hops, defaults) = %v, want nil", err)
	}
	if n := FilterSubqueries(got); n != 4 {
		t.Errorf("FilterSubqueries = %d, want 4", n)
	}

	_, err = ResolveFilter(book, four, Options{Limits: Limits{MaxFilterSubqueries: 3}})
	var e *Error
	if !errors.As(err, &e) {
		t.Fatalf("err = %v, want *Error", err)
	}
	if e.Code != FILTER_TOO_EXPENSIVE || e.Path != "" || e.Status != 400 {
		t.Errorf("err = %+v, want FILTER_TOO_EXPENSIVE, Path \"\", Status 400", e)
	}

	// A tree that crosses no to-many edge costs nothing, whatever the bound.
	flat, err := ResolveFilter(book, FilterCond{Path: steps("author"), Field: "name", Op: OpEq},
		Options{Limits: Limits{MaxFilterSubqueries: 1}})
	if err != nil {
		t.Fatalf("ResolveFilter(to-one) = %v, want nil", err)
	}
	if n := FilterSubqueries(flat); n != 0 {
		t.Errorf("FilterSubqueries(to-one) = %d, want 0", n)
	}
	if n := FilterSubqueries(nil); n != 0 {
		t.Errorf("FilterSubqueries(nil) = %d, want 0", n)
	}
}

// The default of 8 bites on the ninth one-hop condition, a tree well inside
// MaxFilterNodes (32) and MaxFilterMany (2).
func TestResolveFilterDefaultSubqueries(t *testing.T) {
	book, _, _ := filterGraph()
	oneHop := FilterCond{Path: []FilterStep{{Key: "reviews", Quant: QuantAny}}, Field: "rating", Op: OpEq, Value: 5}
	conds := func(n int) FilterAnd {
		out := make(FilterAnd, n)
		for i := range out {
			out[i] = oneHop
		}
		return out
	}
	got, err := ResolveFilter(book, conds(8), Options{})
	if err != nil {
		t.Fatalf("ResolveFilter(8) = %v, want nil", err)
	}
	if n := FilterSubqueries(got); n != 8 {
		t.Errorf("FilterSubqueries = %d, want 8", n)
	}
	_, err = ResolveFilter(book, conds(9), Options{})
	var e *Error
	if !errors.As(err, &e) {
		t.Fatalf("err = %v, want *Error", err)
	}
	if e.Code != FILTER_TOO_EXPENSIVE || e.Path != "" {
		t.Errorf("err = %+v, want FILTER_TOO_EXPENSIVE, Path \"\"", e)
	}
}
