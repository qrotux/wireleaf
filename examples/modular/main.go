// Command modular is the split-by-domain layout: each domain package declares
// its own node as a graph.Spec and knows nothing about the others; this
// assembly package imports them all, registers the specs with graph.Add and
// declares the links between them with graph.OneToMany. Imports flow one way
// (assembly → domains), so a cyclic graph needs no cyclic imports.
package main

import (
	"fmt"

	"github.com/qrotux/wireleaf/examples/modular/author"
	"github.com/qrotux/wireleaf/examples/modular/book"
	"github.com/qrotux/wireleaf/graph"
	"github.com/qrotux/wireleaf/include"
)

func buildGraph() (*graph.Graph, error) {
	b := graph.NewBuilder()

	// Add registers a Spec and returns the same *NodeHandle graph.Node does.
	bk := graph.Add(b, book.Node)
	au := graph.Add(b, author.Node)

	// One relation, both directions: book.author (to-one, with the FK read
	// off book.Row) and author.books (reverse), paired by Inverse. Targets and
	// Row types come from the handles, so no wire type is named here and a
	// wrong handle does not build. The reverse fetcher is bound alongside.
	graph.OneToMany(au, bk, "books", "author").
		ForeignKey("authorId", func(r book.Row) string { return r.AuthorID }).
		Includable().
		Limit(10).
		FetchParents(book.FetchByAuthor)

	b.Root(bk)
	return b.Compile()
}

func main() {
	g, err := buildGraph()
	if err != nil {
		panic(err)
	}

	inc, err := include.ParseInclude("author.books")
	if err != nil {
		panic(err)
	}
	ctx := &include.Ctx{Registry: g}
	raw, _, err := include.HydrateByID(g.Resource("Book"), "b1", inc, nil,
		func(_ *include.Ctx, id string) (any, error) {
			bk, ok := book.Books[id]
			if !ok {
				return nil, nil
			}
			return bk, nil
		},
		ctx, include.DefaultOptions)
	if err != nil {
		panic(err)
	}
	fmt.Println(string(raw))
}
