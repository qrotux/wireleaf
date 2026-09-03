package huma

import (
	"encoding/json"
	"testing"

	"github.com/qrotux/wireleaf/graph"
	"github.com/qrotux/wireleaf/include"
)

type fixtureRow struct {
	ID, Title string
	Year      int
}

// BookWireT is the fixture wire type (exported: the reflector needs a named,
// exported struct to make a component).
type BookWireT struct {
	ID    string `json:"id" col:"id"`
	Title string `json:"title" col:"title,sort,filter"`
	Year  int    `json:"year" col:"pub_year,sort,filter"`
	Seen  string `json:"seen,omitempty"` // filled from X-Remote-Addr by the Wire function
}

var fixtureRows = map[string]fixtureRow{
	"b1": {ID: "b1", Title: "The Hobbit", Year: 1937},
	"b2": {ID: "b2", Title: "Dune", Year: 1965},
}

func bookRowOf(id string) fixtureRow { return fixtureRows[id] }

// fixtureGraph compiles one root node "Book" with the given pagination mode:
// sort and filter enabled, DefaultLimit 2, MaxLimit 50, a FetchIDs over
// fixtureRows, and a Wire function that copies the X-Remote-Addr header.
func fixtureGraph(t *testing.T, mode include.PageMode) *graph.Graph {
	t.Helper()
	b := graph.NewBuilder()
	book := graph.Node[fixtureRow, BookWireT](b, "Book").
		Wire(func(r fixtureRow, c *include.Ctx) BookWireT {
			return BookWireT{ID: r.ID, Title: r.Title, Year: r.Year, Seen: c.Header("X-Remote-Addr")}
		}).
		PrimaryKey(func(r fixtureRow) string { return r.ID }).
		Inputs(graph.Inputs{
			Sort:       graph.SortInput{Enabled: true},
			Filter:     graph.FilterInput{Enabled: true},
			Pagination: graph.PageInput{Mode: mode, DefaultLimit: 2, MaxLimit: 50},
		})
	graph.FetchIDs(b, book, func(_ *include.Ctx, ids []string) ([]fixtureRow, error) {
		out := make([]fixtureRow, 0, len(ids))
		for _, id := range ids {
			if r, ok := fixtureRows[id]; ok {
				out = append(out, r)
			}
		}
		return out, nil
	})
	b.Root(book)
	g, err := b.Compile()
	if err != nil {
		t.Fatalf("fixture graph: %v", err)
	}
	return g
}

func inputsGraph(t *testing.T) *graph.Graph { return fixtureGraph(t, include.PageModeOffset) }
func cursorGraph(t *testing.T) *graph.Graph { return fixtureGraph(t, include.PageModeCursor) }

func mustJSON(t *testing.T, v any) []byte {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return b
}
