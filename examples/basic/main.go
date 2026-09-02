// Command basic is the wireleaf quick-start: a two-node resource graph
// declared on the builder, compiled once, then used twice — to hydrate an
// ?include= response at request time, and to emit the OpenAPI components at
// build time. Both come from the same declaration.
package main

import (
	"fmt"
	"sort"

	"github.com/qrotux/wireleaf/apidoc"
	"github.com/qrotux/wireleaf/graph"
	"github.com/qrotux/wireleaf/include"
	"github.com/qrotux/wireleaf/reflector"
)

// Rows are what the database hands you; wires are what the client sees.

type bookRow struct {
	ID       string
	Title    string
	AuthorID string
}

type authorRow struct {
	ID   string
	Name string
}

type BookWire struct {
	ID    string `json:"id"`
	Title string `json:"title"`
}

type AuthorWire struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

// ------------------------------------------------------------------ the data

var books = map[string]bookRow{
	"b1": {ID: "b1", Title: "The Hobbit", AuthorID: "a1"},
}

var authors = map[string]authorRow{
	"a1": {ID: "a1", Name: "J. R. R. Tolkien"},
}

// bookIDs is the deterministic scan order over books. A real fetcher gets its
// order from the database's ORDER BY; a Go map has none, and a fetcher that
// truncates to q.Limit must be reproducible.
func bookIDs() []string {
	ids := make([]string, 0, len(books))
	for id := range books {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	return ids
}

// ------------------------------------------------------------------ the graph

// buildGraph declares the whole graph on one builder and compiles it. Compile
// validates every declaration at once and returns the immutable *graph.Graph,
// which is also the engine's registry.
func buildGraph() (*graph.Graph, include.Resource) {
	b := graph.NewBuilder()

	// Nodes: Row → Wire mapping and the primary-key extractor are typed
	// closures, so a wrong row type is a compile error, not a runtime panic.
	book := graph.Node[bookRow, BookWire](b, "Book").
		Slug("book").
		Wire(func(r bookRow, _ *include.Ctx) BookWire {
			return BookWire{ID: r.ID, Title: r.Title}
		}).
		PrimaryKey(func(r bookRow) string { return r.ID })
	author := graph.Node[authorRow, AuthorWire](b, "Author").
		Slug("author").
		Wire(func(r authorRow, _ *include.Ctx) AuthorWire {
			return AuthorWire{ID: r.ID, Name: r.Name}
		}).
		PrimaryKey(func(r authorRow) string { return r.ID })

	// Edges: the target is addressed by its WIRE TYPE and resolved in Compile,
	// so declaration order (and even declaration file) does not matter and
	// cyclic graphs need no late binding. Includable is deny-by-default — an
	// edge is invisible to ?include= until it is declared includable.
	book.Edge("author", graph.ToOne[AuthorWire]()).
		ForeignKey(func(r bookRow) string { return r.AuthorID }).
		Inverse("books").
		Includable()
	author.Edge("books", graph.Reverse[BookWire]("authorId")).
		Inverse("author").
		Limit(10).
		Includable()

	// Binds: the fetchers the engine calls when an include pulls a node in.
	// FetchIDs is the forward batch (ids → rows); FetchParents is the reverse
	// batch (one call per level, rows keyed by parent id).
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
			for _, id := range bookIDs() {
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
		// *graph.CompileError lists every finding at once — a wiring bug, so
		// fail loudly at start-up rather than at request time.
		panic(err)
	}
	return g, g.Resource("Book")
}

func main() {
	// Start-up: declare and compile once per process.
	g, bookRes := buildGraph()

	// --- request time: parse the client string, hydrate one entity ----------

	inc, err := include.ParseInclude("author")
	if err != nil {
		panic(err)
	}
	ctx := &include.Ctx{Registry: g} // the compiled graph IS the registry
	raw, _, err := include.HydrateByID(bookRes, "b1", inc, nil,
		func(_ *include.Ctx, id string) (any, error) {
			bk, ok := books[id]
			if !ok {
				return nil, nil // nil doc → NOT_FOUND (404)
			}
			return bk, nil
		},
		ctx, include.DefaultOptions)
	if err != nil {
		// *include.Error carries Code / Path / Status for the HTTP layer.
		panic(err)
	}
	fmt.Println(string(raw))

	// --- build time: OpenAPI components from the same declaration -----------

	// Components is the one shared component set a document is assembled into.
	c := apidoc.NewComponents()

	// The canonical reflector turns a wire struct into wireleaf's IR. It lives
	// in its own module (github.com/qrotux/wireleaf/reflector) so the core stays
	// dependency-free; an application on another doc stack implements
	// apidoc.Reflector itself instead.
	refl := &reflector.Reflector{}

	// Reachable walks the includable edges out of Book, so one root is enough
	// to reach Author. EmitComponents reflects each node's wire struct and
	// stitches the includable edges into it as properties.
	frags, err := apidoc.EmitComponents(refl, g.Reachable(bookRes))
	if err != nil {
		panic(err)
	}
	for _, name := range sortedNames(frags) {
		c.Add(name, frags[name])
	}

	// Verify is the whole-document check: every $ref must resolve to a
	// registered (or explicitly external) component.
	if err := c.Verify(); err != nil {
		panic(err)
	}

	// Schema.IR().MarshalJSON preserves property and keyword order, which plain
	// json.Marshal over a map cannot.
	book, _ := c.Get("Book")
	pretty, err := book.IR().MarshalJSON()
	if err != nil {
		panic(err)
	}
	fmt.Println("Book component:", string(pretty))

	// Every include path a client may legally ask for, for the ?include= docs.
	fmt.Println("include paths:", apidoc.IncludePaths(bookRes, include.DefaultLimits))
}

// sortedNames keeps the registration order deterministic; Components itself is
// order-independent, but a stable order makes a diff of the output readable.
func sortedNames(m map[string]apidoc.Schema) []string {
	names := make([]string, 0, len(m))
	for name := range m {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}
