package huma

import (
	"context"
	"net/http"
	"reflect"
	"strings"
	"testing"

	humav2 "github.com/danielgtaylor/huma/v2"
	"github.com/danielgtaylor/huma/v2/humatest"

	"github.com/qrotux/wireleaf/apidoc"
	"github.com/qrotux/wireleaf/include"
)

func TestBindChecksWireType(t *testing.T) {
	a := New(inputsGraph(t), include.DefaultOptions, "t", "1")
	assertPanicsWith(t, "unknown resource", func() { Bind[BookWireT](a, "Nope") }) // graph.Graph.Resource panics itself
	assertPanicsWith(t, "wire type mismatch", func() { Bind[struct{ X int }](a, "Book") })
	b := Bind[BookWireT](a, "Book")
	if b.Resource().Name() != "Book" {
		t.Fatal("bound the wrong resource")
	}
	if _, ok := a.Components().TypeName(reflect.TypeFor[BookWireT]()); !ok {
		t.Fatal("Bind must RegisterNode")
	}
}

// TestBindIsPerNodeAndWireType pins Bind's idempotency KEY: the pair
// (node, Node[W]). Re-binding the same pair is a no-op; binding W to a node
// while W is already registered under ANOTHER component name is a wiring bug
// and must not silently reuse that other component.
func TestBindIsPerNodeAndWireType(t *testing.T) {
	a := New(inputsGraph(t), include.DefaultOptions, "t", "1")
	Bind[BookWireT](a, "Book")
	Bind[BookWireT](a, "Book") // same (node, W): registers once, no panic

	// The second half of the key matters when W is ALREADY bound to another
	// component name: Bind must still reach RegisterNode, where apidoc refuses
	// the remap, instead of skipping registration and letting Node[W] document
	// as the other component. (A graph cannot itself produce two nodes with one
	// wire type — graph.Compile reports "wire type … is already used by node …"
	// — so the conflict is staged the only way it can reach Bind: a component
	// already registered for W under a different name.)
	a2 := New(inputsGraph(t), include.DefaultOptions, "t", "1")
	RegisterNode[BookWireT](a2.Components(), "Other")
	assertPanicsWith(t, "remap", func() { Bind[BookWireT](a2, "Book") })
}

func TestBoundGetAndErrors(t *testing.T) {
	a := New(inputsGraph(t), include.DefaultOptions, "t", "1")
	books := Bind[BookWireT](a, "Book")
	n, err := books.Get(context.Background(), "b1", "")
	if err != nil || !strings.Contains(string(mustJSON(t, n)), `"b1"`) {
		t.Fatalf("Get: %v %v", n, err)
	}
	_, err = books.Get(context.Background(), "zz", "")
	se, ok := err.(humav2.StatusError)
	if !ok || se.GetStatus() != 404 || !strings.Contains(err.Error(), "NOT_FOUND") {
		t.Fatalf("missing: %v", err)
	}
	if _, err := books.Get(context.Background(), "b1", "ghost"); err == nil || err.(humav2.StatusError).GetStatus() != 400 {
		t.Fatalf("bad include: %v", err)
	}
}

func TestBoundListOffset(t *testing.T) {
	a := New(inputsGraph(t), include.DefaultOptions, "t", "1")
	books := Bind[BookWireT](a, "Book")
	var got include.QueryArgs
	fetch := include.ListFetcher(func(_ *include.Ctx, q include.QueryArgs) (include.ListPage, error) {
		got = q
		return include.ListPage{Docs: []any{bookRowOf("b1"), bookRowOf("b2")}, Total: 5, HasMore: true}, nil
	})
	page, err := books.List(context.Background(), ListQuery{Sort: "-title", Page: 2, Limit: 2, Where: `{"title":{"eq":"x"}}`}, fetch)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if got.Sort != "-title" || got.Page != 2 || got.Limit != 2 || got.Where == nil {
		t.Fatalf("args = %+v", got)
	}
	if page.Mode != include.PageModeOffset || len(page.Data) != 2 || page.Offset != (PagePagination{Page: 2, TotalPages: 3, TotalDocs: 5, HasNextPage: true, HasPrevPage: true}) {
		t.Fatalf("page = %+v", page)
	}
	for name, q := range map[string]ListQuery{
		"bad sort":  {Sort: "ghost"},
		"big limit": {Limit: 999},
		"bad where": {Where: `{"ghost":{"eq":1}}`},
	} {
		t.Run(name, func(t *testing.T) {
			_, err := books.List(context.Background(), q, fetch)
			if se, ok := err.(humav2.StatusError); !ok || se.GetStatus() != 400 {
				t.Fatalf("err = %v", err)
			}
		})
	}
}

func TestBoundListCursorBracket(t *testing.T) {
	a := New(cursorGraph(t), include.DefaultOptions, "t", "1", WithFilterSyntax(apidoc.FilterBracket))
	_, h := humatest.New(t, a.Config())
	a.Attach(h)
	books := Bind[BookWireT](a, "Book")
	var got include.QueryArgs
	fetch := include.ListFetcher(func(_ *include.Ctx, q include.QueryArgs) (include.ListPage, error) {
		got = q
		return include.ListPage{Docs: []any{bookRowOf("b1")}, HasMore: true, NextCursor: "n2", PrevCursor: "p0"}, nil
	})
	var page Page[BookWireT]
	type in struct{ ListQuery }
	type out struct{ Body BookCursorPage }
	Register(a, humav2.Operation{OperationID: "list", Method: http.MethodGet, Path: "/books"},
		func(ctx context.Context, i *in) (*out, error) {
			p, err := books.List(ctx, i.ListQuery, fetch)
			page = p
			return &out{Body: BookCursorPage{Data: p.Data, Pagination: p.Cursor}}, err
		}, books.Inputs())
	resp := h.Get("/books?cursor=c1&limit=1&where[title][eq]=x")
	if resp.Code != 200 {
		t.Fatalf("status %d: %s", resp.Code, resp.Body.String())
	}
	if got.Cursor != "c1" || got.Limit != 1 || got.Where == nil {
		t.Fatalf("args = %+v", got)
	}
	if page.Mode != include.PageModeCursor || page.Cursor.NextCursor == nil || *page.Cursor.NextCursor != "n2" || !page.Cursor.HasNextPage || !page.Cursor.HasPrevPage || page.Cursor.Limit != 1 || page.Cursor.TotalDocs != nil {
		t.Fatalf("cursor page = %+v", page.Cursor)
	}
	if resp := h.Get("/books?page=2"); resp.Code != 400 {
		t.Fatalf("page in cursor mode = %d", resp.Code)
	}
}

// BookCursorPage is the application-side envelope the operation exposes: the
// data convention (envelope.go) wants the pagination block on a json:"pagination"
// field, so a handler picks the block of its resource's mode out of Page[W]
// (whose own pagination fields are json:"-") and puts it there.
type BookCursorPage struct {
	Data       []Node[BookWireT]     `json:"data"`
	Pagination CursorPaginationTotal `json:"pagination"`
}

func TestBoundHydrate(t *testing.T) {
	a := New(inputsGraph(t), include.DefaultOptions, "t", "1")
	books := Bind[BookWireT](a, "Book")
	n, err := books.Hydrate(context.Background(), fixtureRow{ID: "new", Title: "Fresh"}, "")
	if err != nil || !strings.Contains(string(mustJSON(t, n)), `"Fresh"`) {
		t.Fatalf("Hydrate: %v %v", n, err)
	}
	if _, err := books.Hydrate(context.Background(), fixtureRow{ID: "new"}, "ghost"); err == nil || err.(humav2.StatusError).GetStatus() != 400 {
		t.Fatalf("bad include: %v", err)
	}
}
