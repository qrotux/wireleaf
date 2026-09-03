package loadertest

import (
	"errors"
	"strings"
	"sync"
	"testing"

	"github.com/qrotux/wireleaf/include"
)

// ------------------------------------------------------------------ dataset

// child is one row of the in-memory dataset.
type child struct {
	ID       string
	ParentID string
}

// dataset: 3 parents with 3, 3 and 1 children; "p0" is the empty parent.
var dataset = map[string][]child{
	"p1": {{ID: "c11", ParentID: "p1"}, {ID: "c12", ParentID: "p1"}, {ID: "c13", ParentID: "p1"}},
	"p2": {{ID: "c21", ParentID: "p2"}, {ID: "c22", ParentID: "p2"}, {ID: "c23", ParentID: "p2"}},
	"p3": {{ID: "c31", ParentID: "p3"}},
	"p0": nil,
}

func parentsFixture() ParentsFixture {
	return ParentsFixture{
		ParentIDs:     []string{"p1", "p2", "p3"},
		EmptyParentID: "p0",
		ProbeLimit:    2,
	}
}

// rowsOf returns a copy of the parent's children as []any.
func rowsOf(pid string) []any {
	src := dataset[pid]
	out := make([]any, 0, len(src))
	for _, c := range src {
		out = append(out, c)
	}
	return out
}

// goodParents is a fully contract-conforming FetchByParents.
func goodParents(c *include.Ctx, parentIDs []string, q include.EdgeQuery) (map[string]include.ParentRows, error) {
	out := make(map[string]include.ParentRows, len(parentIDs))
	for _, pid := range parentIDs {
		rows := rowsOf(pid)
		if len(rows) == 0 {
			continue // absent parent: key simply absent
		}
		pr := include.ParentRows{Rows: rows}
		if q.Limit > 0 && len(rows) > q.Limit {
			pr.Rows = rows[:q.Limit]
			pr.HasMore = true
		}
		out[pid] = pr
	}
	return out, nil
}

// badExtraKey returns a parent key that was never requested.
func badExtraKey(c *include.Ctx, parentIDs []string, q include.EdgeQuery) (map[string]include.ParentRows, error) {
	out, err := goodParents(c, parentIDs, q)
	if err != nil {
		return nil, err
	}
	out["ghost"] = include.ParentRows{Rows: []any{child{ID: "cX", ParentID: "ghost"}}}
	return out, nil
}

// badOverLimit returns up to Limit+2 rows and never reports HasMore.
func badOverLimit(c *include.Ctx, parentIDs []string, q include.EdgeQuery) (map[string]include.ParentRows, error) {
	out := make(map[string]include.ParentRows, len(parentIDs))
	for _, pid := range parentIDs {
		rows := rowsOf(pid)
		if len(rows) == 0 {
			continue
		}
		if q.Limit > 0 && len(rows) > q.Limit+2 {
			rows = rows[:q.Limit+2]
		}
		out[pid] = include.ParentRows{Rows: rows}
	}
	return out, nil
}

// badSilent truncates correctly but never reports HasMore.
func badSilent(c *include.Ctx, parentIDs []string, q include.EdgeQuery) (map[string]include.ParentRows, error) {
	out, err := goodParents(c, parentIDs, q)
	if err != nil {
		return nil, err
	}
	for pid, pr := range out {
		pr.HasMore = false
		out[pid] = pr
	}
	return out, nil
}

// badLiar always claims HasMore, including for bare (Limit 0) queries.
func badLiar(c *include.Ctx, parentIDs []string, q include.EdgeQuery) (map[string]include.ParentRows, error) {
	out, err := goodParents(c, parentIDs, q)
	if err != nil {
		return nil, err
	}
	for pid, pr := range out {
		pr.HasMore = true
		out[pid] = pr
	}
	return out, nil
}

// badEmptyErrors errors instead of reporting the empty parent as absent.
func badEmptyErrors(c *include.Ctx, parentIDs []string, q include.EdgeQuery) (map[string]include.ParentRows, error) {
	for _, pid := range parentIDs {
		if pid == "p0" {
			return nil, errors.New("no such parent")
		}
	}
	return goodParents(c, parentIDs, q)
}

// badNilParents returns nothing at all, without erroring — internally
// consistent at every limit, and only the bare-call oracle catches it.
func badNilParents(c *include.Ctx, parentIDs []string, q include.EdgeQuery) (map[string]include.ParentRows, error) {
	return nil, nil
}

// badNeedsArgs insists on the declared edge argument the fixture's Query
// template carries — it is only testable through ParentsFixture.Query.
func badNeedsArgs(c *include.Ctx, parentIDs []string, q include.EdgeQuery) (map[string]include.ParentRows, error) {
	if q.Args["status"] != "published" || q.Sort != "-created" {
		return nil, errors.New("missing declared edge arguments")
	}
	return goodParents(c, parentIDs, q)
}

// failingParents fails every call: the concurrency smoke test must surface one
// violation per call (a genuine data race is surfaced by -race instead).
func failingParents(c *include.Ctx, parentIDs []string, q include.EdgeQuery) (map[string]include.ParentRows, error) {
	return nil, errors.New("boom")
}

// ------------------------------------------------------------------ assertions

// assertViolations requires that every want substring is matched by at least
// one violation and that every violation matches at least one want substring.
func assertViolations(t *testing.T, got []string, want []string) {
	t.Helper()
	for _, w := range want {
		found := false
		for _, g := range got {
			if strings.Contains(g, w) {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("missing violation containing %q; got %#v", w, got)
		}
	}
	for _, g := range got {
		matched := false
		for _, w := range want {
			if strings.Contains(g, w) {
				matched = true
				break
			}
		}
		if !matched {
			t.Errorf("unexpected violation %q; want only %#v", g, want)
		}
	}
}

// assertSorted requires the violations to come back in deterministic order.
func assertSorted(t *testing.T, got []string) {
	t.Helper()
	for i := 1; i < len(got); i++ {
		if got[i-1] > got[i] {
			t.Errorf("violations are not sorted: %q before %q", got[i-1], got[i])
		}
	}
}

// ------------------------------------------------------------------ FetchByParents

func TestCheckFetchByParents_Good(t *testing.T) {
	fx := parentsFixture()
	fx.Ctx = &include.Ctx{}
	assertViolations(t, checkFetchByParents(goodParents, fx), nil)
}

func TestCheckFetchByParents_NilCtx(t *testing.T) {
	// A zero fixture Ctx is usable: the harness must not require one.
	assertViolations(t, checkFetchByParents(goodParents, parentsFixture()), nil)
}

func TestCheckFetchByParents_ExtraKey(t *testing.T) {
	got := checkFetchByParents(badExtraKey, parentsFixture())
	assertViolations(t, got, []string{`unrequested parent key "ghost"`})
	assertSorted(t, got)
}

func TestCheckFetchByParents_OverLimit(t *testing.T) {
	// p1/p2 have 3 rows: with Limit 2 the fetcher returns all 3 and stays
	// silent about it, so both the row count and HasMore contradict fetch-all.
	assertViolations(t, checkFetchByParents(badOverLimit, parentsFixture()),
		[]string{
			`parent "p1" returned 3 rows with Limit 2 (a fetcher returns at most Limit rows per parent)`,
			`parent "p2" returned 3 rows with Limit 2 (a fetcher returns at most Limit rows per parent)`,
			`parent "p1" reported HasMore=false at Limit 2 but fetch-all shows 3 rows in total (want HasMore=true)`,
			`parent "p2" reported HasMore=false at Limit 2 but fetch-all shows 3 rows in total (want HasMore=true)`,
		})
}

func TestCheckFetchByParents_SilentHasMore(t *testing.T) {
	assertViolations(t, checkFetchByParents(badSilent, parentsFixture()),
		[]string{
			`parent "p1" reported HasMore=false at Limit 2 but fetch-all shows 3 rows in total (want HasMore=true)`,
			`parent "p2" reported HasMore=false at Limit 2 but fetch-all shows 3 rows in total (want HasMore=true)`,
		})
}

func TestCheckFetchByParents_LyingHasMore(t *testing.T) {
	assertViolations(t, checkFetchByParents(badLiar, parentsFixture()),
		[]string{
			`parent "p3" reported HasMore=true at Limit 2 but fetch-all shows 1 rows in total (want HasMore=false)`,
			`parent "p1" reported HasMore=true with Limit 0`,
			`parent "p2" reported HasMore=true with Limit 0`,
			`parent "p3" reported HasMore=true with Limit 0`,
		})
}

func TestCheckFetchByParents_ErrorsOnEmptyParent(t *testing.T) {
	assertViolations(t, checkFetchByParents(badEmptyErrors, parentsFixture()),
		[]string{`absent parent "p0"`})
}

// A fetcher that returns nothing is internally consistent at every limit: only
// the bare-call oracle exposes it.
func TestCheckFetchByParents_ReturnsNothing(t *testing.T) {
	assertViolations(t, checkFetchByParents(badNilParents, parentsFixture()),
		[]string{
			`parent "p1" returned no rows on the bare (Limit 0) call`,
			`parent "p2" returned no rows on the bare (Limit 0) call`,
			`parent "p3" returned no rows on the bare (Limit 0) call`,
			"fixture never exercises the probe",
		})
}

// A ProbeLimit no parent exceeds would let a limit-ignoring fetcher pass.
func TestCheckFetchByParents_VacuousProbeLimit(t *testing.T) {
	fx := parentsFixture()
	fx.ProbeLimit = 3 // the largest parent has exactly 3 children
	assertViolations(t, checkFetchByParents(goodParents, fx),
		[]string{"fixture never exercises the probe: no parent has more than ProbeLimit rows (largest parent has 3, ProbeLimit is 3)"})
}

// The Query template reaches the fetcher on every call, Limit aside.
func TestCheckFetchByParents_QueryTemplate(t *testing.T) {
	fx := parentsFixture()
	fx.Query = include.EdgeQuery{Sort: "-created", Args: map[string]any{"status": "published"}}
	assertViolations(t, checkFetchByParents(badNeedsArgs, fx), nil)
	// Without the template the same fetcher cannot even be called.
	got := checkFetchByParents(badNeedsArgs, parentsFixture())
	if len(got) == 0 {
		t.Fatal("a fetcher needing declared args must fail without ParentsFixture.Query")
	}
}

func TestCheckFetchByParents_BadFixture(t *testing.T) {
	got := checkFetchByParents(goodParents, ParentsFixture{})
	assertViolations(t, got, []string{"fixture"})
	if len(got) == 0 {
		t.Fatal("empty fixture must be reported")
	}
}

func TestRunFetchByParents_Good(t *testing.T) {
	RunFetchByParents(t, "good", goodParents, parentsFixture())
}

// An omitted EmptyParentID skips the absent-parent subtest rather than
// silently passing it.
func TestRunFetchByParents_NoEmptyParent(t *testing.T) {
	fx := parentsFixture()
	fx.EmptyParentID = ""
	RunFetchByParents(t, "good", goodParents, fx)
}

func TestCheckFetchByParents_ConcurrentError(t *testing.T) {
	got := checkConcurrentParents(&include.Ctx{}, failingParents, parentsFixture())
	if want := concurrentCallers * concurrentRounds; len(got) != want {
		t.Fatalf("got %d violations, want one per concurrent call (%d)", len(got), want)
	}
	for _, g := range got {
		if !strings.Contains(g, "concurrent call") {
			t.Errorf("unexpected violation %q", g)
		}
	}
}

// ------------------------------------------------------------------ FetchByIDs

func docsFixture() IDsFixture {
	return IDsFixture{
		KnownIDs:  []string{"c11", "c12", "c31"},
		UnknownID: "nope",
		IDOf:      func(v any) string { return v.(child).ID },
	}
}

func lookup(id string) (child, bool) {
	for _, rows := range dataset {
		for _, c := range rows {
			if c.ID == id {
				return c, true
			}
		}
	}
	return child{}, false
}

func goodIDs(c *include.Ctx, ids []string) ([]any, error) {
	out := make([]any, 0, len(ids))
	for _, id := range ids {
		if ch, ok := lookup(id); ok {
			out = append(out, ch)
		}
	}
	return out, nil
}

func badIDsExtra(c *include.Ctx, ids []string) ([]any, error) {
	out, _ := goodIDs(c, ids)
	return append(out, child{ID: "c99", ParentID: "p9"}), nil
}

func badIDsUnknownErrors(c *include.Ctx, ids []string) ([]any, error) {
	for _, id := range ids {
		if _, ok := lookup(id); !ok {
			return nil, errors.New("unknown id")
		}
	}
	return goodIDs(c, ids)
}

func badIDsNone(c *include.Ctx, ids []string) ([]any, error) { return nil, nil }

func badIDsDuplicate(c *include.Ctx, ids []string) ([]any, error) {
	out, _ := goodIDs(c, ids)
	return append(out, out...), nil
}

// badIDsWrongType returns a document the fixture's IDOf cannot handle: the
// panic must become a violation, not a dead test binary.
func badIDsWrongType(c *include.Ctx, ids []string) ([]any, error) {
	out, _ := goodIDs(c, ids)
	return append(out, "not-a-child"), nil
}

func TestCheckFetchIDs_Good(t *testing.T) {
	assertViolations(t, checkFetchIDs(goodIDs, docsFixture()), nil)
}

func TestCheckFetchIDs_ExtraDoc(t *testing.T) {
	assertViolations(t, checkFetchIDs(badIDsExtra, docsFixture()),
		[]string{`unrequested id "c99"`, "returned 4 documents for 3 requested ids"})
}

func TestCheckFetchIDs_UnknownErrors(t *testing.T) {
	assertViolations(t, checkFetchIDs(badIDsUnknownErrors, docsFixture()),
		[]string{`unknown id "nope"`})
}

func TestCheckFetchIDs_ReturnsNothing(t *testing.T) {
	assertViolations(t, checkFetchIDs(badIDsNone, docsFixture()),
		[]string{"returned no documents for 3 known ids"})
}

func TestCheckFetchIDs_Duplicates(t *testing.T) {
	assertViolations(t, checkFetchIDs(badIDsDuplicate, docsFixture()),
		[]string{
			`returned id "c11" 2 times`,
			`returned id "c12" 2 times`,
			`returned id "c31" 2 times`,
			"returned 6 documents for 3 requested ids",
		})
}

func TestCheckFetchIDs_IDOfPanics(t *testing.T) {
	assertViolations(t, checkFetchIDs(badIDsWrongType, docsFixture()),
		[]string{
			"IDOf panicked on a returned document",
			"returned 4 documents for 3 requested ids",
		})
}

func TestCheckFetchIDs_BadFixture(t *testing.T) {
	assertViolations(t, checkFetchIDs(goodIDs, IDsFixture{}), []string{"fixture"})
}

func TestRunFetchIDs_Good(t *testing.T) {
	RunFetchIDs(t, "good", goodIDs, docsFixture())
}

// An omitted UnknownID skips the unknown-id subtest.
func TestRunFetchIDs_NoUnknownID(t *testing.T) {
	fx := docsFixture()
	fx.UnknownID = ""
	RunFetchIDs(t, "good", goodIDs, fx)
}

// ------------------------------------------------------------------ Loader fetch

func loaderFixture() LoaderFixture[string] {
	return LoaderFixture[string]{KnownKeys: []string{"c11", "c31"}, UnknownKey: "nope"}
}

func goodLoad(c *include.Ctx, keys []string) (map[string]child, error) {
	out := make(map[string]child, len(keys))
	for _, k := range keys {
		if ch, ok := lookup(k); ok {
			out[k] = ch
		}
	}
	return out, nil
}

func badLoadExtra(c *include.Ctx, keys []string) (map[string]child, error) {
	out, _ := goodLoad(c, keys)
	out["ghost"] = child{ID: "ghost"}
	return out, nil
}

func badLoadUnknownErrors(c *include.Ctx, keys []string) (map[string]child, error) {
	for _, k := range keys {
		if _, ok := lookup(k); !ok {
			return nil, errors.New("unknown key")
		}
	}
	return goodLoad(c, keys)
}

func badLoadInventsUnknown(c *include.Ctx, keys []string) (map[string]child, error) {
	out, _ := goodLoad(c, keys)
	for _, k := range keys {
		if _, ok := out[k]; !ok {
			out[k] = child{ID: k}
		}
	}
	return out, nil
}

func badLoadNothing(c *include.Ctx, keys []string) (map[string]child, error) { return nil, nil }

func TestCheckLoaderFetch_Good(t *testing.T) {
	assertViolations(t, checkLoaderFetch(goodLoad, loaderFixture()), nil)
}

func TestCheckLoaderFetch_ExtraKey(t *testing.T) {
	assertViolations(t, checkLoaderFetch(badLoadExtra, loaderFixture()),
		[]string{`unrequested key "ghost"`})
}

func TestCheckLoaderFetch_UnknownErrors(t *testing.T) {
	assertViolations(t, checkLoaderFetch(badLoadUnknownErrors, loaderFixture()),
		[]string{`unknown key "nope"`})
}

func TestCheckLoaderFetch_InventsUnknown(t *testing.T) {
	assertViolations(t, checkLoaderFetch(badLoadInventsUnknown, loaderFixture()),
		[]string{`value for the fixture's UnknownKey "nope"`})
}

func TestCheckLoaderFetch_ReturnsNothing(t *testing.T) {
	assertViolations(t, checkLoaderFetch(badLoadNothing, loaderFixture()),
		[]string{"returned no values for the 2 known keys"})
}

func TestCheckLoaderFetch_BadFixture(t *testing.T) {
	assertViolations(t, checkLoaderFetch(goodLoad, LoaderFixture[string]{}), []string{"fixture"})
}

func TestRunLoaderFetch_Good(t *testing.T) {
	RunLoaderFetch(t, "good", goodLoad, loaderFixture())
}

// ------------------------------------------------------------------ shared Ctx

// The concurrency smoke must contend on ONE Ctx, not one per goroutine: a
// fetcher that writes request state through Ctx.State is the case that
// -race has to be able to see.
func TestConcurrency_SharesOneCtx(t *testing.T) {
	seen := make(map[*include.Ctx]struct{})
	var mu sync.Mutex
	fn := func(c *include.Ctx, parentIDs []string, q include.EdgeQuery) (map[string]include.ParentRows, error) {
		mu.Lock()
		seen[c] = struct{}{}
		mu.Unlock()
		return goodParents(c, parentIDs, q)
	}
	if v := checkConcurrentParents(ctxOf(nil), fn, parentsFixture()); len(v) > 0 {
		t.Fatalf("unexpected violations: %v", v)
	}
	if len(seen) != 1 {
		t.Errorf("fetcher saw %d distinct Ctx values, want 1", len(seen))
	}
}
