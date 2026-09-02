package graph

import (
	"reflect"
	"testing"

	"github.com/qrotux/wireleaf/include"
)

func names(rs []include.Resource) []string {
	out := make([]string, len(rs))
	for i, r := range rs {
		out[i] = r.Name()
	}
	return out
}

func TestGraphResourcePanicsOnUnknownName(t *testing.T) {
	g := happyGraph(t)
	defer func() {
		if r := recover(); r == nil {
			t.Error("Resource(unknown) did not panic")
		}
	}()
	g.Resource("Nope")
}

// Reachable is BFS over INCLUDABLE edges only, layer by layer, edges in
// declaration order, deduped by Name(), starting node included.
func TestGraphReachable(t *testing.T) {
	g := happyGraph(t)

	// Book edges (declaration order): author (incl), tags (incl),
	// reviews (incl), participants (computed, no target).
	// Author edges: books (incl) → Book (already seen).
	got := names(g.Reachable(g.Resource("Book")))
	want := []string{"Book", "Author", "Tag", "Review"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("Reachable(Book) = %v, want %v", got, want)
	}

	if got2 := names(g.Reachable(g.Resource("Book"))); !reflect.DeepEqual(got, got2) {
		t.Errorf("Reachable is not deterministic: %v vs %v", got, got2)
	}

	if got := names(g.Reachable(g.Resource("Tag"))); !reflect.DeepEqual(got, []string{"Tag"}) {
		t.Errorf("Reachable(Tag) = %v, want [Tag]", got)
	}
	if got := names(g.Reachable(g.Resource("Author"))); !reflect.DeepEqual(got, []string{"Author", "Book", "Tag", "Review"}) {
		t.Errorf("Reachable(Author) = %v", got)
	}
}

func TestGraphReachableSkipsNonIncludableEdges(t *testing.T) {
	b := NewBuilder()
	book, author, _ := okBook(b), okAuthor(b), okTag(b)
	book.Edge("author", ToOne[AuWire]()).ForeignKey(func(r bkRow) string { return r.AuthorID }).Includable()
	book.Edge("tags", ToMany[TgWire]()).ForeignKeys(func(r bkRow) []string { return r.TagIDs }) // NOT includable
	FetchIDs(b, author, func(c *include.Ctx, ids []string) ([]auRow, error) { return nil, nil })
	b.Root(book)

	g, err := b.Compile()
	if err != nil {
		t.Fatalf("Compile: %v", err)
	}
	got := names(g.Reachable(g.Resource("Book")))
	if !reflect.DeepEqual(got, []string{"Book", "Author"}) {
		t.Errorf("Reachable = %v, want [Book Author]", got)
	}
}

func TestGraphReachableTerminatesOnCycles(t *testing.T) {
	g := happyGraph(t)
	// Book → Author → Book is a cycle over includable edges; dedup by name
	// must terminate the walk.
	if got := len(g.Reachable(g.Resource("Book"))); got != 4 {
		t.Errorf("Reachable returned %d nodes, want 4", got)
	}
}

func TestGraphRoots(t *testing.T) {
	g := happyGraph(t)
	if got := names(g.Roots()); !reflect.DeepEqual(got, []string{"Book"}) {
		t.Errorf("Roots() = %v, want [Book]", got)
	}
}
