package graph

import (
	"bytes"
	"errors"
	"reflect"
	"sort"
	"strings"
	"testing"

	"github.com/qrotux/wireleaf/include"
)

// ------------------------------------------------------------------ fixture

// specFixture is the declaration under test in its two forms. Both build the
// same two-node graph: Book -to-one-> Author, Author -reverse-> Book, with
// every EdgeSpec field exercised on one edge or the other.
var (
	specBooks = map[string]bkRow{
		"b1": {ID: "b1", AuthorID: "a1", Title: "The Hobbit"},
		"b2": {ID: "b2", AuthorID: "a1", Title: "Silmarillion"},
	}
	specAuthors = map[string]auRow{"a1": {ID: "a1", Name: "Tolkien"}}
)

func specFetchAuthors(_ *include.Ctx, ids []string) ([]auRow, error) {
	out := make([]auRow, 0, len(ids))
	for _, id := range ids {
		if a, ok := specAuthors[id]; ok {
			out = append(out, a)
		}
	}
	return out, nil
}

func specFetchBooksByAuthor(_ *include.Ctx, parentIDs []string, q include.EdgeQuery) (map[string]ParentRows[bkRow], error) {
	out := make(map[string]ParentRows[bkRow], len(parentIDs))
	for _, pid := range parentIDs {
		var rows []bkRow
		for _, id := range []string{"b1", "b2"} {
			if bk := specBooks[id]; bk.AuthorID == pid {
				rows = append(rows, bk)
			}
		}
		out[pid] = ParentRows[bkRow]{Rows: rows}
	}
	return out, nil
}

var (
	specBookWire   = func(r bkRow, _ *include.Ctx) BkWire { return BkWire{ID: r.ID, Title: r.Title} }
	specBookPK     = func(r bkRow) string { return r.ID }
	specBookFK     = func(r bkRow) string { return r.AuthorID }
	specAuthorWire = func(r auRow, _ *include.Ctx) AuWire { return AuWire{ID: r.ID, Name: r.Name} }
	specAuthorPK   = func(r auRow) string { return r.ID }
	specGuard      = func(_ *include.Ctx, r auRow) bool { return r.ID != "" }
	specEnrich     = func([]bkRow, *include.Ctx) error { return nil }
	specArgOK      = func(any) error { return nil }
	specEnvelope   = include.Envelope{Key: "data"}
	specInputs     = Inputs{Pagination: PageInput{Mode: include.PageModeOffset, DefaultLimit: 10, MaxLimit: 20}}
)

func specViaChain(b *Builder) (book *NodeHandle[bkRow, BkWire], author *NodeHandle[auRow, AuWire]) {
	book = Node[bkRow, BkWire](b, "Book").
		Slug("bk").
		Wire(specBookWire).
		PrimaryKey(specBookPK).
		Enrich(specEnrich).
		Defaults("author").
		Envelope(specEnvelope).
		Inputs(specInputs)
	book.Edge("author", ToOne[AuWire]()).
		ForeignKey(specBookFK).
		Inverse("books").
		Required().
		Includable().
		Policies(include.MissingRequiredError)
	author = Node[auRow, AuWire](b, "Author").
		Wire(specAuthorWire).
		PrimaryKey(specAuthorPK).
		DocExternal()
	author.Edge("books", Reverse[BkWire]("authorId")).
		Guard(specGuard).
		Inverse("author").
		Includable().
		Limit(10).
		Args(Arg("page", specArgOK)).
		Envelope(specEnvelope)
	FetchIDs(b, author, specFetchAuthors)
	FetchParents(b, book, specFetchBooksByAuthor)
	b.Root(book)
	return book, author
}

func specViaAdd(b *Builder) (book *NodeHandle[bkRow, BkWire], author *NodeHandle[auRow, AuWire]) {
	book = Add(b, Spec[bkRow, BkWire]{
		Name:       "Book",
		Slug:       "bk",
		Wire:       specBookWire,
		PrimaryKey: specBookPK,
		Enrich:     specEnrich,
		Defaults:   []string{"author"},
		Envelope:   &specEnvelope,
		Inputs:     &specInputs,
		Edges: []EdgeSpec[bkRow]{
			{Key: "author", Kind: ToOne[AuWire](), ForeignKey: specBookFK, Inverse: "books",
				Required: true, Includable: true, Policies: []include.EdgePolicy{include.MissingRequiredError}},
		},
		FetchParents: specFetchBooksByAuthor,
	})
	author = Add(b, Spec[auRow, AuWire]{
		Name:        "Author",
		Wire:        specAuthorWire,
		PrimaryKey:  specAuthorPK,
		DocExternal: true,
		Edges: []EdgeSpec[auRow]{
			{Key: "books", Kind: Reverse[BkWire]("authorId"), Guard: specGuard, Inverse: "author",
				Includable: true, Limit: 10, Args: []EdgeArgOpt{Arg("page", specArgOK)},
				Envelope: &specEnvelope},
		},
		FetchIDs: specFetchAuthors,
	})
	b.Root(book)
	return book, author
}

// ------------------------------------------------------------------ Add records exactly what the chain records

// nodeShape / edgeShape flatten the recorded state into comparable values:
// every set-flag and scalar, plus "is the closure bound" (closures themselves
// are not comparable).
type edgeShape struct {
	key, inverse, sort                string
	kind                              include.EdgeKindType
	target                            reflect.Type
	fk, fks, guard                    bool
	fkSet, fksSet, guardSet           bool
	required, includable, bare        bool
	filterable                        bool
	limit, estimatedRows              int
	limitSet, estimatedRowsSet        bool
	envelope                          include.Envelope
	envelopeSet                       bool
	args                              []string
	excludeReq, missingReq, missingFK bool
}

type nodeShape struct {
	name, slug                                string
	rowT, wireT                               reflect.Type
	wire, pk, enrich                          bool
	slugSet, wireSet, pkSet, enrichSet        bool
	defaults                                  []string
	docExternal                               bool
	envelope                                  include.Envelope
	envelopeSet, fetchIDs, fetchParents, root bool
	inputs                                    Inputs
	inputsSet                                 bool
	edgeFetch                                 []string // per-edge bind keys, sorted
	edges                                     []edgeShape
}

// The shapes above are hand-maintained mirrors of edgeSettings / nodeSpec; a
// field added to either must be added here too, or the mirror comparison
// silently stops covering it.
func TestShapeMirrorsEveryField(t *testing.T) {
	if n := reflect.TypeOf(edgeSettings{}).NumField(); n != 20 {
		t.Errorf("edgeSettings has %d fields; edgeShape mirrors 20 — extend edgeShape/shapeOf", n)
	}
	if n := reflect.TypeOf(nodeSpec{}).NumField(); n != 21 {
		t.Errorf("nodeSpec has %d fields; nodeShape mirrors 21 — extend nodeShape/shapeOf", n)
	}
	// EdgeSpec must offer every EdgeBuilder option: Key + Kind + 14 options.
	if n := reflect.TypeOf(EdgeSpec[bkRow]{}).NumField(); n != 16 {
		t.Errorf("EdgeSpec has %d fields; expected 16 (Key, Kind + 14 EdgeBuilder options) — extend EdgeSpec/apply/TestEdgeSpecApplyCoversEveryField", n)
	}
	if n := reflect.TypeOf((*EdgeBuilder[bkRow])(nil)).NumMethod(); n != 14 {
		t.Errorf("EdgeBuilder has %d methods; EdgeSpec mirrors 14 — extend EdgeSpec/apply", n)
	}
}

func shapeOf(b *Builder, n *nodeSpec) nodeShape {
	s := nodeShape{
		name: n.name, slug: n.slug, rowT: n.rowT, wireT: n.wireT,
		wire: n.wireFn != nil, pk: n.primaryKeyFn != nil, enrich: n.enrichFn != nil,
		slugSet: n.slugSet, wireSet: n.wireSet, pkSet: n.primaryKeySet, enrichSet: n.enrichSet,
		defaults: n.defaults, docExternal: n.docExternal,
		envelope: n.envelope, envelopeSet: n.envelopeSet,
		inputs: n.inputs, inputsSet: n.inputsSet,
		fetchIDs: n.fetchIDs != nil, fetchParents: n.fetchParents != nil,
	}
	for _, r := range b.roots {
		if r == n {
			s.root = true
		}
	}
	for key := range n.edgeFetch {
		s.edgeFetch = append(s.edgeFetch, key)
	}
	sort.Strings(s.edgeFetch)
	for _, e := range n.edges {
		st := e.set
		es := edgeShape{
			key: e.key, inverse: st.inverse, sort: st.sort,
			kind: e.kind.kind, target: e.kind.targetWireT,
			fk: st.foreignKey != nil, fks: st.foreignKeys != nil, guard: st.guard != nil,
			fkSet: st.foreignKeySet, fksSet: st.foreignKeysSet, guardSet: st.guardSet,
			required: st.required, includable: st.includable, bare: st.bare,
			filterable: st.filterable,
			limit:      st.limit, estimatedRows: st.estimatedRows,
			limitSet: st.limitSet, estimatedRowsSet: st.estimatedRowsSet,
			envelope: st.envelope, envelopeSet: st.envelopeSet,
			excludeReq: st.policies.ExcludeRequired != nil,
			missingReq: st.policies.MissingRequired != nil,
			missingFK:  st.policies.MissingForeign != nil,
		}
		for _, a := range st.args {
			es.args = append(es.args, a.Name)
		}
		s.edges = append(s.edges, es)
	}
	return s
}

func TestAddRecordsLikeChain(t *testing.T) {
	cb, sb := NewBuilder(), NewBuilder()
	cBook, cAuthor := specViaChain(cb)
	sBook, sAuthor := specViaAdd(sb)

	for _, pair := range []struct {
		name        string
		chain, spec *nodeSpec
	}{
		{"Book", cBook.spec, sBook.spec},
		{"Author", cAuthor.spec, sAuthor.spec},
	} {
		want, got := shapeOf(cb, pair.chain), shapeOf(sb, pair.spec)
		if !reflect.DeepEqual(got, want) {
			t.Errorf("%s: Add recorded\n  %+v\nchain recorded\n  %+v", pair.name, got, want)
		}
	}

	// The boxed closures are the SAME closures, not lookalikes.
	if got := sBook.spec.edges[0].set.foreignKey(bkRow{AuthorID: "a9"}); got != "a9" {
		t.Errorf("ForeignKey via Add = %q, want a9", got)
	}
	if got := sAuthor.spec.wireFn(auRow{ID: "a1", Name: "N"}, nil); got != (AuWire{ID: "a1", Name: "N"}) {
		t.Errorf("Wire via Add = %#v", got)
	}
	if !sAuthor.spec.edges[0].set.guard(nil, auRow{ID: "x"}) || sAuthor.spec.edges[0].set.guard(nil, auRow{}) {
		t.Error("Guard via Add is not the declared closure")
	}
}

// ------------------------------------------------------------------ every EdgeSpec field reaches its EdgeBuilder method

// The mirror fixture above cannot carry every option on one legal graph
// (Sort needs a sortCol, Bare excludes Limit…), so apply is checked
// field-by-field against the chain on a bare builder, no Compile involved.
func TestEdgeSpecApplyCoversEveryField(t *testing.T) {
	fks := func(r bkRow) []string { return r.TagIDs }
	guard := func(*include.Ctx, bkRow) bool { return true }
	env := include.Envelope{Key: "d"}
	full := EdgeSpec[bkRow]{
		Key: "tags", Kind: ToMany[TgWire](),
		ForeignKey: specBookFK, ForeignKeys: fks, Guard: guard,
		Inverse: "books", Required: true, Includable: true, Filterable: true, Bare: true,
		Limit: 3, EstimatedRows: 9, Envelope: &env, Sort: "-title",
		Args:     []EdgeArgOpt{Arg("page", specArgOK)},
		Policies: []include.EdgePolicy{include.MissingForeignError},
	}

	sb, cb := NewBuilder(), NewBuilder()
	sBook := Node[bkRow, BkWire](sb, "Book")
	full.apply(sBook.Edge(full.Key, full.Kind))
	cBook := Node[bkRow, BkWire](cb, "Book")
	cBook.Edge("tags", ToMany[TgWire]()).
		ForeignKey(specBookFK).ForeignKeys(fks).Guard(guard).
		Inverse("books").Required().Includable().Filterable().Bare().
		Limit(3).EstimatedRows(9).Envelope(env).Sort("-title").
		Args(Arg("page", specArgOK)).Policies(include.MissingForeignError)

	want, got := shapeOf(cb, cBook.spec), shapeOf(sb, sBook.spec)
	if !reflect.DeepEqual(got, want) {
		t.Errorf("apply recorded\n  %+v\nchain recorded\n  %+v", got, want)
	}
	if s := sBook.spec.edges[0].set; !s.foreignKeySet || !s.foreignKeysSet || !s.guardSet ||
		!s.limitSet || !s.estimatedRowsSet || !s.envelopeSet || !s.bare || s.sort != "-title" {
		t.Errorf("apply left a declared field unset: %+v", *s)
	}
}

// ------------------------------------------------------------------ an EdgeSpec missing Key or Kind is ONE clear finding

func TestAddEdgeSpecMissingKeyOrKind(t *testing.T) {
	b := NewBuilder()
	Add(b, Spec[bkRow, BkWire]{
		Name: "Book", Wire: specBookWire, PrimaryKey: specBookPK,
		Edges: []EdgeSpec[bkRow]{
			{Key: "author", ForeignKey: specBookFK, Required: true, Includable: true}, // no Kind
			{Kind: ToOne[AuWire](), ForeignKey: specBookFK},                           // no Key
		},
	})
	Add(b, Spec[auRow, AuWire]{Name: "Author", Wire: specAuthorWire, PrimaryKey: specAuthorPK})
	_, err := b.Compile()
	var ce *CompileError
	if !errors.As(err, &ce) {
		t.Fatalf("Compile err = %v, want *CompileError", err)
	}
	msgs := make([]string, len(ce.Findings))
	for i, f := range ce.Findings {
		msgs[i] = f.String()
	}
	want := []string{
		"Book.author: edge declares no Kind (use ToOne, ToMany, Reverse, InArray or Computed)",
		"Book: edge key must not be empty",
	}
	if !reflect.DeepEqual(msgs, want) {
		t.Errorf("findings = %q, want exactly %q (no cascade from the kind/setting matrix)", msgs, want)
	}
}

// ------------------------------------------------------------------ zero fields are "not declared", not "declared empty"

func TestAddZeroFieldsAreUndeclared(t *testing.T) {
	b := NewBuilder()
	h := Add(b, Spec[bkRow, BkWire]{
		Name:  "Book",
		Edges: []EdgeSpec[bkRow]{{Key: "author", Kind: ToOne[AuWire]()}},
	})
	n := h.spec
	if n.slugSet || n.wireSet || n.primaryKeySet || n.enrichSet || n.envelopeSet || n.docExternal ||
		n.defaults != nil || n.fetchIDs != nil || n.fetchParents != nil {
		t.Errorf("zero Spec recorded a declaration: %+v", n)
	}
	s := n.edges[0].set
	if s.foreignKeySet || s.foreignKeysSet || s.guardSet || s.limitSet || s.estimatedRowsSet || s.envelopeSet ||
		s.required || s.includable || s.bare || s.inverse != "" || s.sort != "" || s.args != nil {
		t.Errorf("zero EdgeSpec recorded a declaration: %+v", *s)
	}

	// Compile reports them as MISSING (the chain's "never called" wording),
	// never as "X(nil)".
	Add(b, Spec[auRow, AuWire]{Name: "Author"})
	_, err := b.Compile()
	var ce *CompileError
	if !errors.As(err, &ce) {
		t.Fatalf("Compile err = %v, want *CompileError", err)
	}
	msg := ce.Error()
	for _, want := range []string{
		"Book: Wire is required",
		"Book: PrimaryKey is required",
		"Book.author: to-one edge requires ForeignKey",
	} {
		if !strings.Contains(msg, want) {
			t.Errorf("findings lack %q:\n%s", want, msg)
		}
	}
	if strings.Contains(msg, "(nil)") {
		t.Errorf("zero Spec reported as a nil declaration:\n%s", msg)
	}
}

// ------------------------------------------------------------------ same bytes end to end

func TestAddHydratesLikeChain(t *testing.T) {
	hydrate := func(t *testing.T, declare func(*Builder) (*NodeHandle[bkRow, BkWire], *NodeHandle[auRow, AuWire])) []byte {
		t.Helper()
		b := NewBuilder()
		declare(b)
		g, err := b.Compile()
		if err != nil {
			t.Fatalf("Compile: %v", err)
		}
		inc, err := include.ParseInclude("author.books")
		if err != nil {
			t.Fatal(err)
		}
		raw, _, err := include.HydrateEntity(g.Resource("Book"), specBooks["b1"], inc, nil,
			&include.Ctx{Registry: g}, include.DefaultOptions)
		if err != nil {
			t.Fatalf("HydrateEntity: %v", err)
		}
		return raw
	}
	chain, spec := hydrate(t, specViaChain), hydrate(t, specViaAdd)
	if !bytes.Equal(chain, spec) {
		t.Errorf("bytes differ:\n chain %s\n spec  %s", chain, spec)
	}
	if !bytes.Contains(spec, []byte(`"books":{"data":[{"data":{"id":"b1"`)) {
		t.Errorf("edge envelope/limit from Spec not honoured: %s", spec)
	}
}

// ------------------------------------------------------------------ the handle is a live NodeHandle; Add dies with the builder

func TestAddHandleMixesWithChain(t *testing.T) {
	b := NewBuilder()
	book := Add(b, Spec[bkRow, BkWire]{Name: "Book", Wire: specBookWire, PrimaryKey: specBookPK})
	Add(b, Spec[auRow, AuWire]{Name: "Author", Wire: specAuthorWire, PrimaryKey: specAuthorPK, FetchIDs: specFetchAuthors})
	// chained configuration after Add, on the returned handle
	book.Edge("author", ToOne[AuWire]()).ForeignKey(specBookFK).Includable()
	b.Root(book)
	g, err := b.Compile()
	if err != nil {
		t.Fatalf("Compile: %v", err)
	}
	if got := names(g.Roots()); !reflect.DeepEqual(got, []string{"Book"}) {
		t.Errorf("roots = %v", got)
	}

	defer func() {
		if r := recover(); r != "graph: builder is dead after Compile" {
			t.Errorf("Add after Compile panic = %v", r)
		}
	}()
	Add(b, Spec[tgRow, TgWire]{Name: "Tag"})
}
