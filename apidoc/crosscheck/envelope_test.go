package crosscheck_test

import (
	"fmt"
	"reflect"
	"testing"

	"github.com/qrotux/wireleaf/apidoc"
	"github.com/qrotux/wireleaf/apidoc/crosscheck"
	"github.com/qrotux/wireleaf/graph"
	"github.com/qrotux/wireleaf/include"
)

type bookRow struct {
	ID, Title, AuthorID string
	TagIDs              []string
}
type authorRow struct{ ID, Name string }
type tagRow struct{ ID, Name string }

type BookWire struct {
	ID    string `json:"id"`
	Title string `json:"title"`
}
type AuthorWire struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}
type TagWire struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

// rawReflector documents each wire type from a hand-written fragment, so the
// test needs neither the reflector module nor apidoc's internal stub.
type rawReflector map[reflect.Type]map[string]any

func (r rawReflector) ReflectComponents(types []reflect.Type, overrides map[reflect.Type]string) (map[string]*apidoc.IRNode, error) {
	out := map[string]*apidoc.IRNode{}
	for _, t := range types {
		t = apidoc.DerefType(t)
		frag, ok := r[t]
		if !ok {
			return nil, fmt.Errorf("rawReflector: no fragment for %v", t)
		}
		name := overrides[t]
		if name == "" {
			name = t.Name()
		}
		out[name] = apidoc.RawFragment(frag).IR()
	}
	return out, nil
}

func scalarObject(fields ...string) map[string]any {
	props := map[string]any{}
	req := make([]any, 0, len(fields))
	for _, f := range fields {
		props[f] = map[string]any{"type": "string"}
		req = append(req, f)
	}
	return map[string]any{"type": "object", "properties": props, "required": req}
}

var (
	books   = map[string]bookRow{"b1": {ID: "b1", Title: "The Hobbit", AuthorID: "a1", TagIDs: []string{"t1"}}, "b2": {ID: "b2", Title: "Silmarillion", AuthorID: "a1"}}
	authors = map[string]authorRow{"a1": {ID: "a1", Name: "J. R. R. Tolkien"}}
	tags    = map[string]tagRow{"t1": {ID: "t1", Name: "fantasy"}}
)

func envelopeGraph(t *testing.T) *graph.Graph {
	t.Helper()
	b := graph.NewBuilder().Envelope(graph.EnvelopeData)
	book := graph.Node[bookRow, BookWire](b, "Book").
		Wire(func(r bookRow, _ *include.Ctx) BookWire { return BookWire{ID: r.ID, Title: r.Title} }).
		PrimaryKey(func(r bookRow) string { return r.ID })
	author := graph.Node[authorRow, AuthorWire](b, "Author").
		Wire(func(r authorRow, _ *include.Ctx) AuthorWire { return AuthorWire{ID: r.ID, Name: r.Name} }).
		PrimaryKey(func(r authorRow) string { return r.ID })
	tag := graph.Node[tagRow, TagWire](b, "Tag").
		Envelope(graph.EnvelopePlain).
		Wire(func(r tagRow, _ *include.Ctx) TagWire { return TagWire{ID: r.ID, Name: r.Name} }).
		PrimaryKey(func(r tagRow) string { return r.ID })

	book.Edge("author", graph.ToOne[AuthorWire]()).
		ForeignKey(func(r bookRow) string { return r.AuthorID }).
		Inverse("books").
		Includable()
	book.Edge("tags", graph.ToMany[TagWire]()).
		ForeignKeys(func(r bookRow) []string { return r.TagIDs }).
		Bare().EstimatedRows(10).
		Includable()
	author.Edge("books", graph.Reverse[BookWire]("authorId")).
		Inverse("author").
		Limit(1).
		Includable()

	graph.FetchIDs(b, author, func(_ *include.Ctx, ids []string) ([]authorRow, error) {
		out := []authorRow{}
		for _, id := range ids {
			if a, ok := authors[id]; ok {
				out = append(out, a)
			}
		}
		return out, nil
	})
	graph.FetchIDs(b, tag, func(_ *include.Ctx, ids []string) ([]tagRow, error) {
		out := []tagRow{}
		for _, id := range ids {
			if tg, ok := tags[id]; ok {
				out = append(out, tg)
			}
		}
		return out, nil
	})
	graph.FetchParents(b, book, func(_ *include.Ctx, parentIDs []string, q include.EdgeQuery) (map[string]graph.ParentRows[bookRow], error) {
		out := map[string]graph.ParentRows[bookRow]{}
		for _, pid := range parentIDs {
			var rows []bookRow
			for _, id := range []string{"b1", "b2"} { // deterministic order
				if books[id].AuthorID == pid {
					rows = append(rows, books[id])
				}
			}
			pr := graph.ParentRows[bookRow]{Rows: rows}
			if q.Limit > 0 && len(rows) > q.Limit {
				pr.Rows, pr.HasMore, pr.NextCursor = rows[:q.Limit], true, "opaque"
			}
			out[pid] = pr
		}
		return out, nil
	})
	b.Root(book)
	g, err := b.Compile()
	if err != nil {
		t.Fatalf("Compile: %v", err)
	}
	return g
}

func hydrateBook(t *testing.T, g *graph.Graph, includeStr string) []byte {
	t.Helper()
	var inc include.IncludeTree
	if includeStr != "" {
		var err error
		inc, err = include.ParseInclude(includeStr)
		if err != nil {
			t.Fatalf("ParseInclude: %v", err)
		}
	}
	ctx := &include.Ctx{Registry: g}
	raw, _, err := include.HydrateByID(g.Resource("Book"), "b1", inc, nil,
		func(_ *include.Ctx, id string) (any, error) {
			bk, ok := books[id]
			if !ok {
				return nil, nil
			}
			return bk, nil
		}, ctx, include.DefaultOptions)
	if err != nil {
		t.Fatalf("HydrateByID: %v", err)
	}
	return raw
}

func bookValidator(t *testing.T, g *graph.Graph) *crosscheck.Validator {
	t.Helper()
	refl := rawReflector{
		reflect.TypeFor[BookWire]():   scalarObject("id", "title"),
		reflect.TypeFor[AuthorWire](): scalarObject("id", "name"),
		reflect.TypeFor[TagWire]():    scalarObject("id", "name"),
	}
	comps, err := apidoc.EmitComponents(refl, g.Reachable(g.Resource("Book")))
	if err != nil {
		t.Fatalf("EmitComponents: %v", err)
	}
	v, err := crosscheck.Compile(comps, "Book")
	if err != nil {
		t.Fatalf("crosscheck.Compile: %v", err)
	}
	return v
}

func TestEnvelopeBytesMatchDocument(t *testing.T) {
	g := envelopeGraph(t)
	v := bookValidator(t, g)

	raw := hydrateBook(t, g, "author.books,tags")
	want := `{"id":"b1","title":"The Hobbit",` +
		`"author":{"data":{"id":"a1","name":"J. R. R. Tolkien",` +
		`"books":{"data":[{"data":{"id":"b1","title":"The Hobbit"}}],"pagination":{"hasNextPage":true,"nextCursor":"opaque"}}}},` +
		`"tags":[{"id":"t1","name":"fantasy"}]}`
	if string(raw) != want {
		t.Fatalf("hydrated =\n  %s\nwant\n  %s", raw, want)
	}
	if err := v.Validate(raw); err != nil {
		t.Errorf("document rejects the engine's own bytes: %v", err)
	}

	plain := hydrateBook(t, g, "")
	if err := v.Validate(plain); err != nil {
		t.Errorf("no-include bytes rejected: %v\n%s", err, plain)
	}
}

func TestEnvelopeDocumentRejectsUnwrapped(t *testing.T) {
	g := envelopeGraph(t)
	v := bookValidator(t, g)
	bad := map[string]string{
		"author unwrapped":        `{"id":"b1","title":"x","author":{"id":"a1","name":"n"}}`,
		"author null not wrapped": `{"id":"b1","title":"x","author":null}`,
		"books item unwrapped":    `{"id":"b1","title":"x","author":{"data":{"id":"a1","name":"n","books":{"data":[{"id":"b2","title":"y"}],"pagination":{"hasNextPage":false}}}}}`,
		"pagination incomplete":   `{"id":"b1","title":"x","author":{"data":{"id":"a1","name":"n","books":{"data":[],"pagination":{}}}}}`,
		"tags wrapped by mistake": `{"id":"b1","title":"x","tags":{"data":[]}}`,
	}
	for name, s := range bad {
		if err := v.Validate([]byte(s)); err == nil {
			t.Errorf("%s: accepted %s", name, s)
		}
	}
}
