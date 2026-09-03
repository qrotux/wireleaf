package main

// graph_to_many.go — the assembly: register the Specs and declare the links
// between them. A relation declares BOTH directions in one place and pairs
// them with Inverse; targets and Row types are inferred from the handles, so
// no wire type is named and a wrong handle is a Go compile error. Compile
// sees two ordinary edges per relation.

import "github.com/qrotux/wireleaf/graph"

func buildGraph() (*graph.Graph, error) {
	b := graph.NewBuilder()

	// Add returns the same *NodeHandle graph.Node does, so Root, chained
	// Edge calls and the bind functions all still apply to it.
	author := graph.Add(b, authorSpec)
	book := graph.Add(b, bookSpec)
	tag := graph.Add(b, tagSpec)

	// One-to-many: author.books (reverse) and book.author (to-one), the FK
	// read off the many-side row. Required/Policies address the to-one side,
	// Limit/Sort/Args the reverse side, Includable both. FetchParents binds
	// the reverse fetcher of THIS edge (author.books): the join lives next to
	// the link, and tag.tagged below gets its own.
	graph.OneToMany(author, book, "books", "author").
		ForeignKey("authorId", func(r bookRow) string { return r.AuthorID }).
		Required().
		Includable().
		Limit(10).
		FetchParents(fetchBooksByAuthor)

	// Many-to-many, LEFT holding the id list: book.tags (forward hasMany,
	// the ids read off bookRow) and tag.tagged (reverse). Limit applies to
	// both enveloped sides; Left/Right expose each side's full EdgeBuilder
	// for anything else. ForeignKeys names the id-list field and reads it in
	// one call; the name is what marks tag.tagged as a reverse edge.
	graph.ManyToMany(book, tag, "tags", "tagged").
		ForeignKeys("tagIds", func(r bookRow) []string { return r.TagIDs }).
		Includable().
		Limit(5).
		FetchParents(fetchBooksByTag)

	b.Root(book)
	b.Root(tag)
	return b.Compile()
}
