package include

import (
	"errors"
	"strings"
	"testing"
)

func TestHydratorByID(t *testing.T) {
	g := buildToyGraph()
	h := Bind(g.A, g.Reg, Options{})
	raw, err := h.ByID(nil, "a1", "child")
	if err != nil {
		t.Fatalf("ByID: %v", err)
	}
	if !strings.Contains(string(raw), `"child"`) {
		t.Errorf("include not applied: %s", raw)
	}
	_, err = h.ByID(nil, "nope", "")
	var ie *Error
	if !errors.As(err, &ie) || ie.Code != NOT_FOUND || ie.Status != 404 {
		t.Fatalf("missing id: %v", err)
	}
	if _, err := h.ByID(nil, "a1", "ghost"); err == nil {
		t.Fatal("bad include must fail before fetch")
	}
}

func TestHydratorQueryAndHydrate(t *testing.T) {
	g := buildToyGraph()
	h := Bind(g.A, g.Reg, Options{})
	var got QueryArgs
	fetch := ListFetcher(func(_ *Ctx, q QueryArgs) (ListPage, error) {
		got = q
		return ListPage{Docs: []any{toyARow{id: "a1", name: "Alpha"}}, Total: 7, HasMore: true, NextCursor: "n1"}, nil
	})
	res, err := h.Query(&Ctx{}, QueryArgs{Cursor: "c0", Limit: 1}, "", fetch)
	if err != nil {
		t.Fatalf("Query: %v", err)
	}
	if got.Cursor != "c0" || res.Total != 7 || !res.HasMore || res.NextCursor != "n1" || res.Limit != 1 || len(res.Data) != 1 {
		t.Fatalf("res = %+v, args = %+v", res, got)
	}
	raw, err := h.Hydrate(nil, toyARow{id: "a2", name: "Beta"}, "")
	if err != nil || !strings.Contains(string(raw), "Beta") {
		t.Fatalf("Hydrate: %s %v", raw, err)
	}
}

func TestBindPanicsOnNil(t *testing.T) {
	defer func() {
		if recover() == nil {
			t.Fatal("expected panic")
		}
	}()
	Bind(nil, nil, Options{})
}

// ---------------------------------------------------------------- registry stubs
//
// Both wrap the toy graph's *fakeReg (toygraph_test.go, untouched) and override
// only FetchByIDs: one to simulate a buggy fetcher returning two rows for one
// id, one to simulate a node with no by-id fetcher registered at all.

// dupIDReg returns two rows for any single requested id.
type dupIDReg struct{ *fakeReg }

func (r dupIDReg) FetchByIDs(Resource) (FetchByIDs, bool) {
	return func(_ *Ctx, _ []string) ([]any, error) {
		return []any{
			toyARow{id: "a1", name: "Alpha"},
			toyARow{id: "a1", name: "Alpha again"},
		}, nil
	}, true
}

// noIDReg has no by-id fetcher for any node.
type noIDReg struct{ *fakeReg }

func (r noIDReg) FetchByIDs(Resource) (FetchByIDs, bool) { return nil, false }

func TestHydratorByIDFetcherFaults(t *testing.T) {
	g := buildToyGraph()

	// Two rows for one id is a fetcher bug, reported as a plain error.
	h := Bind(g.A, dupIDReg{g.Reg}, Options{})
	if h.Root() != g.A {
		t.Errorf("Root() = %v, want the bound root", h.Root())
	}
	_, err := h.ByID(nil, "a1", "")
	if err == nil || !strings.Contains(err.Error(), "returned 2 rows for one id") {
		t.Fatalf("duplicate rows: err = %v", err)
	}
	var ie *Error
	if errors.As(err, &ie) {
		t.Errorf("duplicate rows should be a plain error, got *Error %+v", ie)
	}

	// No FetchByIDs registered for the root is a wiring error.
	h = Bind(g.A, noIDReg{g.Reg}, Options{})
	if _, err := h.ByID(nil, "a1", ""); err == nil ||
		!strings.Contains(err.Error(), "has no FetchByIDs fetcher") {
		t.Fatalf("missing fetcher: err = %v", err)
	}
}

func TestHydratorQueryBudgetPreCheck(t *testing.T) {
	g := buildToyGraph()
	// MaxRows 2 (the defaults otherwise) against a plan that costs at least
	// one row per document: any sizeable page blows the budget.
	lim := DefaultLimits
	lim.MaxRows = 2
	h := Bind(g.A, g.Reg, Options{Limits: lim})

	called := false
	fetch := ListFetcher(func(_ *Ctx, _ QueryArgs) (ListPage, error) {
		called = true
		return ListPage{}, nil
	})
	_, err := h.Query(nil, QueryArgs{Limit: 100}, "kids", fetch)
	var ie *Error
	if !errors.As(err, &ie) || ie.Code != INCLUDE_BUDGET_EXCEEDED {
		t.Fatalf("budget pre-check: err = %v", err)
	}
	if called {
		t.Error("fetcher must not run once the budget pre-check fails")
	}

	// A page that fits the budget still reaches the fetcher.
	if _, err := h.Query(nil, QueryArgs{Limit: 1}, "", fetch); err != nil {
		t.Fatalf("within budget: %v", err)
	}
	if !called {
		t.Error("fetcher should run when the page fits the budget")
	}
}
