package include

import (
	"encoding/json"
	"errors"
	"testing"
)

// The list facade materializes every fetched root doc in input order, and the
// requested include reaches both the plan and the bytes.
func TestHydrateByQuery(t *testing.T) {
	g := buildToyGraph()
	ctx := &Ctx{Registry: g.Reg}

	a1 := toyARow{id: "a1", name: "Alpha", childFK: "b1", selfFK: "a2"}
	a2 := toyARow{id: "a2", name: "Beta", childFK: "b2", selfFK: ""}

	var fetchCalled int
	fetcher := RootFetcher(func(_ *Ctx, _ QueryArgs) ([]any, int, bool, error) {
		fetchCalled++
		return []any{a1, a2}, 2, false, nil
	})

	inc := IncludeTree{"child": IncludeTree{}}
	q := QueryArgs{Limit: 20}
	result, plan, err := HydrateByQuery(g.A, q, inc, nil, fetcher, ctx, DefaultOptions)
	if err != nil {
		t.Fatalf("HydrateByQuery: %v", err)
	}
	if fetchCalled != 1 {
		t.Errorf("fetch called %d times, want 1", fetchCalled)
	}
	if result.Total != 2 {
		t.Errorf("Total = %d, want 2", result.Total)
	}
	if result.HasMore {
		t.Error("HasMore should be false")
	}
	if result.Page != q.Page || result.Limit != q.Limit {
		t.Errorf("Page/Limit = %d/%d, want %d/%d", result.Page, result.Limit, q.Page, q.Limit)
	}
	if len(result.Data) != 2 {
		t.Fatalf("Data len = %d, want 2", len(result.Data))
	}

	if plan == nil {
		t.Fatal("plan is nil")
	}
	if !HasInclude(plan, "child") {
		t.Error("plan should have 'child' child (explicit include)")
	}

	var doc0 map[string]json.RawMessage
	if err := json.Unmarshal(result.Data[0], &doc0); err != nil {
		t.Fatalf("unmarshal data[0]: %v", err)
	}
	if _, ok := doc0["id"]; !ok {
		t.Error("data[0] missing 'id'")
	}
	if _, ok := doc0["child"]; !ok {
		t.Error("data[0] missing 'child' (explicit include)")
	}

	var doc1 map[string]json.RawMessage
	if err := json.Unmarshal(result.Data[1], &doc1); err != nil {
		t.Fatalf("unmarshal data[1]: %v", err)
	}
	if string(doc1["id"]) != `"a2"` {
		t.Errorf("data[1].id = %s, want \"a2\"", doc1["id"])
	}
}

// An invalid include fails BEFORE the fetch closure runs: the fetch counter
// stays 0.
func TestHydrateByQuery_400BeforeFetch(t *testing.T) {
	g := buildToyGraph()
	ctx := &Ctx{Registry: g.Reg}

	var fetchCount int
	fetcher := RootFetcher(func(_ *Ctx, _ QueryArgs) ([]any, int, bool, error) {
		fetchCount++
		return nil, 0, false, nil
	})

	// "bogus" is not an edge on toyA → INVALID_INCLUDE.
	inc := IncludeTree{"bogus": IncludeTree{}}
	_, _, err := HydrateByQuery(g.A, QueryArgs{}, inc, nil, fetcher, ctx, DefaultOptions)
	if err == nil {
		t.Fatal("expected error for unknown include edge, got nil")
	}
	var ie *Error
	if !errors.As(err, &ie) || ie.Code != INVALID_INCLUDE {
		t.Errorf("error = %v, want *Error{Code:INVALID_INCLUDE}", err)
	}
	if fetchCount != 0 {
		t.Errorf("fetch called %d times, want 0 (should never be called)", fetchCount)
	}
}

// A fetch returning (nil, nil) is the 404 path: *Error with Status 404.
func TestHydrateByID_NotFound(t *testing.T) {
	g := buildToyGraph()
	ctx := &Ctx{Registry: g.Reg}

	fetch := func(_ *Ctx, _ string) (any, error) {
		return nil, nil // not found
	}

	_, _, err := HydrateByID(g.A, "no-such-id", IncludeTree{}, nil, fetch, ctx, DefaultOptions)
	if err == nil {
		t.Fatal("expected 404 error, got nil")
	}
	var ie *Error
	if !errors.As(err, &ie) {
		t.Fatalf("error type = %T, want *Error", err)
	}
	if ie.Status != 404 {
		t.Errorf("Status = %d, want 404", ie.Status)
	}
	if ie.Code != NOT_FOUND {
		t.Errorf("Code = %q, want NOT_FOUND", ie.Code)
	}
}

func TestHydrateByID_Found(t *testing.T) {
	g := buildToyGraph()
	ctx := &Ctx{Registry: g.Reg}

	a1 := toyARow{id: "a1", name: "Alpha", childFK: "b1", selfFK: "a2"}
	fetch := func(_ *Ctx, _ string) (any, error) {
		return a1, nil
	}

	inc := IncludeTree{"child": IncludeTree{}}
	raw, plan, err := HydrateByID(g.A, "a1", inc, nil, fetch, ctx, DefaultOptions)
	if err != nil {
		t.Fatalf("HydrateByID: %v", err)
	}
	if plan == nil {
		t.Fatal("plan is nil")
	}
	if len(raw) == 0 {
		t.Fatal("raw is empty")
	}
	var m map[string]json.RawMessage
	if err := json.Unmarshal(raw, &m); err != nil {
		t.Fatalf("unmarshal result: %v", err)
	}
	if string(m["id"]) != `"a1"` {
		t.Errorf("id = %s, want \"a1\"", m["id"])
	}
	if _, ok := m["child"]; !ok {
		t.Error("result missing 'child' (explicit include)")
	}
}

// With an empty include tree the default-cycle guard leaves the root plan
// childless, so the output is the scalar wire struct alone.
func TestHydrateEntity(t *testing.T) {
	g := buildToyGraph()
	ctx := &Ctx{Registry: g.Reg}

	doc := toyARow{id: "a2", name: "Beta", childFK: "b2", selfFK: ""}

	// Empty include: no default children survive the cycle guard on ToyA.
	raw, plan, err := HydrateEntity(g.A, doc, IncludeTree{}, nil, ctx, DefaultOptions)
	if err != nil {
		t.Fatalf("HydrateEntity: %v", err)
	}
	if plan == nil {
		t.Fatal("plan is nil")
	}
	var m map[string]json.RawMessage
	if err := json.Unmarshal(raw, &m); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if string(m["id"]) != `"a2"` {
		t.Errorf("id = %s, want \"a2\"", m["id"])
	}
	if string(m["name"]) != `"Beta"` {
		t.Errorf("name = %s, want \"Beta\"", m["name"])
	}

	inc := IncludeTree{"child": IncludeTree{}}
	raw2, plan2, err := HydrateEntity(g.A, doc, inc, nil, ctx, DefaultOptions)
	if err != nil {
		t.Fatalf("HydrateEntity with child: %v", err)
	}
	if !HasInclude(plan2, "child") {
		t.Error("plan2 should have 'child' edge")
	}
	var m2 map[string]json.RawMessage
	if err := json.Unmarshal(raw2, &m2); err != nil {
		t.Fatalf("unmarshal m2: %v", err)
	}
	if _, ok := m2["child"]; !ok {
		t.Error("result missing 'child' (explicit include)")
	}
}

func TestHasInclude(t *testing.T) {
	g := buildToyGraph()

	// Empty IncludeTree: the self-cycle guard cuts "self" at the first hop (root
	// is seeded with "ToyA" so a default self-edge to "ToyA" is immediately cut).
	// Result: no children.
	plan, err := ResolvePlan(g.A, IncludeTree{}, nil, DefaultOptions)
	if err != nil {
		t.Fatalf("ResolvePlan: %v", err)
	}

	// No default children survive for ToyA (self-cycle cut at root).
	if HasInclude(plan, "self") {
		t.Error("HasInclude('self') = true, want false (cut by cycle guard at root)")
	}
	if HasInclude(plan, "child") {
		t.Error("HasInclude('child') = true, want false (not in include tree)")
	}
	if HasInclude(plan, "bogus") {
		t.Error("HasInclude('bogus') = true, want false")
	}

	plan2, err := ResolvePlan(g.A, IncludeTree{"child": IncludeTree{}}, nil, DefaultOptions)
	if err != nil {
		t.Fatalf("ResolvePlan with child: %v", err)
	}
	if !HasInclude(plan2, "child") {
		t.Error("HasInclude('child') = false after explicit include, want true")
	}
	if HasInclude(plan2, "kids") {
		t.Error("HasInclude('kids') = true, want false (not requested)")
	}

	plan3, err := ResolvePlan(g.A, IncludeTree{"kids": IncludeTree{}}, nil, DefaultOptions)
	if err != nil {
		t.Fatalf("ResolvePlan with kids: %v", err)
	}
	if !HasInclude(plan3, "kids") {
		t.Error("HasInclude('kids') = false after explicit include, want true")
	}
}

func TestHydrateByQuery_BudgetPrecheck(t *testing.T) {
	A := costGraph(nil) // A --a(Limit 20)--> A; a.a costs 421 per document
	called := 0
	fetch := RootFetcher(func(_ *Ctx, _ QueryArgs) ([]any, int, bool, error) {
		called++
		return []any{}, 0, false, nil
	})
	opts := Options{Limits: Limits{MaxDepth: 4, MaxNodes: 50}}
	inc := IncludeTree{"a": IncludeTree{"a": nil}}
	// 421 × 200 = 84 200 > default MaxRows 50 000.
	_, _, err := HydrateByQuery(A, QueryArgs{Limit: 200}, inc, nil, fetch, &Ctx{}, opts)
	if errCode(err) != INCLUDE_BUDGET_EXCEEDED {
		t.Fatalf("err = %v, want INCLUDE_BUDGET_EXCEEDED", err)
	}
	if called != 0 {
		t.Fatal("root fetcher was called despite the budget pre-check")
	}
	// Unknown page size: no pre-check, fetch runs.
	if _, _, err := HydrateByQuery(A, QueryArgs{}, inc, nil, fetch, &Ctx{}, opts); err != nil {
		t.Fatalf("Limit 0: %v", err)
	}
	if called != 1 {
		t.Fatalf("root fetcher called %d times with Limit 0, want 1", called)
	}
}
