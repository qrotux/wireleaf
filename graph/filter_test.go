package graph

import (
	"errors"
	"reflect"
	"testing"

	"github.com/qrotux/wireleaf/include"
)

// End-to-end over a COMPILED graph: col tags → Filterable() → Compile →
// ResolveFilter → HydrateByQuery hands the resolved filter to the RootFetcher.

type fbRow struct{ ID, AuthorID, Title string }

type FbWire struct {
	ID     string `json:"id" col:"id,filter"`
	Title  string `json:"title" col:"title,sort,filter"`
	Secret string `json:"secret" col:"secret"` // bound, not filterable
}

type faRow struct {
	ID   string
	Name string
	Age  int
}

type FaWire struct {
	ID   string `json:"id" col:"id,filter"`
	Name string `json:"name" col:"name,filter"`
	Age  int    `json:"age" col:"age,filter"`
}

type frRow struct {
	ID     string
	BookID string
	Rating int
}

type FrWire struct {
	ID     string `json:"id" col:"id,filter"`
	Rating int    `json:"rating" col:"rating,filter"`
}

func filterTestGraph(t *testing.T) *Graph {
	t.Helper()
	b := NewBuilder()
	book := Node[fbRow, FbWire](b, "FBook").
		Wire(func(r fbRow, _ *include.Ctx) FbWire { return FbWire{ID: r.ID, Title: r.Title} }).
		PrimaryKey(func(r fbRow) string { return r.ID })
	author := Node[faRow, FaWire](b, "FAuthor").
		Wire(func(r faRow, _ *include.Ctx) FaWire { return FaWire{ID: r.ID, Name: r.Name, Age: r.Age} }).
		PrimaryKey(func(r faRow) string { return r.ID })
	review := Node[frRow, FrWire](b, "FReview").
		Wire(func(r frRow, _ *include.Ctx) FrWire { return FrWire{ID: r.ID, Rating: r.Rating} }).
		PrimaryKey(func(r frRow) string { return r.ID })
	_ = review
	// filterable but not includable: a filter never loads the reviews, so no
	// FetchParents bind is required
	book.Edge("reviews", Reverse[FrWire]("bookId")).Filterable()
	book.Edge("author", ToOne[FaWire]()).
		ForeignKey(func(r fbRow) string { return r.AuthorID }).
		Filterable().
		Includable()
	FetchIDs(b, author, func(_ *include.Ctx, ids []string) ([]faRow, error) { return nil, nil })
	b.Root(book)
	g, err := b.Compile()
	if err != nil {
		t.Fatalf("Compile: %v", err)
	}
	return g
}

func TestFilterResolvesOverCompiledGraph(t *testing.T) {
	g := filterTestGraph(t)
	root := g.Resource("FBook")
	f := include.FilterAnd{
		include.FilterCond{Field: "title", Op: include.OpEq, Value: "Dune"},
		include.FilterCond{Path: []include.FilterStep{{Key: "author"}}, Field: "age", Op: include.OpGte, Value: 18},
	}
	got, err := include.ResolveFilter(root, f, include.DefaultOptions)
	if err != nil {
		t.Fatalf("ResolveFilter: %v", err)
	}
	and, ok := got.(include.ResolvedAnd)
	if !ok || len(and) != 2 {
		t.Fatalf("resolved = %#v, want a 2-element ResolvedAnd", got)
	}
	c0 := and[0].(include.ResolvedCond)
	if c0.Column.Col != "title" || len(c0.Hops) != 0 {
		t.Errorf("and[0] = %+v, want root column title", c0)
	}
	c1 := and[1].(include.ResolvedCond)
	if c1.Column.Col != "age" || c1.Column.Type != reflect.TypeFor[int]() || len(c1.Hops) != 1 {
		t.Fatalf("and[1] = %+v, want age through one hop", c1)
	}
	if h := c1.Hops[0]; h.Key != "author" || h.From.Name() != "FBook" || h.To.Name() != "FAuthor" {
		t.Errorf("hop = %+v", h)
	}

	_, err = include.ResolveFilter(root, include.FilterCond{Field: "secret", Op: include.OpEq, Value: "x"}, include.DefaultOptions)
	var e *include.Error
	if !errors.As(err, &e) || e.Code != include.INVALID_FILTER || e.Path != "secret" {
		t.Errorf("secret (no filter option): err = %v, want INVALID_FILTER at \"secret\"", err)
	}
}

func TestFilterReachesRootFetcher(t *testing.T) {
	g := filterTestGraph(t)
	root := g.Resource("FBook")
	rf, err := include.ResolveFilter(root, include.FilterCond{Field: "title", Op: include.OpEq, Value: "Dune"}, include.DefaultOptions)
	if err != nil {
		t.Fatalf("ResolveFilter: %v", err)
	}
	var seen include.QueryArgs
	fetch := func(_ *include.Ctx, q include.QueryArgs) ([]any, int, bool, error) {
		seen = q
		return []any{fbRow{ID: "b1", Title: "Dune"}}, 1, false, nil
	}
	q := include.QueryArgs{Where: rf, Limit: 10}
	res, _, err := include.HydrateByQuery(root, q, include.IncludeTree{}, nil, fetch, &include.Ctx{Registry: g}, include.DefaultOptions)
	if err != nil {
		t.Fatalf("HydrateByQuery: %v", err)
	}
	if !reflect.DeepEqual(seen.Where, rf) {
		t.Errorf("RootFetcher saw %+v, want the resolved filter passed through verbatim", seen)
	}
	if len(res.Data) != 1 {
		t.Errorf("Data = %d docs, want 1", len(res.Data))
	}
}

// A quantified hop over a compiled reverse edge: the quantifier the client
// put on the step reaches the adapter on the hop.
func TestFilterQuantifierOverCompiledGraph(t *testing.T) {
	g := filterTestGraph(t)
	root := g.Resource("FBook")
	f := include.FilterCond{
		Path:  []include.FilterStep{{Key: "reviews", Quant: include.QuantAny}},
		Field: "rating", Op: include.OpGt, Value: 4,
	}
	got, err := include.ResolveFilter(root, f, include.DefaultOptions)
	if err != nil {
		t.Fatalf("ResolveFilter: %v", err)
	}
	c := got.(include.ResolvedCond)
	if len(c.Hops) != 1 {
		t.Fatalf("hops = %d, want 1", len(c.Hops))
	}
	if h := c.Hops[0]; h.Key != "reviews" || h.Quant != include.QuantAny || h.To.Name() != "FReview" || !h.Edge.Many {
		t.Errorf("hop = %+v", h)
	}
	if c.Column.Col != "rating" {
		t.Errorf("column = %+v", c.Column)
	}

	_, err = include.ResolveFilter(root, include.FilterCond{
		Path: []include.FilterStep{{Key: "reviews"}}, Field: "rating", Op: include.OpGt, Value: 4,
	}, include.DefaultOptions)
	var e *include.Error
	if !errors.As(err, &e) || e.Code != include.INVALID_FILTER || e.Path != "reviews" {
		t.Errorf("no quantifier: err = %v, want INVALID_FILTER at \"reviews\"", err)
	}
}
