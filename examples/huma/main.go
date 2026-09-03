// Command huma wires a wireleaf graph into a huma v2 API through
// adapters/huma's facade: wfhuma.New builds the graph's components and the
// huma config together, Attach installs the request middleware, Bind ties one
// graph node to its wire type, and Register documents an operation with the
// SAME include.Options the planner enforces.
//
// The node itself declares what a list accepts — sort keys, filter fields and
// pagination come from `graph.Inputs` over the wire struct's `col` tags — so
// the ?sort/?limit/?where parameters in the document and the 400s a bad value
// gets are two faces of one declaration.
//
// Two services share one http.ServeMux: Books (offset pagination, JSON
// ?where=) and Authors (cursor pagination, bracket ?where[field][op]=). The
// program serves them in-process with httptest, performs a handful of
// requests, and prints what the document says — so it runs to completion like
// the other examples instead of listening on a port.
package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"maps"
	"net/http"
	"net/http/httptest"
	"net/url"
	"slices"
	"sort"
	"strconv"
	"strings"

	humav2 "github.com/danielgtaylor/huma/v2"
	"github.com/danielgtaylor/huma/v2/adapters/humago"

	wfhuma "github.com/qrotux/wireleaf/adapters/huma"
	"github.com/qrotux/wireleaf/apidoc"
	"github.com/qrotux/wireleaf/graph"
	"github.com/qrotux/wireleaf/include"
)

// ------------------------------------------------------------------ rows and wires

type bookRow struct {
	ID       string
	Title    string
	Year     int
	AuthorID string
}

type authorRow struct {
	ID   string
	Name string
}

// BookWire is a wire type; wire types carry TWO tag sets with different jobs.
//
//   - `doc`, `example`, `minLength`, `minimum`, … are the reflector's
//     documentation tags: they land in the component's JSON Schema verbatim.
//     (`description:` is the equivalent spelling; `doc:` is huma's, and the
//     reflector accepts both so an input struct and a wire struct can be
//     annotated the same way.) Nullability is NOT a tag concern — the policy
//     decides it from the Go shape.
//   - `col` binds the field to its SQL-side column and grants ROLES:
//     `col:"pub_year,sort,filter"` says the client may sort by ?sort=year and
//     filter on year, and that both mean the `pub_year` column. Deny by
//     default: a field with no role is neither sortable nor filterable, and a
//     sort key or filter field can never name a column the node does not bind.
type BookWire struct {
	ID         string `json:"id" col:"id" doc:"Stable book identifier" example:"b1"`
	Title      string `json:"title" col:"title,sort,filter" doc:"Title as printed on the cover" minLength:"1"`
	Year       int    `json:"year" col:"pub_year,sort,filter" doc:"Year of first publication" minimum:"1450"`
	ViewedFrom string `json:"viewedFrom,omitempty" doc:"Address the request came from (X-Remote-Addr)"`
}

type AuthorWire struct {
	ID   string `json:"id" col:"id" doc:"Stable author identifier" example:"a1"`
	Name string `json:"name" col:"name,sort,filter" doc:"Display name, as commonly credited"`
}

// CreateBookBody is a request body: it goes through the bridge to the same
// reflector, so its tags are read exactly like a wire type's.
type CreateBookBody struct {
	Title    string `json:"title" doc:"Title as printed on the cover" minLength:"1"`
	Year     int    `json:"year" doc:"Year of first publication" minimum:"1450"`
	AuthorID string `json:"authorId" doc:"Owning author" example:"a1"`
}

// ------------------------------------------------------------------ the store

var books = map[string]bookRow{
	"b1": {ID: "b1", Title: "The Hobbit", Year: 1937, AuthorID: "a1"},
	"b2": {ID: "b2", Title: "The Silmarillion", Year: 1977, AuthorID: "a1"},
	"b3": {ID: "b3", Title: "Dune", Year: 1965, AuthorID: "a2"},
}

var authors = map[string]authorRow{
	"a1": {ID: "a1", Name: "J. R. R. Tolkien"},
	"a2": {ID: "a2", Name: "Frank Herbert"},
}

func insertBook(body CreateBookBody) bookRow {
	// The first free "b<n>": counting rows would reuse an id once any row
	// were removed.
	id := ""
	for n := len(books) + 1; ; n++ {
		if _, taken := books["b"+strconv.Itoa(n)]; !taken {
			id = "b" + strconv.Itoa(n)
			break
		}
	}
	row := bookRow{ID: id, Title: body.Title, Year: body.Year, AuthorID: body.AuthorID}
	books[row.ID] = row
	return row
}

// ------------------------------------------------------------------ the graph

// Book is the declarative form of a node: the facts, the edges, the list
// inputs and the fetchers in one literal. graph.Add replays it through the
// chained builder, so it is indistinguishable from a hand-chained node.
var Book = graph.Spec[bookRow, BookWire]{
	Name: "Book",
	Slug: "book",
	// The Ctx is the request in engine terms: Header reads what the middleware
	// snapshotted, so a wire field can depend on the caller.
	Wire: func(r bookRow, c *include.Ctx) BookWire {
		return BookWire{ID: r.ID, Title: r.Title, Year: r.Year, ViewedFrom: c.Header("X-Remote-Addr")}
	},
	PrimaryKey: func(r bookRow) string { return r.ID },
	Edges: []graph.EdgeSpec[bookRow]{
		{Key: "author", Kind: graph.ToOne[AuthorWire](), ForeignKey: func(r bookRow) string { return r.AuthorID }, Inverse: "books", Includable: true},
	},
	// Offset pagination, a default sort, and filtering over the `filter`
	// columns. apidoc.InputParams documents exactly this and
	// include.ResolveInputs enforces exactly this — they cannot drift.
	Inputs: &graph.Inputs{
		Sort:       graph.SortInput{Enabled: true, Default: "title"},
		Filter:     graph.FilterInput{Enabled: true},
		Pagination: graph.PageInput{DefaultLimit: 2, MaxLimit: 50},
	},
	FetchIDs:     fetchBooksByID,
	FetchParents: fetchBooksByAuthor,
}

// Author pages by CURSOR: the client sends ?cursor= and never ?page=, and the
// document says so — the resolver rejects the off-mode parameter with
// INVALID_PAGINATION.
var Author = graph.Spec[authorRow, AuthorWire]{
	Name:       "Author",
	Slug:       "author",
	Wire:       func(r authorRow, _ *include.Ctx) AuthorWire { return AuthorWire{ID: r.ID, Name: r.Name} },
	PrimaryKey: func(r authorRow) string { return r.ID },
	Edges: []graph.EdgeSpec[authorRow]{
		{Key: "books", Kind: graph.Reverse[BookWire]("authorId"), Inverse: "author", Limit: 10, Includable: true},
	},
	Inputs: &graph.Inputs{
		Filter:     graph.FilterInput{Enabled: true},
		Pagination: graph.PageInput{Mode: include.PageModeCursor, DefaultLimit: 1, MaxLimit: 10},
	},
	FetchIDs: fetchAuthorsByID,
}

func fetchBooksByID(_ *include.Ctx, ids []string) ([]bookRow, error) {
	out := make([]bookRow, 0, len(ids))
	for _, id := range ids {
		if bk, ok := books[id]; ok {
			out = append(out, bk)
		}
	}
	return out, nil
}

func fetchAuthorsByID(_ *include.Ctx, ids []string) ([]authorRow, error) {
	out := make([]authorRow, 0, len(ids))
	for _, id := range ids {
		if a, ok := authors[id]; ok {
			out = append(out, a)
		}
	}
	return out, nil
}

// fetchBooksByAuthor is the reverse edge's loader: the books of every parent
// author in one call, honouring the edge's top-N limit.
func fetchBooksByAuthor(_ *include.Ctx, parentIDs []string, q include.EdgeQuery) (map[string]graph.ParentRows[bookRow], error) {
	out := make(map[string]graph.ParentRows[bookRow], len(parentIDs))
	for _, pid := range parentIDs {
		var rows []bookRow
		for _, id := range slices.Sorted(maps.Keys(books)) {
			if bk := books[id]; bk.AuthorID == pid {
				rows = append(rows, bk)
			}
		}
		hasMore := q.Limit > 0 && len(rows) > q.Limit
		if hasMore {
			rows = rows[:q.Limit]
		}
		out[pid] = graph.ParentRows[bookRow]{Rows: rows, HasMore: hasMore}
	}
	return out, nil
}

func buildGraph() *graph.Graph {
	b := graph.NewBuilder()
	book := graph.Add(b, Book)
	author := graph.Add(b, Author)
	b.Root(book)
	b.Root(author)
	g, err := b.Compile()
	if err != nil {
		panic(err)
	}
	return g
}

// ------------------------------------------------------------------ the filter, in Go

// This example has no database, so it evaluates the resolved filter in memory.
// What arrives is include.ResolvedFilter: every client name is already
// resolved to its SQL side (Column.Col), every operator already checked
// against the column's Go type, and the tree already within the configured
// limits — a real adapter would print SQL from the same walk (see
// examples/filter).
func matches(f include.ResolvedFilter, cols map[string]any) bool {
	switch t := f.(type) {
	case nil:
		return true
	case include.ResolvedAnd:
		for _, m := range t {
			if !matches(m, cols) {
				return false
			}
		}
		return true
	case include.ResolvedOr:
		// The resolver refuses an empty group, so no member holding is false.
		for _, m := range t {
			if matches(m, cols) {
				return true
			}
		}
		return false
	case include.ResolvedCond:
		return condHolds(t, cols)
	default:
		return false
	}
}

func condHolds(c include.ResolvedCond, cols map[string]any) bool {
	// Hops are the traversed edges of a nested condition; this store is flat,
	// so only root-level columns are evaluated.
	if len(c.Hops) > 0 {
		return true
	}
	// The resolver already checked the client's name against the node's
	// columns, so a column this store cannot answer is a bug in bookCols /
	// authorCols, not in the request.
	got, ok := cols[c.Column.Col]
	if !ok {
		panic(fmt.Sprintf("example store has no column %q", c.Column.Col))
	}
	switch c.Op {
	case include.OpIn, include.OpNin:
		list, _ := c.Value.([]any)
		found := false
		for _, v := range list {
			if n, ok := compare(got, v); ok && n == 0 {
				found = true
				break
			}
		}
		return found == (c.Op == include.OpIn)
	}
	n, ok := compare(got, c.Value)
	if !ok {
		// Incomparable values are unequal: ne holds, everything else fails.
		return c.Op == include.OpNe
	}
	switch c.Op {
	case include.OpEq:
		return n == 0
	case include.OpNe:
		return n != 0
	case include.OpLt:
		return n < 0
	case include.OpLte:
		return n <= 0
	case include.OpGt:
		return n > 0
	case include.OpGte:
		return n >= 0
	}
	return false
}

// compare orders two filter values. Values reach the engine as the PARSER
// produced them — float64 from the JSON syntax, column-typed from the bracket
// syntax — and the engine deliberately never coerces them, so the comparison
// normalizes numbers here.
func compare(a, b any) (int, bool) {
	an, aok := asNumber(a)
	bn, bok := asNumber(b)
	if aok && bok {
		switch {
		case an < bn:
			return -1, true
		case an > bn:
			return 1, true
		}
		return 0, true
	}
	as, aok := a.(string)
	bs, bok := b.(string)
	if aok && bok {
		return strings.Compare(as, bs), true
	}
	ab, aok := a.(bool)
	bb, bok := b.(bool)
	if aok && bok {
		if ab == bb {
			return 0, true
		}
		return 1, true // unequal is all a bool comparison can say
	}
	return 0, false
}

func asNumber(v any) (float64, bool) {
	switch n := v.(type) {
	case int:
		return float64(n), true
	case int64:
		return float64(n), true
	case float64:
		return n, true
	}
	return 0, false
}

func bookCols(r bookRow) map[string]any {
	return map[string]any{"id": r.ID, "title": r.Title, "pub_year": r.Year}
}

func authorCols(r authorRow) map[string]any {
	return map[string]any{"id": r.ID, "name": r.Name}
}

// ------------------------------------------------------------------ the fetchers

// listBooks is the offset-mode list fetcher. QueryArgs is what
// include.ResolveInputs made of the query string: Sort is already the SQL
// column with its direction prefix, Limit is already clamped to the node's
// MaxLimit, Where is the resolved tree. `q` is an EXTRA parameter this
// operation declares on its own input struct, outside the node's contract.
func listBooks(q string) include.ListFetcher {
	return func(_ *include.Ctx, args include.QueryArgs) (include.ListPage, error) {
		rows := make([]bookRow, 0, len(books))
		for _, id := range slices.Sorted(maps.Keys(books)) {
			r := books[id]
			if q != "" && !strings.Contains(strings.ToLower(r.Title), strings.ToLower(q)) {
				continue
			}
			if !matches(args.Where, bookCols(r)) {
				continue
			}
			rows = append(rows, r)
		}
		switch strings.TrimPrefix(args.Sort, "-") {
		case "pub_year":
			sort.SliceStable(rows, func(i, j int) bool { return rows[i].Year < rows[j].Year })
		default:
			sort.SliceStable(rows, func(i, j int) bool { return rows[i].Title < rows[j].Title })
		}
		if strings.HasPrefix(args.Sort, "-") {
			slices.Reverse(rows)
		}
		start := min((args.Page-1)*args.Limit, len(rows))
		end := min(start+args.Limit, len(rows))
		docs := make([]any, 0, end-start)
		for _, r := range rows[start:end] {
			docs = append(docs, r)
		}
		return include.ListPage{Docs: docs, Total: len(rows), HasMore: end < len(rows)}, nil
	}
}

// listAuthors is the cursor-mode list fetcher. The cursor is OPAQUE to the
// engine: whoever produced it decodes it, and here it is simply the last
// author id of the previous page.
func listAuthors(_ *include.Ctx, args include.QueryArgs) (include.ListPage, error) {
	var rows []authorRow
	for _, id := range slices.Sorted(maps.Keys(authors)) {
		r := authors[id]
		if args.Cursor != "" && id <= args.Cursor {
			continue
		}
		if !matches(args.Where, authorCols(r)) {
			continue
		}
		rows = append(rows, r)
	}
	page := include.ListPage{}
	if len(rows) > args.Limit {
		rows, page.HasMore = rows[:args.Limit], true
	}
	for _, r := range rows {
		page.Docs = append(page.Docs, r)
	}
	if page.HasMore && len(rows) > 0 {
		page.NextCursor = rows[len(rows)-1].ID
	}
	return page, nil
}

// ------------------------------------------------------------------ the catalogue

// An ErrorDef is declared once and used twice: Errors(def) puts it in the
// document, def.Err(detail) returns the matching runtime error — so the
// responses cannot drift from what the document promises.
var (
	errBookNotFound  = wfhuma.ErrorDef{Status: 404, Code: "BOOK_NOT_FOUND", Message: "no such book"}
	errAuthorUnknown = wfhuma.ErrorDef{Status: 409, Code: "AUTHOR_UNKNOWN", Message: "no such author"}
)

// ------------------------------------------------------------------ HTTP bodies

// Input structs are huma's: path/query parameters use huma's tag set.
// wfhuma.ListQuery is the embedded list block — its tags carry only the NAMES,
// because the document comes from Inputs() and the validation from
// include.ResolveInputs; huma never sees a constraint it could disagree with.
type getBookInput struct {
	ID string `path:"id" doc:"Book identifier" example:"b1"`
	// Include() declares this parameter first (with x-include-paths); huma
	// sees the name is taken, keeps that declaration, and still binds here.
	Include string `query:"include"`
}

type listBooksInput struct {
	wfhuma.ListQuery
	Q string `query:"q" minLength:"2" example:"hobbit" doc:"Case-insensitive substring match on the title"`
}

type createBookInput struct {
	Include string `query:"include"`
	Body    CreateBookBody
}

type listAuthorsInput struct{ wfhuma.ListQuery }

// BookEnvelope follows the envelope convention: a struct whose first field is
// `data` typed as a Node wrapper (or a slice of one) documents as
// {data: $ref Book}, not as its Go field shape. Node[W] carries the engine's
// hydrated bytes verbatim.
//
// wfhuma.Page[W] is NOT a huma body — copy its Data and the pagination block
// of the resource's mode into the application's own envelope.
type BookEnvelope struct {
	Data wfhuma.Node[BookWire] `json:"data"`
}

type BookPageEnvelope struct {
	Data       []wfhuma.Node[BookWire] `json:"data"`
	Pagination wfhuma.PagePagination   `json:"pagination"`
}

type AuthorPageEnvelope struct {
	Data       []wfhuma.Node[AuthorWire]    `json:"data"`
	Pagination wfhuma.CursorPaginationTotal `json:"pagination"`
}

type getBookOutput struct{ Body BookEnvelope }

type listBooksOutput struct{ Body BookPageEnvelope }

type createBookOutput struct {
	Status int
	Body   BookEnvelope
}

type listAuthorsOutput struct{ Body AuthorPageEnvelope }

// ------------------------------------------------------------------ wiring

func buildAPI() (*http.ServeMux, *wfhuma.API, *wfhuma.API) {
	g := buildGraph()
	mux := http.NewServeMux()

	// New → Attach → Bind → Register. New emits the graph's components and
	// builds the huma config over them; Attach installs the middleware that
	// snapshots the request for Ctx.Header; Bind ties the node to its wire
	// type and registers the Node[W] wrapper component.
	booksSvc := wfhuma.New(g, include.DefaultOptions, "Books", "1.0.0")
	booksAPI := humago.New(mux, booksSvc.Config())
	booksSvc.Attach(booksAPI)
	book := wfhuma.Bind[BookWire](booksSvc, "Book")

	wfhuma.Register(booksSvc, humav2.Operation{
		OperationID: "get-book", Method: http.MethodGet, Path: "/books/{id}", Summary: "Get a book",
	}, func(ctx context.Context, in *getBookInput) (*getBookOutput, error) {
		b, err := book.Get(ctx, in.ID, in.Include)
		if err != nil {
			return nil, err
		}
		return &getBookOutput{Body: BookEnvelope{Data: b}}, nil
	}, book.Include(), booksSvc.Errors(errBookNotFound))

	wfhuma.Register(booksSvc, humav2.Operation{
		OperationID: "list-books", Method: http.MethodGet, Path: "/books", Summary: "List books",
	}, func(ctx context.Context, in *listBooksInput) (*listBooksOutput, error) {
		page, err := book.List(ctx, in.ListQuery, listBooks(in.Q))
		if err != nil {
			return nil, err
		}
		return &listBooksOutput{Body: BookPageEnvelope{Data: page.Data, Pagination: page.Offset}}, nil
	}, book.Inputs())

	wfhuma.Register(booksSvc, humav2.Operation{
		OperationID: "create-book", Method: http.MethodPost, Path: "/books", Summary: "Create a book",
		DefaultStatus: http.StatusCreated,
	}, func(ctx context.Context, in *createBookInput) (*createBookOutput, error) {
		if _, ok := authors[in.Body.AuthorID]; !ok {
			return nil, errAuthorUnknown.Err(in.Body.AuthorID)
		}
		b, err := book.Hydrate(ctx, insertBook(in.Body), in.Include)
		if err != nil {
			return nil, err
		}
		return &createBookOutput{Status: http.StatusCreated, Body: BookEnvelope{Data: b}}, nil
	}, book.Include(), booksSvc.Errors(errAuthorUnknown))

	// A second service on the SAME mux: cursor pagination and the bracket
	// filter syntax. The config is taken by value and its document paths moved
	// aside, so the two documents do not collide on one router.
	authorsSvc := wfhuma.New(g, include.DefaultOptions, "Authors", "1.0.0", wfhuma.WithFilterSyntax(apidoc.FilterBracket))
	cfg := authorsSvc.Config()
	cfg.OpenAPIPath = "/authors-openapi"
	cfg.DocsPath = "/authors-docs"
	authorsAPI := humago.New(mux, cfg)
	authorsSvc.Attach(authorsAPI)
	author := wfhuma.Bind[AuthorWire](authorsSvc, "Author")

	wfhuma.Register(authorsSvc, humav2.Operation{
		OperationID: "list-authors", Method: http.MethodGet, Path: "/authors", Summary: "List authors",
	}, func(ctx context.Context, in *listAuthorsInput) (*listAuthorsOutput, error) {
		page, err := author.List(ctx, in.ListQuery, listAuthors)
		if err != nil {
			return nil, err
		}
		return &listAuthorsOutput{Body: AuthorPageEnvelope{Data: page.Data, Pagination: page.Cursor}}, nil
	}, author.Inputs())

	return mux, booksSvc, authorsSvc
}

// ------------------------------------------------------------------ main

func main() {
	mux, booksSvc, authorsSvc := buildAPI()
	srv := httptest.NewServer(mux)
	defer srv.Close()

	do := func(req *http.Request) []byte {
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			panic(err)
		}
		body, _ := io.ReadAll(resp.Body)
		_ = resp.Body.Close()
		label := req.Method + " " + req.URL.RequestURI()
		fmt.Printf("%-62s %d %s\n", label, resp.StatusCode, body)
		return body
	}
	get := func(path string) []byte {
		req, err := http.NewRequest(http.MethodGet, srv.URL+path, nil)
		if err != nil {
			panic(err)
		}
		return do(req)
	}

	for _, path := range []string{
		"/books/b1?include=author",
		"/books/b1?include=author.books",
		"/books?include=author&page=2",
		"/books?q=the&sort=-year&limit=5",
		"/books?sort=-year&limit=5&where=" + url.QueryEscape(`{"year":{"gte":1950}}`),
		"/books?sort=isbn", // 400 INVALID_SORT: isbn is not a `sort` column
		"/books/nope",
		"/books/b1?include=ghost",
	} {
		get(path)
	}

	// The Wire function reads the request header off the include.Ctx, so the
	// same document gains a viewedFrom field only on this request.
	req, err := http.NewRequest(http.MethodGet, srv.URL+"/books/b1", nil)
	if err != nil {
		panic(err)
	}
	req.Header.Set("X-Remote-Addr", "203.0.113.7")
	do(req)

	post, err := http.NewRequest(http.MethodPost, srv.URL+"/books?include=author",
		strings.NewReader(`{"title":"Children of Dune","year":1976,"authorId":"a2"}`))
	if err != nil {
		panic(err)
	}
	post.Header.Set("Content-Type", "application/json")
	do(post)

	// Cursor mode: the first page carries nextCursor, and the follow-up
	// resumes from it. ?page= on this resource is INVALID_PAGINATION.
	first := get("/authors?limit=1")
	var page struct {
		Pagination struct {
			NextCursor string `json:"nextCursor"`
		} `json:"pagination"`
	}
	if err := json.Unmarshal(first, &page); err != nil {
		panic(err)
	}
	get("/authors?limit=1&cursor=" + url.QueryEscape(page.Pagination.NextCursor))
	get("/authors?" + url.QueryEscape("where[name][eq]") + "=" + url.QueryEscape("Frank Herbert"))
	get("/authors?page=2")

	// The served document. Every list parameter below was written by
	// Inputs() from the node's declaration: the sort enum is the node's
	// sortable columns, the limit maximum its MaxLimit, and the bracket
	// ?where is a deepObject.
	printParams("GET /books", booksSvc.Config().OpenAPI, "/books")
	printParams("GET /authors", authorsSvc.Config().OpenAPI, "/authors")

	if sc, ok := booksSvc.Components().Get("Book"); ok {
		component, _ := json.Marshal(sc.Map())
		fmt.Println("components.Book:", string(component))
	}
}

func printParams(label string, oapi *humav2.OpenAPI, path string) {
	fmt.Println(label + " parameters:")
	for _, p := range oapi.Paths[path].Get.Parameters {
		schema, _ := json.Marshal(p.Schema)
		extra := ""
		if p.Style != "" {
			extra = fmt.Sprintf(" style=%s", p.Style)
		}
		if p.Explode != nil {
			extra += fmt.Sprintf(" explode=%t", *p.Explode)
		}
		fmt.Printf("  %-8s %-52q %s%s\n", p.Name, p.Description, schema, extra)
	}
}
