package graph

import (
	"reflect"
	"testing"

	"github.com/qrotux/wireleaf/apidoc"
	"github.com/qrotux/wireleaf/include"
)

// envGraph declares Book → Author (to-one), Book → Tag (to-many "tags", and a
// to-one "tag1" with an EDGE override), Author → Book (reverse "books",
// inverse of Book.author). Tag carries a NODE override. The graph default is
// set by the caller through cfg.
func envGraph(cfg func(b *Builder)) (*Graph, error) {
	b := NewBuilder()
	cfg(b)
	book := okBook(b)
	author := okAuthor(b)
	tag := okTag(b).Envelope(EnvelopePlain)

	book.Edge("author", ToOne[AuWire]()).
		ForeignKey(func(r bkRow) string { return r.AuthorID }).
		Inverse("books").
		Includable()
	book.Edge("tags", ToMany[TgWire]()).
		ForeignKeys(func(r bkRow) []string { return r.TagIDs }).
		Includable()
	book.Edge("tag1", ToOne[TgWire]()).
		ForeignKey(func(r bkRow) string { return r.AuthorID }).
		Envelope(include.Envelope{Key: "values"}).
		Includable()
	author.Edge("books", Reverse[BkWire]("authorId")).
		Inverse("author").
		Includable()

	FetchIDs(b, author, func(*include.Ctx, []string) ([]auRow, error) { return nil, nil })
	FetchIDs(b, tag, func(*include.Ctx, []string) ([]tgRow, error) { return nil, nil })
	FetchParents(b, book, func(*include.Ctx, []string, include.EdgeQuery) (map[string]ParentRows[bkRow], error) { return nil, nil })
	b.Root(book)
	return b.Compile()
}

func edgeEnv(t *testing.T, g *Graph, node, key string) include.Envelope {
	t.Helper()
	e, ok := g.Resource(node).Edges()[key]
	if !ok {
		t.Fatalf("no edge %s.%s", node, key)
	}
	return e.Envelope
}

func TestEnvelopeResolution_ThreeLayers(t *testing.T) {
	g, err := envGraph(func(b *Builder) { b.Envelope(EnvelopeData) })
	if err != nil {
		t.Fatalf("Compile: %v", err)
	}
	if got := edgeEnv(t, g, "Book", "author"); got != EnvelopeData {
		t.Errorf("Book.author = %+v, want graph default EnvelopeData", got)
	}
	if got := edgeEnv(t, g, "Book", "tags"); got != EnvelopePlain {
		t.Errorf("Book.tags = %+v, want Tag's node override EnvelopePlain", got)
	}
	if got, want := edgeEnv(t, g, "Book", "tag1"), (include.Envelope{Key: "values"}); got != want {
		t.Errorf("Book.tag1 = %+v, want edge override %+v", got, want)
	}
	// Inverse does NOT sync: Author.books targets Book, which has no node
	// override, so it takes the graph default — independently of Book.author.
	if got := edgeEnv(t, g, "Author", "books"); got != EnvelopeData {
		t.Errorf("Author.books = %+v, want EnvelopeData", got)
	}
}

func TestEnvelopeResolution_DefaultIsPlain(t *testing.T) {
	g, err := envGraph(func(*Builder) {})
	if err != nil {
		t.Fatalf("Compile: %v", err)
	}
	if got := edgeEnv(t, g, "Book", "author"); got != EnvelopePlain {
		t.Errorf("Book.author = %+v, want zero Envelope", got)
	}
	if got := edgeEnv(t, g, "Author", "books"); got != EnvelopePlain {
		t.Errorf("Author.books = %+v, want zero Envelope", got)
	}
}

func TestEnvelope_InverseNotSynced(t *testing.T) {
	// Graph default plain; Book.author gets an EDGE override; Author.books
	// (its inverse) must stay plain.
	b := NewBuilder()
	book, author := okBook(b), okAuthor(b)
	book.Edge("author", ToOne[AuWire]()).
		ForeignKey(func(r bkRow) string { return r.AuthorID }).
		Envelope(EnvelopeData).
		Inverse("books").
		Includable()
	author.Edge("books", Reverse[BkWire]("authorId")).Inverse("author").Includable()
	FetchIDs(b, author, func(*include.Ctx, []string) ([]auRow, error) { return nil, nil })
	FetchParents(b, book, func(*include.Ctx, []string, include.EdgeQuery) (map[string]ParentRows[bkRow], error) { return nil, nil })
	b.Root(book)
	g, err := b.Compile()
	if err != nil {
		t.Fatalf("Compile: %v", err)
	}
	if got := edgeEnv(t, g, "Book", "author"); got != EnvelopeData {
		t.Errorf("Book.author = %+v", got)
	}
	if got := edgeEnv(t, g, "Author", "books"); got != EnvelopePlain {
		t.Errorf("Author.books = %+v, want plain (Inverse must not sync Envelope)", got)
	}
}

func TestEnvelope_ComputedIgnoresGraphDefault(t *testing.T) {
	b := NewBuilder().Envelope(EnvelopeData)
	book := okBook(b)
	book.Edge("stats", Computed(apidoc.RawFragment(map[string]any{"type": "object"}))).Includable()
	b.Root(book)
	g, err := b.Compile()
	if err != nil {
		t.Fatalf("Compile: %v", err)
	}
	if got := edgeEnv(t, g, "Book", "stats"); got != EnvelopePlain {
		t.Errorf("computed edge Envelope = %+v, want zero", got)
	}
}

func TestEnvelopeFindings(t *testing.T) {
	cases := []struct {
		name  string
		build func(b *Builder)
		want  string
		node  string
		edge  string
	}{
		{
			name:  "Envelope on computed edge",
			build: edgeCase(computedK, envelopeCfg(EnvelopeData)),
			want:  "Envelope is not valid on computed edges",
			node:  "Book", edge: "e",
		},
		{
			name:  "graph: Pagination without Key",
			build: func(b *Builder) { b.Envelope(include.Envelope{Pagination: "pagination"}); okBook(b) },
			want:  `Envelope: Pagination "pagination" without Key`,
		},
		{
			name:  "node: Key and Pagination collide",
			build: func(b *Builder) { okBook(b).Envelope(include.Envelope{Key: "data", Pagination: "data"}) },
			want:  "Envelope: Key and Pagination collide",
			node:  "Book",
		},
		{
			name:  "edge: key needs JSON escaping",
			build: edgeCase(toOneK, fkCfg(), envelopeCfg(include.Envelope{Key: "da\"ta"})),
			want:  "needs JSON escaping",
			node:  "Book", edge: "e",
		},
		{
			name:  "edge: control character in Pagination",
			build: edgeCase(reverseK, envelopeCfg(include.Envelope{Key: "data", Pagination: "pa\nge"})),
			want:  "needs JSON escaping",
			node:  "Book", edge: "e",
		},
		{
			name:  "edge: invalid UTF-8 in Key",
			build: edgeCase(toOneK, fkCfg(), envelopeCfg(include.Envelope{Key: "da\xffta"})),
			want:  "needs JSON escaping",
			node:  "Book", edge: "e",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			fs := compileFindings(t, tc.build)
			var hit *Finding
			for i := range fs {
				if hasFinding(fs[i:i+1], tc.want) {
					hit = &fs[i]
					break
				}
			}
			if hit == nil {
				t.Fatalf("no finding containing %q; got:%s", tc.want, dumpFindings(fs))
			}
			if hit.Node != tc.node || hit.Edge != tc.edge {
				t.Errorf("finding scoped to %q/%q, want %q/%q", hit.Node, hit.Edge, tc.node, tc.edge)
			}
		})
	}
}

func envelopeCfg(env include.Envelope) bkCfg {
	return func(e *EdgeBuilder[bkRow]) *EdgeBuilder[bkRow] { return e.Envelope(env) }
}

// The builder-level constants are what the README quotes; pin them.
func TestEnvelopeConstants(t *testing.T) {
	if !reflect.DeepEqual(EnvelopePlain, include.Envelope{}) {
		t.Errorf("EnvelopePlain = %+v", EnvelopePlain)
	}
	if want := (include.Envelope{Key: "data", Pagination: "pagination"}); EnvelopeData != want {
		t.Errorf("EnvelopeData = %+v, want %+v", EnvelopeData, want)
	}
}
