// Command policies demonstrates wireleaf's three edge policies — the knobs
// that decide what the engine does when reality does not match the contract
// the graph declared.
//
// Each policy has an engine-wide default and a per-edge OVERRIDE declared
// with graph.Policies(...). ExcludeRequiredPolicy is resolve-time and lives on
// include.Options; the other two are materialize-time and live on
// include.Ctx.Policies. Every default is the permissive one, so a graph that
// declares no policy stays permissive:
//
//	ExcludeRequiredPolicy  what ?exclude= naming a Required() edge does
//	                       ExcludeRequiredTolerant (default) | ExcludeRequiredStrict
//	MissingRequiredPolicy  what a Required() edge with no target does
//	                       MissingRequiredNull (default) | MissingRequiredError
//	MissingForeignPolicy   what a NON-EMPTY foreign key that resolves to no
//	                       row does — a dangling reference
//	                       MissingForeignNull (default) | MissingForeignError
//
// The data below is deliberately broken in two ways a real database gets
// broken: one book points at an author id that does not exist, another at a
// missing publisher, and one book's tag list holds an id nothing answers to.
// Run the program to see each policy react to them.
package main

import (
	"errors"
	"fmt"
	"sort"

	"github.com/qrotux/wireleaf/graph"
	"github.com/qrotux/wireleaf/include"
)

// ------------------------------------------------------------------ the data

type bookRow struct {
	ID          string
	Title       string
	AuthorID    string
	PublisherID string
	TagIDs      []string
}

type authorRow struct{ ID, Name string }
type publisherRow struct{ ID, Name string }
type tagRow struct{ ID, Label string }

type BookWire struct {
	ID    string `json:"id"`
	Title string `json:"title"`
}

type AuthorWire struct {
	ID   string `json:"id" pk:"" fk:"books"`
	Name string `json:"name"`
}

type PublisherWire struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

type TagWire struct {
	ID    string `json:"id"`
	Label string `json:"label"`
}

// Exactly one row is missing behind each broken reference below.
var (
	authors    = map[string]authorRow{"a1": {ID: "a1", Name: "J. R. R. Tolkien"}}
	publishers = map[string]publisherRow{"p1": {ID: "p1", Name: "Allen & Unwin"}}
	tags       = map[string]tagRow{"t1": {ID: "t1", Label: "fantasy"}}

	books = map[string]bookRow{
		// intact, except for one tag id nothing answers to
		"b1": {ID: "b1", Title: "The Hobbit", AuthorID: "a1", PublisherID: "p1",
			TagIDs: []string{"t1", "ghost-tag"}},
		// an ORPHANED author FK under a Required() edge
		"b2": {ID: "b2", Title: "The Lost Road", AuthorID: "ghost-author", PublisherID: "p1",
			TagIDs: []string{"t1"}},
		// an orphaned publisher FK under an ordinary, nullable edge
		"b3": {ID: "b3", Title: "Leaf by Niggle", AuthorID: "a1", PublisherID: "ghost-pub",
			TagIDs: []string{"t1"}},
	}
)

// ------------------------------------------------------------------ the graph

func buildGraph() (*graph.Graph, include.Resource) {
	b := graph.NewBuilder()

	book := graph.Node[bookRow, BookWire](b, "Book").
		Wire(func(r bookRow, _ *include.Ctx) BookWire {
			return BookWire{ID: r.ID, Title: r.Title}
		}).
		PrimaryKey(func(r bookRow) string { return r.ID }).
		// Both to-one edges are default-included, so every response carries
		// them and the policies below are observable without any ?include=.
		// Only publisher needs saying: the Required() author edge lands in
		// Defaults automatically (Compile appends required keys — a required
		// key the engine never materializes would break the published
		// component on every plain response, so the coupling is constructed,
		// not policed).
		Defaults("publisher")
	author := graph.Node[authorRow, AuthorWire](b, "Author").
		Wire(func(r authorRow, _ *include.Ctx) AuthorWire {
			return AuthorWire{ID: r.ID, Name: r.Name}
		}).
		PrimaryKey(func(r authorRow) string { return r.ID })
	publisher := graph.Node[publisherRow, PublisherWire](b, "Publisher").
		Wire(func(r publisherRow, _ *include.Ctx) PublisherWire {
			return PublisherWire{ID: r.ID, Name: r.Name}
		}).
		PrimaryKey(func(r publisherRow) string { return r.ID })
	tag := graph.Node[tagRow, TagWire](b, "Tag").
		Wire(func(r tagRow, _ *include.Ctx) TagWire {
			return TagWire{ID: r.ID, Label: r.Label}
		}).
		PrimaryKey(func(r tagRow) string { return r.ID })

	// author — Required(), and DECLARES NO POLICY. It therefore inherits
	// whatever the request passes, which is what scenarios 2/3/6/7 vary.
	book.Edge("author", graph.ToOne[AuthorWire]()).
		ForeignKey(func(r bookRow) string { return r.AuthorID }).
		Required().
		Includable()

	// publisher — an ordinary nullable edge that PINS strictness. This is why
	// MissingForeignPolicy is scoped by edge KIND and not by Required(): the
	// document may well permit null here, and an orphaned FK is still a bug
	// worth failing on. The pin beats a permissive request (scenario 4).
	book.Edge("publisher", graph.ToOne[PublisherWire]()).
		ForeignKey(func(r bookRow) string { return r.PublisherID }).
		Includable().
		Policies(include.MissingForeignError)

	// tags — pins the OTHER direction: tolerant no matter how strict the
	// request is (scenario 5). A dangling id here vanishes from the list
	// silently, which is exactly what MissingForeignError exists to catch —
	// so an edge that opts out should mean it.
	book.Edge("tags", graph.ToMany[TagWire]()).
		ForeignKeys(func(r bookRow) []string { return r.TagIDs }).
		Includable().
		Policies(include.MissingForeignNull)

	graph.FetchIDs(b, author, func(_ *include.Ctx, ids []string) ([]authorRow, error) {
		return pick(authors, ids), nil
	})
	graph.FetchIDs(b, publisher, func(_ *include.Ctx, ids []string) ([]publisherRow, error) {
		return pick(publishers, ids), nil
	})
	graph.FetchIDs(b, tag, func(_ *include.Ctx, ids []string) ([]tagRow, error) {
		return pick(tags, ids), nil
	})

	b.Root(book)

	g, err := b.Compile()
	if err != nil {
		panic(err)
	}
	return g, g.Resource("Book")
}

// pick returns the rows that exist, in the requested order. An id with no row
// is simply absent from the result — the very thing the policies react to.
func pick[T any](src map[string]T, ids []string) []T {
	out := make([]T, 0, len(ids))
	for _, id := range ids {
		if row, ok := src[id]; ok {
			out = append(out, row)
		}
	}
	return out
}

// ------------------------------------------------------------------ scenarios

// run hydrates one book and prints the bytes, or the error the policy raised.
func run(res include.Resource, ctx *include.Ctx, opts include.Options, id, inc, exc, note string) {
	tree, err := include.ParseInclude(inc)
	if err != nil {
		panic(err)
	}
	var excludes [][]string
	if exc != "" {
		if excludes, err = include.ParseExclude(exc); err != nil {
			panic(err)
		}
	}

	raw, _, err := include.HydrateByID(res, id, tree, excludes,
		func(_ *include.Ctx, id string) (any, error) {
			row, ok := books[id]
			if !ok {
				return nil, nil // nil doc → NOT_FOUND (404)
			}
			return row, nil
		},
		ctx, opts)

	fmt.Println(note)
	if err != nil {
		fmt.Printf("  error: %v\n\n", err)
		return
	}
	fmt.Printf("  %s\n\n", raw)
}

func main() {
	g, bookRes := buildGraph()

	// The three strict variants, each changing ONE policy. The two
	// materialize-time policies vary on Ctx.Policies; the resolve-time exclude
	// policy varies on Options.
	var (
		tolerant = include.DefaultOptions
		ctx      = &include.Ctx{Registry: g}

		strictRequiredCtx = &include.Ctx{Registry: g,
			Policies: include.Policies{MissingRequired: include.MissingRequiredError}}
		strictForeignCtx = &include.Ctx{Registry: g,
			Policies: include.Policies{MissingForeign: include.MissingForeignError}}
		strictExclude = include.Options{Limits: include.DefaultLimits,
			ExcludeRequiredPolicy: include.ExcludeRequiredStrict}
	)

	fmt.Println("=== defaults: every policy permissive ===")

	run(bookRes, ctx, tolerant, "b1", "tags", "",
		"1. b1 — tag \"ghost-tag\" resolves to nothing and is DROPPED from the list,\n"+
			"   silently: the response simply has one tag fewer than the row said.")

	run(bookRes, ctx, tolerant, "b2", "", "",
		"2. b2 — its author FK is orphaned, so a Required() key comes back NULL.\n"+
			"   The published component says this key is non-null; the bytes disagree,\n"+
			"   and nothing on the server said so. That is the default, by choice.")

	fmt.Println("=== the same data, one policy made strict ===")

	run(bookRes, strictRequiredCtx, tolerant, "b2", "", "",
		"3. b2 + MissingRequiredError — the contradiction from 2 becomes a failed\n"+
			"   request instead of a quiet null. Uncoded (a 5xx to the client): this is\n"+
			"   a server-side integrity fault, not a malformed client request.")

	run(bookRes, ctx, tolerant, "b3", "", "",
		"4. b3 + permissive Ctx — but the publisher edge PINS\n"+
			"   MissingForeignError, and a per-edge policy beats the engine-wide default.\n"+
			"   Note publisher is not Required(): a dangling FK is a bug either way.")

	run(bookRes, strictForeignCtx, tolerant, "b1", "tags", "",
		"5. b1 + MissingForeignError on the Ctx — the opposite direction: the tags edge\n"+
			"   pins MissingForeignNull, so \"ghost-tag\" is still dropped in silence\n"+
			"   while the rest of the graph runs strict.")

	fmt.Println("=== ?exclude= against a Required() key ===")

	run(bookRes, ctx, tolerant, "b1", "", "author",
		"6. b1 + ?exclude=author, permissive — the key is declared always present,\n"+
			"   so the exclude is a silent no-op and author stays in the response.\n"+
			"   A required key is never actually removed, under either policy.")

	run(bookRes, ctx, strictExclude, "b1", "", "author",
		"7. b1 + ?exclude=author + ExcludeRequiredStrict — the same request is\n"+
			"   REFUSED instead, as INVALID_INCLUDE at that path: the client asked for\n"+
			"   a response shape the contract forbids, and now hears about it.")

	fmt.Println("=== what Compile rejects at start-up ===")
	for _, f := range rejectedDeclarations() {
		fmt.Printf("  %s\n", f)
	}
}

// rejectedDeclarations compiles a deliberately wrong graph and returns the
// findings, so the scope rules are visible rather than merely documented:
// the two Required-scoped policies are inert on an ordinary edge, and
// MissingForeign is inert where no parent-side FK is read.
func rejectedDeclarations() []string {
	b := graph.NewBuilder()
	book := graph.Node[bookRow, BookWire](b, "Book").
		Wire(func(r bookRow, _ *include.Ctx) BookWire { return BookWire{ID: r.ID} }).
		PrimaryKey(func(r bookRow) string { return r.ID })
	author := graph.Node[authorRow, AuthorWire](b, "Author").
		Wire(func(r authorRow, _ *include.Ctx) AuthorWire { return AuthorWire{ID: r.ID} }).
		PrimaryKey(func(r authorRow) string { return r.ID })

	// Not Required() → the two Required-scoped policies are meaningless here.
	book.Edge("author", graph.ToOne[AuthorWire]()).
		ForeignKey(func(r bookRow) string { return r.AuthorID }).
		Policies(include.ExcludeRequiredStrict, include.MissingRequiredError)
	// A reverse edge fetches by the PARENT's own id, so "no children" is an
	// empty collection, never a dangling reference.
	author.Edge("books", graph.Reverse[BookWire]("authorId")).
		Policies(include.MissingForeignError)
	b.Root(book)

	_, err := b.Compile()
	var ce *graph.CompileError
	if !errors.As(err, &ce) {
		return []string{"expected a *graph.CompileError"}
	}
	out := make([]string, 0, len(ce.Findings))
	for _, f := range ce.Findings {
		out = append(out, f.String())
	}
	sort.Strings(out)
	return out
}
