package graph

import (
	"errors"
	"reflect"
	"testing"

	"github.com/qrotux/wireleaf/apidoc"
	"github.com/qrotux/wireleaf/include"
)

// ------------------------------------------------------------------ fixture

type bkRow struct {
	ID       string
	AuthorID string
	Title    string
	TagIDs   []string
}

type BkWire struct {
	ID    string `json:"id"`
	Title string `json:"title"`
}

type auRow struct {
	ID   string
	Name string
}

type AuWire struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

type tgRow struct{ ID string }

type TgWire struct {
	ID string `json:"id"`
}

// ------------------------------------------------------------------ AC #1: the spec §1 declaration sketch compiles verbatim

func TestBuilderRecordsDeclarations(t *testing.T) {
	validatePage := func(raw any) error { return nil }
	guardFn := func(c *include.Ctx, r auRow) bool { return true }
	schema := apidoc.RawFragment(map[string]any{"type": "array"})

	b := NewBuilder()

	book := Node[bkRow, BkWire](b, "Book").
		Slug("book").
		Wire(func(r bkRow, c *include.Ctx) BkWire {
			return BkWire{ID: r.ID, Title: r.Title}
		}).
		PrimaryKey(func(r bkRow) string { return r.ID }).
		Enrich(func(rows []bkRow, c *include.Ctx) error { return nil }).
		Defaults("author")

	author := Node[auRow, AuWire](b, "Author").
		Slug("author").
		Wire(func(r auRow, c *include.Ctx) AuWire {
			return AuWire{ID: r.ID, Name: r.Name}
		}).
		PrimaryKey(func(r auRow) string { return r.ID }).
		DocExternal()

	book.Edge("author", ToOne[AuWire]()).
		ForeignKey(func(r bkRow) string { return r.AuthorID }).
		Inverse("books").
		Required().
		Includable()

	author.Edge("books", Reverse[BkWire]("authorId")).
		Includable().
		Limit(10).
		Sort("-createdAt").
		Args(Arg("page", validatePage)).
		Guard(guardFn)

	book.Edge("participants", Computed(schema)).Includable()

	FetchIDs(b, author, func(c *include.Ctx, ids []string) ([]auRow, error) {
		return nil, nil
	})
	FetchParents(b, book, func(c *include.Ctx, parentIDs []string, q include.EdgeQuery) (map[string]ParentRows[bkRow], error) {
		return nil, nil
	})
	FetchParents(b, book, PerParent(func(c *include.Ctx, parentID string, q include.EdgeQuery) ([]bkRow, bool, error) {
		return nil, false, nil
	}))

	b.Root(book)

	// --- nodes ---------------------------------------------------------
	if len(b.nodes) != 2 {
		t.Fatalf("nodes = %d, want 2", len(b.nodes))
	}
	bs, as := book.spec, author.spec
	if bs.name != "Book" || bs.slug != "book" || !bs.slugSet {
		t.Errorf("book node = %+v, want name Book / slug book", bs)
	}
	if bs.rowT != reflect.TypeFor[bkRow]() || bs.wireT != reflect.TypeFor[BkWire]() {
		t.Errorf("book types = %v/%v", bs.rowT, bs.wireT)
	}
	if !bs.wireSet || bs.wireFn == nil || !bs.primaryKeySet || bs.primaryKeyFn == nil || bs.enrichFn == nil {
		t.Error("book map/id/enrich not recorded")
	}
	if got := bs.wireFn(bkRow{ID: "b1", Title: "T"}, nil); got != (BkWire{ID: "b1", Title: "T"}) {
		t.Errorf("boxed wireFn = %#v", got)
	}
	if got := bs.primaryKeyFn(bkRow{ID: "b1"}); got != "b1" {
		t.Errorf("boxed primaryKeyFn = %q", got)
	}
	if err := bs.enrichFn([]any{bkRow{ID: "b1"}}, nil); err != nil {
		t.Errorf("boxed enrichFn err = %v", err)
	}
	if !reflect.DeepEqual(bs.defaults, []string{"author"}) {
		t.Errorf("book defaults = %v", bs.defaults)
	}
	if bs.docExternal {
		t.Error("book should not be doc-external")
	}
	if !as.docExternal {
		t.Error("author should be doc-external")
	}
	// --- edges ---------------------------------------------------------
	if len(bs.edges) != 2 || len(as.edges) != 1 {
		t.Fatalf("edges: book=%d author=%d, want 2/1", len(bs.edges), len(as.edges))
	}

	e0 := bs.edges[0]
	if e0.key != "author" {
		t.Errorf("book edge[0] key = %q", e0.key)
	}
	if e0.kind.kind != include.KindToOne || e0.kind.targetWireT != reflect.TypeFor[AuWire]() {
		t.Errorf("book edge[0] kind = %+v", e0.kind)
	}
	s0 := e0.set
	if !s0.foreignKeySet || s0.foreignKey == nil || s0.foreignKey(bkRow{AuthorID: "a1"}) != "a1" {
		t.Error("ForeignKey not boxed")
	}
	if s0.inverse != "books" || !s0.required || !s0.includable {
		t.Errorf("book edge[0] settings = %+v", s0)
	}

	e1 := bs.edges[1]
	if e1.key != "participants" || e1.kind.targetWireT != nil || e1.kind.kind != include.KindComputed {
		t.Errorf("book edge[1] = %+v", e1)
	}
	if !reflect.DeepEqual(e1.kind.schema.Map(), schema.Map()) {
		t.Errorf("computed schema not carried: %v", e1.kind.schema.Map())
	}

	e2 := as.edges[0]
	if e2.key != "books" || e2.kind.kind != include.KindReverse || e2.kind.targetWireT != reflect.TypeFor[BkWire]() || e2.kind.backref != "authorId" {
		t.Errorf("author edge[0] = %+v", e2)
	}
	s2 := e2.set
	if !s2.includable || s2.limit != 10 || !s2.limitSet || s2.sort != "-createdAt" {
		t.Errorf("author edge settings = %+v", s2)
	}
	if len(s2.args) != 1 || s2.args[0].Name != "page" || s2.args[0].Validate == nil {
		t.Errorf("author edge args = %+v", s2.args)
	}
	if !s2.guardSet || s2.guard == nil || !s2.guard(nil, auRow{ID: "a1"}) {
		t.Error("Guard not boxed")
	}

	// --- binds ---------------------------------------------------------
	if as.fetchIDs == nil {
		t.Error("author FetchIDs bind missing")
	}
	if bs.fetchParents == nil {
		t.Error("book FetchParents bind missing")
	}
	if bs.fetchIDs != nil || as.fetchParents != nil {
		t.Error("unexpected extra binds")
	}

	// --- roots ---------------------------------------------------------
	if len(b.roots) != 1 || b.roots[0] != bs {
		t.Fatalf("roots = %v, want [Book]", b.roots)
	}
}

// TestBuilderDeclarationOrder: an edge may reference a node declared LATER —
// handles are pointers, not thunks.
func TestBuilderDeclarationOrderDoesNotMatter(t *testing.T) {
	b := NewBuilder()
	book := Node[bkRow, BkWire](b, "Book")
	// Tag is declared AFTER the edge that targets it — wire-type addressing
	// resolves in Compile, so declaration (and file) order does not matter.
	book.Edge("tags", ToMany[TgWire]()).ForeignKeys(func(r bkRow) []string { return r.TagIDs }).Bare()
	Node[tgRow, TgWire](b, "Tag")

	e := book.spec.edges[0]
	if e.kind.kind != include.KindForwardHasMany || e.kind.targetWireT != reflect.TypeFor[TgWire]() {
		t.Fatalf("edge kind = %+v", e.kind)
	}
	s := e.set
	if !s.bare || !s.foreignKeysSet || s.foreignKeys == nil || len(s.foreignKeys(bkRow{TagIDs: []string{"t1"}})) != 1 {
		t.Fatalf("settings = %+v", s)
	}
}

func TestBuilderInArrayKind(t *testing.T) {
	b := NewBuilder()
	Node[tgRow, TgWire](b, "Tag")
	book := Node[bkRow, BkWire](b, "Book")
	book.Edge("tagRefs", InArray[TgWire]("tags", "tagId")).Includable()

	k := book.spec.edges[0].kind
	if k.kind != include.KindInArray || k.targetWireT != reflect.TypeFor[TgWire]() || k.arrayPath != "tags" || k.subField != "tagId" {
		t.Fatalf("in-array kind = %+v", k)
	}
}

// A nil typed closure must NOT be boxed into a non-nil wrapper: the set-flag
// stays, the boxed closure stays nil, so Compile can emit an "X(nil)" finding
// instead of production panicking later.
func TestBuilderNilClosuresStayNil(t *testing.T) {
	b := NewBuilder()
	book := Node[bkRow, BkWire](b, "Book").
		Wire(nil).
		PrimaryKey(nil).
		Enrich(nil)
	Node[auRow, AuWire](b, "Author")
	book.Edge("author", ToOne[AuWire]()).
		ForeignKey(nil).
		ForeignKeys(nil).
		Guard(nil)

	s := book.spec
	if !s.wireSet || s.wireFn != nil {
		t.Errorf("Wire(nil): wireSet=%v wireFn nil=%v, want true/true", s.wireSet, s.wireFn == nil)
	}
	if !s.primaryKeySet || s.primaryKeyFn != nil {
		t.Errorf("PrimaryKey(nil): primaryKeySet=%v primaryKeyFn nil=%v, want true/true", s.primaryKeySet, s.primaryKeyFn == nil)
	}
	if !s.enrichSet || s.enrichFn != nil {
		t.Errorf("Enrich(nil): enrichSet=%v enrichFn nil=%v, want true/true", s.enrichSet, s.enrichFn == nil)
	}
	es := s.edges[0].set
	if !es.foreignKeySet || !es.foreignKeysSet || !es.guardSet {
		t.Errorf("nil edge methods must still set their declared-flags: %+v", es)
	}
	if es.foreignKey != nil || es.foreignKeys != nil || es.guard != nil {
		t.Errorf("nil edge closures must stay nil: %+v", es)
	}
}

// ------------------------------------------------------------------ AC: PerParent

func TestPerParentLoops(t *testing.T) {
	var seen []string
	fn := PerParent(func(c *include.Ctx, parentID string, q include.EdgeQuery) ([]bkRow, bool, error) {
		seen = append(seen, parentID)
		return []bkRow{{ID: parentID + "-1"}}, parentID == "p2", nil
	})

	out, err := fn(nil, []string{"p1", "p2"}, include.EdgeQuery{Limit: 5})
	if err != nil {
		t.Fatalf("err = %v", err)
	}
	if !reflect.DeepEqual(seen, []string{"p1", "p2"}) {
		t.Fatalf("call order = %v, want [p1 p2]", seen)
	}
	if len(out) != 2 {
		t.Fatalf("map size = %d, want 2", len(out))
	}
	if got := out["p1"]; len(got.Rows) != 1 || got.Rows[0].ID != "p1-1" || got.HasMore {
		t.Errorf("p1 = %+v", got)
	}
	if got := out["p2"]; !got.HasMore {
		t.Errorf("p2 hasMore = false, want true")
	}
}

func TestPerParentPropagatesFirstError(t *testing.T) {
	boom := errors.New("boom")
	calls := 0
	fn := PerParent(func(c *include.Ctx, parentID string, q include.EdgeQuery) ([]bkRow, bool, error) {
		calls++
		if parentID == "p1" {
			return nil, false, boom
		}
		return nil, false, nil
	})

	out, err := fn(nil, []string{"p1", "p2"}, include.EdgeQuery{})
	if !errors.Is(err, boom) {
		t.Fatalf("err = %v, want boom", err)
	}
	if out != nil {
		t.Errorf("out = %v, want nil", out)
	}
	if calls != 1 {
		t.Errorf("calls = %d, want 1 (short-circuit)", calls)
	}
}

// ------------------------------------------------------------------ AC: dead after Compile

func TestBuilderDeadAfterCompile(t *testing.T) {
	b := NewBuilder()
	book := Node[bkRow, BkWire](b, "Book")
	edgeB := book.Edge("pre", ToOne[BkWire]()) // an EdgeBuilder kept across Compile
	if _, err := b.Compile(); err == nil {
		t.Fatal("stub Compile should return an error")
	}

	for name, fn := range map[string]func(){
		"Node":       func() { Node[auRow, AuWire](b, "Author") },
		"Edge":       func() { book.Edge("x", ToOne[BkWire]()) },
		"Root":       func() { b.Root(book) },
		"node chain": func() { book.Wire(nil) },
		"edge chain": func() { edgeB.Required() },
		"Filterable": func() { edgeB.Filterable() },
	} {
		func() {
			defer func() {
				r := recover()
				if r == nil {
					t.Errorf("%s after Compile did not panic", name)
					return
				}
				if s, _ := r.(string); s != "graph: builder is dead after Compile" {
					t.Errorf("%s panic = %v", name, r)
				}
			}()
			fn()
		}()
	}
}

// boxing of ParentRows[Row] → include.ParentRows at the bind seam.
func TestFetchParentsBoxing(t *testing.T) {
	b := NewBuilder()
	book := Node[bkRow, BkWire](b, "Book")
	FetchParents(b, book, func(c *include.Ctx, ids []string, q include.EdgeQuery) (map[string]ParentRows[bkRow], error) {
		return map[string]ParentRows[bkRow]{
			"p1": {Rows: []bkRow{{ID: "b1"}}, HasMore: true, NextCursor: "cur"},
			"p2": {Rows: nil},
		}, nil
	})

	got, err := book.spec.fetchParents(nil, []string{"p1", "p2"}, include.EdgeQuery{})
	if err != nil {
		t.Fatalf("err = %v", err)
	}
	if len(got["p1"].Rows) != 1 || got["p1"].Rows[0].(bkRow).ID != "b1" || !got["p1"].HasMore || got["p1"].NextCursor != "cur" {
		t.Errorf("p1 = %+v", got["p1"])
	}
	if got["p2"].Rows != nil {
		t.Errorf("nil rows should stay nil, got %#v", got["p2"].Rows)
	}
}

func TestFetchIDsBoxing(t *testing.T) {
	b := NewBuilder()
	author := Node[auRow, AuWire](b, "Author")
	FetchIDs(b, author, func(c *include.Ctx, ids []string) ([]auRow, error) {
		return []auRow{{ID: ids[0]}}, nil
	})
	rows, err := author.spec.fetchIDs(nil, []string{"a1"})
	if err != nil {
		t.Fatalf("err = %v", err)
	}
	if len(rows) != 1 || rows[0].(auRow).ID != "a1" {
		t.Fatalf("rows = %#v", rows)
	}
}
