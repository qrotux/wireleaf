// Package author is one domain package of the modular example: it owns the
// Author row, wire and fetcher, declared as a graph.Spec, and knows nothing
// about books. The links between domains live in the assembly package.
package author

import (
	"github.com/qrotux/wireleaf/graph"
	"github.com/qrotux/wireleaf/include"
)

// Row is what the database hands you.
type Row struct {
	ID   string
	Name string
}

// Wire is what the client sees.
type Wire struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

// Node is the declarative node: facts and fetchers, no edges — those are the
// assembly package's business.
var Node = graph.Spec[Row, Wire]{
	Name:       "Author",
	Slug:       "author",
	Wire:       func(r Row, _ *include.Ctx) Wire { return Wire{ID: r.ID, Name: r.Name} },
	PrimaryKey: func(r Row) string { return r.ID },
	FetchIDs:   FetchByIDs,
}

// Authors is the example's data.
var Authors = map[string]Row{
	"a1": {ID: "a1", Name: "J. R. R. Tolkien"},
}

// FetchByIDs is the forward batch fetcher (ids → rows).
func FetchByIDs(_ *include.Ctx, ids []string) ([]Row, error) {
	out := make([]Row, 0, len(ids))
	for _, id := range ids {
		if a, ok := Authors[id]; ok {
			out = append(out, a)
		}
	}
	return out, nil
}
