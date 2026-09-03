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
	"encoding/json"
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

	// Every list request overwrites the one snapshot: a test reads the
	// arguments of the LAST list it made, so it asserts them before the next.
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

// docParam is the slice of a documented parameter the e2e tests assert on.
type docParam struct {
	Name    string         `json:"name"`
	In      string         `json:"in"`
	Style   string         `json:"style"`
	Explode *bool          `json:"explode"`
	Schema  map[string]any `json:"schema"`
}

// queryParams returns the query parameters of GET path as the served document
// lists them, in order — so a test asserts on the parameter, not on a
// substring that could match anywhere in the document.
func queryParams(t *testing.T, h humatest.TestAPI, path string) []docParam {
	t.Helper()
	var doc struct {
		Paths map[string]struct {
			Get struct {
				Parameters []docParam `json:"parameters"`
				Responses  map[string]struct {
					Description string `json:"description"`
				} `json:"responses"`
			} `json:"get"`
		} `json:"paths"`
	}
	body := h.Get("/openapi.json").Body.Bytes()
	if err := json.Unmarshal(body, &doc); err != nil {
		t.Fatalf("openapi.json: %v", err)
	}
	op, ok := doc.Paths[path]
	if !ok {
		t.Fatalf("document has no %s: %s", path, body)
	}
	var out []docParam
	for _, p := range op.Get.Parameters {
		if p.In == "query" {
			out = append(out, p)
		}
	}
	return out
}

func paramNames(ps []docParam) []string {
	names := make([]string, 0, len(ps))
	for _, p := range ps {
		names = append(names, p.Name)
	}
	return names
}

func paramNamed(t *testing.T, ps []docParam, name string) docParam {
	t.Helper()
	for _, p := range ps {
		if p.Name == name {
			return p
		}
	}
	t.Fatalf("no %s parameter in %v", name, paramNames(ps))
	return docParam{}
}

func TestInputsEndToEndOffsetJSON(t *testing.T) {
	h, seen := serve(t, include.PageModeOffset, apidoc.FilterJSON)
	ps := queryParams(t, h, "/books")
	if got, want := strings.Join(paramNames(ps), ","), "include,sort,page,limit,where"; got != want {
		t.Errorf("parameters = %s, want %s", got, want)
	}
	if s := paramNamed(t, ps, "include").Schema; s["x-include-paths"] == nil {
		t.Errorf("include schema lacks x-include-paths: %v", s)
	}
	if s := paramNamed(t, ps, "sort").Schema; !strings.Contains(string(mustJSON(t, s["enum"])), `"-title"`) {
		t.Errorf("sort enum = %v, want the node's sortable columns both ways", s["enum"])
	}
	if s := paramNamed(t, ps, "limit").Schema; s["maximum"] != float64(50) {
		t.Errorf("limit maximum = %v, want the node's MaxLimit 50", s["maximum"])
	}
	if s := paramNamed(t, ps, "where").Schema; s["x-filter-syntax"] != "json" || s["x-filter-fields"] == nil {
		t.Errorf("where schema = %v, want json syntax with x-filter-fields", s)
	}
	if doc := h.Get("/openapi.json").Body.String(); !strings.Contains(doc, "INVALID_SORT") {
		t.Error("document lacks the INVALID_SORT 400")
	}
	// One request per path: the status and, for a 400, the code in the body.
	for _, tc := range []struct {
		path string
		code int
		body string
	}{
		{"/books?sort=ghost", 400, "INVALID_SORT"},
		{"/books?limit=999", 400, "INVALID_PAGINATION"},
		{"/books?where=%7B%22ghost%22%3A%7B%22eq%22%3A1%7D%7D", 400, "INVALID_FILTER"},
		{"/books?limit=abc", 422, ""},
		{"/books?sort=-title&page=2&limit=1", 200, ""},
	} {
		resp := h.Get(tc.path)
		if resp.Code != tc.code || !strings.Contains(resp.Body.String(), tc.body) {
			t.Errorf("%s = %d %s, want %d containing %q", tc.path, resp.Code, resp.Body.String(), tc.code, tc.body)
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
	ps := queryParams(t, h, "/books")
	if got, want := strings.Join(paramNames(ps), ","), "include,sort,cursor,limit,where"; got != want {
		t.Errorf("parameters = %s, want %s", got, want)
	}
	where := paramNamed(t, ps, "where")
	if where.Style != "deepObject" || where.Explode == nil || !*where.Explode || where.Schema["x-filter-syntax"] != "bracket" || where.Schema["x-filter-fields"] == nil {
		t.Errorf("where = %+v, want an exploded deepObject in bracket syntax", where)
	}
	if doc := h.Get("/openapi.json").Body.String(); !strings.Contains(doc, "INVALID_PAGINATION") {
		t.Error("document lacks the INVALID_PAGINATION 400")
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

	if got, want := strings.Join(paramNames(queryParams(t, h, "/plain")), ","), "include,page,limit"; got != want {
		t.Errorf("parameters = %s, want %s (sort and where pruned)", got, want)
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

	// queryParams fails on a missing path, so the prefix reaching the
	// document is asserted by the lookup itself.
	if got, want := strings.Join(paramNames(queryParams(t, h, "/v1/books")), ","), "include,page,limit"; got != want {
		t.Errorf("parameters = %s, want %s (sort and where pruned under the prefix)", got, want)
	}
	if resp := h.Get("/v1/books?limit=1"); resp.Code != 200 {
		t.Errorf("prefixed list = %d %s", resp.Code, resp.Body.String())
	}
}
