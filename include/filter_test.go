package include

import (
	"errors"
	"reflect"
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

// filterGraph: Book -author(filterable)-> Author, Book -editor-> Author (not
// filterable), Author -self(filterable)-> Author (for depth), Author -books->
// Book (reverse, not filterable), Author -filterableBooks-> Book (reverse and
// filterable, which only a hand-built graph can be).
func filterGraph() (book, author *fRes) {
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
	book.edges = map[string]Edge{
		"author": {Target: func() Resource { return author }, Filterable: true},
		"editor": {Target: func() Resource { return author }},
	}
	author.edges = map[string]Edge{
		"self":  {Target: func() Resource { return author }, Filterable: true},
		"books": {Target: func() Resource { return book }, Many: true, Backref: "authorId"},
		// declared filterable on purpose: a hand-built graph can do what
		// graph.Compile refuses, and the resolver must still refuse the hop
		"filterableBooks": {Target: func() Resource { return book }, Many: true, Backref: "authorId", Filterable: true},
	}
	return book, author
}

func TestResolveFilterNil(t *testing.T) {
	book, _ := filterGraph()
	got, err := ResolveFilter(book, nil, DefaultOptions)
	if err != nil || got != nil {
		t.Fatalf("ResolveFilter(nil) = %v, %v; want nil, nil", got, err)
	}
}

func TestResolveFilterRootColumn(t *testing.T) {
	book, _ := filterGraph()
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
}

func TestResolveFilterOneHop(t *testing.T) {
	book, author := filterGraph()
	got, err := ResolveFilter(book, FilterCond{Path: []string{"author"}, Field: "age", Op: OpGte, Value: 18}, DefaultOptions)
	if err != nil {
		t.Fatalf("ResolveFilter: %v", err)
	}
	c := got.(ResolvedCond)
	if len(c.Hops) != 1 {
		t.Fatalf("hops = %d, want 1", len(c.Hops))
	}
	h := c.Hops[0]
	if h.Key != "author" || h.From != Resource(book) || h.To != Resource(author) || !h.Edge.Filterable {
		t.Errorf("hop = %+v", h)
	}
	if c.Column.Col != "age" || c.Column.Type != reflect.TypeFor[int]() || c.Value != 18 {
		t.Errorf("resolved = %+v", c)
	}
}

func TestResolveFilterPreservesGroups(t *testing.T) {
	book, _ := filterGraph()
	f := FilterOr{
		FilterCond{Field: "title", Op: OpEq, Value: "a"},
		FilterAnd{
			FilterCond{Path: []string{"author"}, Field: "name", Op: OpNe, Value: "b"},
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

// Ordering operators: numbers, strings and time.Time yes, bool no.
func TestResolveFilterOrderingByType(t *testing.T) {
	book, _ := filterGraph()
	ok := []FilterCond{
		{Field: "createdAt", Op: OpLte, Value: "x"},
		{Field: "title", Op: OpGt, Value: "x"},
		{Path: []string{"author"}, Field: "age", Op: OpLt, Value: 3},
		{Path: []string{"author"}, Field: "active", Op: OpEq, Value: true},
		{Path: []string{"author"}, Field: "active", Op: OpIn, Value: []any{true}},
	}
	for _, c := range ok {
		if _, err := ResolveFilter(book, c, DefaultOptions); err != nil {
			t.Errorf("%+v: unexpected error %v", c, err)
		}
	}
}

func TestResolveFilterErrors(t *testing.T) {
	book, _ := filterGraph()
	cases := []struct {
		name string
		root Resource
		f    Filter
		opts Options
		code Code
		path string
	}{
		{name: "unknown field", root: book, f: FilterCond{Field: "nope", Op: OpEq}, opts: DefaultOptions, code: INVALID_FILTER, path: "nope"},
		{name: "non-filterable column", root: book, f: FilterCond{Path: []string{"author"}, Field: "email", Op: OpEq}, opts: DefaultOptions, code: INVALID_FILTER, path: "author.email"},
		{name: "unknown edge", root: book, f: FilterCond{Path: []string{"ghost"}, Field: "name", Op: OpEq}, opts: DefaultOptions, code: INVALID_FILTER, path: "ghost"},
		{name: "non-filterable edge", root: book, f: FilterCond{Path: []string{"editor"}, Field: "name", Op: OpEq}, opts: DefaultOptions, code: INVALID_FILTER, path: "editor"},
		{name: "filterable to-many edge", root: book, f: FilterCond{Path: []string{"author", "filterableBooks"}, Field: "title", Op: OpEq}, opts: DefaultOptions, code: INVALID_FILTER, path: "author.filterableBooks"},
		{name: "column without a type", root: &fRes{name: "Typeless", cols: map[string]Column{"x": {Col: "x", Filterable: true}}}, f: FilterCond{Field: "x", Op: OpEq}, opts: DefaultOptions, code: INVALID_FILTER, path: "x:eq"},
		{name: "non-filterable edge deeper", root: book, f: FilterCond{Path: []string{"author", "books"}, Field: "title", Op: OpEq}, opts: DefaultOptions, code: INVALID_FILTER, path: "author.books"},
		{name: "unknown operator", root: book, f: FilterCond{Field: "title", Op: FilterOp("like")}, opts: DefaultOptions, code: INVALID_FILTER, path: "title:like"},
		{name: "ordering on bool", root: book, f: FilterCond{Path: []string{"author"}, Field: "active", Op: OpGt}, opts: DefaultOptions, code: INVALID_FILTER, path: "author.active:gt"},
		{name: "root without columns", root: &tRes{name: "Bare"}, f: FilterCond{Field: "x", Op: OpEq}, opts: DefaultOptions, code: INVALID_FILTER, path: "x"},
		{name: "empty group", root: book, f: FilterAnd{}, opts: DefaultOptions, code: INVALID_FILTER, path: ""},
		{name: "nil group element", root: book, f: FilterOr{nil}, opts: DefaultOptions, code: INVALID_FILTER, path: ""},
		{name: "nil *FilterCond", root: book, f: (*FilterCond)(nil), opts: DefaultOptions, code: INVALID_FILTER, path: ""},
		{name: "nil *FilterAnd", root: book, f: (*FilterAnd)(nil), opts: DefaultOptions, code: INVALID_FILTER, path: ""},
		{name: "nil *FilterOr", root: book, f: (*FilterOr)(nil), opts: DefaultOptions, code: INVALID_FILTER, path: ""},
		{name: "too deep", root: book, f: FilterCond{Path: []string{"author", "self"}, Field: "name", Op: OpEq}, opts: Options{Limits: Limits{MaxFilterDepth: 1}}, code: FILTER_TOO_DEEP, path: "author.self…"},
		{name: "too deep at the default of 2", root: book, f: FilterCond{Path: []string{"author", "self", "self"}, Field: "name", Op: OpEq}, opts: Options{}, code: FILTER_TOO_DEEP, path: "author.self.self…"},
		{name: "too many nodes", root: book, f: FilterAnd{FilterCond{Field: "title", Op: OpEq}, FilterCond{Field: "title", Op: OpEq}, FilterCond{Field: "title", Op: OpEq}}, opts: Options{Limits: Limits{MaxFilterNodes: 3}}, code: FILTER_TOO_DEEP, path: ""},
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

// The pointer forms of the AST nodes resolve exactly like their value twins.
func TestResolveFilterPointerNodes(t *testing.T) {
	book, _ := filterGraph()

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

// Depth 2 is the default: author.self.name is exactly at the limit.
func TestResolveFilterDefaultDepth(t *testing.T) {
	book, _ := filterGraph()
	if _, err := ResolveFilter(book, FilterCond{Path: []string{"author", "self"}, Field: "name", Op: OpEq}, Options{}); err != nil {
		t.Errorf("depth 2 under zero limits: %v (0 must mean DefaultLimits.MaxFilterDepth = 2)", err)
	}
}
