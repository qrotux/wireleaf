package huma

// inputs_e2e_test.go — the integration checkpoint of the node-inputs feature.
//
// One served API per (pagination mode, filter syntax) pair, and both halves
// asserted against it: the OpenAPI document huma emits at /openapi.json, and
// the runtime behaviour of the same operations. They must agree — the document
// comes from apidoc.InputParams and the enforcement from
// include.ResolveInputs, both reading the node's include.Inputs.

import (
	"context"
	"net/http"
	"strings"
	"testing"

	humav2 "github.com/danielgtaylor/huma/v2"
	"github.com/danielgtaylor/huma/v2/humatest"

	"github.com/qrotux/wireleaf/apidoc"
	"github.com/qrotux/wireleaf/graph"
	"github.com/qrotux/wireleaf/include"
)

// BookOffsetPage / BookCursorPage-style envelopes: Page[W] is not a huma body,
// so the operation exposes its own {data, pagination} struct with the
// pagination block of its resource's mode. `any` is not an option — the
// envelope derivation matches field 1 against the library pagination types —
// hence one envelope per mode.
type BookOffsetPageE2E struct {
	Data       []Node[BookWireT] `json:"data"`
	Pagination PagePagination    `json:"pagination"`
}

type BookCursorPageE2E struct {
	Data       []Node[BookWireT]     `json:"data"`
	Pagination CursorPaginationTotal `json:"pagination"`
}

type listInputE2E struct{ ListQuery }

type getInputE2E struct {
	ID      string `path:"id"`
	Include string `query:"include"`
}

type getOutputE2E struct{ Body Node[BookWireT] }

type offsetOutputE2E struct{ Body BookOffsetPageE2E }

type cursorOutputE2E struct{ Body BookCursorPageE2E }

// serve builds one wireleaf-backed huma API over the fixture graph in the
// given pagination mode and filter syntax, registers GET /books and
// GET /books/{id}, and returns the test API plus the pointer the list
// fetcher records its resolved QueryArgs into.
func serve(t *testing.T, mode include.PageMode, syntax apidoc.FilterSyntax) (humatest.TestAPI, *include.QueryArgs) {
	t.Helper()
	a := New(fixtureGraph(t, mode), include.DefaultOptions, "books", "1.0.0", WithFilterSyntax(syntax))
	_, h := humatest.New(t, a.Config())
	a.Attach(h)
	books := Bind[BookWireT](a, "Book")

	seen := &include.QueryArgs{}
	fetch := include.ListFetcher(func(_ *include.Ctx, q include.QueryArgs) (include.ListPage, error) {
		*seen = q
		return include.ListPage{
			Docs:       []any{bookRowOf("b1"), bookRowOf("b2")},
			Total:      5,
			HasMore:    true,
			NextCursor: "n2",
		}, nil
	})

	listOp := humav2.Operation{OperationID: "list-books", Method: http.MethodGet, Path: "/books"}
	if mode == include.PageModeCursor {
		Register(a, listOp, func(ctx context.Context, i *listInputE2E) (*cursorOutputE2E, error) {
			p, err := books.List(ctx, i.ListQuery, fetch)
			if err != nil {
				return nil, err
			}
			return &cursorOutputE2E{Body: BookCursorPageE2E{Data: p.Data, Pagination: p.Cursor}}, nil
		}, books.Inputs())
	} else {
		Register(a, listOp, func(ctx context.Context, i *listInputE2E) (*offsetOutputE2E, error) {
			p, err := books.List(ctx, i.ListQuery, fetch)
			if err != nil {
				return nil, err
			}
			return &offsetOutputE2E{Body: BookOffsetPageE2E{Data: p.Data, Pagination: p.Offset}}, nil
		}, books.Inputs())
	}

	Register(a, humav2.Operation{OperationID: "get-book", Method: http.MethodGet, Path: "/books/{id}"},
		func(ctx context.Context, i *getInputE2E) (*getOutputE2E, error) {
			n, err := books.Get(ctx, i.ID, i.Include)
			if err != nil {
				return nil, err
			}
			return &getOutputE2E{Body: n}, nil
		}, books.Include())

	return h, seen
}

func TestInputsEndToEndOffsetJSON(t *testing.T) {
	h, seen := serve(t, include.PageModeOffset, apidoc.FilterJSON)
	doc := h.Get("/openapi.json").Body.String()
	for _, want := range []string{`"x-include-paths"`, `"-title"`, `"maximum":50`, `"x-filter-syntax":"json"`, `"x-filter-fields"`, "INVALID_SORT", `"name":"page"`} {
		if !strings.Contains(doc, want) {
			t.Errorf("document lacks %s", want)
		}
	}
	if strings.Contains(doc, `"name":"cursor"`) {
		t.Error("offset mode must not document cursor")
	}
	for path, code := range map[string]int{
		"/books?sort=ghost": 400,
		"/books?limit=999":  400,
		"/books?where=%7B%22ghost%22%3A%7B%22eq%22%3A1%7D%7D": 400,
		"/books?limit=abc":                  422,
		"/books?sort=-title&page=2&limit=1": 200,
	} {
		if resp := h.Get(path); resp.Code != code {
			t.Errorf("%s = %d, want %d: %s", path, resp.Code, code, resp.Body.String())
		}
	}
	for _, tc := range []struct{ path, code string }{
		{"/books?sort=ghost", "INVALID_SORT"},
		{"/books?limit=999", "INVALID_PAGINATION"},
		{"/books?where=%7B%22ghost%22%3A%7B%22eq%22%3A1%7D%7D", "INVALID_FILTER"},
	} {
		if body := h.Get(tc.path).Body.String(); !strings.Contains(body, tc.code) {
			t.Errorf("%s body lacks %s: %s", tc.path, tc.code, body)
		}
	}
	if seen.Sort != "-title" || seen.Page != 2 || seen.Limit != 1 {
		t.Errorf("fetcher saw %+v", *seen)
	}
	resp := h.Get("/books/b1", "X-Remote-Addr: 10.0.0.9")
	if resp.Code != 200 || !strings.Contains(resp.Body.String(), `"seen":"10.0.0.9"`) {
		t.Errorf("header did not reach the Wire function: %d %s", resp.Code, resp.Body.String())
	}
}

func TestInputsEndToEndCursorBracket(t *testing.T) {
	h, seen := serve(t, include.PageModeCursor, apidoc.FilterBracket)
	doc := h.Get("/openapi.json").Body.String()
	for _, want := range []string{`"name":"cursor"`, `"style":"deepObject"`, `"explode":true`, `"x-filter-syntax":"bracket"`, `"x-filter-fields"`, "INVALID_PAGINATION"} {
		if !strings.Contains(doc, want) {
			t.Errorf("document lacks %s", want)
		}
	}
	if strings.Contains(doc, `"name":"page"`) {
		t.Error("cursor mode must not document page")
	}
	resp := h.Get("/books?cursor=c1&where[title][eq]=x")
	if resp.Code != 200 || seen.Cursor != "c1" || seen.Where == nil || !strings.Contains(resp.Body.String(), `"nextCursor":"n2"`) {
		t.Fatalf("cursor list: %d %s, args %+v", resp.Code, resp.Body.String(), *seen)
	}
	if resp := h.Get("/books?page=2"); resp.Code != 400 || !strings.Contains(resp.Body.String(), "INVALID_PAGINATION") {
		t.Errorf("page in cursor mode = %d %s", resp.Code, resp.Body.String())
	}
	// The JSON spelling is the wrong spelling here: bracket mode documents
	// `where` as a deepObject, so `?where=<json>` must be refused, not
	// answered with an unfiltered page.
	if resp := h.Get("/books?where=%7B%7D"); resp.Code != 400 || !strings.Contains(resp.Body.String(), "INVALID_FILTER") {
		t.Errorf("json where in bracket mode = %d %s", resp.Code, resp.Body.String())
	}
}

// PlainWireT is the wire type of a node that declares NO sort and NO filter:
// the other half of the ListQuery drift. ListQuery still carries ?sort and
// ?where (the resolver needs to see them to reject them), so huma would derive
// both parameters — Inputs must prune what apidoc.InputParams did not document.
type PlainWireT struct {
	ID    string `json:"id" col:"id"`
	Title string `json:"title" col:"title"`
}

type plainOutputE2E struct {
	Body struct {
		Data       []Node[PlainWireT] `json:"data"`
		Pagination PagePagination     `json:"pagination"`
	}
}

func TestInputsEndToEndNoSortNoFilter(t *testing.T) {
	b := graph.NewBuilder()
	plain := graph.Node[fixtureRow, PlainWireT](b, "Plain").
		Wire(func(r fixtureRow, _ *include.Ctx) PlainWireT {
			return PlainWireT{ID: r.ID, Title: r.Title}
		}).
		PrimaryKey(func(r fixtureRow) string { return r.ID }).
		Inputs(graph.Inputs{Pagination: graph.PageInput{Mode: include.PageModeOffset, DefaultLimit: 2, MaxLimit: 50}})
	graph.FetchIDs(b, plain, func(_ *include.Ctx, ids []string) ([]fixtureRow, error) {
		out := make([]fixtureRow, 0, len(ids))
		for _, id := range ids {
			if r, ok := fixtureRows[id]; ok {
				out = append(out, r)
			}
		}
		return out, nil
	})
	b.Root(plain)
	g, err := b.Compile()
	if err != nil {
		t.Fatalf("plain graph: %v", err)
	}

	a := New(g, include.DefaultOptions, "plain", "1.0.0")
	_, h := humatest.New(t, a.Config())
	a.Attach(h)
	plains := Bind[PlainWireT](a, "Plain")
	fetch := include.ListFetcher(func(_ *include.Ctx, _ include.QueryArgs) (include.ListPage, error) {
		return include.ListPage{Docs: []any{bookRowOf("b1")}, Total: 1}, nil
	})
	Register(a, humav2.Operation{OperationID: "list-plain", Method: http.MethodGet, Path: "/plain"},
		func(ctx context.Context, i *listInputE2E) (*plainOutputE2E, error) {
			p, err := plains.List(ctx, i.ListQuery, fetch)
			if err != nil {
				return nil, err
			}
			out := &plainOutputE2E{}
			out.Body.Data, out.Body.Pagination = p.Data, p.Offset
			return out, nil
		}, plains.Inputs())

	doc := h.Get("/openapi.json").Body.String()
	for _, unwanted := range []string{`"name":"sort"`, `"name":"where"`, `"name":"cursor"`} {
		if strings.Contains(doc, unwanted) {
			t.Errorf("a node without sort/filter must not document %s", unwanted)
		}
	}
	for _, want := range []string{`"name":"include"`, `"name":"page"`, `"name":"limit"`} {
		if !strings.Contains(doc, want) {
			t.Errorf("document lacks %s", want)
		}
	}
	// Pruning is documentation only: the resolver still sees and rejects the
	// values ListQuery carries.
	if resp := h.Get("/plain?sort=title"); resp.Code != 400 || !strings.Contains(resp.Body.String(), "INVALID_SORT") {
		t.Errorf("sort on a non-sortable resource = %d %s", resp.Code, resp.Body.String())
	}
	if resp := h.Get("/plain?limit=1"); resp.Code != 200 {
		t.Errorf("plain list = %d %s", resp.Code, resp.Body.String())
	}
}

// TestInputsEndToEndGroupPrefix attaches the API to a humav2.Group. The
// group's prefix modifier rewrites op.Path on the way into the document, so
// the operation is filed under /v1/books while Register only ever saw /books:
// pruning by that path would panic ("is not in the OpenAPI doc"). It runs from
// Attach's OnAddOperation hook instead, on the operation huma actually filed.
func TestInputsEndToEndGroupPrefix(t *testing.T) {
	b := graph.NewBuilder()
	plain := graph.Node[fixtureRow, PlainWireT](b, "Plain").
		Wire(func(r fixtureRow, _ *include.Ctx) PlainWireT {
			return PlainWireT{ID: r.ID, Title: r.Title}
		}).
		PrimaryKey(func(r fixtureRow) string { return r.ID }).
		Inputs(graph.Inputs{Pagination: graph.PageInput{Mode: include.PageModeOffset, DefaultLimit: 2, MaxLimit: 50}})
	graph.FetchIDs(b, plain, func(_ *include.Ctx, _ []string) ([]fixtureRow, error) { return nil, nil })
	b.Root(plain)
	g, err := b.Compile()
	if err != nil {
		t.Fatalf("plain graph: %v", err)
	}

	a := New(g, include.DefaultOptions, "plain", "1.0.0")
	_, h := humatest.New(t, a.Config())
	grp := humav2.NewGroup(h, "/v1")
	a.Attach(grp)
	plains := Bind[PlainWireT](a, "Plain")
	fetch := include.ListFetcher(func(_ *include.Ctx, _ include.QueryArgs) (include.ListPage, error) {
		return include.ListPage{Docs: []any{bookRowOf("b1")}, Total: 1}, nil
	})

	Register(a, humav2.Operation{OperationID: "list-plain-v1", Method: http.MethodGet, Path: "/books"},
		func(ctx context.Context, i *listInputE2E) (*plainOutputE2E, error) {
			p, err := plains.List(ctx, i.ListQuery, fetch)
			if err != nil {
				return nil, err
			}
			out := &plainOutputE2E{}
			out.Body.Data, out.Body.Pagination = p.Data, p.Offset
			return out, nil
		}, plains.Inputs())

	doc := h.Get("/openapi.json").Body.String()
	if !strings.Contains(doc, `"/v1/books"`) {
		t.Fatalf("the group prefix did not reach the document: %s", doc)
	}
	for _, unwanted := range []string{`"name":"sort"`, `"name":"where"`, `"name":"cursor"`} {
		if strings.Contains(doc, unwanted) {
			t.Errorf("a prefixed no-sort/no-filter operation must not document %s", unwanted)
		}
	}
	for _, want := range []string{`"name":"include"`, `"name":"page"`, `"name":"limit"`} {
		if !strings.Contains(doc, want) {
			t.Errorf("document lacks %s", want)
		}
	}
	if resp := h.Get("/v1/books?limit=1"); resp.Code != 200 {
		t.Errorf("prefixed list = %d %s", resp.Code, resp.Body.String())
	}
}
