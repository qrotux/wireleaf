package main

// node_spec.go — the nodes, each declared as a graph.Spec: the struct-literal
// twin of the chained builder. A Spec holds the node's facts (name, slug,
// Row → Wire mapping, primary key), its fetchers, and — when a node owns an
// edge outright — its edges, all next to the wire type. The links BETWEEN
// nodes are declared once, in both directions, in graph_to_many.go.

import (
	"maps"
	"slices"

	"github.com/qrotux/wireleaf/graph"
	"github.com/qrotux/wireleaf/include"
)

// ------------------------------------------------------------------ Author

type authorRow struct {
	ID       string
	Name     string
	MentorID string // "" = no mentor
}

type AuthorWire struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

// authorSpec shows Spec.Edges: an edge the node owns alone — a self-reference
// has no second node to meet in the assembly, so it belongs right here. A zero
// EdgeSpec field is "not declared", like the chained method never called.
var authorSpec = graph.Spec[authorRow, AuthorWire]{
	Name:       "Author",
	Slug:       "author",
	Wire:       func(r authorRow, _ *include.Ctx) AuthorWire { return AuthorWire{ID: r.ID, Name: r.Name} },
	PrimaryKey: func(r authorRow) string { return r.ID },
	Edges: []graph.EdgeSpec[authorRow]{
		{
			Key:        "mentor",
			Kind:       graph.ToOne[AuthorWire](),
			ForeignKey: func(r authorRow) string { return r.MentorID },
			Includable: true,
		},
	},
	FetchIDs: fetchAuthors,
}

var authors = map[string]authorRow{
	"a1": {ID: "a1", Name: "J. R. R. Tolkien"},
	"a2": {ID: "a2", Name: "Christopher Tolkien", MentorID: "a1"},
}

func fetchAuthors(_ *include.Ctx, ids []string) ([]authorRow, error) {
	out := make([]authorRow, 0, len(ids))
	for _, id := range ids {
		if a, ok := authors[id]; ok {
			out = append(out, a)
		}
	}
	return out, nil
}

// ------------------------------------------------------------------ Book

type bookRow struct {
	ID       string
	Title    string
	AuthorID string
	TagIDs   []string // the id-list side of the many-to-many relation
}

type BookWire struct {
	ID    string `json:"id"`
	Title string `json:"title"`
}

var bookSpec = graph.Spec[bookRow, BookWire]{
	Name:       "Book",
	Slug:       "book",
	Wire:       func(r bookRow, _ *include.Ctx) BookWire { return BookWire{ID: r.ID, Title: r.Title} },
	PrimaryKey: func(r bookRow) string { return r.ID },
	FetchIDs:   fetchBooks,
	// No FetchParents here: two reverse edges land on Book (author.books and
	// tag.tagged), and each is a different join, so each relation binds its
	// own reverse fetcher in graph_to_many.go.
}

var books = map[string]bookRow{
	"b1": {ID: "b1", Title: "The Hobbit", AuthorID: "a1", TagIDs: []string{"t1", "t2"}},
	"b2": {ID: "b2", Title: "The Silmarillion", AuthorID: "a2", TagIDs: []string{"t1"}},
}

func fetchBooks(_ *include.Ctx, ids []string) ([]bookRow, error) {
	out := make([]bookRow, 0, len(ids))
	for _, id := range ids {
		if bk, ok := books[id]; ok {
			out = append(out, bk)
		}
	}
	return out, nil
}

// fetchBooksByAuthor is the reverse batch of author.books (the FK is on the
// book); fetchBooksByTag that of tag.tagged (the tag id is in the book's
// list). Each is bound to its own edge by the relation that declares it.
func fetchBooksByAuthor(_ *include.Ctx, parentIDs []string, q include.EdgeQuery) (map[string]graph.ParentRows[bookRow], error) {
	return booksWhere(parentIDs, q, func(bk bookRow, pid string) bool { return bk.AuthorID == pid })
}

func fetchBooksByTag(_ *include.Ctx, parentIDs []string, q include.EdgeQuery) (map[string]graph.ParentRows[bookRow], error) {
	return booksWhere(parentIDs, q, func(bk bookRow, pid string) bool {
		for _, t := range bk.TagIDs {
			if t == pid {
				return true
			}
		}
		return false
	})
}

// booksWhere groups the books that belong to each parent id. A real fetcher
// gets its order from ORDER BY; a map has none, so the ids are sorted to make
// the Limit truncation reproducible.
func booksWhere(parentIDs []string, q include.EdgeQuery, belongs func(bk bookRow, pid string) bool) (map[string]graph.ParentRows[bookRow], error) {
	out := make(map[string]graph.ParentRows[bookRow], len(parentIDs))
	for _, pid := range parentIDs {
		var rows []bookRow
		for _, id := range slices.Sorted(maps.Keys(books)) {
			if bk := books[id]; belongs(bk, pid) {
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

// ------------------------------------------------------------------ Tag

type tagRow struct {
	ID   string
	Name string
}

type TagWire struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

var tagSpec = graph.Spec[tagRow, TagWire]{
	Name:       "Tag",
	Slug:       "tag",
	Wire:       func(r tagRow, _ *include.Ctx) TagWire { return TagWire{ID: r.ID, Name: r.Name} },
	PrimaryKey: func(r tagRow) string { return r.ID },
	FetchIDs:   fetchTags,
}

var tags = map[string]tagRow{
	"t1": {ID: "t1", Name: "fantasy"},
	"t2": {ID: "t2", Name: "classic"},
}

func fetchTags(_ *include.Ctx, ids []string) ([]tagRow, error) {
	out := make([]tagRow, 0, len(ids))
	for _, id := range ids {
		if t, ok := tags[id]; ok {
			out = append(out, t)
		}
	}
	return out, nil
}
