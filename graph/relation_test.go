package graph

import (
	"bytes"
	"errors"
	"reflect"
	"strings"
	"testing"

	"github.com/qrotux/wireleaf/include"
)

// specViaRelation declares the spec_test fixture through Spec nodes plus one
// OneToMany relation, so it must record and hydrate exactly like the chain.
func specViaRelation(b *Builder) (book *NodeHandle[bkRow, BkWire], author *NodeHandle[auRow, AuWire]) {
	// The node-level FetchParents mirrors the chain fixture; the relation's own
	// (per-edge) FetchParents is covered by TestRelationsKeepSeparateReverseFetchers.
	book = Add(b, Spec[bkRow, BkWire]{
		Name: "Book", Slug: "bk", Wire: specBookWire, PrimaryKey: specBookPK, Enrich: specEnrich,
		Defaults: []string{"author"}, Envelope: &specEnvelope, Inputs: &specInputs,
		FetchParents: specFetchBooksByAuthor,
	})
	author = Add(b, Spec[auRow, AuWire]{
		Name: "Author", Wire: specAuthorWire, PrimaryKey: specAuthorPK, DocExternal: true,
		FetchIDs: specFetchAuthors,
	})
	OneToMany(author, book, "books", "author").
		ForeignKey("authorId", specBookFK).
		Required().
		Includable().
		Policies(include.MissingRequiredError).
		Limit(10).
		Args(Arg("page", specArgOK)).
		Many(func(e *EdgeBuilder[auRow]) { e.Guard(specGuard).Envelope(specEnvelope) })
	b.Root(book)
	return book, author
}

func TestOneToManyRecordsLikeChain(t *testing.T) {
	cb, rb := NewBuilder(), NewBuilder()
	cBook, cAuthor := specViaChain(cb)
	rBook, rAuthor := specViaRelation(rb)
	for _, pair := range []struct {
		name        string
		chain, spec *nodeSpec
	}{{"Book", cBook.spec, rBook.spec}, {"Author", cAuthor.spec, rAuthor.spec}} {
		want, got := shapeOf(cb, pair.chain), shapeOf(rb, pair.spec)
		if !reflect.DeepEqual(got, want) {
			t.Errorf("%s: relation recorded\n  %+v\nchain recorded\n  %+v", pair.name, got, want)
		}
	}
	// ForeignKey's field name became the reverse edge's backref, and the FK
	// closure is the real thing
	if got := rAuthor.spec.edges[0].kind.backref; got != "authorId" {
		t.Errorf("backref = %q, want authorId", got)
	}
	if got := rBook.spec.edges[0].set.foreignKey(bkRow{AuthorID: "a9"}); got != "a9" {
		t.Errorf("ForeignKey via relation = %q", got)
	}
}

func TestOneToManyHydratesLikeChain(t *testing.T) {
	hydrate := func(declare func(*Builder) (*NodeHandle[bkRow, BkWire], *NodeHandle[auRow, AuWire])) []byte {
		t.Helper()
		b := NewBuilder()
		declare(b)
		g, err := b.Compile()
		if err != nil {
			t.Fatalf("Compile: %v", err)
		}
		inc, _ := include.ParseInclude("author.books")
		raw, _, err := include.HydrateEntity(g.Resource("Book"), specBooks["b1"], inc, nil,
			&include.Ctx{Registry: g}, include.DefaultOptions)
		if err != nil {
			t.Fatalf("HydrateEntity: %v", err)
		}
		return raw
	}
	if chain, rel := hydrate(specViaChain), hydrate(specViaRelation); !bytes.Equal(chain, rel) {
		t.Errorf("bytes differ:\n chain    %s\n relation %s", chain, rel)
	}
}

func TestOneToManyBackrefAndSides(t *testing.T) {
	b := NewBuilder()
	book := Node[bkRow, BkWire](b, "Book")
	author := Node[auRow, AuWire](b, "Author")
	OneToMany(author, book, "books", "author").
		ForeignKey("author_id", specBookFK).
		Sort("-title").
		Bare().
		EstimatedRows(5).
		One(func(e *EdgeBuilder[bkRow]) { e.Guard(func(*include.Ctx, bkRow) bool { return true }) })

	one, many := book.spec.edges[0], author.spec.edges[0]
	if one.key != "author" || one.kind.kind != include.KindToOne || one.kind.targetWireT != reflect.TypeFor[AuWire]() {
		t.Errorf("to-one edge = %+v", one)
	}
	if many.key != "books" || many.kind.kind != include.KindReverse || many.kind.targetWireT != reflect.TypeFor[BkWire]() {
		t.Errorf("reverse edge = %+v", many)
	}
	if many.kind.backref != "author_id" {
		t.Errorf("backref = %q", many.kind.backref)
	}
	if one.set.inverse != "books" || many.set.inverse != "author" {
		t.Errorf("inverse = %q / %q", one.set.inverse, many.set.inverse)
	}
	if !one.set.guardSet || many.set.guardSet {
		t.Error("One() did not reach the to-one side only")
	}
	if many.set.sort != "-title" || !many.set.bare || many.set.estimatedRows != 5 || one.set.bare {
		t.Errorf("many-side settings misplaced: one=%+v many=%+v", *one.set, *many.set)
	}
}

// The reverse edge's index is taken when the relation is declared, so
// ForeignKey (which writes the backref through it) must land on the right
// edge when the one-side node already had edges — declared via Spec.Edges
// here — and more are appended afterwards.
func TestRelationBackrefIndexWithSurroundingEdges(t *testing.T) {
	b := NewBuilder()
	env := include.Envelope{Key: "d"}
	book := Node[bkRow, BkWire](b, "Book")
	author := Add(b, Spec[auRow, AuWire]{
		Name: "Author",
		Edges: []EdgeSpec[auRow]{
			{Key: "mentor", Kind: ToOne[AuWire](), ForeignKey: func(r auRow) string { return "" }},
			{Key: "peers", Kind: Reverse[AuWire]("mentorId")},
		},
	})
	tag := Node[tgRow, TgWire](b, "Tag")
	rel := OneToMany(author, book, "books", "author").Envelope(env)
	author.Edge("later", Reverse[BkWire]("x"))                                 // appended AFTER the relation
	rel.ForeignKey("author_id", specBookFK).ForeignKey("authorId", specBookFK) // last wins
	m2m := ManyToMany(book, tag, "tags", "tagged").Envelope(env).
		Left(func(e *EdgeBuilder[bkRow]) { e.Sort("-x") })
	book.Edge("more", Reverse[TgWire]("y"))
	m2m.ForeignKeys("tagIds", func(r bkRow) []string { return r.TagIDs })

	byKey := func(n *nodeSpec) map[string]*edgeDecl {
		out := map[string]*edgeDecl{}
		for _, e := range n.edges {
			out[e.key] = e
		}
		return out
	}
	au, bk, tg := byKey(author.spec), byKey(book.spec), byKey(tag.spec)
	if got := au["books"].kind.backref; got != "authorId" {
		t.Errorf("author.books backref = %q, want authorId", got)
	}
	if au["peers"].kind.backref != "mentorId" || au["later"].kind.backref != "x" {
		t.Errorf("ForeignKey touched a neighbouring edge: peers=%q later=%q", au["peers"].kind.backref, au["later"].kind.backref)
	}
	if got := tg["tagged"].kind.backref; got != "tagIds" {
		t.Errorf("tag.tagged backref = %q, want tagIds", got)
	}
	if bk["more"].kind.backref != "y" {
		t.Errorf("ForeignKeys touched book.more: %q", bk["more"].kind.backref)
	}
	// Envelope reached both sides of both relations; Left reached the forward edge only.
	for _, e := range []*edgeDecl{au["books"], bk["author"], bk["tags"], tg["tagged"]} {
		if !e.set.envelopeSet || e.set.envelope != env {
			t.Errorf("edge %s: relation Envelope not applied", e.key)
		}
	}
	if bk["tags"].set.sort != "-x" || tg["tagged"].set.sort != "" {
		t.Error("Left() did not reach the forward side only")
	}
}

func TestManyToManyRecords(t *testing.T) {
	b := NewBuilder()
	book := Node[bkRow, BkWire](b, "Book")
	tag := Node[tgRow, TgWire](b, "Tag")
	fetched := false
	ManyToMany(book, tag, "tags", "books").
		ForeignKeys("tagIds", func(r bkRow) []string { return r.TagIDs }).
		Includable().
		Limit(7).
		Policies(include.MissingForeignError).
		Right(func(e *EdgeBuilder[tgRow]) { e.Sort("-title") }).
		FetchParents(func(*include.Ctx, []string, include.EdgeQuery) (map[string]ParentRows[bkRow], error) {
			fetched = true
			return nil, nil
		})

	left, right := book.spec.edges[0], tag.spec.edges[0]
	if left.key != "tags" || left.kind.kind != include.KindForwardHasMany || left.kind.targetWireT != reflect.TypeFor[TgWire]() {
		t.Errorf("forward edge = %+v", left)
	}
	if right.key != "books" || right.kind.kind != include.KindReverse || right.kind.targetWireT != reflect.TypeFor[BkWire]() || right.kind.backref != "tagIds" {
		t.Errorf("reverse edge = %+v", right)
	}
	if left.set.inverse != "books" || right.set.inverse != "tags" {
		t.Errorf("inverse = %q / %q", left.set.inverse, right.set.inverse)
	}
	if got := left.set.foreignKeys(bkRow{TagIDs: []string{"t1"}}); !reflect.DeepEqual(got, []string{"t1"}) {
		t.Errorf("ForeignKeys = %v", got)
	}
	if !left.set.includable || !right.set.includable || left.set.limit != 7 || right.set.limit != 7 {
		t.Error("Includable/Limit did not reach both sides")
	}
	if left.set.policies.MissingForeign == nil || right.set.policies.MissingForeign != nil {
		t.Error("Policies did not land on the forward side only")
	}
	if right.set.sort != "-title" || left.set.sort != "" {
		t.Error("Right() did not reach the reverse side only")
	}
	// The bind is PER EDGE, on the reverse edge tag.books, typed by Book rows.
	if book.spec.fetchParents != nil || tag.spec.fetchParents != nil || book.spec.edgeFetch != nil {
		t.Fatal("relation FetchParents must not bind a node-level fetcher")
	}
	bind := tag.spec.edgeFetch["books"]
	if bind == nil || bind.target != book.spec {
		t.Fatalf("edge bind on tag.books = %+v, want target Book", bind)
	}
	if _, _ = bind.fn(nil, nil, include.EdgeQuery{}); !fetched {
		t.Error("bound fetcher is not the declared one")
	}
}

// Two relations whose reverse edges land on the same node each keep their
// own fetcher: no clobbering, and no dispatch inside the fetcher needed.
func TestRelationsKeepSeparateReverseFetchers(t *testing.T) {
	b := NewBuilder()
	book := Add(b, Spec[bkRow, BkWire]{Name: "Book", Wire: specBookWire, PrimaryKey: specBookPK})
	author := Add(b, Spec[auRow, AuWire]{Name: "Author", Wire: specAuthorWire, PrimaryKey: specAuthorPK, FetchIDs: specFetchAuthors})
	tag := Add(b, Spec[tgRow, TgWire]{
		Name: "Tag", Wire: func(r tgRow, _ *include.Ctx) TgWire { return TgWire{ID: r.ID} },
		PrimaryKey: func(r tgRow) string { return r.ID },
		FetchIDs:   func(_ *include.Ctx, ids []string) ([]tgRow, error) { return []tgRow{{ID: "t1"}}, nil },
	})
	byLabel := func(label string) FetchByParents[bkRow] {
		return func(_ *include.Ctx, parentIDs []string, _ include.EdgeQuery) (map[string]ParentRows[bkRow], error) {
			out := map[string]ParentRows[bkRow]{}
			for _, pid := range parentIDs {
				out[pid] = ParentRows[bkRow]{Rows: []bkRow{{ID: label + "-" + pid}}}
			}
			return out, nil
		}
	}
	OneToMany(author, book, "books", "author").ForeignKey("authorId", specBookFK).Includable().FetchParents(byLabel("by-author"))
	ManyToMany(book, tag, "tags", "tagged").ForeignKeys("tagIds", func(r bkRow) []string { return r.TagIDs }).Includable().FetchParents(byLabel("by-tag"))
	b.Root(book)
	g, err := b.Compile()
	if err != nil {
		t.Fatalf("Compile: %v", err)
	}
	inc, _ := include.ParseInclude("author.books,tags.tagged")
	raw, _, err := include.HydrateEntity(g.Resource("Book"), bkRow{ID: "b1", AuthorID: "a1", TagIDs: []string{"t1"}},
		inc, nil, &include.Ctx{Registry: g}, include.DefaultOptions)
	if err != nil {
		t.Fatalf("HydrateEntity: %v", err)
	}
	for _, want := range []string{`"books":{"items":[{"id":"by-author-a1"`, `"tagged":{"items":[{"id":"by-tag-t1"`} {
		if !bytes.Contains(raw, []byte(want)) {
			t.Errorf("output lacks %s:\n%s", want, raw)
		}
	}
}

// FetchEdge findings: unknown key, non-reverse edge, wrong target handle; and
// a per-edge bind satisfies checkFetchers on its own.
func TestFetchEdgeFindings(t *testing.T) {
	b := NewBuilder()
	book := Add(b, Spec[bkRow, BkWire]{Name: "Book", Wire: specBookWire, PrimaryKey: specBookPK})
	author := Add(b, Spec[auRow, AuWire]{Name: "Author", Wire: specAuthorWire, PrimaryKey: specAuthorPK, FetchIDs: specFetchAuthors})
	tag := Add(b, Spec[tgRow, TgWire]{Name: "Tag", Wire: func(r tgRow, _ *include.Ctx) TgWire { return TgWire{ID: r.ID} }, PrimaryKey: func(r tgRow) string { return r.ID }})
	OneToMany(author, book, "books", "author").ForeignKey("authorId", specBookFK).Includable()
	fetchBooks := func(_ *include.Ctx, ids []string, _ include.EdgeQuery) (map[string]ParentRows[bkRow], error) {
		return nil, nil
	}
	fetchTags := func(_ *include.Ctx, ids []string, _ include.EdgeQuery) (map[string]ParentRows[tgRow], error) {
		return nil, nil
	}

	FetchEdge(b, author, "nope", book, fetchBooks) // no such edge
	FetchEdge(b, book, "author", author, nil)      // nil clears: no finding
	FetchEdge(b, book, "author", author, func(_ *include.Ctx, ids []string, _ include.EdgeQuery) (map[string]ParentRows[auRow], error) {
		return nil, nil
	}) // to-one
	FetchEdge(b, author, "books", tag, fetchTags) // wrong target
	b.Root(book)
	_, err := b.Compile()
	var ce *CompileError
	if !errors.As(err, &ce) {
		t.Fatalf("Compile err = %v, want *CompileError", err)
	}
	msg := ce.Error()
	for _, want := range []string{
		"Author.nope: FetchEdge: no such edge",
		"Book.author: FetchEdge is only valid on reverse edges (this is to-one)",
		"Author.books: FetchEdge target node Tag is not the edge's target Book",
		// the wrong-target bind does NOT count as a fetcher for author.books
		"Book: missing FetchParents bind: reached by reverse includable/default edge(s) Author.books",
	} {
		if !strings.Contains(msg, want) {
			t.Errorf("findings lack %q:\n%s", want, msg)
		}
	}

	// A correct per-edge bind alone satisfies the reverse edge.
	b2 := NewBuilder()
	book2 := Add(b2, Spec[bkRow, BkWire]{Name: "Book", Wire: specBookWire, PrimaryKey: specBookPK})
	author2 := Add(b2, Spec[auRow, AuWire]{Name: "Author", Wire: specAuthorWire, PrimaryKey: specAuthorPK, FetchIDs: specFetchAuthors})
	OneToMany(author2, book2, "books", "author").ForeignKey("authorId", specBookFK).Includable()
	FetchEdge(b2, author2, "books", book2, fetchBooks)
	b2.Root(book2)
	g, err := b2.Compile()
	if err != nil {
		t.Fatalf("Compile with FetchEdge only: %v", err)
	}
	if _, ok := g.FetchByEdge(g.Resource("Author"), "books"); !ok {
		t.Error("FetchByEdge(Author, books) not found on the compiled graph")
	}
	if _, ok := g.FetchByParents(g.Resource("Book")); ok {
		t.Error("node-level FetchByParents(Book) must stay unbound")
	}
}

// A relation without its ForeignKey call is ONE finding in the relation's
// vocabulary on the FK side; an empty field is one finding on the reverse
// side; the chained equivalent keeps its two independent findings.
func TestRelationForeignKeyFindings(t *testing.T) {
	findings := func(t *testing.T, declare func(b *Builder)) []string {
		t.Helper()
		b := NewBuilder()
		declare(b)
		_, err := b.Compile()
		var ce *CompileError
		if !errors.As(err, &ce) {
			t.Fatalf("Compile err = %v, want *CompileError", err)
		}
		out := make([]string, len(ce.Findings))
		for i, f := range ce.Findings {
			out[i] = f.String()
		}
		return out
	}
	nodes := func(b *Builder) (*NodeHandle[bkRow, BkWire], *NodeHandle[auRow, AuWire], *NodeHandle[tgRow, TgWire]) {
		book := Add(b, Spec[bkRow, BkWire]{Name: "Book", Wire: specBookWire, PrimaryKey: specBookPK})
		author := Add(b, Spec[auRow, AuWire]{Name: "Author", Wire: specAuthorWire, PrimaryKey: specAuthorPK})
		tag := Add(b, Spec[tgRow, TgWire]{Name: "Tag", Wire: func(r tgRow, _ *include.Ctx) TgWire { return TgWire{ID: r.ID} }, PrimaryKey: func(r tgRow) string { return r.ID }})
		return book, author, tag
	}

	got := findings(t, func(b *Builder) {
		book, author, tag := nodes(b)
		OneToMany(author, book, "books", "author")
		ManyToMany(book, tag, "tags", "tagged")
	})
	want := []string{
		"Book.author: OneToMany relation declares no ForeignKey(field, fn)",
		"Book.tags: ManyToMany relation declares no ForeignKeys(field, fn)",
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("no-ForeignKey findings = %q, want exactly %q", got, want)
	}

	got = findings(t, func(b *Builder) {
		book, author, tag := nodes(b)
		OneToMany(author, book, "books", "author").ForeignKey("", specBookFK)
		ManyToMany(book, tag, "tags", "tagged").ForeignKeys("", func(r bkRow) []string { return r.TagIDs })
	})
	want = []string{
		"Author.books: OneToMany relation: ForeignKey field must not be empty",
		"Tag.tagged: ManyToMany relation: ForeignKeys field must not be empty",
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("empty-field findings = %q, want exactly %q", got, want)
	}

	// chained edges keep their own two findings
	got = findings(t, func(b *Builder) {
		book, author, _ := nodes(b)
		book.Edge("author", ToOne[AuWire]()).Inverse("books")
		author.Edge("books", Reverse[BkWire]("")).Inverse("author")
	})
	want = []string{
		"Book.author: to-one edge requires ForeignKey",
		"Author.books: reverse edge requires a backref",
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("chained findings = %q, want exactly %q", got, want)
	}
}

// Filterable reaches both sides of both relations, and compiles clean when
// the targets have a filterable column.
func TestRelationFilterable(t *testing.T) {
	type flRow struct{ ID, Name, OwnerID string }
	type FlOwner struct {
		ID   string `json:"id"`
		Name string `json:"name" col:"name,filter"`
	}
	type FlItem struct {
		ID   string `json:"id"`
		Name string `json:"name" col:"name,filter"`
	}
	b := NewBuilder()
	owner := Node[flRow, FlOwner](b, "Owner").Wire(func(r flRow, _ *include.Ctx) FlOwner { return FlOwner{ID: r.ID} }).PrimaryKey(func(r flRow) string { return r.ID })
	item := Node[flRow, FlItem](b, "Item").Wire(func(r flRow, _ *include.Ctx) FlItem { return FlItem{ID: r.ID} }).PrimaryKey(func(r flRow) string { return r.ID })
	OneToMany(owner, item, "items", "owner").ForeignKey("ownerId", func(r flRow) string { return r.OwnerID }).Filterable()
	ManyToMany(item, owner, "likedBy", "likes").ForeignKeys("likedByIds", func(r flRow) []string { return nil }).Filterable()
	for _, e := range append(owner.spec.edges, item.spec.edges...) {
		if !e.set.filterable {
			t.Errorf("edge %s: Filterable did not reach it", e.key)
		}
	}
	if _, err := b.Compile(); err != nil {
		t.Fatalf("Compile: %v", err)
	}
}

// FetchEdge(..., nil) clears a per-edge bind, including one a relation made.
func TestFetchEdgeNilClears(t *testing.T) {
	b := NewBuilder()
	book := Add(b, Spec[bkRow, BkWire]{Name: "Book", Wire: specBookWire, PrimaryKey: specBookPK})
	author := Add(b, Spec[auRow, AuWire]{Name: "Author", Wire: specAuthorWire, PrimaryKey: specAuthorPK, FetchIDs: specFetchAuthors})
	OneToMany(author, book, "books", "author").ForeignKey("authorId", specBookFK).Includable().
		FetchParents(specFetchBooksByAuthor)
	FetchEdge(b, author, "books", book, nil)
	b.Root(book)
	_, err := b.Compile()
	if err == nil || !strings.Contains(err.Error(), "Book: missing FetchParents bind") {
		t.Errorf("after FetchEdge(nil) Compile err = %v, want the missing-bind finding", err)
	}
}

// An edge without a Kind is still a KNOWN edge to the cross-reference passes:
// Defaults and Inverse naming it, a duplicate of it and a FetchEdge on it add
// no findings of their own.
func TestKindlessEdgeIsKnownToCrossReferences(t *testing.T) {
	b := NewBuilder()
	book := Add(b, Spec[bkRow, BkWire]{
		Name: "Book", Wire: specBookWire, PrimaryKey: specBookPK, Defaults: []string{"author"},
		Edges: []EdgeSpec[bkRow]{
			{Key: "author", ForeignKey: specBookFK, Inverse: "books"}, // no Kind
			{Key: "author", Kind: ToOne[AuWire]()},                    // duplicate of a kindless edge
		},
	})
	author := Add(b, Spec[auRow, AuWire]{
		Name: "Author", Wire: specAuthorWire, PrimaryKey: specAuthorPK,
		Edges: []EdgeSpec[auRow]{{Key: "books", Kind: Reverse[BkWire]("authorId"), Inverse: "author"}},
	})
	FetchEdge(b, book, "author", author, func(*include.Ctx, []string, include.EdgeQuery) (map[string]ParentRows[auRow], error) { return nil, nil })
	_, err := b.Compile()
	var ce *CompileError
	if !errors.As(err, &ce) {
		t.Fatalf("Compile err = %v, want *CompileError", err)
	}
	got := make([]string, len(ce.Findings))
	for i, f := range ce.Findings {
		got[i] = f.String()
	}
	want := []string{
		"Book.author: edge declares no Kind (use ToOne, ToMany, Reverse, InArray or Computed)",
		"Book.author: duplicate edge key",
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("findings = %q, want exactly %q", got, want)
	}
}

func TestRelationsDeadAfterCompile(t *testing.T) {
	b := NewBuilder()
	book := Node[bkRow, BkWire](b, "Book")
	author := Node[auRow, AuWire](b, "Author")
	rel := OneToMany(author, book, "books", "author")
	_, _ = b.Compile()
	for name, fn := range map[string]func(){
		"OneToMany":  func() { OneToMany(author, book, "b2", "a2") },
		"ManyToMany": func() { ManyToMany(book, author, "x", "y") },
		"ForeignKey": func() { rel.ForeignKey("z", specBookFK) },
		"setting":    func() { rel.Limit(1) },
	} {
		func() {
			defer func() {
				if r := recover(); r != "graph: builder is dead after Compile" {
					t.Errorf("%s after Compile panic = %v", name, r)
				}
			}()
			fn()
		}()
	}
}

// Two reverse edges landing on one node share its single FetchParents; the
// EdgeQuery names the edge and its owner so the fetcher can pick the join.
func TestTwoReverseEdgesDispatchByEdgeKey(t *testing.T) {
	b := NewBuilder()
	book := Add(b, Spec[bkRow, BkWire]{Name: "Book", Wire: specBookWire, PrimaryKey: specBookPK})
	author := Add(b, Spec[auRow, AuWire]{Name: "Author", Wire: specAuthorWire, PrimaryKey: specAuthorPK, FetchIDs: specFetchAuthors})
	tag := Add(b, Spec[tgRow, TgWire]{
		Name: "Tag", Wire: func(r tgRow, _ *include.Ctx) TgWire { return TgWire{ID: r.ID} },
		PrimaryKey: func(r tgRow) string { return r.ID },
		FetchIDs:   func(_ *include.Ctx, ids []string) ([]tgRow, error) { return []tgRow{{ID: "t1"}}, nil },
	})
	OneToMany(author, book, "books", "author").ForeignKey("authorId", specBookFK).Includable()
	ManyToMany(book, tag, "tags", "tagged").ForeignKeys("tagIds", func(r bkRow) []string { return r.TagIDs }).Includable()

	var seen []string
	FetchParents(b, book, func(_ *include.Ctx, parentIDs []string, q include.EdgeQuery) (map[string]ParentRows[bkRow], error) {
		seen = append(seen, q.Edge.String())
		out := map[string]ParentRows[bkRow]{}
		for _, pid := range parentIDs {
			out[pid] = ParentRows[bkRow]{Rows: []bkRow{{ID: "via-" + q.Edge.Key + "-" + pid}}}
		}
		return out, nil
	})
	b.Root(book)
	g, err := b.Compile()
	if err != nil {
		t.Fatalf("Compile: %v", err)
	}
	inc, _ := include.ParseInclude("author.books,tags.tagged")
	raw, _, err := include.HydrateEntity(g.Resource("Book"), bkRow{ID: "b1", AuthorID: "a1", TagIDs: []string{"t1"}},
		inc, nil, &include.Ctx{Registry: g}, include.DefaultOptions)
	if err != nil {
		t.Fatalf("HydrateEntity: %v", err)
	}
	if want := []string{"Author.books", "Tag.tagged"}; !reflect.DeepEqual(seen, want) {
		t.Errorf("FetchParents calls = %v, want %v", seen, want)
	}
	for _, want := range []string{`"books":{"items":[{"id":"via-books-a1"`, `"tagged":{"items":[{"id":"via-tagged-t1"`} {
		if !bytes.Contains(raw, []byte(want)) {
			t.Errorf("output lacks %s:\n%s", want, raw)
		}
	}
}
