// sort_fallback_test.go — pins the SortFallback policy end to end.
//
// Claim: under Options{SortPolicy: SortFallback} an unknown `:sort()` token in
// an include string does NOT fail the plan — it silently falls back to the
// edge's declared default order. The mirror-image claim (SortStrict rejects the
// same token with INVALID_INCLUDE at resolve time) lives in policy_test.go.
//
// The whole assertion is about include-string handling —
// ParseInclude → ResolvePlan → reverseSortKey → Materialize — and needs no
// database: the fixture registry sorts its canned rows itself.
package include

import (
	"encoding/json"
	"sort"
	"testing"
)

// fallbackOptions is the policy pair this file pins: tolerant on both axes.
var fallbackOptions = Options{Limits: DefaultLimits, SortPolicy: SortFallback, ArgPolicy: ArgsTolerant}

// reviewFallbackWire carries the sortCol tags a production wire struct would
// declare; graph.Compile derives the edge whitelist from them. Package include
// cannot import graph (import cycle), so the fixture spells the SAME whitelist
// out by hand in reviewFallbackSortCols.
type reviewFallbackWire struct {
	ID        string  `json:"id"`
	PostedAt  *string `json:"postedAt" sortCol:"posted_at"`
	CreatedAt *string `json:"createdAt" sortCol:"created_at"`
	UpdatedAt *string `json:"updatedAt" sortCol:"updated_at"`
}

// reviewFallbackSortCols is the whitelist graph.Compile would derive from
// reviewFallbackWire's `sortCol` tags (wire json key → SQL-side sort key).
// It is spelled out by hand because package include cannot import graph, so it
// can silently desync from the tags above: the DERIVATION itself is pinned by
// graph/compile_test.go's TestCompileSortColsSkipsUnsortableFields.
var reviewFallbackSortCols = map[string]string{
	"postedAt":  "posted_at",
	"createdAt": "created_at",
	"updatedAt": "updated_at",
}

// reviewFallbackRow is a review "DB row".
type reviewFallbackRow struct {
	id        string
	postedAt  string
	createdAt string
}

// bookFallbackRow is a book "DB row" (the reverse edge's parent).
type bookFallbackRow struct{ id string }

// bookFallbackWire is the parent's scalar projection (its shape is irrelevant here).
type bookFallbackWire struct {
	ID string `json:"id"`
}

// reviewFallbackReg is an in-memory Registry whose batched reverse fetcher
// ACTUALLY sorts the canned rows by the SQL key it receives. Without that there
// is nothing to observe: a fetcher ignoring the key would make any
// order-equality assertion trivially green.
type reviewFallbackReg struct {
	rows  []reviewFallbackRow // in "table order" (the fetcher's own default)
	sorts []string            // every sort key the fetcher has seen
}

var _ Registry = (*reviewFallbackReg)(nil)

func (r *reviewFallbackReg) FetchByIDs(Resource) (FetchByIDs, bool) { return nil, false }
func (r *reviewFallbackReg) FetchByEdge(Resource, string) (FetchByParents, bool) {
	return nil, false
}

func (r *reviewFallbackReg) FetchByParents(Resource) (FetchByParents, bool) {
	return func(_ *Ctx, parentIDs []string, q EdgeQuery) (map[string]ParentRows, error) {
		sortKey := q.Sort
		r.sorts = append(r.sorts, sortKey)

		out := make([]reviewFallbackRow, len(r.rows))
		copy(out, r.rows)
		switch sortKey {
		case "posted_at":
			sort.SliceStable(out, func(i, j int) bool { return out[i].postedAt < out[j].postedAt })
		case "-posted_at":
			sort.SliceStable(out, func(i, j int) bool { return out[i].postedAt > out[j].postedAt })
		case "created_at":
			sort.SliceStable(out, func(i, j int) bool { return out[i].createdAt < out[j].createdAt })
		case "-created_at":
			sort.SliceStable(out, func(i, j int) bool { return out[i].createdAt > out[j].createdAt })
		default:
			// "" (key unresolved) — the fetcher's own order. It DELIBERATELY differs
			// from posted_at ASC, so a fallback landing on "" would show up as a
			// different order rather than a coincidental match.
		}

		docs := make([]any, len(out))
		for i, row := range out {
			docs[i] = row
		}
		res := make(map[string]ParentRows, len(parentIDs))
		for _, pid := range parentIDs {
			res[pid] = ParentRows{Rows: docs}
		}
		return res, nil
	}, true
}

// buildReviewFallbackGraph assembles a minimal "book → reviews" graph: a
// reverse edge, backref "book", includable, limit 10, default sort "postedAt",
// whitelist = reviewFallbackSortCols (the map graph.Compile derives from the
// wire's sortCol tags).
//
// The canned rows are laid out so that table order (r1,r2,r3) matches NEITHER
// posted_at ASC (r3,r1,r2) NOR posted_at DESC.
func buildReviewFallbackGraph() (Resource, *reviewFallbackReg) {
	reviewNode := &toyRes{
		name:   "SortFallbackReview",
		slug:   "review",
		fields: []string{"id", "postedAt", "createdAt"},
		idOf:   func(doc any) string { return doc.(reviewFallbackRow).id },
		serialize: func(doc any, _ *Ctx) any {
			r := doc.(reviewFallbackRow)
			posted, created := r.postedAt, r.createdAt
			return reviewFallbackWire{ID: r.id, PostedAt: &posted, CreatedAt: &created}
		},
	}
	reviewNode.setEdges(map[string]Edge{})

	bookNode := &toyRes{
		name:   "SortFallbackBook",
		slug:   "book",
		fields: []string{"id"},
		idOf:   func(doc any) string { return doc.(bookFallbackRow).id },
		serialize: func(doc any, _ *Ctx) any {
			return bookFallbackWire{ID: doc.(bookFallbackRow).id}
		},
	}
	bookNode.setEdges(map[string]Edge{
		"reviews": {Target: func() Resource { return reviewNode }, Many: true, Backref: "book", Includable: true, Limit: 10, Sort: "postedAt", SortCols: reviewFallbackSortCols},
	})

	reg := &reviewFallbackReg{rows: []reviewFallbackRow{
		{id: "r1", postedAt: "2026-09-05", createdAt: "2026-01-03"},
		{id: "r2", postedAt: "2026-09-09", createdAt: "2026-01-02"},
		{id: "r3", postedAt: "2026-09-01", createdAt: "2026-01-01"},
	}}
	return bookNode, reg
}

// resolveReviewInclude drives one include string through the whole path —
// ParseInclude → ResolvePlan → reverseSortKey → Materialize — and returns the
// resolved SQL sort key plus the id order in items[]. Any error along the way
// is what a live route would answer as 400, so it is fatal here.
func resolveReviewInclude(t *testing.T, raw string) (sortKey string, ids []string) {
	t.Helper()

	root, reg := buildReviewFallbackGraph()

	tree, err := ParseInclude(raw)
	if err != nil {
		t.Fatalf("ParseInclude(%q) = %v, want no error (this would be the 400)", raw, err)
	}
	plan, err := ResolvePlan(root, tree, nil, fallbackOptions)
	if err != nil {
		t.Fatalf("ResolvePlan(%q) = %v, want no error (this would be the 400)", raw, err)
	}

	idx := indexOfChild(plan, "reviews")
	if idx == -1 {
		t.Fatalf("plan(%q) has no `reviews` child: %+v", raw, plan.Children)
	}
	sortKey = reverseSortKey(plan.Children[idx])

	out, err := Materialize(plan, []any{bookFallbackRow{id: "b1"}}, &Ctx{Registry: reg})
	if err != nil {
		t.Fatalf("Materialize(%q): %v", raw, err)
	}
	if len(out) != 1 {
		t.Fatalf("Materialize(%q) returned %d docs, want 1", raw, len(out))
	}

	var doc struct {
		Reviews struct {
			Items []struct {
				ID string `json:"id"`
			} `json:"items"`
		} `json:"reviews"`
	}
	if err := json.Unmarshal(out[0], &doc); err != nil {
		t.Fatalf("decode(%q): %v; body: %s", raw, err, out[0])
	}
	ids = make([]string, 0, len(doc.Reviews.Items))
	for _, it := range doc.Reviews.Items {
		ids = append(ids, it.ID)
	}
	if len(reg.sorts) != 1 {
		t.Fatalf("FetchByParents calls for %q = %v, want exactly 1", raw, reg.sorts)
	}
	if reg.sorts[0] != sortKey {
		t.Fatalf("sort key seen by FetchByParents = %q, resolved = %q", reg.sorts[0], sortKey)
	}
	return sortKey, ids
}

func eqIDs(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// TestSortFallbackAcceptsUnknownToken: under SortFallback, `reviews:sort(bogus)`
// resolves without error and yields EXACTLY the order of an include with no sort.
func TestSortFallbackAcceptsUnknownToken(t *testing.T) {
	// "bogus" must not accidentally be whitelisted — otherwise the "fallback"
	// would be exercised on an allowed key and the test would mean nothing.
	if _, ok := reviewFallbackSortCols["bogus"]; ok {
		t.Fatal("`bogus` is whitelisted in the fixture — the fallback is not being exercised")
	}

	plainKey, plainIDs := resolveReviewInclude(t, "reviews")
	bogusKey, bogusIDs := resolveReviewInclude(t, "reviews:sort(bogus)")

	// The edge default is postedAt ASC.
	if plainKey != "posted_at" {
		t.Errorf("include=reviews resolved sort = %q, want %q", plainKey, "posted_at")
	}
	if want := []string{"r3", "r1", "r2"}; !eqIDs(plainIDs, want) {
		t.Errorf("include=reviews order = %v, want %v (postedAt ASC)", plainIDs, want)
	}

	// The pin proper: an invalid token is not a 400, it is the default order.
	if bogusKey != plainKey {
		t.Errorf("include=reviews:sort(bogus) resolved sort = %q, want %q (silent fallback)", bogusKey, plainKey)
	}
	if !eqIDs(bogusIDs, plainIDs) {
		t.Errorf("include=reviews:sort(bogus) order = %v, want %v (same as no-sort include)", bogusIDs, plainIDs)
	}
}

// TestSortFallbackHarnessIsNotInert is the positive control for the test above.
// Without it "the orders matched" would also hold for a broken fixture that
// never sorts: a valid `:sort(-postedAt)` MUST produce a different key and a
// different order. It also pins that a whitelisted token still gets through.
func TestSortFallbackHarnessIsNotInert(t *testing.T) {
	_, plainIDs := resolveReviewInclude(t, "reviews")
	descKey, descIDs := resolveReviewInclude(t, "reviews:sort(-postedAt)")

	if descKey != "-posted_at" {
		t.Errorf("include=reviews:sort(-postedAt) resolved sort = %q, want %q", descKey, "-posted_at")
	}
	if want := []string{"r2", "r1", "r3"}; !eqIDs(descIDs, want) {
		t.Errorf("include=reviews:sort(-postedAt) order = %v, want %v (postedAt DESC)", descIDs, want)
	}
	if eqIDs(descIDs, plainIDs) {
		t.Fatalf("valid override produced the no-sort order %v — the fixture does not observe sorting at all", descIDs)
	}
}
