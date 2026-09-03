// Package book is one domain package of the modular example: it owns the Book
// row, wire and fetchers, declared as a graph.Spec. It carries the author
// foreign key as plain data but declares no edge — the assembly package links
// it to the author domain, which this package never imports.
package book

import (
	"sort"

	"github.com/qrotux/wireleaf/graph"
	"github.com/qrotux/wireleaf/include"
)

// Row is what the database hands you.
type Row struct {
	ID       string
	Title    string
	AuthorID string
}

// Wire is what the client sees.
type Wire struct {
	ID    string `json:"id"`
	Title string `json:"title"`
}

// Node is the declarative node: facts and the forward fetcher.
var Node = graph.Spec[Row, Wire]{
	Name:       "Book",
	Slug:       "book",
	Wire:       func(r Row, _ *include.Ctx) Wire { return Wire{ID: r.ID, Title: r.Title} },
	PrimaryKey: func(r Row) string { return r.ID },
	FetchIDs:   FetchByIDs,
}

// Books is the example's data.
var Books = map[string]Row{
	"b1": {ID: "b1", Title: "The Hobbit", AuthorID: "a1"},
	"b2": {ID: "b2", Title: "The Silmarillion", AuthorID: "a1"},
}

// ids is the deterministic scan order; a real fetcher gets it from ORDER BY.
func ids() []string {
	out := make([]string, 0, len(Books))
	for id := range Books {
		out = append(out, id)
	}
	sort.Strings(out)
	return out
}

// FetchByIDs is the forward batch fetcher (ids → rows).
func FetchByIDs(_ *include.Ctx, ids []string) ([]Row, error) {
	out := make([]Row, 0, len(ids))
	for _, id := range ids {
		if bk, ok := Books[id]; ok {
			out = append(out, bk)
		}
	}
	return out, nil
}

// FetchByAuthor is the reverse batch fetcher (author ids → each author's
// books). It is bound by the assembly package, next to the relation it serves.
func FetchByAuthor(_ *include.Ctx, parentIDs []string, q include.EdgeQuery) (map[string]graph.ParentRows[Row], error) {
	out := make(map[string]graph.ParentRows[Row], len(parentIDs))
	for _, pid := range parentIDs {
		var rows []Row
		for _, id := range ids() {
			if bk := Books[id]; bk.AuthorID == pid {
				rows = append(rows, bk)
			}
		}
		hasMore := q.Limit > 0 && len(rows) > q.Limit
		if hasMore {
			rows = rows[:q.Limit]
		}
		out[pid] = graph.ParentRows[Row]{Rows: rows, HasMore: hasMore}
	}
	return out, nil
}
