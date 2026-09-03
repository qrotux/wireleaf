// Package loadertest is the contract harness for wireleaf fetchers.
//
// A fetcher is application code the engine calls with a batch of keys, so its
// bugs surface as wrong pages rather than as compile errors. This package
// turns the normative fetcher contract (spec §2) into runnable subtests an
// application adds to its own test suite:
//
//	func TestPostsByAuthor(t *testing.T) {
//	    loadertest.RunFetchByParents(t, "postsByAuthor", postsByAuthor,
//	        loadertest.ParentsFixture{
//	            Ctx:           &include.Ctx{Env: testEnv(t)},
//	            ParentIDs:     []string{"a1", "a2", "a3"},
//	            EmptyParentID: "a-empty",
//	            ProbeLimit:    2,
//	        })
//	}
//
// Run the suite with -race: the concurrency subtest is only meaningful there.
//
// The rules checked, in the fetcher's own words:
//   - Return ONLY keys you were asked for. Extra keys are dropped by the
//     engine, but they mean the query is wrong.
//   - "Limit 0" is a BARE edge: fetch everything, and never report HasMore.
//     The harness runs it FIRST and uses the row counts it returns as the
//     oracle for every other check.
//   - "Limit n" means: select n+1 rows, return at most n, and set HasMore iff
//     that extra row existed — so with T rows in total a parent returns
//     exactly min(n, T) rows and HasMore == (T > n).
//   - A parent with no children is a key that is absent (or has empty rows) —
//     never an error.
//   - Be safe for concurrent use.
//
// Every check is available twice: as a Run* function that reports through
// *testing.T, and as an unexported check* core returning the violations as
// sorted strings (which is how this package tests itself). Both drive the same
// table of subtests per fetcher kind, so they cannot diverge.
package loadertest

import (
	"fmt"
	"maps"
	"slices"
	"sync"
	"testing"

	"github.com/qrotux/wireleaf/include"
)

// concurrentCallers is how many goroutines the concurrency smoke test starts,
// and concurrentRounds is how many calls each of them makes. Several rounds
// past a common start barrier widen the window a racy fetcher must survive.
const (
	concurrentCallers = 4
	concurrentRounds  = 3
)

// ------------------------------------------------------------------ fixtures

// ParentsFixture describes the data an include.FetchByParents is checked
// against. The parents must really exist in the test database and, between
// them, hold enough children that at least one has MORE than ProbeLimit of
// them — otherwise the probe semantics are never exercised, which the harness
// reports as a fixture violation.
type ParentsFixture struct {
	// Ctx is the request context handed to the fetcher, shared by every call
	// of a check (including the concurrent ones, so that state hung off Ctx is
	// actually contended). nil is replaced by one zero &include.Ctx{}, which is
	// usable but carries no Env.
	Ctx *include.Ctx
	// ParentIDs are parent ids that HAVE children. At least one is required;
	// two or more also exercise the only-requested-keys subset call.
	ParentIDs []string
	// EmptyParentID is a parent id with no children at all. Optional: when
	// empty, the absent-parent subtest is skipped.
	EmptyParentID string
	// ProbeLimit is the per-parent limit used for the probe subtest. Must be
	// >= 1, and at least one parent must have more than ProbeLimit children.
	ProbeLimit int
	// Query is the template for every EdgeQuery the harness sends: the harness
	// copies it and overrides only Limit. Set it when the edge declares
	// required Args or a Sort the fetcher insists on — without it such a
	// fetcher would have to be wrapped just to be testable. Zero value is fine
	// for an edge with no declared arguments.
	Query include.EdgeQuery
}

// query returns the fixture's query template with Limit set to limit.
func (fx ParentsFixture) query(limit int) include.EdgeQuery {
	q := fx.Query
	q.Limit = limit
	return q
}

// IDsFixture describes the data an include.FetchByIDs is checked against.
type IDsFixture struct {
	// Ctx is the request context handed to the fetcher, shared by every call;
	// nil → one zero Ctx.
	Ctx *include.Ctx
	// KnownIDs are ids that exist. At least one is required, and the fetcher
	// must return a document for at least one of them.
	KnownIDs []string
	// UnknownID is an id that does not exist. Optional: when empty, the
	// unknown-id subtest is skipped.
	UnknownID string
	// IDOf extracts the id from one returned document — the same identity the
	// resource's IDOf uses. Required: without it the returned documents cannot
	// be matched against the requested ids. A panic inside IDOf is caught and
	// reported as a violation.
	IDOf func(doc any) string
}

// LoaderFixture describes the data a loader.Loader fetch function is checked
// against.
type LoaderFixture[K comparable] struct {
	// Ctx is the request context handed to fetch, shared by every call;
	// nil → one zero Ctx.
	Ctx *include.Ctx
	// KnownKeys are keys that exist. At least one is required, and fetch must
	// return a value for at least one of them.
	KnownKeys []K
	// UnknownKey is a key that does not exist. It is always requested, so the
	// zero value is fine when the key space has no obvious "missing" value.
	UnknownKey K
}

// ctxOf returns the fixture context, defaulting to a usable zero Ctx. It is
// called ONCE per check family: handing each concurrent goroutine its own Ctx
// would hide exactly the races the concurrency subtest looks for.
func ctxOf(c *include.Ctx) *include.Ctx {
	if c != nil {
		return c
	}
	return &include.Ctx{}
}

// ------------------------------------------------------------------ subtest table

// subcheck is one row of a fetcher kind's subtest table. Run* turns it into a
// t.Run, check* calls run directly — one table, so the two cannot drift.
type subcheck struct {
	name string
	// skip is a non-empty reason when the fixture does not supply what this
	// check needs: Run* skips it, check* omits it.
	skip string
	// parallel marks the concurrency smoke, which runs with t.Parallel.
	parallel bool
	run      func() []string
}

// runTable reports a fixture-level failure, or every subcheck, through t.
func runTable(t *testing.T, name string, fixtureV []string, checks []subcheck) {
	t.Helper()
	t.Run(name, func(t *testing.T) {
		if len(fixtureV) > 0 {
			report(t, sorted(fixtureV))
			return
		}
		for _, ch := range checks {
			t.Run(ch.name, func(t *testing.T) {
				if ch.skip != "" {
					t.Skip(ch.skip)
				}
				if ch.parallel {
					t.Parallel()
				}
				report(t, ch.run())
			})
		}
	})
}

// collectTable returns the fixture-level failure, or every subcheck's
// violations, sorted for deterministic output.
func collectTable(fixtureV []string, checks []subcheck) []string {
	if len(fixtureV) > 0 {
		return sorted(fixtureV)
	}
	var out []string
	for _, ch := range checks {
		if ch.skip != "" {
			continue
		}
		out = append(out, ch.run()...)
	}
	return sorted(out)
}

// ------------------------------------------------------------------ FetchByParents

// RunFetchByParents checks fn against the batched reverse fetcher contract,
// as subtests under name. Run the suite with -race.
func RunFetchByParents(t *testing.T, name string, fn include.FetchByParents, fx ParentsFixture) {
	t.Helper()
	runTable(t, name, checkParentsFixture(fx), parentsChecks(fn, fx))
}

// checkFetchByParents runs every FetchByParents check and returns the sorted
// violations. It is the core RunFetchByParents reports through.
func checkFetchByParents(fn include.FetchByParents, fx ParentsFixture) []string {
	return collectTable(checkParentsFixture(fx), parentsChecks(fn, fx))
}

// checkParentsFixture validates the fixture itself, so a fetcher is never
// blamed for a test that asked it nothing.
func checkParentsFixture(fx ParentsFixture) []string {
	var v []string
	if len(fx.ParentIDs) == 0 {
		v = append(v, "invalid fixture: ParentsFixture.ParentIDs is empty")
	}
	if fx.ProbeLimit < 1 {
		v = append(v, fmt.Sprintf("invalid fixture: ParentsFixture.ProbeLimit is %d, want >= 1", fx.ProbeLimit))
	}
	return v
}

// parentsChecks builds the FetchByParents subtest table. The bare (Limit 0)
// call runs first and once; its per-parent row counts are the oracle the probe
// checks compare against, so a fetcher cannot satisfy the harness by being
// consistently wrong at two limits.
func parentsChecks(fn include.FetchByParents, fx ParentsFixture) []subcheck {
	c := ctxOf(fx.Ctx)

	var (
		once    sync.Once
		bare    map[string]include.ParentRows
		totals  map[string]int
		bareErr []string
	)
	// oracle performs the bare call at most once and reports its row counts.
	oracle := func() (map[string]include.ParentRows, map[string]int, []string) {
		once.Do(func() {
			bare, bareErr = callParents(c, fn, fx.ParentIDs, fx.query(0), "bare (Limit 0) call")
			totals = make(map[string]int, len(fx.ParentIDs))
			for _, pid := range fx.ParentIDs {
				totals[pid] = len(bare[pid].Rows)
			}
		})
		return bare, totals, bareErr
	}

	absentSkip := ""
	if fx.EmptyParentID == "" {
		absentSkip = "ParentsFixture.EmptyParentID is empty: no childless parent to check"
	}

	return []subcheck{
		{name: "bare_limit", run: func() []string { return checkParentsBare(fx, oracle) }},
		{name: "fixture_exercises_probe", run: func() []string { return checkParentsProbeFixture(fx, oracle) }},
		{name: "only_requested_keys", run: func() []string { return checkParentsOnlyRequested(c, fn, fx) }},
		{name: "probe_semantics", run: func() []string { return checkParentsProbe(c, fn, fx, oracle) }},
		{name: "absent_parent", skip: absentSkip, run: func() []string { return checkParentsAbsent(c, fn, fx) }},
		{name: "concurrency", parallel: true, run: func() []string { return checkConcurrentParents(c, fn, fx) }},
	}
}

// callParents invokes fn, converting an error or a panic into a violation.
func callParents(c *include.Ctx, fn include.FetchByParents, ids []string, q include.EdgeQuery, what string) (map[string]include.ParentRows, []string) {
	return call("FetchByParents", what, func() (map[string]include.ParentRows, error) { return fn(c, ids, q) })
}

// unrequestedParentKeys reports map keys that were never asked for.
func unrequestedParentKeys(res map[string]include.ParentRows, ids []string) []string {
	return unrequestedKeys(slices.Collect(maps.Keys(res)), ids,
		"FetchByParents: returned unrequested parent key %v (the engine drops it, but the query is wrong)")
}

// parentsOracle is the memoized bare call shared by the parent checks.
type parentsOracle func() (map[string]include.ParentRows, map[string]int, []string)

// checkParentsBare verifies fetch-all: a bare edge returns every row of every
// parent that has children, and never reports HasMore.
func checkParentsBare(fx ParentsFixture, oracle parentsOracle) []string {
	res, totals, v := oracle()
	if len(v) > 0 {
		return sorted(v)
	}
	out := unrequestedParentKeys(res, fx.ParentIDs)
	for _, pid := range fx.ParentIDs {
		if totals[pid] == 0 {
			out = append(out, fmt.Sprintf("FetchByParents: parent %q returned no rows on the bare (Limit 0) call, but the fixture lists it as a parent WITH children (fetch-all must return them)", pid))
		}
		if res[pid].HasMore {
			out = append(out, fmt.Sprintf("FetchByParents: parent %q reported HasMore=true with Limit 0 (fetch-all always has everything)", pid))
		}
	}
	return sorted(out)
}

// checkParentsProbeFixture requires the fixture to actually reach past
// ProbeLimit — otherwise the probe subtest proves nothing.
func checkParentsProbeFixture(fx ParentsFixture, oracle parentsOracle) []string {
	_, totals, v := oracle()
	if len(v) > 0 {
		return sorted(v)
	}
	largest := 0
	for _, pid := range fx.ParentIDs {
		largest = max(largest, totals[pid])
	}
	if largest <= fx.ProbeLimit {
		return []string{fmt.Sprintf("fixture never exercises the probe: no parent has more than ProbeLimit rows (largest parent has %d, ProbeLimit is %d) — add a parent with more children or lower ProbeLimit", largest, fx.ProbeLimit)}
	}
	return nil
}

// checkParentsOnlyRequested calls fn with a subset of the parents and requires
// the result to stay inside that subset.
func checkParentsOnlyRequested(c *include.Ctx, fn include.FetchByParents, fx ParentsFixture) []string {
	ids := fx.ParentIDs
	if len(ids) > 1 {
		ids = ids[:len(ids)-1] // a strict subset makes an over-broad query visible
	}
	res, v := callParents(c, fn, ids, fx.query(fx.ProbeLimit), "subset call")
	if len(v) > 0 {
		return sorted(v)
	}
	return sorted(unrequestedParentKeys(res, ids))
}

// checkParentsProbe verifies the limit+1 probe against the bare call's totals:
// with T rows in total a parent returns exactly min(n, T) rows and HasMore ==
// (T > n). The Limit n+1 call corroborates that differentially and is itself
// checked for over-limit rows.
func checkParentsProbe(c *include.Ctx, fn include.FetchByParents, fx ParentsFixture, oracle parentsOracle) []string {
	_, totals, v := oracle()
	if len(v) > 0 {
		return sorted(v)
	}
	n := fx.ProbeLimit
	atN, v1 := callParents(c, fn, fx.ParentIDs, fx.query(n), fmt.Sprintf("Limit %d call", n))
	if len(v1) > 0 {
		return sorted(v1)
	}
	atN1, v2 := callParents(c, fn, fx.ParentIDs, fx.query(n+1), fmt.Sprintf("Limit %d call", n+1))
	if len(v2) > 0 {
		return sorted(v2)
	}
	out := unrequestedParentKeys(atN, fx.ParentIDs)
	out = append(out, unrequestedParentKeys(atN1, fx.ParentIDs)...)
	for _, pid := range fx.ParentIDs {
		a, b, total := atN[pid], atN1[pid], totals[pid]

		// Row count against the oracle, at both limits.
		out = append(out, checkRowCount(pid, len(a.Rows), n, total)...)
		out = append(out, checkRowCount(pid, len(b.Rows), n+1, total)...)

		// HasMore against the oracle; the differential is the corroboration.
		if want := total > n; a.HasMore != want {
			out = append(out, fmt.Sprintf("FetchByParents: parent %q reported HasMore=%v at Limit %d but fetch-all shows %d rows in total (want HasMore=%v)", pid, a.HasMore, n, total, want))
		} else if a.HasMore && len(b.Rows) <= len(a.Rows) {
			out = append(out, fmt.Sprintf("FetchByParents: parent %q reported HasMore=true at Limit %d but Limit %d returned no additional rows (%d then %d) — HasMore lied", pid, n, n+1, len(a.Rows), len(b.Rows)))
		} else if !a.HasMore && len(b.Rows) > len(a.Rows) {
			out = append(out, fmt.Sprintf("FetchByParents: parent %q reported HasMore=false at Limit %d but Limit %d returned more rows (%d then %d) — the limit+1 probe is missing", pid, n, n+1, len(a.Rows), len(b.Rows)))
		}
	}
	return sorted(out)
}

// checkRowCount compares one parent's row count at limit against the total the
// bare call reported.
func checkRowCount(pid string, got, limit, total int) []string {
	if got > limit {
		return []string{fmt.Sprintf("FetchByParents: parent %q returned %d rows with Limit %d (a fetcher returns at most Limit rows per parent)", pid, got, limit)}
	}
	want := min(total, limit)
	if got != want {
		return []string{fmt.Sprintf("FetchByParents: parent %q returned %d rows with Limit %d but fetch-all shows %d rows in total (want %d)", pid, got, limit, total, want)}
	}
	return nil
}

// checkParentsAbsent verifies that a childless parent is reported as an absent
// key or empty rows, never as an error.
func checkParentsAbsent(c *include.Ctx, fn include.FetchByParents, fx ParentsFixture) []string {
	ids := []string{fx.EmptyParentID}
	res, v := callParents(c, fn, ids, fx.query(fx.ProbeLimit), fmt.Sprintf("absent parent %q call", fx.EmptyParentID))
	if len(v) > 0 {
		return sorted(v)
	}
	out := unrequestedParentKeys(res, ids)
	if pr, ok := res[fx.EmptyParentID]; ok && len(pr.Rows) > 0 {
		out = append(out, fmt.Sprintf("FetchByParents: absent parent %q returned %d rows (the fixture says it has none)", fx.EmptyParentID, len(pr.Rows)))
	}
	return sorted(out)
}

// checkConcurrentParents is the FetchByParents concurrency smoke.
func checkConcurrentParents(c *include.Ctx, fn include.FetchByParents, fx ParentsFixture) []string {
	return checkConcurrent(
		func(what string) (map[string]include.ParentRows, []string) {
			return callParents(c, fn, fx.ParentIDs, fx.query(fx.ProbeLimit), what)
		},
		func(res map[string]include.ParentRows) []string { return unrequestedParentKeys(res, fx.ParentIDs) },
	)
}

// ------------------------------------------------------------------ FetchByIDs

// RunFetchIDs checks fn against the forward-batch fetcher contract, as
// subtests under name. Run the suite with -race.
func RunFetchIDs(t *testing.T, name string, fn include.FetchByIDs, fx IDsFixture) {
	t.Helper()
	runTable(t, name, checkIDsFixture(fx), idsChecks(fn, fx))
}

// checkFetchIDs runs every FetchByIDs check and returns the sorted violations.
func checkFetchIDs(fn include.FetchByIDs, fx IDsFixture) []string {
	return collectTable(checkIDsFixture(fx), idsChecks(fn, fx))
}

// checkIDsFixture validates the fixture itself.
func checkIDsFixture(fx IDsFixture) []string {
	var v []string
	if len(fx.KnownIDs) == 0 {
		v = append(v, "invalid fixture: IDsFixture.KnownIDs is empty")
	}
	if fx.IDOf == nil {
		v = append(v, "invalid fixture: IDsFixture.IDOf is nil (needed to match documents to requested ids)")
	}
	return v
}

// idsChecks builds the FetchByIDs subtest table.
func idsChecks(fn include.FetchByIDs, fx IDsFixture) []subcheck {
	c := ctxOf(fx.Ctx)
	unknownSkip := ""
	if fx.UnknownID == "" {
		unknownSkip = "IDsFixture.UnknownID is empty: no missing id to check"
	}
	return []subcheck{
		{name: "only_requested_ids", run: func() []string { return checkIDsOnlyRequested(c, fn, fx) }},
		{name: "unknown_id", skip: unknownSkip, run: func() []string { return checkIDsUnknown(c, fn, fx) }},
		{name: "concurrency", parallel: true, run: func() []string { return checkConcurrentIDs(c, fn, fx) }},
	}
}

// callIDs invokes fn, converting an error or a panic into a violation.
func callIDs(c *include.Ctx, fn include.FetchByIDs, ids []string, what string) ([]any, []string) {
	return call("FetchByIDs", what, func() ([]any, error) { return fn(c, ids) })
}

// safeIDOf calls the fixture's IDOf, catching a panic on an unexpected
// document type — the harness reports it instead of taking the process down
// (a panic inside a concurrency goroutine would otherwise be unrecoverable).
func safeIDOf(fx IDsFixture, doc any) (id string, v []string) {
	defer func() {
		if r := recover(); r != nil {
			id, v = "", []string{fmt.Sprintf("FetchByIDs: IDOf panicked on a returned document: %v (the fetcher returned a document of an unexpected type)", r)}
		}
	}()
	return fx.IDOf(doc), nil
}

// docIDs extracts the ids of every returned document, reporting IDOf panics.
func docIDs(fx IDsFixture, docs []any) ([]string, []string) {
	ids := make([]string, 0, len(docs))
	var v []string
	for _, d := range docs {
		id, pv := safeIDOf(fx, d)
		if len(pv) > 0 {
			v = append(v, pv...)
			continue
		}
		ids = append(ids, id)
	}
	return ids, v
}

// unrequestedDocs reports returned documents whose id was never asked for.
func unrequestedDocs(got []string, ids []string) []string {
	return unrequestedKeys(got, ids,
		"FetchByIDs: returned unrequested id %v (the engine drops it, but the query is wrong)")
}

// checkIDsOnlyRequested requires the returned documents to stay inside the
// requested id set, to be free of duplicates, and to be non-empty: a fetcher
// that returns nothing for known ids is broken, not merely unlucky.
func checkIDsOnlyRequested(c *include.Ctx, fn include.FetchByIDs, fx IDsFixture) []string {
	docs, v := callIDs(c, fn, fx.KnownIDs, "known ids call")
	if len(v) > 0 {
		return sorted(v)
	}
	got, out := docIDs(fx, docs)
	out = append(out, unrequestedDocs(got, fx.KnownIDs)...)
	if len(docs) == 0 {
		out = append(out, fmt.Sprintf("FetchByIDs: returned no documents for %d known ids (%v) — the fixture says they exist", len(fx.KnownIDs), fx.KnownIDs))
	}
	seen := make(map[string]int, len(got))
	for _, id := range got {
		seen[id]++
	}
	for id, n := range seen {
		if n > 1 {
			out = append(out, fmt.Sprintf("FetchByIDs: returned id %q %d times for one requested id (documents must be unique per id)", id, n))
		}
	}
	if len(docs) > len(fx.KnownIDs) {
		out = append(out, fmt.Sprintf("FetchByIDs: returned %d documents for %d requested ids (%v)", len(docs), len(fx.KnownIDs), fx.KnownIDs))
	}
	return sorted(out)
}

// checkIDsUnknown requires an id that does not exist to be dropped silently,
// both alone and mixed with known ids.
func checkIDsUnknown(c *include.Ctx, fn include.FetchByIDs, fx IDsFixture) []string {
	var out []string
	mixed := append(append([]string{}, fx.KnownIDs...), fx.UnknownID)
	for _, ids := range [][]string{{fx.UnknownID}, mixed} {
		docs, v := callIDs(c, fn, ids, fmt.Sprintf("unknown id %q call", fx.UnknownID))
		if len(v) > 0 {
			out = append(out, v...)
			continue
		}
		got, pv := docIDs(fx, docs)
		out = append(out, pv...)
		out = append(out, unrequestedDocs(got, ids)...)
		for _, id := range got {
			if id == fx.UnknownID {
				out = append(out, fmt.Sprintf("FetchByIDs: returned a document for the fixture's unknown id %q", fx.UnknownID))
			}
		}
	}
	return sorted(out)
}

// checkConcurrentIDs is the FetchByIDs concurrency smoke.
func checkConcurrentIDs(c *include.Ctx, fn include.FetchByIDs, fx IDsFixture) []string {
	return checkConcurrent(
		func(what string) ([]any, []string) { return callIDs(c, fn, fx.KnownIDs, what) },
		func(docs []any) []string {
			got, pv := docIDs(fx, docs)
			return append(pv, unrequestedDocs(got, fx.KnownIDs)...)
		},
	)
}

// ------------------------------------------------------------------ Loader fetch

// RunLoaderFetch checks a loader.Loader fetch function (the argument of
// loader.New) against its contract, as subtests under name. Run the suite
// with -race.
func RunLoaderFetch[K comparable, V any](t *testing.T, name string, fetch func(*include.Ctx, []K) (map[K]V, error), fx LoaderFixture[K]) {
	t.Helper()
	runTable(t, name, checkLoaderFixture(fx), loaderChecks(fetch, fx))
}

// checkLoaderFetch runs every Loader fetch check and returns the sorted
// violations.
func checkLoaderFetch[K comparable, V any](fetch func(*include.Ctx, []K) (map[K]V, error), fx LoaderFixture[K]) []string {
	return collectTable(checkLoaderFixture(fx), loaderChecks(fetch, fx))
}

// checkLoaderFixture validates the fixture itself.
func checkLoaderFixture[K comparable](fx LoaderFixture[K]) []string {
	if len(fx.KnownKeys) == 0 {
		return []string{"invalid fixture: LoaderFixture.KnownKeys is empty"}
	}
	return nil
}

// loaderChecks builds the Loader fetch subtest table.
func loaderChecks[K comparable, V any](fetch func(*include.Ctx, []K) (map[K]V, error), fx LoaderFixture[K]) []subcheck {
	c := ctxOf(fx.Ctx)
	return []subcheck{
		{name: "only_requested_keys", run: func() []string { return checkLoaderKeys(c, fetch, fx) }},
		{name: "concurrency", parallel: true, run: func() []string { return checkConcurrentLoader(c, fetch, fx) }},
	}
}

// callLoader invokes fetch, converting an error or a panic into a violation.
func callLoader[K comparable, V any](c *include.Ctx, fetch func(*include.Ctx, []K) (map[K]V, error), keys []K, what string) (map[K]V, []string) {
	return call("Loader fetch", what, func() (map[K]V, error) { return fetch(c, keys) })
}

// unrequestedLoaderKeys reports map keys that were never asked for.
func unrequestedLoaderKeys[K comparable, V any](res map[K]V, keys []K) []string {
	return unrequestedKeys(slices.Collect(maps.Keys(res)), keys,
		"Loader fetch: returned unrequested key %v (a Loader never reads it, but the query is wrong)")
}

// checkLoaderKeys requires the result to stay inside the requested key set, to
// be non-empty for the known keys, and to omit an unknown key rather than
// erroring on it or inventing a value.
func checkLoaderKeys[K comparable, V any](c *include.Ctx, fetch func(*include.Ctx, []K) (map[K]V, error), fx LoaderFixture[K]) []string {
	keys := append(append([]K{}, fx.KnownKeys...), fx.UnknownKey)
	res, v := callLoader(c, fetch, keys, fmt.Sprintf("call with the known keys and the unknown key %v", quoteKey(fx.UnknownKey)))
	if len(v) > 0 {
		return sorted(v)
	}
	out := unrequestedLoaderKeys(res, keys)
	known := 0
	for _, k := range fx.KnownKeys {
		if _, found := res[k]; found {
			known++
		}
	}
	if known == 0 {
		out = append(out, fmt.Sprintf("Loader fetch: returned no values for the %d known keys — the fixture says they exist", len(fx.KnownKeys)))
	}
	if _, found := res[fx.UnknownKey]; found && !slices.Contains(fx.KnownKeys, fx.UnknownKey) {
		out = append(out, fmt.Sprintf("Loader fetch: returned a value for the fixture's UnknownKey %v (an absent key must simply be omitted)", quoteKey(fx.UnknownKey)))
	}
	return sorted(out)
}

// checkConcurrentLoader is the Loader fetch concurrency smoke.
func checkConcurrentLoader[K comparable, V any](c *include.Ctx, fetch func(*include.Ctx, []K) (map[K]V, error), fx LoaderFixture[K]) []string {
	return checkConcurrent(
		func(what string) (map[K]V, []string) { return callLoader(c, fetch, fx.KnownKeys, what) },
		func(res map[K]V) []string { return unrequestedLoaderKeys(res, fx.KnownKeys) },
	)
}

// quoteKey renders a key for a violation message, quoting strings so an empty
// or space-bearing key is still readable.
func quoteKey(k any) string {
	if s, ok := k.(string); ok {
		return fmt.Sprintf("%q", s)
	}
	return fmt.Sprintf("%v", k)
}

// ------------------------------------------------------------------ shared

// call invokes fn, converting an error or a panic into a violation. prefix
// names the fetcher kind and what names the call, exactly as the violation
// messages render them.
func call[T any](prefix, what string, fn func() (T, error)) (res T, v []string) {
	defer func() {
		if r := recover(); r != nil {
			var zero T
			res, v = zero, []string{fmt.Sprintf("%s: %s panicked: %v", prefix, what, r)}
		}
	}()
	res, err := fn()
	if err != nil {
		var zero T
		return zero, []string{fmt.Sprintf("%s: %s returned error: %v", prefix, what, err)}
	}
	return res, nil
}

// unrequestedKeys reports the values of got that were never asked for — one
// violation per occurrence, rendered through format, whose single verb
// receives the key pre-quoted by quoteKey.
func unrequestedKeys[K comparable](got, want []K, format string) []string {
	wantSet := make(map[K]struct{}, len(want))
	for _, k := range want {
		wantSet[k] = struct{}{}
	}
	var v []string
	for _, k := range got {
		if _, ok := wantSet[k]; !ok {
			v = append(v, fmt.Sprintf(format, quoteKey(k)))
		}
	}
	return v
}

// checkConcurrent is the shared concurrency smoke: it runs doCall from several
// goroutines at once (on the fetcher kind's ONE shared Ctx, closed over by
// doCall) and applies check to every successful result. Under -race this is
// where a fetcher with shared state is caught.
func checkConcurrent[T any](doCall func(what string) (T, []string), check func(T) []string) []string {
	return runConcurrently(func(what string) []string {
		res, v := doCall(what)
		if len(v) > 0 {
			return v
		}
		return check(res)
	})
}

// runConcurrently runs body in concurrentCallers goroutines, each making
// concurrentRounds calls, all released from a common start barrier so the
// calls really overlap. Every violation any of them reports is collected.
func runConcurrently(body func(what string) []string) []string {
	var (
		mu    sync.Mutex
		out   []string
		wg    sync.WaitGroup
		start = make(chan struct{})
	)
	wg.Add(concurrentCallers)
	for i := 0; i < concurrentCallers; i++ {
		go func(i int) {
			defer wg.Done()
			<-start // all callers enter the fetcher together
			for round := 0; round < concurrentRounds; round++ {
				v := body(fmt.Sprintf("concurrent call (goroutine %d, round %d)", i, round))
				if len(v) == 0 {
					continue
				}
				mu.Lock()
				out = append(out, v...)
				mu.Unlock()
			}
		}(i)
	}
	close(start)
	wg.Wait()
	return out
}

// report fails t with one Errorf per violation.
func report(t *testing.T, violations []string) {
	t.Helper()
	for _, v := range violations {
		t.Errorf("%s", v)
	}
}

// sorted returns a sorted copy of v (never v itself, so callers' slices are
// safe from mutation), for deterministic violation output.
func sorted(v []string) []string {
	out := slices.Clone(v)
	slices.Sort(out)
	return out
}
