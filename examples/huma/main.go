// Command huma wires a wireleaf graph into a huma v2 API through
// adapters/huma: the graph's OpenAPI components become huma's schema registry,
// handlers return engine-hydrated bytes through Node[W] wrappers, and the
// ?include= parameter is documented from the graph itself.
//
// The program builds the API on net/http's ServeMux, serves it in-process
// with httptest, performs a handful of requests, and prints what the document
// says about the parameters and the Book component — so it runs to completion
// like the other examples instead of listening on a port.
package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"slices"
	"sort"
	"strings"

	humav2 "github.com/danielgtaylor/huma/v2"
	"github.com/danielgtaylor/huma/v2/adapters/humago"

	wfhuma "github.com/qrotux/wireleaf/adapters/huma"
	"github.com/qrotux/wireleaf/apidoc"
	"github.com/qrotux/wireleaf/graph"
	"github.com/qrotux/wireleaf/include"
	"github.com/qrotux/wireleaf/reflector"
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

// Wire types are what the reflector documents. Field annotations use the
// swaggest tag set (description, title, format, minimum, enum, ...): they land
// in the component's JSON Schema verbatim. Nullability is NOT a tag concern —
// the policy decides it from the Go shape.
type BookWire struct {
	ID    string `json:"id" description:"Stable book identifier" example:"b1"`
	Title string `json:"title" description:"Title as printed on the cover" minLength:"1"`
	Year  int    `json:"year" description:"Year of first publication" minimum:"1450"`
}

type AuthorWire struct {
	ID   string `json:"id" description:"Stable author identifier" example:"a1"`
	Name string `json:"name" description:"Display name, as commonly credited"`
}

var books = map[string]bookRow{
	"b1": {ID: "b1", Title: "The Hobbit", Year: 1937, AuthorID: "a1"},
	"b2": {ID: "b2", Title: "The Silmarillion", Year: 1977, AuthorID: "a1"},
	"b3": {ID: "b3", Title: "Dune", Year: 1965, AuthorID: "a2"},
}

var authors = map[string]authorRow{
	"a1": {ID: "a1", Name: "J. R. R. Tolkien"},
	"a2": {ID: "a2", Name: "Frank Herbert"},
}

func sortedKeys[V any](m map[string]V) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

// ------------------------------------------------------------------ the graph

func buildGraph() (*graph.Graph, include.Resource) {
	b := graph.NewBuilder()

	book := graph.Node[bookRow, BookWire](b, "Book").
		Slug("book").
		Wire(func(r bookRow, _ *include.Ctx) BookWire { return BookWire{ID: r.ID, Title: r.Title, Year: r.Year} }).
		PrimaryKey(func(r bookRow) string { return r.ID })
	author := graph.Node[authorRow, AuthorWire](b, "Author").
		Slug("author").
		Wire(func(r authorRow, _ *include.Ctx) AuthorWire { return AuthorWire{ID: r.ID, Name: r.Name} }).
		PrimaryKey(func(r authorRow) string { return r.ID })

	book.Edge("author", graph.ToOne[AuthorWire]()).
		ForeignKey(func(r bookRow) string { return r.AuthorID }).
		Inverse("books").
		Includable()
	author.Edge("books", graph.Reverse[BookWire]("authorId")).
		Inverse("author").
		Limit(10).
		Includable()

	graph.FetchIDs(b, author, func(_ *include.Ctx, ids []string) ([]authorRow, error) {
		out := make([]authorRow, 0, len(ids))
		for _, id := range ids {
			if a, ok := authors[id]; ok {
				out = append(out, a)
			}
		}
		return out, nil
	})
	graph.FetchIDs(b, book, func(_ *include.Ctx, ids []string) ([]bookRow, error) {
		out := make([]bookRow, 0, len(ids))
		for _, id := range ids {
			if bk, ok := books[id]; ok {
				out = append(out, bk)
			}
		}
		return out, nil
	})
	graph.FetchParents(b, book, func(_ *include.Ctx, parentIDs []string, q include.EdgeQuery) (map[string]graph.ParentRows[bookRow], error) {
		out := make(map[string]graph.ParentRows[bookRow], len(parentIDs))
		for _, pid := range parentIDs {
			var rows []bookRow
			for _, id := range sortedKeys(books) {
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
	})

	b.Root(book)
	g, err := b.Compile()
	if err != nil {
		panic(err)
	}
	return g, g.Resource("Book")
}

// ------------------------------------------------------------------ HTTP bodies

// The envelope convention: a struct whose first field is `data` typed as a
// Node wrapper (or a slice of one) documents as {data: $ref Book}, not as its
// Go field shape. Node[W] carries the engine's bytes verbatim.

type BookEnvelope struct {
	Data wfhuma.Node[BookWire] `json:"data"`
}

type BookPageEnvelope struct {
	Data       []wfhuma.Node[BookWire] `json:"data"`
	Pagination wfhuma.PagePagination   `json:"pagination"`
}

// Input structs are huma's: path/query parameters are documented with huma's
// own tag set (doc, default, minimum, maximum, enum, example, ...), which is a
// DIFFERENT dialect from the wire types above — `doc:` here, `description:`
// there. A body field would go back through the bridge to the reflector.
type getBookInput struct {
	ID string `path:"id" doc:"Book identifier" example:"b1"`
	// Op's IncludeParam declares this parameter first (with x-include-paths);
	// huma sees the name is taken, keeps that declaration, and still binds
	// the query value here.
	Include string `query:"include"`
}

type getBookOutput struct {
	Body BookEnvelope
}

type listBooksInput struct {
	Include string `query:"include"`
	Page    int    `query:"page" default:"1" minimum:"1" doc:"Page number, 1-based"`
	Limit   int    `query:"limit" default:"2" minimum:"1" maximum:"50" doc:"Books per page"`
	Sort    string `query:"sort" default:"title" enum:"title,-title,year,-year" doc:"Sort key; a leading '-' sorts descending"`
	Q       string `query:"q" minLength:"2" example:"hobbit" doc:"Case-insensitive substring match on the title"`
}

type listBooksOutput struct {
	Body BookPageEnvelope
}

// asHumaError maps the engine's structured *include.Error (Code, Path, Status)
// onto huma's status error; anything else is a 500.
func asHumaError(err error) error {
	var ie *include.Error
	if errors.As(err, &ie) {
		return humav2.NewError(ie.Status, ie.Error())
	}
	return err
}

// selectBooks applies the list query: substring filter on the title, then the
// declared sort key. huma has already validated q and sort against the tags.
func selectBooks(q, sortKey string) []bookRow {
	rows := make([]bookRow, 0, len(books))
	for _, id := range sortedKeys(books) {
		if r := books[id]; q == "" || strings.Contains(strings.ToLower(r.Title), strings.ToLower(q)) {
			rows = append(rows, r)
		}
	}
	desc := strings.HasPrefix(sortKey, "-")
	switch strings.TrimPrefix(sortKey, "-") {
	case "year":
		sort.SliceStable(rows, func(i, j int) bool { return rows[i].Year < rows[j].Year })
	default:
		sort.SliceStable(rows, func(i, j int) bool { return rows[i].Title < rows[j].Title })
	}
	if desc {
		slices.Reverse(rows)
	}
	return rows
}

// ------------------------------------------------------------------ wiring

func buildAPI() (*http.ServeMux, humav2.Config) {
	g, bookRes := buildGraph()

	// 1. One shared component set: the graph's components go in first, then
	//    the Node wrappers are bound to their component names.
	c := apidoc.NewComponents()
	frags, err := apidoc.EmitComponents(&reflector.Reflector{}, g.Reachable(bookRes))
	if err != nil {
		panic(err)
	}
	for _, name := range sortedKeys(frags) {
		c.Add(name, frags[name])
	}
	wfhuma.RegisterNode[BookWire](c, "Book")
	wfhuma.RegisterNode[AuthorWire](c, "Author")

	// 2. The document's registry IS the bridge over c. Everything huma
	//    reflects on its own (inputs, envelopes) lands in the same set.
	cfg := wfhuma.NewConfig("Books", "1.0.0", wfhuma.WithRegistry(c))
	mux := http.NewServeMux()
	api := humago.New(mux, cfg)

	opts := include.DefaultOptions
	notFound := wfhuma.ErrorDef{Status: 404, Code: "BOOK_NOT_FOUND", Message: "no such book"}

	// 3. GET /books/{id}: one hydrated entity.
	humav2.Register(api, wfhuma.Op(humav2.Operation{
		OperationID: "get-book",
		Method:      http.MethodGet,
		Path:        "/books/{id}",
		Summary:     "Get a book",
	},
		wfhuma.IncludeParamWithLimits(bookRes, opts.Limits),
		wfhuma.Errors(notFound),
	), func(ctx context.Context, in *getBookInput) (*getBookOutput, error) {
		inc, err := include.ParseInclude(in.Include)
		if err != nil {
			return nil, asHumaError(err)
		}
		raw, _, err := include.HydrateByID(bookRes, in.ID, inc, nil,
			func(_ *include.Ctx, id string) (any, error) {
				bk, ok := books[id]
				if !ok {
					return nil, nil // → include.NOT_FOUND, status 404
				}
				return bk, nil
			},
			&include.Ctx{Context: ctx, Registry: g}, opts)
		if err != nil {
			return nil, asHumaError(err)
		}
		return &getBookOutput{Body: BookEnvelope{Data: wfhuma.NodeOf[BookWire](raw)}}, nil
	})

	// 4. GET /books: a page of hydrated entities in the PagePagination envelope.
	humav2.Register(api, wfhuma.Op(humav2.Operation{
		OperationID: "list-books",
		Method:      http.MethodGet,
		Path:        "/books",
		Summary:     "List books",
	},
		wfhuma.IncludeParamWithLimits(bookRes, opts.Limits),
	), func(ctx context.Context, in *listBooksInput) (*listBooksOutput, error) {
		inc, err := include.ParseInclude(in.Include)
		if err != nil {
			return nil, asHumaError(err)
		}
		fetch := func(_ *include.Ctx, q include.QueryArgs) ([]any, int, bool, error) {
			rows := selectBooks(in.Q, in.Sort)
			start := min((q.Page-1)*q.Limit, len(rows))
			end := min(start+q.Limit, len(rows))
			docs := make([]any, 0, end-start)
			for _, r := range rows[start:end] {
				docs = append(docs, r)
			}
			return docs, len(rows), end < len(rows), nil
		}
		res, _, err := include.HydrateByQuery(bookRes, include.QueryArgs{Page: in.Page, Limit: in.Limit}, inc, nil, fetch,
			&include.Ctx{Context: ctx, Registry: g}, opts)
		if err != nil {
			return nil, asHumaError(err)
		}
		data := make([]wfhuma.Node[BookWire], len(res.Data))
		for i, raw := range res.Data {
			data[i] = wfhuma.NodeOf[BookWire](raw)
		}
		totalPages := (res.Total + in.Limit - 1) / in.Limit
		return &listBooksOutput{Body: BookPageEnvelope{
			Data: data,
			Pagination: wfhuma.PagePagination{
				Page: in.Page, TotalPages: totalPages, TotalDocs: res.Total,
				HasNextPage: res.HasMore, HasPrevPage: in.Page > 1,
			},
		}}, nil
	})

	return mux, cfg
}

// ------------------------------------------------------------------ main

func main() {
	mux, cfg := buildAPI()
	srv := httptest.NewServer(mux)
	defer srv.Close()

	for _, path := range []string{
		"/books/b1?include=author",
		"/books/b1?include=author.books",
		"/books?include=author&page=2",
		"/books?q=the&sort=-year&limit=5",
		"/books?sort=isbn",
		"/books/nope",
		"/books/b1?include=ghost",
	} {
		resp, err := http.Get(srv.URL + path)
		if err != nil {
			panic(err)
		}
		body, _ := io.ReadAll(resp.Body)
		_ = resp.Body.Close()
		fmt.Printf("GET %-32s %d %s\n", path, resp.StatusCode, body)
	}

	// The served document: the include parameter carries every legal path,
	// the other parameters carry their huma tags, and the response bodies
	// $ref the graph-derived components whose properties carry the wire tags.
	oapi := cfg.OpenAPI
	get := oapi.Paths["/books/{id}"].Get
	for _, p := range get.Parameters {
		if p.Name == "include" {
			fmt.Println("x-include-paths:", p.Schema.Extensions[apidoc.XIncludePaths])
		}
	}
	fmt.Println("GET /books parameters:")
	for _, p := range oapi.Paths["/books"].Get.Parameters {
		schema, _ := json.Marshal(p.Schema)
		fmt.Printf("  %-8s %-48q %s\n", p.Name, p.Description, schema)
	}
	book, _ := json.Marshal(wfhuma.BridgeOf(oapi).Map()["Book"])
	fmt.Println("components.Book:", string(book))
	names := make([]string, 0)
	for name := range wfhuma.BridgeOf(oapi).Map() {
		names = append(names, name)
	}
	sort.Strings(names)
	fmt.Println("components:", names)
}
