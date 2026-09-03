package graph

import (
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/qrotux/wireleaf/apidoc"
	"github.com/qrotux/wireleaf/include"
)

// ------------------------------------------------------------------ helpers

// compileFindings runs build against a fresh Builder, compiles it and returns
// the findings of the resulting *CompileError. It fails the test when Compile
// succeeds or returns some other error type.
func compileFindings(t *testing.T, build func(b *Builder)) []Finding {
	t.Helper()
	b := NewBuilder()
	build(b)
	g, err := b.Compile()
	if err == nil {
		t.Fatalf("Compile succeeded, want findings (graph = %v)", g)
	}
	if g != nil {
		t.Errorf("Compile returned a non-nil graph alongside an error")
	}
	var ce *CompileError
	if !errors.As(err, &ce) {
		t.Fatalf("Compile error = %T (%v), want *CompileError", err, err)
	}
	if len(ce.Findings) == 0 {
		t.Fatal("*CompileError carries no findings")
	}
	return ce.Findings
}

func hasFinding(fs []Finding, sub string) bool {
	for _, f := range fs {
		if strings.Contains(f.Msg, sub) {
			return true
		}
	}
	return false
}

func dumpFindings(fs []Finding) string {
	var sb strings.Builder
	for _, f := range fs {
		sb.WriteString("\n  - ")
		sb.WriteString(f.String())
	}
	return sb.String()
}

// ------------------------------------------------------------------ fixtures

// (bkRow/BkWire, auRow/AuWire, tgRow/TgWire live in builder_test.go)

type rvRow struct {
	ID     string
	BookID string
}

type RvWire struct {
	ID        string `json:"id" sortCol:"id"`
	CreatedAt string `json:"createdAt" sortCol:"created_at"`
}

func okBook(b *Builder) *NodeHandle[bkRow, BkWire] {
	return Node[bkRow, BkWire](b, "Book").
		Wire(func(r bkRow, c *include.Ctx) BkWire { return BkWire{ID: r.ID, Title: r.Title} }).
		PrimaryKey(func(r bkRow) string { return r.ID })
}

func okAuthor(b *Builder) *NodeHandle[auRow, AuWire] {
	return Node[auRow, AuWire](b, "Author").
		Wire(func(r auRow, c *include.Ctx) AuWire { return AuWire{ID: r.ID, Name: r.Name} }).
		PrimaryKey(func(r auRow) string { return r.ID })
}

func okTag(b *Builder) *NodeHandle[tgRow, TgWire] {
	return Node[tgRow, TgWire](b, "Tag").
		Wire(func(r tgRow, c *include.Ctx) TgWire { return TgWire{ID: r.ID} }).
		PrimaryKey(func(r tgRow) string { return r.ID })
}

func okReview(b *Builder) *NodeHandle[rvRow, RvWire] {
	return Node[rvRow, RvWire](b, "Review").
		Wire(func(r rvRow, c *include.Ctx) RvWire { return RvWire{ID: r.ID} }).
		PrimaryKey(func(r rvRow) string { return r.ID })
}

// kind constructors with one uniform signature, for the option-matrix table
// (every row's edge targets Tag).
func toOneK() EdgeKind   { return ToOne[TgWire]() }
func toManyK() EdgeKind  { return ToMany[TgWire]() }
func reverseK() EdgeKind { return Reverse[TgWire]("bookId") }
func inArrayK() EdgeKind { return InArray[TgWire]("people", "user") }
func computedK() EdgeKind {
	return Computed(apidoc.RawFragment(map[string]any{"type": "object"}))
}

// bkCfg is one chained configuration step over a Book edge — the shape every
// option-matrix row needs.
type bkCfg = func(*EdgeBuilder[bkRow]) *EdgeBuilder[bkRow]

// Chain-step constructors for the matrix rows.
func fkCfg() bkCfg {
	return func(e *EdgeBuilder[bkRow]) *EdgeBuilder[bkRow] {
		return e.ForeignKey(func(r bkRow) string { return r.AuthorID })
	}
}
func fksCfg() bkCfg {
	return func(e *EdgeBuilder[bkRow]) *EdgeBuilder[bkRow] {
		return e.ForeignKeys(func(r bkRow) []string { return r.TagIDs })
	}
}
func limitCfg(n int) bkCfg {
	return func(e *EdgeBuilder[bkRow]) *EdgeBuilder[bkRow] { return e.Limit(n) }
}
func estCfg(n int) bkCfg {
	return func(e *EdgeBuilder[bkRow]) *EdgeBuilder[bkRow] { return e.EstimatedRows(n) }
}
func bareCfg() bkCfg {
	return func(e *EdgeBuilder[bkRow]) *EdgeBuilder[bkRow] { return e.Bare() }
}
func sortCfg(k string) bkCfg {
	return func(e *EdgeBuilder[bkRow]) *EdgeBuilder[bkRow] { return e.Sort(k) }
}
func argsCfg(a ...EdgeArgOpt) bkCfg {
	return func(e *EdgeBuilder[bkRow]) *EdgeBuilder[bkRow] { return e.Args(a...) }
}
func guardCfg() bkCfg {
	return func(e *EdgeBuilder[bkRow]) *EdgeBuilder[bkRow] {
		return e.Guard(func(c *include.Ctx, r bkRow) bool { return true })
	}
}

// edgeCase declares Book + Tag and one Book edge "e" of the given kind,
// configured by the given chain steps.
func edgeCase(kind func() EdgeKind, cfgs ...bkCfg) func(b *Builder) {
	return func(b *Builder) {
		book, _ := okBook(b), okTag(b)
		e := book.Edge("e", kind())
		for _, c := range cfgs {
			e = c(e)
		}
	}
}

// ------------------------------------------------------------------ every finding class

func TestCompileFindingClasses(t *testing.T) {
	cases := []struct {
		name  string
		build func(b *Builder)
		want  string
	}{
		{ // (1)
			name: "duplicate node names",
			build: func(b *Builder) {
				okBook(b)
				Node[tgRow, TgWire](b, "Book").
					Wire(func(r tgRow, c *include.Ctx) TgWire { return TgWire{ID: r.ID} }).
					PrimaryKey(func(r tgRow) string { return r.ID })
			},
			want: "duplicate node name",
		},
		{ // (2)
			name: "duplicate edge keys",
			build: func(b *Builder) {
				book, _ := okBook(b), okAuthor(b)
				book.Edge("author", ToOne[AuWire]()).ForeignKey(func(r bkRow) string { return r.AuthorID })
				book.Edge("author", ToOne[AuWire]()).ForeignKey(func(r bkRow) string { return r.AuthorID })
			},
			want: "duplicate edge key",
		},
		{
			name: "edge key collides with wire field",
			build: func(b *Builder) {
				book, _ := okBook(b), okAuthor(b)
				// BkWire has `json:"title"`; an edge with the same key would
				// produce a duplicate member in the response.
				book.Edge("title", ToOne[AuWire]()).ForeignKey(func(r bkRow) string { return r.AuthorID })
			},
			want: "collides with wire field",
		},
		{
			name: "edge key needs JSON escaping",
			build: func(b *Builder) {
				book, _ := okBook(b), okAuthor(b)
				book.Edge(`a":1,"pwned`, ToOne[AuWire]()).ForeignKey(func(r bkRow) string { return r.AuthorID })
			},
			want: "needs JSON escaping",
		},
		{ // (3)
			name: "two nodes share one wire type",
			build: func(b *Builder) {
				okBook(b)
				Node[bkRow, BkWire](b, "Book2").
					Wire(func(r bkRow, c *include.Ctx) BkWire { return BkWire{ID: r.ID} }).
					PrimaryKey(func(r bkRow) string { return r.ID })
			},
			want: "is already used by node",
		},
		{ // (4a)
			name: "inverse edge missing on target",
			build: func(b *Builder) {
				book, author := okBook(b), okAuthor(b)
				_ = author
				book.Edge("author", ToOne[AuWire]()).ForeignKey(func(r bkRow) string { return r.AuthorID }).Inverse("books")
			},
			want: "inverse: no edge",
		},
		{ // (4b)
			name: "inverse edge has the same direction",
			build: func(b *Builder) {
				book, author := okBook(b), okAuthor(b)
				book.Edge("author", ToOne[AuWire]()).ForeignKey(func(r bkRow) string { return r.AuthorID }).Inverse("books")
				author.Edge("books", ToMany[BkWire]()).ForeignKeys(func(r auRow) []string { return nil })
			},
			want: "has the same direction",
		},
		{ // (4c)
			name: "inverse edge points at another node",
			build: func(b *Builder) {
				book, author, _ := okBook(b), okAuthor(b), okTag(b)
				book.Edge("author", ToOne[AuWire]()).ForeignKey(func(r bkRow) string { return r.AuthorID }).Inverse("books")
				author.Edge("books", Reverse[TgWire]("authorId"))
			},
			want: "points at node",
		},
		{ // (4d) FK/backref disagreement: the two sides do not name each other
			name: "inverse pair disagrees",
			build: func(b *Builder) {
				book, author := okBook(b), okAuthor(b)
				book.Edge("author", ToOne[AuWire]()).ForeignKey(func(r bkRow) string { return r.AuthorID }).Inverse("books")
				author.Edge("books", Reverse[BkWire]("authorId")).Inverse("writer")
			},
			want: "declares Inverse",
		},
		{ // (5)
			name: "to-one without ForeignKey",
			build: func(b *Builder) {
				book, _ := okBook(b), okAuthor(b)
				book.Edge("author", ToOne[AuWire]())
			},
			want: "to-one edge requires ForeignKey",
		},
		{ // (6)
			name: "forward-hasMany without ForeignKeys",
			build: func(b *Builder) {
				book, _ := okBook(b), okTag(b)
				book.Edge("tags", ToMany[TgWire]())
			},
			want: "forward-hasMany edge requires ForeignKeys",
		},
		{ // (7a)
			name: "reverse without backref",
			build: func(b *Builder) {
				_, author := okBook(b), okAuthor(b)
				author.Edge("books", Reverse[BkWire](""))
			},
			want: "reverse edge requires a backref",
		},
		{ // (7b)
			name: "reverse includable from a root without FetchParents",
			build: func(b *Builder) {
				_, author := okBook(b), okAuthor(b)
				author.Edge("books", Reverse[BkWire]("authorId")).Includable()
				b.Root(author)
			},
			want: "missing FetchParents",
		},
		{ // (8a)
			name: "in-array without arrayPath/subField",
			build: func(b *Builder) {
				book, _ := okBook(b), okTag(b)
				book.Edge("participants", InArray[TgWire]("", "")).ForeignKeys(func(r bkRow) []string { return r.TagIDs })
			},
			want: "in-array edge requires arrayPath and subField",
		},
		{ // (8b)
			name: "in-array without ForeignKeys",
			build: func(b *Builder) {
				book, _ := okBook(b), okTag(b)
				book.Edge("participants", InArray[TgWire]("people", "user"))
			},
			want: "in-array edge requires ForeignKeys",
		},
		{ // (9a)
			name: "computed with Inverse",
			build: func(b *Builder) {
				book := okBook(b)
				book.Edge("stats", Computed(apidoc.RawFragment(map[string]any{"type": "object"}))).Inverse("x")
			},
			want: "computed edge cannot declare Inverse",
		},
		{ // (9b)
			name: "computed with Required",
			build: func(b *Builder) {
				book := okBook(b)
				book.Edge("stats", Computed(apidoc.RawFragment(map[string]any{"type": "object"}))).Required()
			},
			want: "Required() is only valid on to-one edges",
		},
		{ // (10a) unresolvable same-depth tie: encoding/json drops the field
			name: "ambiguous json name at the same embedding depth",
			build: func(b *Builder) {
				Node[dupRow, genuineTieWire](b, "Tie").
					Wire(func(r dupRow, c *include.Ctx) genuineTieWire { return genuineTieWire{} }).
					PrimaryKey(func(r dupRow) string { return r.ID })
			},
			want: `duplicate json name "Nick"`,
		},
		{ // (10b)
			name: "sortCol tag on an unexported field",
			build: func(b *Builder) {
				Node[dupRow, badSortWire](b, "BadSort").
					Wire(func(r dupRow, c *include.Ctx) badSortWire { return badSortWire{} }).
					PrimaryKey(func(r dupRow) string { return r.ID })
			},
			want: "sortCol tag on unexported field",
		},
		{ // (10b') an unexported EMBED carrying a sortCol tag is inert too
			name: "sortCol tag on an unexported embedded field",
			build: func(b *Builder) {
				Node[dupRow, embSortWire](b, "EmbSort").
					Wire(func(r dupRow, c *include.Ctx) embSortWire { return embSortWire{} }).
					PrimaryKey(func(r dupRow) string { return r.ID })
			},
			want: "sortCol tag on unexported field embSortInner",
		},
		{ // (10c)
			name: "Sort default key absent from derived sort cols",
			build: func(b *Builder) {
				book, _ := okBook(b), okReview(b)
				book.Edge("reviews", Reverse[RvWire]("bookId")).Sort("-nope")
			},
			want: "is not a sortCol of node",
		},
		{ // (11)
			name: "default-edge cycle",
			build: func(b *Builder) {
				book := okBook(b).Defaults("author")
				author := okAuthor(b).Defaults("books")
				book.Edge("author", ToOne[AuWire]()).ForeignKey(func(r bkRow) string { return r.AuthorID })
				author.Edge("books", Reverse[BkWire]("authorId"))
			},
			want: "default-edge cycle",
		},
		{ // (12)
			name: "forward includable target missing FetchIDs",
			build: func(b *Builder) {
				book, _ := okBook(b), okAuthor(b)
				book.Edge("author", ToOne[AuWire]()).ForeignKey(func(r bkRow) string { return r.AuthorID }).Includable()
				b.Root(book)
			},
			want: "missing FetchIDs",
		},
		{ // (12b) ":limit" is engine-owned: declaring it would silently shadow
			// the built-in coercion/clamp.
			name: "declared edge arg named limit",
			build: func(b *Builder) {
				book, _ := okBook(b), okReview(b)
				book.Edge("reviews", Reverse[RvWire]("bookId")).
					Args(Arg("limit", func(raw any) error { return nil }))
			},
			want: `edge arg "limit" conflicts with the built-in :limit`,
		},
		{ // (13)
			name: "Required on a reverse edge",
			build: func(b *Builder) {
				_, author := okBook(b), okAuthor(b)
				author.Edge("books", Reverse[BkWire]("authorId")).Required()
			},
			want: "Required() is only valid on to-one edges",
		},
		{
			name: "Wire missing",
			build: func(b *Builder) {
				Node[bkRow, BkWire](b, "Book").
					PrimaryKey(func(r bkRow) string { return r.ID })
			},
			want: "Wire is required",
		},
		{
			name: "PrimaryKey missing",
			build: func(b *Builder) {
				Node[bkRow, BkWire](b, "Book").
					Wire(func(r bkRow, c *include.Ctx) BkWire { return BkWire{} })
			},
			want: "PrimaryKey is required",
		},
		{
			name: "Wire(nil)",
			build: func(b *Builder) {
				Node[bkRow, BkWire](b, "Book").
					Wire(nil).
					PrimaryKey(func(r bkRow) string { return r.ID })
			},
			want: "Wire(nil) on node Book",
		},
		{
			name: "PrimaryKey(nil)",
			build: func(b *Builder) {
				Node[bkRow, BkWire](b, "Book").
					Wire(func(r bkRow, c *include.Ctx) BkWire { return BkWire{} }).
					PrimaryKey(nil)
			},
			want: "PrimaryKey(nil) on node Book",
		},
		{
			name: "Enrich(nil)",
			build: func(b *Builder) {
				okBook(b).Enrich(nil)
			},
			want: "Enrich(nil) on node Book",
		},
		{
			name: "ForeignKey(nil)",
			build: func(b *Builder) {
				book, _ := okBook(b), okAuthor(b)
				book.Edge("author", ToOne[AuWire]()).ForeignKey(nil)
			},
			want: "ForeignKey(nil) on edge author",
		},
		{
			name: "ForeignKeys(nil)",
			build: func(b *Builder) {
				book, _ := okBook(b), okTag(b)
				book.Edge("tags", ToMany[TgWire]()).ForeignKeys(nil)
			},
			want: "ForeignKeys(nil) on edge tags",
		},
		{
			name: "Guard(nil)",
			build: func(b *Builder) {
				book, _ := okBook(b), okAuthor(b)
				book.Edge("author", ToOne[AuWire]()).ForeignKey(func(r bkRow) string { return r.AuthorID }).Guard(nil)
			},
			want: "Guard(nil) on edge author",
		},
		{
			name: "Defaults naming an unknown edge",
			build: func(b *Builder) {
				okBook(b).Defaults("nosuch")
			},
			want: "Defaults names unknown edge key",
		},

		// --- kind ↔ option matrix: an option that is INERT for the kind is a
		// finding, one row per cell outside the matrix.
		{name: "ForeignKey on reverse", build: edgeCase(reverseK, fkCfg()), want: "ForeignKey is not valid on reverse edges"},
		{name: "ForeignKey on forward-hasMany", build: edgeCase(toManyK, fkCfg(), fksCfg()), want: "ForeignKey is not valid on forward-hasMany edges"},
		{name: "ForeignKey on in-array", build: edgeCase(inArrayK, fkCfg(), fksCfg()), want: "ForeignKey is not valid on in-array edges"},
		{name: "ForeignKey on computed", build: edgeCase(computedK, fkCfg()), want: "ForeignKey is not valid on computed edges"},

		{name: "ForeignKeys on to-one", build: edgeCase(toOneK, fkCfg(), fksCfg()), want: "ForeignKeys is not valid on to-one edges"},
		{name: "ForeignKeys on reverse", build: edgeCase(reverseK, fksCfg()), want: "ForeignKeys is not valid on reverse edges"},
		{name: "ForeignKeys on computed", build: edgeCase(computedK, fksCfg()), want: "ForeignKeys is not valid on computed edges"},

		{name: "Limit on to-one", build: edgeCase(toOneK, fkCfg(), limitCfg(5)), want: "Limit is not valid on to-one edges"},
		{name: "Limit on in-array", build: edgeCase(inArrayK, fksCfg(), limitCfg(5)), want: "Limit is not valid on in-array edges"},
		{name: "Limit on computed", build: edgeCase(computedK, limitCfg(5)), want: "Limit is not valid on computed edges"},
		{name: "Limit below 1", build: edgeCase(reverseK, limitCfg(0)), want: "Limit must be >= 1"},
		{name: "Bare with Limit", build: edgeCase(reverseK, bareCfg(), limitCfg(5)), want: "Bare edge cannot carry Limit"},
		{name: "EstimatedRows on enveloped edge", build: edgeCase(reverseK, estCfg(5)), want: "EstimatedRows is only valid"},
		{name: "EstimatedRows below 1", build: edgeCase(reverseK, bareCfg(), estCfg(0)), want: "EstimatedRows must be >= 1"},

		{name: "Bare on to-one", build: edgeCase(toOneK, fkCfg(), bareCfg()), want: "Bare is not valid on to-one edges"},
		{name: "Bare on in-array", build: edgeCase(inArrayK, fksCfg(), bareCfg()), want: "Bare is not valid on in-array edges"},
		{name: "Bare on computed", build: edgeCase(computedK, bareCfg()), want: "Bare is not valid on computed edges"},

		{name: "Sort on to-one", build: edgeCase(toOneK, fkCfg(), sortCfg("id")), want: "Sort is not valid on to-one edges"},
		{name: "Sort on forward-hasMany", build: edgeCase(toManyK, fksCfg(), sortCfg("id")), want: "Sort is not valid on forward-hasMany edges"},
		{name: "Sort on in-array", build: edgeCase(inArrayK, fksCfg(), sortCfg("id")), want: "Sort is not valid on in-array edges"},
		{name: "Sort on computed", build: edgeCase(computedK, sortCfg("id")), want: "Sort is not valid on computed edges"},

		{name: "Args on to-one", build: edgeCase(toOneK, fkCfg(), argsCfg(Arg("page", nil))), want: "Args is not valid on to-one edges"},
		{name: "Args on forward-hasMany", build: edgeCase(toManyK, fksCfg(), argsCfg(Arg("page", nil))), want: "Args is not valid on forward-hasMany edges"},
		{name: "Args on in-array", build: edgeCase(inArrayK, fksCfg(), argsCfg(Arg("page", nil))), want: "Args is not valid on in-array edges"},

		{name: "Guard on computed", build: edgeCase(computedK, guardCfg()), want: "Guard is not valid on computed edges"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			fs := compileFindings(t, tc.build)
			if !hasFinding(fs, tc.want) {
				t.Errorf("no finding containing %q; got:%s", tc.want, dumpFindings(fs))
			}
		})
	}
}

type dupRow struct{ ID string }

// dupWire is the LEGAL Base-embed-override idiom: the outer `id` is shallower
// than the embedded one, so encoding/json marshals the outer field and drops
// the inner one. (The duplicate is spread across an embed on purpose — vet's
// structtag check rejects two identical json tags inside one struct literal.)
type dupInner struct {
	A string `json:"id" sortCol:"inner_id"`
	C string `json:"c"`
}

type dupWire struct {
	dupInner
	B *string `json:"id" sortCol:"outer_id"`
}

// tieWire is a same-depth collision that encoding/json RESOLVES: exactly one
// candidate carries a json tag, so the tagged field wins and the struct
// marshals fine. It must compile clean.
type tieA struct {
	Nick string // untagged: encoding/json names it "Nick"
}

type tieB struct {
	Alias *string `json:"Nick"`
}

type tieWire struct {
	tieA
	tieB
	Other string `json:"other"`
}

// genuineTieWire is a same-depth collision that SURVIVES the tie-break: two
// untagged `Nick` fields at depth 1, so encoding/json drops the name from the
// wire altogether. (Untagged on purpose: `go vet`'s structtag check rejects
// two identical json TAGS across same-level embeds, which would break the
// build before the test could run.)
type tieC struct {
	Nick string
}

type tieD struct {
	Nick string
}

type genuineTieWire struct {
	tieC
	tieD
	Other string `json:"other"`
}

// badSortWire carries a sortCol tag on an unexported field (no json tag: vet
// rejects a json tag on an unexported field).
type badSortWire struct {
	ID     string `json:"id"`
	hidden string `sortCol:"hidden"` //nolint:unused // fixture
}

// embSortWire carries a sortCol tag on an unexported EMBEDDED field: its own
// fields are promoted, so the tag can never apply to anything.
type embSortInner struct {
	A string `json:"a"`
}

type embSortWire struct {
	embSortInner `sortCol:"nope"`
	ID           string `json:"id"`
}

// ------------------------------------------------------------------ all problems in one run

func TestCompileAggregatesAllFindings(t *testing.T) {
	fs := compileFindings(t, func(b *Builder) {
		book, author := okBook(b), okAuthor(b)
		// (1) duplicate node name
		Node[tgRow, TgWire](b, "Book").
			Wire(func(r tgRow, c *include.Ctx) TgWire { return TgWire{ID: r.ID} }).
			PrimaryKey(func(r tgRow) string { return r.ID })
		// (2) to-one without ForeignKey
		book.Edge("author", ToOne[AuWire]())
		// (3) reverse without a backref
		author.Edge("books", Reverse[BkWire](""))
	})
	if len(fs) != 3 {
		t.Fatalf("got %d findings, want 3:%s", len(fs), dumpFindings(fs))
	}
	for _, sub := range []string{"duplicate node name", "to-one edge requires ForeignKey", "reverse edge requires a backref"} {
		if !hasFinding(fs, sub) {
			t.Errorf("missing finding %q; got:%s", sub, dumpFindings(fs))
		}
	}
}

func TestCompileErrorMessageJoinsFindings(t *testing.T) {
	b := NewBuilder()
	Node[bkRow, BkWire](b, "Book")
	_, err := b.Compile()
	if err == nil {
		t.Fatal("want error")
	}
	msg := err.Error()
	if !strings.Contains(msg, "Wire is required") || !strings.Contains(msg, "PrimaryKey is required") {
		t.Errorf("Error() = %q, want both findings joined", msg)
	}
}

// ------------------------------------------------------------------ shape derivation

type shapeRow struct{ ID string }

type shapeWire struct {
	ID        string  `json:"id" sortCol:"id"`
	Title     string  `json:"title"`
	Note      *string `json:"note"`
	Hidden    string  `json:"-"`
	Untagged  string
	CreatedAt string `json:"createdAt,omitempty" sortCol:"created_at"`
}

func TestCompileDerivesShapeFromWireTags(t *testing.T) {
	b := NewBuilder()
	Node[shapeRow, shapeWire](b, "Shape").
		Wire(func(r shapeRow, c *include.Ctx) shapeWire { return shapeWire{ID: r.ID} }).
		PrimaryKey(func(r shapeRow) string { return r.ID })
	g, err := b.Compile()
	if err != nil {
		t.Fatalf("Compile: %v", err)
	}
	res := g.Resource("Shape")

	wantFields := []string{"id", "title", "note", "Untagged", "createdAt"}
	if got := res.Fields(); !reflect.DeepEqual(got, wantFields) {
		t.Errorf("Fields() = %v, want %v", got, wantFields)
	}

	// Verdicts must be exactly what the ONE policy says (delegation, not a
	// re-derivation): compare against direct apidoc.DefaultNullability calls.
	wt := reflect.TypeFor[shapeWire]()
	want := map[string]apidoc.Verdict{}
	for jsonName, fieldName := range map[string]string{
		"id": "ID", "title": "Title", "note": "Note", "Untagged": "Untagged", "createdAt": "CreatedAt",
	} {
		f, ok := wt.FieldByName(fieldName)
		if !ok {
			t.Fatalf("fixture field %s missing", fieldName)
		}
		want[jsonName] = apidoc.DefaultNullability(f)
	}
	vp, ok := res.(interface {
		FieldVerdicts() map[string]apidoc.Verdict
	})
	if !ok {
		t.Fatal("compiled node does not expose FieldVerdicts()")
	}
	if got := vp.FieldVerdicts(); !reflect.DeepEqual(got, want) {
		t.Errorf("FieldVerdicts() = %v, want %v", got, want)
	}
	// sanity: the policy really did classify the three shapes differently
	if want["id"] != apidoc.VerdictPlain || want["note"] != apidoc.VerdictNull || want["createdAt"] != apidoc.VerdictOptional {
		t.Errorf("fixture no longer exercises all three verdicts: %v", want)
	}
}

type embInner struct {
	A string `json:"a" sortCol:"a_col"`
	B string `json:"b"`
}

type embTagged struct {
	C string `json:"c"`
}

type embWire struct {
	ID string `json:"id"`
	embInner
	Tagged embTagged `json:"tagged"`
	Nested embTagged `json:"nested"`
}

// The Base-embed-override idiom is LEGAL: the outer `id` is shallower, wins,
// and carries its own verdict and sortCol. Nothing is flagged.
func TestCompileDepthDominanceOverEmbeddedField(t *testing.T) {
	b := NewBuilder()
	Node[dupRow, dupWire](b, "Dup").
		Wire(func(r dupRow, c *include.Ctx) dupWire { return dupWire{} }).
		PrimaryKey(func(r dupRow) string { return r.ID })
	g, err := b.Compile()
	if err != nil {
		t.Fatalf("Compile: %v (the shallowest field must win silently)", err)
	}
	res := g.Resource("Dup")
	if got := res.Fields(); !reflect.DeepEqual(got, []string{"c", "id"}) {
		t.Errorf("Fields() = %v, want [c id] (one winning \"id\")", got)
	}
	// the WINNER is the outer *string field: nullable, sortCol outer_id
	verdicts := res.(interface {
		FieldVerdicts() map[string]apidoc.Verdict
	}).FieldVerdicts()
	if verdicts["id"] != apidoc.VerdictNull {
		t.Errorf("verdict[id] = %v, want VerdictNull (the outer *string wins)", verdicts["id"])
	}
	b2 := NewBuilder()
	book := okBook(b2)
	Node[dupRow, dupWire](b2, "Dup").
		Wire(func(r dupRow, c *include.Ctx) dupWire { return dupWire{} }).
		PrimaryKey(func(r dupRow) string { return r.ID })
	book.Edge("dups", Reverse[dupWire]("bookId")).Sort("id")
	g2, err := b2.Compile()
	if err != nil {
		t.Fatalf("Compile: %v", err)
	}
	want := map[string]string{"id": "outer_id"}
	if got := g2.Resource("Book").Edges()["dups"].SortCols; !reflect.DeepEqual(got, want) {
		t.Errorf("SortCols = %v, want %v (the outer field's sortCol wins)", got, want)
	}
}

// encoding/json's tie-break: at equal depth a SINGLE json-tagged candidate
// still wins, so the struct marshals fine and nothing is flagged.
func TestCompileSameDepthTagWinsTieBreak(t *testing.T) {
	b := NewBuilder()
	Node[dupRow, tieWire](b, "Tie").
		Wire(func(r dupRow, c *include.Ctx) tieWire { return tieWire{} }).
		PrimaryKey(func(r dupRow) string { return r.ID })
	g, err := b.Compile()
	if err != nil {
		t.Fatalf("Compile: %v (the single tagged candidate must win silently)", err)
	}
	res := g.Resource("Tie")
	if got := res.Fields(); !reflect.DeepEqual(got, []string{"Nick", "other"}) {
		t.Errorf("Fields() = %v, want [Nick other]", got)
	}
	// the winner is tieB.Alias (*string, tagged) — not tieA.Nick (string)
	verdicts := res.(interface {
		FieldVerdicts() map[string]apidoc.Verdict
	}).FieldVerdicts()
	if verdicts["Nick"] != apidoc.VerdictNull {
		t.Errorf("verdict[Nick] = %v, want VerdictNull (the tagged *string wins)", verdicts["Nick"])
	}
}

func TestCompileFlattensEmbeddedStructs(t *testing.T) {
	b := NewBuilder()
	Node[shapeRow, embWire](b, "Emb").
		Wire(func(r shapeRow, c *include.Ctx) embWire { return embWire{} }).
		PrimaryKey(func(r shapeRow) string { return r.ID })
	g, err := b.Compile()
	if err != nil {
		t.Fatalf("Compile: %v", err)
	}
	want := []string{"id", "a", "b", "tagged", "nested"}
	if got := g.Resource("Emb").Fields(); !reflect.DeepEqual(got, want) {
		t.Errorf("Fields() = %v, want %v", got, want)
	}
}

func TestCompileDerivesSortCols(t *testing.T) {
	b := NewBuilder()
	book := okBook(b)
	okReview(b)
	book.Edge("reviews", Reverse[RvWire]("bookId")).Sort("-createdAt")
	g, err := b.Compile()
	if err != nil {
		t.Fatalf("Compile: %v", err)
	}
	e := g.Resource("Book").Edges()["reviews"]
	want := map[string]string{"id": "id", "createdAt": "created_at"}
	if !reflect.DeepEqual(e.SortCols, want) {
		t.Errorf("SortCols = %v, want %v", e.SortCols, want)
	}
	if e.Sort != "-createdAt" {
		t.Errorf("Sort = %q", e.Sort)
	}
}

// sortEdgeRow/sortEdgeWire carry the whitelist edge cases: an untagged field
// and a `json:"-"` field are NOT sortable even when they carry a sortCol tag,
// and a comma-suffixed json tag keys the whitelist on the name BEFORE the
// comma.
type sortEdgeRow struct{ ID string }

type sortEdgeWire struct {
	ID        string  `json:"id"`
	StartDate *string `json:"startDate" sortCol:"start_date"`
	Name      *string `json:"name"`
	Hidden    string  `json:"-" sortCol:"hidden_col"`
	Untagged  string  `sortCol:"untagged_col"`
	CreatedAt *string `json:"createdAt,omitempty" sortCol:"created_at"`
}

func TestCompileSortColsSkipsUnsortableFields(t *testing.T) {
	b := NewBuilder()
	book := okBook(b)
	Node[sortEdgeRow, sortEdgeWire](b, "SortEdge").
		Wire(func(r sortEdgeRow, c *include.Ctx) sortEdgeWire { return sortEdgeWire{ID: r.ID} }).
		PrimaryKey(func(r sortEdgeRow) string { return r.ID })
	book.Edge("sorted", Reverse[sortEdgeWire]("bookId")).Sort("startDate")

	g, err := b.Compile()
	if err != nil {
		t.Fatalf("Compile: %v", err)
	}
	want := map[string]string{"startDate": "start_date", "createdAt": "created_at"}
	if got := g.Resource("Book").Edges()["sorted"].SortCols; !reflect.DeepEqual(got, want) {
		t.Errorf("SortCols = %v, want %v (json:\"-\" and untagged fields are not sortable)", got, want)
	}
}

// A wire with no sortCol tag at all yields a NIL whitelist, which makes a
// client `:sort()` an undeclared argument (deny-by-default).
func TestCompileNoSortColTagsYieldsNilWhitelist(t *testing.T) {
	b := NewBuilder()
	book, _ := okBook(b), okTag(b)
	book.Edge("tagsRev", Reverse[TgWire]("bookId"))

	g, err := b.Compile()
	if err != nil {
		t.Fatalf("Compile: %v", err)
	}
	if got := g.Resource("Book").Edges()["tagsRev"].SortCols; got != nil {
		t.Errorf("SortCols = %v, want nil (nothing tagged → `:sort()` inert)", got)
	}
}

// TestDeclaredSortArgStaysLegal is the positive control for the "declared edge
// arg named limit" finding: only "limit" is engine-owned. A declared ":sort"
// override is a supported feature and must compile clean.
func TestDeclaredSortArgStaysLegal(t *testing.T) {
	b := NewBuilder()
	book, review := okBook(b), okReview(b)
	book.Edge("reviews", Reverse[RvWire]("bookId")).
		Args(Arg("sort", func(raw any) error { return nil }))
	FetchParents(b, review, PerParent(func(c *include.Ctx, pid string, q include.EdgeQuery) ([]rvRow, bool, error) {
		return nil, false, nil
	}))
	b.Root(book)

	if _, err := b.Compile(); err != nil {
		t.Fatalf("declared :sort arg must compile clean, got: %v", err)
	}
}

// ------------------------------------------------------------------ happy path

// happyGraph builds Book/Author/Tag/Review + a computed edge, all binds, Book
// as root.
func happyGraph(t *testing.T) *Graph {
	t.Helper()
	b := NewBuilder()

	book := Node[bkRow, BkWire](b, "Book").
		Slug("book").
		Wire(func(r bkRow, c *include.Ctx) BkWire { return BkWire{ID: r.ID, Title: r.Title} }).
		PrimaryKey(func(r bkRow) string { return r.ID }).
		Enrich(func(rows []bkRow, c *include.Ctx) error { return nil }).
		Defaults("author")
	author := okAuthor(b)
	tag := okTag(b)
	review := okReview(b)

	book.Edge("author", ToOne[AuWire]()).
		ForeignKey(func(r bkRow) string { return r.AuthorID }).
		Inverse("books").
		Required().
		Includable()
	// Guard lives on a NON-required edge: Required()+Guard() is a compile
	// error (a guard-false parent would null a required key).
	book.Edge("tags", ToMany[TgWire]()).
		ForeignKeys(func(r bkRow) []string { return r.TagIDs }).
		Includable().
		Guard(func(c *include.Ctx, r bkRow) bool { return true })
	book.Edge("reviews", Reverse[RvWire]("bookId")).
		Includable().Limit(10).Sort("-createdAt").
		Args(Arg("page", func(raw any) error { return nil }))
	book.Edge("participants", Computed(apidoc.RawFragment(map[string]any{"type": "array"}))).Includable()
	author.Edge("books", Reverse[BkWire]("authorId")).Inverse("author").Includable()

	FetchIDs(b, author, func(c *include.Ctx, ids []string) ([]auRow, error) { return nil, nil })
	FetchIDs(b, tag, func(c *include.Ctx, ids []string) ([]tgRow, error) { return nil, nil })
	FetchParents(b, review, PerParent(func(c *include.Ctx, pid string, q include.EdgeQuery) ([]rvRow, bool, error) {
		return nil, false, nil
	}))
	FetchParents(b, book, PerParent(func(c *include.Ctx, pid string, q include.EdgeQuery) ([]bkRow, bool, error) {
		return nil, false, nil
	}))
	b.Root(book)

	g, err := b.Compile()
	if err != nil {
		t.Fatalf("Compile: %v", err)
	}
	return g
}

func TestCompileHappyPath(t *testing.T) {
	g := happyGraph(t)

	res := g.Resource("Book")
	if res.Name() != "Book" || res.Slug() != "book" {
		t.Errorf("Name/Slug = %q/%q", res.Name(), res.Slug())
	}
	if got := g.Resource("Author").Slug(); got != "author" {
		t.Errorf("default slug = %q, want %q", got, "author")
	}
	if !reflect.DeepEqual(res.Defaults(), []string{"author"}) {
		t.Errorf("Defaults() = %v", res.Defaults())
	}
	if !reflect.DeepEqual(res.Fields(), []string{"id", "title"}) {
		t.Errorf("Fields() = %v", res.Fields())
	}

	edges := res.Edges()
	if len(edges) != 4 {
		t.Fatalf("Edges() = %d entries, want 4", len(edges))
	}

	// to-one
	a := edges["author"]
	if a.Target == nil || a.Target().Name() != "Author" {
		t.Errorf("author.Target wrong")
	}
	if a.Many || !a.Includable || a.ForeignKey == nil {
		t.Errorf("author edge = %+v", a)
	}
	// Required() reaches the engine-facing edge (doc-layer: bare $ref + required).
	if !a.Required {
		t.Errorf("author.Required = false, want true (declared with Required())")
	}
	if include.EdgeKind(a) != include.KindToOne {
		t.Errorf("author kind = %v", include.EdgeKind(a))
	}
	if got := a.ForeignKey(bkRow{AuthorID: "a1"}); got != "a1" {
		t.Errorf("ForeignKey boxed wrong: %q", got)
	}

	// forward-hasMany: default limit 20
	tags := edges["tags"]
	if !tags.Many || tags.ForeignKeys == nil || tags.Limit != 20 || tags.Guard == nil {
		t.Errorf("tags edge = %+v (want Many, ForeignKeys, Limit 20, boxed Guard)", tags)
	}
	if include.EdgeKind(tags) != include.KindForwardHasMany {
		t.Errorf("tags kind = %v", include.EdgeKind(tags))
	}

	// reverse: backref, explicit limit, sort cols, args
	rev := edges["reviews"]
	if rev.Backref != "bookId" || rev.Limit != 10 || rev.Sort != "-createdAt" {
		t.Errorf("reviews edge = %+v", rev)
	}
	if len(rev.Args) != 1 || rev.Args[0].Name != "page" {
		t.Errorf("reviews args = %+v", rev.Args)
	}
	if include.EdgeKind(rev) != include.KindReverse {
		t.Errorf("reviews kind = %v", include.EdgeKind(rev))
	}

	// computed
	c := edges["participants"]
	if !c.Computed || c.Target != nil {
		t.Errorf("participants edge = %+v", c)
	}
	if _, ok := c.ComputedSchema.(apidoc.Schema); !ok {
		t.Errorf("ComputedSchema = %T, want apidoc.Schema", c.ComputedSchema)
	}
	if include.EdgeKind(c) != include.KindComputed {
		t.Errorf("participants kind = %v", include.EdgeKind(c))
	}

	// duck-typed doc seams
	if _, ok := res.(interface{ WireSample() any }); !ok {
		t.Error("compiled node lacks WireSample()")
	}
	if s := res.(interface{ WireSample() any }).WireSample(); reflect.TypeOf(s) != reflect.TypeFor[BkWire]() {
		t.Errorf("WireSample type = %T", s)
	}
	if res.(interface{ IsDocExternal() bool }).IsDocExternal() {
		t.Error("IsDocExternal should be false")
	}

	// boxed closures
	if got := res.IDOf(bkRow{ID: "b1"}); got != "b1" {
		t.Errorf("IDOf = %q", got)
	}
	if got := res.Serialize(bkRow{ID: "b1", Title: "T"}, nil); !reflect.DeepEqual(got, BkWire{ID: "b1", Title: "T"}) {
		t.Errorf("Serialize = %#v", got)
	}
	if err := res.Enrich([]any{bkRow{ID: "b1"}}, nil); err != nil {
		t.Errorf("Enrich: %v", err)
	}
	if err := g.Resource("Tag").Enrich([]any{tgRow{ID: "t"}}, nil); err != nil {
		t.Errorf("no-op Enrich: %v", err)
	}
}

// In-array edges load through the forward FetchIDs batch and EdgeQuery does
// not apply to them, so they get NO default ceiling.
func TestCompileInArrayEdgeHasNoDefaultLimit(t *testing.T) {
	b := NewBuilder()
	book, tag := okBook(b), okTag(b)
	book.Edge("participants", InArray[TgWire]("people", "user")).ForeignKeys(func(r bkRow) []string { return r.TagIDs }).Includable()
	FetchIDs(b, tag, func(c *include.Ctx, ids []string) ([]tgRow, error) { return nil, nil })
	b.Root(book)

	g, err := b.Compile()
	if err != nil {
		t.Fatalf("Compile: %v", err)
	}
	e := g.Resource("Book").Edges()["participants"]
	if e.Limit != 0 {
		t.Errorf("in-array Limit = %d, want 0 (no inert ceiling)", e.Limit)
	}
	if !e.Many || e.ArrayPath != "people" || e.SubField != "user" {
		t.Errorf("in-array edge = %+v", e)
	}
	if include.EdgeKind(e) != include.KindInArray {
		t.Errorf("kind = %v", include.EdgeKind(e))
	}
}

func TestCompileDocExternal(t *testing.T) {
	b := NewBuilder()
	okBook(b).DocExternal()
	g, err := b.Compile()
	if err != nil {
		t.Fatalf("Compile: %v", err)
	}
	if !g.Resource("Book").(interface{ IsDocExternal() bool }).IsDocExternal() {
		t.Error("IsDocExternal() = false, want true")
	}
}

// ------------------------------------------------------------------ Registry

func TestGraphImplementsRegistry(t *testing.T) {
	g := happyGraph(t)
	var reg include.Registry = g

	if _, ok := reg.FetchByIDs(g.Resource("Author")); !ok {
		t.Error("FetchByIDs(Author) not found")
	}
	if _, ok := reg.FetchByIDs(g.Resource("Book")); ok {
		t.Error("FetchByIDs(Book) found, but Book has no forward bind")
	}
	if _, ok := reg.FetchByParents(g.Resource("Review")); !ok {
		t.Error("FetchByParents(Review) not found")
	}
	if _, ok := reg.FetchByParents(g.Resource("Author")); ok {
		t.Error("FetchByParents(Author) found, but Author has no reverse bind")
	}
}

// ------------------------------------------------------------------ second Compile panics

func TestSecondCompilePanics(t *testing.T) {
	b := NewBuilder()
	okBook(b)
	if _, err := b.Compile(); err != nil {
		t.Fatalf("first Compile: %v", err)
	}
	defer func() {
		r := recover()
		if s, _ := r.(string); s != "graph: builder is dead after Compile" {
			t.Errorf("panic = %v, want the dead-builder message", r)
		}
	}()
	_, _ = b.Compile()
	t.Error("second Compile did not panic")
}

// ------------------------------------------------------------------ typed→boxed seams
//
// These pin the boxing seams: builder.go (Wire/Enrich), graph.go
// (compiledNode.Serialize/Enrich + the Registry lookups) and fetch.go
// (FetchIDs/FetchParents).

type boxRow struct{ ID string }

type BoxWire struct {
	ID     string `json:"id"`
	Locale string `json:"locale"`
}

// PlainWire keeps the no-Enrich node's wire type distinct (Compile rejects two
// nodes sharing one Wire type).
type PlainWire struct {
	ID string `json:"id"`
}

// boxEnv stands in for the application's per-request state, which typed
// closures read off Ctx.Env.
type boxEnv struct{ locale string }

// TestCompiledNodeSerializeThreadsCtx: Serialize hands the caller's very *Ctx
// to the Serialize closure, so per-request Env reaches typed code.
func TestCompiledNodeSerializeThreadsCtx(t *testing.T) {
	var seenCtx *include.Ctx
	b := NewBuilder()
	Node[boxRow, BoxWire](b, "Box").
		Wire(func(r boxRow, c *include.Ctx) BoxWire {
			seenCtx = c
			return BoxWire{ID: r.ID, Locale: c.Env.(boxEnv).locale}
		}).
		PrimaryKey(func(r boxRow) string { return r.ID })
	g, err := b.Compile()
	if err != nil {
		t.Fatalf("Compile: %v", err)
	}

	ctx := &include.Ctx{Env: boxEnv{locale: "de"}}
	got := g.Resource("Box").Serialize(boxRow{ID: "1"}, ctx).(BoxWire)
	if got != (BoxWire{ID: "1", Locale: "de"}) {
		t.Fatalf("Serialize = %+v, want {1 de}", got)
	}
	if seenCtx != ctx {
		t.Fatal("Serialize did not receive the very Ctx passed to Serialize")
	}
	if id := g.Resource("Box").IDOf(boxRow{ID: "1"}); id != "1" {
		t.Fatalf("IDOf = %q, want 1", id)
	}
}

// TestCompiledNodeEnrichBoxing: Enrich unboxes []any→[]Row (contents and order)
// and threads the caller's *Ctx identity into the Enrich closure.
func TestCompiledNodeEnrichBoxing(t *testing.T) {
	var seen []boxRow
	var seenCtx *include.Ctx
	b := NewBuilder()
	Node[boxRow, BoxWire](b, "Box").
		Wire(func(r boxRow, _ *include.Ctx) BoxWire { return BoxWire{ID: r.ID} }).
		PrimaryKey(func(r boxRow) string { return r.ID }).
		Enrich(func(rows []boxRow, c *include.Ctx) error {
			seen, seenCtx = rows, c
			return nil
		})
	g, err := b.Compile()
	if err != nil {
		t.Fatalf("Compile: %v", err)
	}

	ctx := &include.Ctx{Env: boxEnv{locale: "en"}}
	if err := g.Resource("Box").Enrich([]any{boxRow{ID: "a"}, boxRow{ID: "b"}}, ctx); err != nil {
		t.Fatalf("Enrich: %v", err)
	}
	if len(seen) != 2 || seen[0].ID != "a" || seen[1].ID != "b" {
		t.Fatalf("Enrich saw %+v, want [{a} {b}]", seen)
	}
	if seenCtx != ctx {
		t.Fatal("Enrich did not receive the Ctx passed to Enrich")
	}
}

// TestCompiledNodeEnrichErrorPassthrough: an Enrich error reaches the
// engine unwrapped. A node without Enrich is a safe no-op.
func TestCompiledNodeEnrichErrorPassthrough(t *testing.T) {
	sentinel := errors.New("enrich failed")
	b := NewBuilder()
	Node[boxRow, BoxWire](b, "Box").
		Wire(func(r boxRow, _ *include.Ctx) BoxWire { return BoxWire{ID: r.ID} }).
		PrimaryKey(func(r boxRow) string { return r.ID }).
		Enrich(func([]boxRow, *include.Ctx) error { return sentinel })
	Node[boxRow, PlainWire](b, "Plain"). // no Enrich at all
						Wire(func(r boxRow, _ *include.Ctx) PlainWire { return PlainWire{ID: r.ID} }).
						PrimaryKey(func(r boxRow) string { return r.ID })
	g, err := b.Compile()
	if err != nil {
		t.Fatalf("Compile: %v", err)
	}

	if err := g.Resource("Box").Enrich([]any{boxRow{ID: "a"}}, nil); !errors.Is(err, sentinel) {
		t.Fatalf("Enrich err = %v, want the sentinel", err)
	}
	if err := g.Resource("Plain").Enrich([]any{boxRow{ID: "a"}}, nil); err != nil {
		t.Fatalf("a node without Enrich must be a no-op, got %v", err)
	}
}

// TestFetchIDsBoxesNilAsNil pins boxSlice's nil contract at the FetchIDs seam:
// a fetcher returning a nil slice must NOT become an empty non-nil one — the
// engine's "no rows" checks distinguish them. The same holds for a parent's
// nil Rows on the reverse seam.
func TestFetchIDsBoxesNilAsNil(t *testing.T) {
	b := NewBuilder()
	node := Node[boxRow, BoxWire](b, "Box").
		Wire(func(r boxRow, _ *include.Ctx) BoxWire { return BoxWire{ID: r.ID} }).
		PrimaryKey(func(r boxRow) string { return r.ID })
	FetchIDs(b, node, func(*include.Ctx, []string) ([]boxRow, error) { return nil, nil })
	FetchParents(b, node, func(_ *include.Ctx, parentIDs []string, _ include.EdgeQuery) (map[string]ParentRows[boxRow], error) {
		return map[string]ParentRows[boxRow]{parentIDs[0]: {}}, nil
	})
	g, err := b.Compile()
	if err != nil {
		t.Fatalf("Compile: %v", err)
	}
	res := g.Resource("Box")

	fi, ok := g.FetchByIDs(res)
	if !ok {
		t.Fatal("FetchByIDs not registered")
	}
	rows, err := fi(&include.Ctx{}, []string{"x"})
	if err != nil {
		t.Fatalf("FetchByIDs err: %v", err)
	}
	if rows != nil {
		t.Fatalf("boxed nil into %#v, want nil", rows)
	}

	fp, ok := g.FetchByParents(res)
	if !ok {
		t.Fatal("FetchByParents not registered")
	}
	out, err := fp(&include.Ctx{}, []string{"p"}, include.EdgeQuery{Limit: 1})
	if err != nil {
		t.Fatalf("FetchByParents err: %v", err)
	}
	if kids := out["p"]; kids.Rows != nil || kids.HasMore {
		t.Fatalf("parent rows = %#v, want a zero ParentRows with nil Rows", kids)
	}
}

// TestFetchBoxErrorShortCircuits: a fetcher error propagates unwrapped out of
// BOTH box wrappers, and nothing is boxed alongside it.
func TestFetchBoxErrorShortCircuits(t *testing.T) {
	sentinel := errors.New("boom")
	b := NewBuilder()
	node := Node[boxRow, BoxWire](b, "Box").
		Wire(func(r boxRow, _ *include.Ctx) BoxWire { return BoxWire{ID: r.ID} }).
		PrimaryKey(func(r boxRow) string { return r.ID })
	// Both fetchers return rows AND an error: the wrappers must drop the rows.
	FetchIDs(b, node, func(*include.Ctx, []string) ([]boxRow, error) {
		return []boxRow{{ID: "partial"}}, sentinel
	})
	FetchParents(b, node, func(_ *include.Ctx, parentIDs []string, _ include.EdgeQuery) (map[string]ParentRows[boxRow], error) {
		return map[string]ParentRows[boxRow]{"p": {Rows: []boxRow{{ID: "partial"}}}}, sentinel
	})
	g, err := b.Compile()
	if err != nil {
		t.Fatalf("Compile: %v", err)
	}
	res := g.Resource("Box")

	fi, _ := g.FetchByIDs(res)
	rows, err := fi(&include.Ctx{}, []string{"x"})
	if !errors.Is(err, sentinel) {
		t.Fatalf("FetchByIDs err = %v, want the sentinel", err)
	}
	if rows != nil {
		t.Fatalf("FetchByIDs boxed %#v alongside an error, want nil", rows)
	}

	fp, _ := g.FetchByParents(res)
	out, err := fp(&include.Ctx{}, []string{"p"}, include.EdgeQuery{Limit: 1})
	if !errors.Is(err, sentinel) {
		t.Fatalf("FetchByParents err = %v, want the sentinel", err)
	}
	if out != nil {
		t.Fatalf("FetchByParents boxed %#v alongside an error, want nil", out)
	}
}

// TestFetchParentsQueryAndEnvPassthrough: the engine's EdgeQuery (Limit, Sort,
// the non-built-in Args) reaches the TYPED reverse fetcher verbatim, Ctx.Env is
// readable inside it, and each parent's rows/HasMore/NextCursor are boxed per
// parent.
func TestFetchParentsQueryAndEnvPassthrough(t *testing.T) {
	var gotQ include.EdgeQuery
	var gotEnv any
	b := NewBuilder()
	node := Node[boxRow, BoxWire](b, "Box").
		Wire(func(r boxRow, _ *include.Ctx) BoxWire { return BoxWire{ID: r.ID} }).
		PrimaryKey(func(r boxRow) string { return r.ID })
	FetchParents(b, node, func(c *include.Ctx, parentIDs []string, q include.EdgeQuery) (map[string]ParentRows[boxRow], error) {
		gotQ, gotEnv = q, c.Env
		out := make(map[string]ParentRows[boxRow], len(parentIDs))
		for _, pid := range parentIDs {
			out[pid] = ParentRows[boxRow]{
				Rows:       []boxRow{{ID: pid + "-child"}},
				HasMore:    q.Limit > 0 && q.Limit < 5,
				NextCursor: "c-" + pid,
			}
		}
		return out, nil
	})
	g, err := b.Compile()
	if err != nil {
		t.Fatalf("Compile: %v", err)
	}

	fp, ok := g.FetchByParents(g.Resource("Box"))
	if !ok {
		t.Fatal("FetchByParents not registered")
	}
	q := include.EdgeQuery{Limit: 3, Sort: "-created_at", Args: map[string]any{"status": "published"}}
	out, err := fp(&include.Ctx{Env: boxEnv{locale: "en"}}, []string{"P1"}, q)
	if err != nil {
		t.Fatalf("FetchByParents err: %v", err)
	}
	if !reflect.DeepEqual(gotQ, q) {
		t.Errorf("typed fetcher saw EdgeQuery %+v, want %+v", gotQ, q)
	}
	if env, ok := gotEnv.(boxEnv); !ok || env.locale != "en" {
		t.Errorf("Ctx.Env inside the fetcher = %#v, want boxEnv{en}", gotEnv)
	}
	pr, ok := out["P1"]
	if !ok {
		t.Fatalf("no entry for P1: %v", out)
	}
	if !pr.HasMore {
		t.Error("HasMore should be true (limit 3 < 5)")
	}
	if pr.NextCursor != "c-P1" {
		t.Errorf("NextCursor = %q, want %q", pr.NextCursor, "c-P1")
	}
	if len(pr.Rows) != 1 {
		t.Fatalf("got %d rows, want 1", len(pr.Rows))
	}
	if r0, ok := pr.Rows[0].(boxRow); !ok || r0.ID != "P1-child" {
		t.Fatalf("row 0 = %#v, want a boxed boxRow{P1-child}", pr.Rows[0])
	}
}

// A node with no bind reports absent on both lookups (the engine's "missing
// fetcher" path).
func TestUnboundNodeLookupsAbsent(t *testing.T) {
	b := NewBuilder()
	Node[boxRow, BoxWire](b, "Box").
		Wire(func(r boxRow, _ *include.Ctx) BoxWire { return BoxWire{ID: r.ID} }).
		PrimaryKey(func(r boxRow) string { return r.ID })
	g, err := b.Compile()
	if err != nil {
		t.Fatalf("Compile: %v", err)
	}
	res := g.Resource("Box")
	if f, ok := g.FetchByIDs(res); ok || f != nil {
		t.Error("FetchByIDs should be absent for an unbound node")
	}
	if f, ok := g.FetchByParents(res); ok || f != nil {
		t.Error("FetchByParents should be absent for an unbound node")
	}
}

// Required()+Guard() on one edge guarantees responses that violate the
// published component (guard-false → null under a required key), so Compile
// rejects the pair.
func TestCompileRejectsRequiredPlusGuard(t *testing.T) {
	fs := compileFindings(t, func(b *Builder) {
		book := okBook(b).Defaults("author")
		author := okAuthor(b)
		book.Edge("author", ToOne[AuWire]()).
			ForeignKey(func(r bkRow) string { return r.AuthorID }).
			Required().
			Includable().
			Guard(func(c *include.Ctx, r bkRow) bool { return true })
		FetchIDs(b, author, func(c *include.Ctx, ids []string) ([]auRow, error) { return nil, nil })
		b.Root(book)
	})
	if !hasFinding(fs, "Required() edge cannot carry Guard()") {
		t.Errorf("missing Required+Guard finding:%s", dumpFindings(fs))
	}
}

// A NON-includable default edge still materializes (the engine merges
// Defaults() ∪ client keys regardless of Includable), so its target's fetcher
// bind is checked too.
func TestCompileChecksFetchersThroughDefaultEdges(t *testing.T) {
	fs := compileFindings(t, func(b *Builder) {
		book := okBook(b).Defaults("author")
		okAuthor(b)
		book.Edge("author", ToOne[AuWire]()).ForeignKey(func(r bkRow) string { return r.AuthorID }) // default, NOT includable
		b.Root(book)
		// no FetchIDs bind for author
	})
	if !hasFinding(fs, "missing FetchIDs bind") {
		t.Errorf("missing FetchIDs finding for a non-includable default edge:%s", dumpFindings(fs))
	}
}

// SortCols reaches the engine-facing edge on REVERSE edges only: it feeds
// EdgeQuery.Sort, which no other kind's loading contract can honour.
func TestCompileSortColsOnlyOnReverse(t *testing.T) {
	b := NewBuilder()
	book := okBook(b)
	author := okAuthor(b)
	review := okReview(b) // RvWire carries sortCol tags
	book.Edge("author", ToOne[AuWire]()).ForeignKey(func(r bkRow) string { return r.AuthorID }).Includable()
	book.Edge("reviews", Reverse[RvWire]("bookId")).Includable()
	FetchIDs(b, author, func(c *include.Ctx, ids []string) ([]auRow, error) { return nil, nil })
	FetchParents(b, review, PerParent(func(c *include.Ctx, pid string, q include.EdgeQuery) ([]rvRow, bool, error) {
		return nil, false, nil
	}))
	b.Root(book)
	g, err := b.Compile()
	if err != nil {
		t.Fatalf("Compile: %v", err)
	}
	edges := g.Resource("Book").Edges()
	if cols := edges["author"].SortCols; cols != nil {
		t.Errorf("to-one edge carries SortCols %v, want nil", cols)
	}
	if cols := edges["reviews"].SortCols; cols["createdAt"] != "created_at" {
		t.Errorf("reverse edge SortCols = %v, want the target's sortCol whitelist", cols)
	}
}

// The two policy overrides are meaningful on Required() edges only; declaring
// either elsewhere is an inert declaration and therefore a finding.
func TestCompileRejectsPolicyOverridesOnNonRequiredEdges(t *testing.T) {
	cases := []struct {
		name string
		pol  include.EdgePolicy
		want string
	}{
		{"ExcludeRequired", include.ExcludeRequiredStrict, "Policies: ExcludeRequiredPolicy is only valid on Required() edges"},
		{"MissingRequired", include.MissingRequiredError, "Policies: MissingRequiredPolicy is only valid on Required() edges"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			fs := compileFindings(t, func(b *Builder) {
				book := okBook(b)
				author := okAuthor(b)
				book.Edge("author", ToOne[AuWire]()).
					ForeignKey(func(r bkRow) string { return r.AuthorID }).
					Includable().
					Policies(tc.pol)
				FetchIDs(b, author, func(c *include.Ctx, ids []string) ([]auRow, error) { return nil, nil })
				b.Root(book)
			})
			if !hasFinding(fs, tc.want) {
				t.Errorf("missing finding %q:%s", tc.want, dumpFindings(fs))
			}
		})
	}
}

// On a Required() edge both overrides compile and reach the engine-facing edge.
func TestCompileCarriesPolicyOverrides(t *testing.T) {
	b := NewBuilder()
	book := okBook(b).Defaults("author")
	author := okAuthor(b)
	book.Edge("author", ToOne[AuWire]()).
		ForeignKey(func(r bkRow) string { return r.AuthorID }).
		Required().
		Includable().
		Policies(include.ExcludeRequiredStrict, include.MissingRequiredError)
	FetchIDs(b, author, func(c *include.Ctx, ids []string) ([]auRow, error) { return nil, nil })
	b.Root(book)
	g, err := b.Compile()
	if err != nil {
		t.Fatalf("Compile: %v", err)
	}
	e := g.Resource("Book").Edges()["author"]
	if e.ExcludeRequired == nil || *e.ExcludeRequired != include.ExcludeRequiredStrict {
		t.Errorf("ExcludeRequiredPolicy = %v, want ExcludeRequiredStrict", e.ExcludeRequired)
	}
	if e.MissingRequired == nil || *e.MissingRequired != include.MissingRequiredError {
		t.Errorf("RequiredMissing = %v, want MissingRequiredError", e.MissingRequired)
	}
}

// MissingForeign is scoped by KIND, not by Required(): legal wherever the
// edge reads a parent-side FK, rejected on reverse and computed edges.
func TestCompileScopesOnMissingForeignByKind(t *testing.T) {
	polCfg := func(e *EdgeBuilder[bkRow]) *EdgeBuilder[bkRow] {
		return e.Policies(include.MissingForeignError)
	}

	for _, kind := range []struct {
		name string
		k    func() EdgeKind
		with bkCfg
	}{
		{"to-one", toOneK, fkCfg()},
		{"forward-hasMany", toManyK, fksCfg()},
		{"in-array", inArrayK, fksCfg()},
	} {
		t.Run("legal on "+kind.name, func(t *testing.T) {
			b := NewBuilder()
			book, _ := okBook(b), okTag(b)
			polCfg(kind.with(book.Edge("e", kind.k())))
			b.Root(book)
			if _, err := b.Compile(); err != nil {
				t.Fatalf("MissingForeign on a %s edge must compile: %v", kind.name, err)
			}
		})
	}

	for _, kind := range []struct {
		name string
		k    func() EdgeKind
	}{
		{"reverse", reverseK},
		{"computed", computedK},
	} {
		t.Run("rejected on "+kind.name, func(t *testing.T) {
			fs := compileFindings(t, edgeCase(kind.k, polCfg))
			if !hasFinding(fs, "Policies: MissingForeignPolicy is not valid on") {
				t.Errorf("missing kind finding:%s", dumpFindings(fs))
			}
		})
	}
}

// The override reaches the engine-facing edge.
func TestCompileCarriesMissingForeign(t *testing.T) {
	b := NewBuilder()
	book, author := okBook(b), okAuthor(b)
	book.Edge("author", ToOne[AuWire]()).
		ForeignKey(func(r bkRow) string { return r.AuthorID }).
		Includable().
		Policies(include.MissingForeignError)
	FetchIDs(b, author, func(c *include.Ctx, ids []string) ([]auRow, error) { return nil, nil })
	b.Root(book)
	g, err := b.Compile()
	if err != nil {
		t.Fatalf("Compile: %v", err)
	}
	e := g.Resource("Book").Edges()["author"]
	if e.MissingForeign == nil || *e.MissingForeign != include.MissingForeignError {
		t.Errorf("MissingForeign = %v, want MissingForeignError", e.MissingForeign)
	}
}

// Policies is one option carrying several values: unnamed policies inherit,
// a repeated one is last-wins, and two Policies(...) calls MERGE.
func TestPoliciesFoldsAndMerges(t *testing.T) {
	b := NewBuilder()
	book := okBook(b).Defaults("author")
	author := okAuthor(b)
	book.Edge("author", ToOne[AuWire]()).
		ForeignKey(func(r bkRow) string { return r.AuthorID }).
		Required().
		Includable().
		Policies(include.MissingRequiredNull, include.MissingRequiredError). // last wins
		Policies(include.MissingForeignError)                                // merges in
	FetchIDs(b, author, func(c *include.Ctx, ids []string) ([]auRow, error) { return nil, nil })
	b.Root(book)
	g, err := b.Compile()
	if err != nil {
		t.Fatalf("Compile: %v", err)
	}
	e := g.Resource("Book").Edges()["author"]
	if e.MissingRequired == nil || *e.MissingRequired != include.MissingRequiredError {
		t.Errorf("MissingRequired = %v, want the LAST declared value", e.MissingRequired)
	}
	if e.MissingForeign == nil || *e.MissingForeign != include.MissingForeignError {
		t.Errorf("MissingForeign = %v, want the second call to have merged in", e.MissingForeign)
	}
	if e.ExcludeRequired != nil {
		t.Errorf("an unnamed policy must stay nil (inherit), got %v", e.ExcludeRequired)
	}
}

// Wire-type target addressing: an edge naming a wire type no node declares is
// a finding (the typo class the Go compiler cannot see — the type exists, the
// node does not).
func TestCompileRejectsUnknownTargetWireType(t *testing.T) {
	type strayWire struct {
		ID string `json:"id"`
	}
	fs := compileFindings(t, func(b *Builder) {
		book := okBook(b)
		book.Edge("stray", ToOne[strayWire]()).ForeignKey(func(r bkRow) string { return r.AuthorID })
		b.Root(book)
	})
	if !hasFinding(fs, "no node of this builder declares wire type") {
		t.Errorf("missing unknown-wire-type finding:%s", dumpFindings(fs))
	}
}

// Required() implies default-included: Compile appends required keys to the
// node's Defaults — after the explicit ones, in edge-declaration order — and
// never duplicates an explicitly listed key. Defaults() itself is additive
// across calls.
func TestCompileAutoDefaultsRequiredEdges(t *testing.T) {
	b := NewBuilder()
	book := okBook(b).Defaults("tags") // explicit default first
	author, tag := okAuthor(b), okTag(b)
	_ = author
	_ = tag
	book.Edge("author", ToOne[AuWire]()).
		ForeignKey(func(r bkRow) string { return r.AuthorID }).
		Required().
		Includable()
	book.Edge("tags", ToMany[TgWire]()).
		ForeignKeys(func(r bkRow) []string { return r.TagIDs })
	FetchIDs(b, author, func(c *include.Ctx, ids []string) ([]auRow, error) { return nil, nil })
	FetchIDs(b, tag, func(c *include.Ctx, ids []string) ([]tgRow, error) { return nil, nil })
	b.Root(book)

	g, err := b.Compile()
	if err != nil {
		t.Fatalf("Required without explicit Defaults must compile: %v", err)
	}
	if got := g.Resource("Book").Defaults(); !reflect.DeepEqual(got, []string{"tags", "author"}) {
		t.Fatalf("Defaults() = %v, want [tags author] (explicit first, required appended)", got)
	}

	// Explicitly listing the required key does not duplicate it, and repeated
	// Defaults() calls append.
	b2 := NewBuilder()
	book2 := okBook(b2).Defaults("author").Defaults("tags")
	author2, tag2 := okAuthor(b2), okTag(b2)
	_ = author2
	_ = tag2
	book2.Edge("author", ToOne[AuWire]()).
		ForeignKey(func(r bkRow) string { return r.AuthorID }).
		Required().
		Includable()
	book2.Edge("tags", ToMany[TgWire]()).
		ForeignKeys(func(r bkRow) []string { return r.TagIDs })
	FetchIDs(b2, author2, func(c *include.Ctx, ids []string) ([]auRow, error) { return nil, nil })
	FetchIDs(b2, tag2, func(c *include.Ctx, ids []string) ([]tgRow, error) { return nil, nil })
	b2.Root(book2)

	g2, err := b2.Compile()
	if err != nil {
		t.Fatalf("Compile: %v", err)
	}
	if got := g2.Resource("Book").Defaults(); !reflect.DeepEqual(got, []string{"author", "tags"}) {
		t.Fatalf("Defaults() = %v, want [author tags] (no duplicate, calls append)", got)
	}
}

// ------------------------------------------------------------------ EstimatedRows

// Author --books(reverse)--> Book --tags(reverse, bare)--> Tag: Book is the
// target of a to-many edge, so its unbounded edge must state an estimate.
func TestEstimatedRowsUnderToManyRequiresDeclaration(t *testing.T) {
	b := NewBuilder()
	author, book, _ := okAuthor(b), okBook(b), okTag(b)
	author.Edge("books", Reverse[BkWire]("authorId"))
	book.Edge("tags", Reverse[TgWire]("bookId")).Bare()
	_, err := b.Compile()
	if err == nil || !strings.Contains(err.Error(), "unbounded edge") {
		t.Fatalf("want an 'unbounded edge' finding, got %v", err)
	}
}

func TestEstimatedRowsCompiles(t *testing.T) {
	b := NewBuilder()
	author, book, _ := okAuthor(b), okBook(b), okTag(b)
	author.Edge("books", Reverse[BkWire]("authorId"))
	book.Edge("tags", Reverse[TgWire]("bookId")).Bare().EstimatedRows(10)
	g, err := b.Compile()
	if err != nil {
		t.Fatalf("Compile: %v", err)
	}
	if got := g.Resource("Book").Edges()["tags"].EstimatedRows; got != 10 {
		t.Errorf("EstimatedRows = %d, want 10", got)
	}
	// Bare edge whose owner is not a to-many target: no estimate needed.
	b2 := NewBuilder()
	book2, _ := okBook(b2), okTag(b2)
	book2.Edge("tags", Reverse[TgWire]("bookId")).Bare()
	if _, err := b2.Compile(); err != nil {
		t.Fatalf("bare edge on a root-only node must compile: %v", err)
	}
}

// ------------------------------------------------------------------ col tags → Columns()

type colRow struct{ ID string }

// colWire exercises every col-tag shape: both options, none, one, the legacy
// sortCol spelling, no tag, and a json:"-" field whose tag must be dropped.
type colWire struct {
	ID        string     `json:"id" col:"id,sort,filter"`
	Title     string     `json:"title" col:"title"`
	CreatedAt *time.Time `json:"createdAt,omitempty" col:"created_at,filter"`
	Legacy    string     `json:"legacy" sortCol:"legacy_col"`
	Plain     string     `json:"plain"`
	Hidden    string     `json:"-" col:"hidden_col,filter"`
}

func okCol(b *Builder) *NodeHandle[colRow, colWire] {
	return Node[colRow, colWire](b, "Col").
		Wire(func(r colRow, c *include.Ctx) colWire { return colWire{ID: r.ID} }).
		PrimaryKey(func(r colRow) string { return r.ID })
}

func TestCompileDerivesColumns(t *testing.T) {
	b := NewBuilder()
	book := okBook(b)
	okCol(b)
	book.Edge("cols", Reverse[colWire]("bookId"))
	g, err := b.Compile()
	if err != nil {
		t.Fatalf("Compile: %v", err)
	}
	str, tm := reflect.TypeFor[string](), reflect.TypeFor[time.Time]()
	want := map[string]include.Column{
		"id":        {Col: "id", Type: str, Sortable: true, Filterable: true},
		"title":     {Col: "title", Type: str},
		"createdAt": {Col: "created_at", Type: tm, Filterable: true},
		"legacy":    {Col: "legacy_col", Type: str, Sortable: true},
	}
	if got := include.ColumnsOf(g.Resource("Col")); !reflect.DeepEqual(got, want) {
		t.Errorf("Columns() = %#v, want %#v", got, want)
	}
	wantSort := map[string]string{"id": "id", "legacy": "legacy_col"}
	if got := g.Resource("Book").Edges()["cols"].SortCols; !reflect.DeepEqual(got, wantSort) {
		t.Errorf("SortCols = %v, want %v (only sort-flagged columns are sortable)", got, wantSort)
	}
}

// No column tag at all → a nil map, and a Resource that is no ColumnSource
// reads as nil through ColumnsOf too.
func TestCompileNoColTagsYieldsNilColumns(t *testing.T) {
	b := NewBuilder()
	okTag(b)
	g, err := b.Compile()
	if err != nil {
		t.Fatalf("Compile: %v", err)
	}
	if got := include.ColumnsOf(g.Resource("Tag")); got != nil {
		t.Errorf("Columns() = %v, want nil", got)
	}
	if _, ok := g.Resource("Tag").(include.ColumnSource); !ok {
		t.Error("compiled node does not implement include.ColumnSource")
	}
}

// Columns() hands out the node's LIVE map, not a copy per call: it is on the
// hot path of every filter and sort resolution, and copying it there would
// allocate once per request for a map that Compile froze. Two calls must be
// the same map.
func TestCompileColumnsIsTheLiveMap(t *testing.T) {
	b := NewBuilder()
	okCol(b)
	g, err := b.Compile()
	if err != nil {
		t.Fatalf("Compile: %v", err)
	}
	n := g.Resource("Col").(include.ColumnSource)
	a, c := n.Columns(), n.Columns()
	if reflect.ValueOf(a).Pointer() != reflect.ValueOf(c).Pointer() {
		t.Error("Columns() returned a different map on the second call, want the live one")
	}
}

// A bare `col:"name"` is a PROJECTION binding and grants no permission: the
// column is known to the engine, and neither sortable nor filterable until
// the option says so.
func TestCompileBareColTagGrantsNothing(t *testing.T) {
	b := NewBuilder()
	Node[colRow, colBareWire](b, "Bare").
		Wire(func(r colRow, c *include.Ctx) colBareWire { return colBareWire{ID: r.ID} }).
		PrimaryKey(func(r colRow) string { return r.ID })
	g, err := b.Compile()
	if err != nil {
		t.Fatalf("Compile: %v", err)
	}
	col, ok := include.ColumnsOf(g.Resource("Bare"))["id"]
	if !ok {
		t.Fatal("id: no column bound by a bare col tag")
	}
	if col.Col != "id" || col.Sortable || col.Filterable {
		t.Errorf("column = %+v, want Col \"id\" with neither grant", col)
	}
}

// Depth dominance decides which col tag binds a shadowed json key: the OUTER
// field wins the wire key, so its tag — SQL name and grants together — is the
// one that reaches Columns(). The embedded tag never applies to a key the
// embedded field does not own.
func TestCompileColTagFollowsDepthDominance(t *testing.T) {
	b := NewBuilder()
	Node[colRow, colShadowWire](b, "Shadow").
		Wire(func(r colRow, c *include.Ctx) colShadowWire { return colShadowWire{ID: r.ID} }).
		PrimaryKey(func(r colRow) string { return r.ID })
	g, err := b.Compile()
	if err != nil {
		t.Fatalf("Compile: %v", err)
	}
	col := include.ColumnsOf(g.Resource("Shadow"))["id"]
	if col.Col != "outer_id" || !col.Filterable {
		t.Errorf("column = %+v, want the OUTER tag (outer_id, filter)", col)
	}
}

type colBothWire struct {
	ID string `json:"id" col:"id" sortCol:"id"`
}

type colUnknownOptWire struct {
	ID string `json:"id" col:"id,srot"`
}

type colRepeatOptWire struct {
	ID string `json:"id" col:"id,sort,sort"`
}

type colEmptyNameWire struct {
	ID string `json:"id" col:",sort"`
}

type colLegacyEmptyNameWire struct {
	ID string `json:"id" sortCol:""`
}

type colTrailingOptWire struct {
	ID string `json:"id" col:"id,"`
}

type colFilterSliceWire struct {
	ID   string   `json:"id"`
	Tags []string `json:"tags" col:"tags,filter"`
}

// colBareWire: a col tag with no options at all. It binds a projection and
// nothing else — neither sortable nor filterable — so the two grants stay
// opt-in.
type colBareWire struct {
	ID string `json:"id" col:"id"`
}

// ColShadowed is embedded and shadowed: the outer field wins depth dominance
// on the json key "id", so the OUTER col tag is the binding.
type ColShadowed struct {
	ID string `json:"id" col:"inner_id"`
}

type colShadowWire struct {
	ColShadowed
	ID string `json:"id" col:"outer_id,filter"`
}

type colReservedWire struct {
	ID string `json:"id"`
	Or string `json:"or" col:"or_col,filter"`
}

type colUnexportedWire struct {
	ID     string `json:"id"`
	hidden string `col:"hidden"` //nolint:unused // fixture
}

// A comma in a sortCol VALUE is a finding, never an option list: the legacy
// tag means one SQL name and grants `sort` and nothing else.
type colLegacyFilterOptWire struct {
	ID string `json:"id" sortCol:"a,filter"`
}

type colLegacySortOptWire struct {
	ID string `json:"id" sortCol:"a,sort"`
}

// ColEmbedded is embedded, so its own col tag binds nothing: encoding/json
// promotes its FIELDS, and the embedding field never appears on the wire.
type ColEmbedded struct {
	Name string `json:"name" col:"name"`
}

type colEmbedTagWire struct {
	ID          string `json:"id"`
	ColEmbedded `col:"x"`
}

func TestCompileColTagFindings(t *testing.T) {
	cases := []struct {
		name  string
		build func(b *Builder)
		want  string
	}{
		{
			name: "both col and sortCol on one field",
			build: func(b *Builder) {
				Node[colRow, colBothWire](b, "X").
					Wire(func(r colRow, c *include.Ctx) colBothWire { return colBothWire{} }).
					PrimaryKey(func(r colRow) string { return r.ID })
			},
			want: "carries both col and sortCol tags",
		},
		{
			name: "unknown option",
			build: func(b *Builder) {
				Node[colRow, colUnknownOptWire](b, "X").
					Wire(func(r colRow, c *include.Ctx) colUnknownOptWire { return colUnknownOptWire{} }).
					PrimaryKey(func(r colRow) string { return r.ID })
			},
			want: `unknown option "srot"`,
		},
		{
			name: "repeated option",
			build: func(b *Builder) {
				Node[colRow, colRepeatOptWire](b, "X").
					Wire(func(r colRow, c *include.Ctx) colRepeatOptWire { return colRepeatOptWire{} }).
					PrimaryKey(func(r colRow) string { return r.ID })
			},
			want: `repeats option "sort"`,
		},
		{
			name: "empty column name",
			build: func(b *Builder) {
				Node[colRow, colEmptyNameWire](b, "X").
					Wire(func(r colRow, c *include.Ctx) colEmptyNameWire { return colEmptyNameWire{} }).
					PrimaryKey(func(r colRow) string { return r.ID })
			},
			want: "empty column name",
		},
		{
			name: "empty legacy sortCol names the sortCol tag",
			build: func(b *Builder) {
				Node[colRow, colLegacyEmptyNameWire](b, "X").
					Wire(func(r colRow, c *include.Ctx) colLegacyEmptyNameWire { return colLegacyEmptyNameWire{} }).
					PrimaryKey(func(r colRow) string { return r.ID })
			},
			want: "sortCol tag on field ID has an empty column name",
		},
		{
			name: "trailing empty option",
			build: func(b *Builder) {
				Node[colRow, colTrailingOptWire](b, "X").
					Wire(func(r colRow, c *include.Ctx) colTrailingOptWire { return colTrailingOptWire{} }).
					PrimaryKey(func(r colRow) string { return r.ID })
			},
			want: "has an empty option",
		},
		{
			name: "filter on a slice field",
			build: func(b *Builder) {
				Node[colRow, colFilterSliceWire](b, "X").
					Wire(func(r colRow, c *include.Ctx) colFilterSliceWire { return colFilterSliceWire{} }).
					PrimaryKey(func(r colRow) string { return r.ID })
			},
			want: "filter needs a bool, number, string or time.Time field",
		},
		{
			name: "filterable column named or",
			build: func(b *Builder) {
				Node[colRow, colReservedWire](b, "X").
					Wire(func(r colRow, c *include.Ctx) colReservedWire { return colReservedWire{} }).
					PrimaryKey(func(r colRow) string { return r.ID })
			},
			want: `filterable column "or"`,
		},
		{
			name: "col tag on an unexported field",
			build: func(b *Builder) {
				Node[colRow, colUnexportedWire](b, "X").
					Wire(func(r colRow, c *include.Ctx) colUnexportedWire { return colUnexportedWire{} }).
					PrimaryKey(func(r colRow) string { return r.ID })
			},
			want: "col tag on unexported field hidden",
		},
		{
			name: "legacy sortCol carrying a filter option",
			build: func(b *Builder) {
				Node[colRow, colLegacyFilterOptWire](b, "X").
					Wire(func(r colRow, c *include.Ctx) colLegacyFilterOptWire { return colLegacyFilterOptWire{} }).
					PrimaryKey(func(r colRow) string { return r.ID })
			},
			want: `sortCol tag on field ID must not contain options (use col:"name,sort,…")`,
		},
		{
			name: "legacy sortCol carrying a sort option",
			build: func(b *Builder) {
				Node[colRow, colLegacySortOptWire](b, "X").
					Wire(func(r colRow, c *include.Ctx) colLegacySortOptWire { return colLegacySortOptWire{} }).
					PrimaryKey(func(r colRow) string { return r.ID })
			},
			want: `sortCol tag on field ID must not contain options (use col:"name,sort,…")`,
		},
		{
			name: "col tag on an embedded field",
			build: func(b *Builder) {
				Node[colRow, colEmbedTagWire](b, "X").
					Wire(func(r colRow, c *include.Ctx) colEmbedTagWire { return colEmbedTagWire{} }).
					PrimaryKey(func(r colRow) string { return r.ID })
			},
			want: "col tag on embedded field ColEmbedded is inert",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			fs := compileFindings(t, tc.build)
			if !hasFinding(fs, tc.want) {
				t.Errorf("no finding containing %q; got:%s", tc.want, dumpFindings(fs))
			}
		})
	}
}

// ------------------------------------------------------------------ Filterable()

func TestCompileFilterableEdge(t *testing.T) {
	b := NewBuilder()
	book := okBook(b)
	okCol(b)
	// filterable, deliberately NOT includable: the two permissions are independent
	book.Edge("col", ToOne[colWire]()).
		ForeignKey(func(r bkRow) string { return r.AuthorID }).
		Filterable()
	g, err := b.Compile()
	if err != nil {
		t.Fatalf("Compile: %v", err)
	}
	e := g.Resource("Book").Edges()["col"]
	if !e.Filterable {
		t.Error("Filterable = false, want true")
	}
	if e.Includable {
		t.Error("Includable = true, want false (Filterable must not imply Includable)")
	}
}

// A filterable edge whose target has no filterable column is still legal when
// the target has a filterable edge of its own: the condition can go one hop
// further.
func TestCompileFilterableEdgeThroughFilterableEdge(t *testing.T) {
	b := NewBuilder()
	book := okBook(b)
	author := okAuthor(b)
	okCol(b)
	book.Edge("author", ToOne[AuWire]()).
		ForeignKey(func(r bkRow) string { return r.AuthorID }).
		Filterable()
	author.Edge("col", ToOne[colWire]()).
		ForeignKey(func(r auRow) string { return r.ID }).
		Filterable()
	if _, err := b.Compile(); err != nil {
		t.Fatalf("Compile: %v", err)
	}
}

// The filterable surface may lie beyond a REVERSE edge, not only a to-one one:
// Author has no filterable column of its own, and the only column a condition
// can ever name sits on Col, one reverse hop further.
func TestCompileFilterableSurfaceBeyondReverseEdge(t *testing.T) {
	b := NewBuilder()
	book := okBook(b)
	author := okAuthor(b) // AuWire carries no col tags: no filterable column
	okCol(b)
	book.Edge("author", ToOne[AuWire]()).
		ForeignKey(func(r bkRow) string { return r.AuthorID }).
		Filterable()
	author.Edge("cols", Reverse[colWire]("authorId")).Filterable()
	if _, err := b.Compile(); err != nil {
		t.Fatalf("Compile: %v", err)
	}
}

// Reverse and forward to-many edges are filterable too: a condition crosses
// them with a quantifier (include.FilterStep.Quant), which ResolveFilter
// demands; Compile only grants the permission.
func TestCompileFilterableManyEdges(t *testing.T) {
	b := NewBuilder()
	book := okBook(b)
	okCol(b)
	book.Edge("cols", Reverse[colWire]("bookId")).Filterable()
	book.Edge("tags", ToMany[colWire]()).
		ForeignKeys(func(r bkRow) []string { return r.TagIDs }).
		Filterable()
	g, err := b.Compile()
	if err != nil {
		t.Fatalf("Compile: %v", err)
	}
	edges := g.Resource("Book").Edges()
	for _, key := range []string{"cols", "tags"} {
		if !edges[key].Filterable {
			t.Errorf("%s: Filterable = false, want true", key)
		}
	}
}

func TestCompileFilterableFindings(t *testing.T) {
	cases := []struct {
		name  string
		build func(b *Builder)
		want  string
	}{
		{
			name: "Filterable on an in-array edge",
			build: func(b *Builder) {
				book := okBook(b)
				okCol(b)
				book.Edge("participants", InArray[colWire]("people", "user")).
					ForeignKeys(func(r bkRow) []string { return r.TagIDs }).
					Filterable()
			},
			want: "Filterable() is not valid on in-array edges (to-one, reverse and to-many only)",
		},
		{
			name: "Filterable on a computed edge",
			build: func(b *Builder) {
				book := okBook(b)
				book.Edge("stats", Computed(apidoc.RawFragment(map[string]any{"type": "object"}))).Filterable()
			},
			want: "Filterable() is not valid on computed edges (to-one, reverse and to-many only)",
		},
		{
			name: "Filterable with Guard",
			build: func(b *Builder) {
				book := okBook(b)
				okCol(b)
				book.Edge("col", ToOne[colWire]()).
					ForeignKey(func(r bkRow) string { return r.AuthorID }).
					Guard(func(c *include.Ctx, r bkRow) bool { return true }).
					Filterable()
			},
			want: "Filterable() edge cannot carry Guard()",
		},
		{
			name: "Filterable to a node with nothing to filter on",
			build: func(b *Builder) {
				book := okBook(b)
				okAuthor(b)
				book.Edge("author", ToOne[AuWire]()).
					ForeignKey(func(r bkRow) string { return r.AuthorID }).
					Filterable()
			},
			want: "Filterable() edge to node Author, which has no filterable column and no filterable edge",
		},
		{
			// The closure is a cycle with no column in it: one hop out of Book
			// reaches Author, whose only filterable edge comes straight back.
			// A one-hop check would call both edges useful.
			name: "Filterable into a cycle with nothing to filter on",
			build: func(b *Builder) {
				book := okBook(b)
				author := okAuthor(b)
				book.Edge("author", ToOne[AuWire]()).
					ForeignKey(func(r bkRow) string { return r.AuthorID }).
					Filterable()
				author.Edge("self", ToOne[AuWire]()).
					ForeignKey(func(r auRow) string { return r.ID }).
					Filterable()
			},
			want: "Filterable() edge to node Author, which has no filterable column and no filterable edge",
		},
		{
			name: "Filterable edge keyed as a reserved group key",
			build: func(b *Builder) {
				book := okBook(b)
				okCol(b)
				book.Edge("or", ToOne[colWire]()).
					ForeignKey(func(r bkRow) string { return r.AuthorID }).
					Filterable()
			},
			want: `Filterable() edge key "or": the json keys "and" and "or" are reserved for filter groups`,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			fs := compileFindings(t, tc.build)
			if !hasFinding(fs, tc.want) {
				t.Errorf("no finding containing %q; got:%s", tc.want, dumpFindings(fs))
			}
		})
	}
}
