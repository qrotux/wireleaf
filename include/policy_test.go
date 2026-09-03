package include

import (
	"errors"
	"testing"
)

// policy_test.go — the plan-time edge-argument contract: SortPolicy, ArgPolicy
// and EdgeArg.Validate. Every rejection here is INVALID_INCLUDE at "path:arg",
// raised by ResolvePlan before any fetch.

// policyRoot builds a root resource whose single includable to-many edge
// "reviews" carries the given options.
func policyRoot(mods ...func(*Edge)) Resource {
	leaf := &tRes{name: "PolicyLeaf"}
	// A REVERSE edge (Backref set): the one kind whose loading contract honours
	// the built-in :sort, which these policy cases exercise.
	e := Edge{Target: func() Resource { return leaf }, Many: true, Includable: true, Backref: "rootId"}
	for _, m := range mods {
		m(&e)
	}
	return &tRes{
		name:  "PolicyRoot",
		edges: map[string]Edge{"reviews": e},
	}
}

// policySortCols is the whitelist used by the sort-policy cases.
var policySortCols = map[string]string{"postedAt": "posted_at"}

// assertInvalidInclude fails unless err is *Error{INVALID_INCLUDE} at wantPath.
func assertInvalidInclude(t *testing.T, err error, wantPath string) {
	t.Helper()
	if err == nil {
		t.Fatalf("want INVALID_INCLUDE at %q, got nil error", wantPath)
	}
	var ie *Error
	if !errors.As(err, &ie) {
		t.Fatalf("error type = %T, want *Error", err)
	}
	if ie.Code != INVALID_INCLUDE {
		t.Errorf("Code = %q, want %q", ie.Code, INVALID_INCLUDE)
	}
	if ie.Path != wantPath {
		t.Errorf("Path = %q, want %q", ie.Path, wantPath)
	}
}

// Under SortStrict an unknown :sort() key fails at resolve time, at "edge:sort".
func TestSortStrict_UnknownKeyRejected(t *testing.T) {
	root := policyRoot(func(e *Edge) { e.Sort = "postedAt"; e.SortCols = policySortCols })
	tree := IncludeTree{"reviews": IncludeTree{":sort": "bogus"}}

	_, err := ResolvePlan(root, tree, nil, DefaultOptions)
	assertInvalidInclude(t, err, "reviews:sort")
}

// A whitelisted key (with or without the '-' desc prefix) passes SortStrict and
// reaches PlanNode.Args unchanged.
func TestSortStrict_WhitelistedKeyPasses(t *testing.T) {
	root := policyRoot(func(e *Edge) { e.Sort = "postedAt"; e.SortCols = policySortCols })
	for _, key := range []string{"postedAt", "-postedAt"} {
		tree := IncludeTree{"reviews": IncludeTree{":sort": key}}
		plan, err := ResolvePlan(root, tree, nil, DefaultOptions)
		if err != nil {
			t.Fatalf(":sort(%s): unexpected error: %v", key, err)
		}
		if got := findChild(plan, "reviews").Args["sort"]; got != key {
			t.Errorf(":sort(%s): Args[sort] = %v, want %q", key, got, key)
		}
	}
}

// A non-string :sort() value (multi-value arg) cannot name a column → rejected
// under SortStrict.
func TestSortStrict_NonStringRejected(t *testing.T) {
	root := policyRoot(func(e *Edge) { e.Sort = "postedAt"; e.SortCols = policySortCols })
	tree := IncludeTree{"reviews": IncludeTree{":sort": []string{"postedAt", "createdAt"}}}

	_, err := ResolvePlan(root, tree, nil, DefaultOptions)
	assertInvalidInclude(t, err, "reviews:sort")
}

// An edge with no SortCols makes ":sort" an undeclared argument: rejected under
// ArgsStrict regardless of the sort policy.
func TestSortStrict_NoSortColsRejected(t *testing.T) {
	root := policyRoot() // no SortCols whitelist
	tree := IncludeTree{"reviews": IncludeTree{":sort": "postedAt"}}

	_, err := ResolvePlan(root, tree, nil, DefaultOptions)
	assertInvalidInclude(t, err, "reviews:sort")
}

// Under SortFallback the same unknown key resolves without error and stays in
// PlanNode.Args; sort.go's lookup is what drops it later.
func TestSortFallback_UnknownKeyAccepted(t *testing.T) {
	root := policyRoot(func(e *Edge) { e.Sort = "postedAt"; e.SortCols = policySortCols })
	tree := IncludeTree{"reviews": IncludeTree{":sort": "bogus"}}

	plan, err := ResolvePlan(root, tree, nil, Options{Limits: DefaultLimits, SortPolicy: SortFallback})
	if err != nil {
		t.Fatalf("SortFallback should accept an unknown sort key, got %v", err)
	}
	child := findChild(plan, "reviews")
	if got := child.Args["sort"]; got != "bogus" {
		t.Errorf("Args[sort] = %v, want %q", got, "bogus")
	}
	if got := reverseSortKey(child); got != "posted_at" {
		t.Errorf("reverseSortKey = %q, want the edge default %q", got, "posted_at")
	}
}

// Under ArgsStrict an undeclared argument fails the plan at "path:arg".
func TestArgsStrict_UndeclaredRejected(t *testing.T) {
	root := policyRoot()
	tree := IncludeTree{"reviews": IncludeTree{":tags": []string{"a", "b"}}}

	_, err := ResolvePlan(root, tree, nil, DefaultOptions)
	assertInvalidInclude(t, err, "reviews:tags")
}

// Under ArgsTolerant the same undeclared argument passes straight through to
// PlanNode.Args.
func TestArgsTolerant_UndeclaredPassesThrough(t *testing.T) {
	root := policyRoot()
	tree := IncludeTree{"reviews": IncludeTree{":tags": []string{"a", "b"}}}

	plan, err := ResolvePlan(root, tree, nil, Options{Limits: DefaultLimits, ArgPolicy: ArgsTolerant})
	if err != nil {
		t.Fatalf("ArgsTolerant should accept an undeclared arg, got %v", err)
	}
	got, ok := findChild(plan, "reviews").Args["tags"].([]string)
	if !ok || len(got) != 2 || got[0] != "a" || got[1] != "b" {
		t.Errorf("Args[tags] = %#v, want []string{a,b}", findChild(plan, "reviews").Args["tags"])
	}
}

// "limit" is a built-in on every edge: it needs no declaration and survives
// ArgsStrict. It is COERCED to int at plan time.
func TestArgsStrict_BuiltinLimitPasses(t *testing.T) {
	root := policyRoot()
	tree := IncludeTree{"reviews": IncludeTree{":limit": "5"}}

	plan, err := ResolvePlan(root, tree, nil, DefaultOptions)
	if err != nil {
		t.Fatalf("built-in :limit must pass ArgsStrict, got %v", err)
	}
	got := findChild(plan, "reviews").Args["limit"]
	if n, ok := got.(int); !ok || n != 5 {
		t.Errorf("Args[limit] = %#v, want int 5 (coerced at plan time)", got)
	}
}

// A non-integer, out-of-range or multi-value :limit fails the plan at
// "path:limit" — under BOTH arg policies (the built-in is never tolerated raw).
func TestLimitCoercion_Rejected(t *testing.T) {
	cases := []struct {
		name string
		raw  any
	}{
		{"non-numeric", "abc"},
		{"empty (bare arg)", ""},
		{"zero", "0"},
		{"negative", "-3"},
		{"float", "2.5"},
		{"multi-value", []string{"1", "2"}},
	}
	for _, tc := range cases {
		for _, opts := range []Options{DefaultOptions, {Limits: DefaultLimits, ArgPolicy: ArgsTolerant, SortPolicy: SortFallback}} {
			t.Run(tc.name, func(t *testing.T) {
				root := policyRoot()
				tree := IncludeTree{"reviews": IncludeTree{":limit": tc.raw}}
				_, err := ResolvePlan(root, tree, nil, opts)
				assertInvalidInclude(t, err, "reviews:limit")
			})
		}
	}
}

// A DECLARED arg named "limit" must NOT shadow the built-in: its validator
// runs, AND the built-in coercion still applies, so Args["limit"] is an int.
// (graph.Compile rejects such a declaration outright; this is the belt for
// hand-built edges declared outside the builder.)
func TestDeclaredLimitArgDoesNotShadowBuiltin(t *testing.T) {
	validated := false
	root := policyRoot(func(e *Edge) {
		e.Args = []EdgeArg{{
			Name: "limit",
			Validate: func(raw any) error {
				validated = true
				if raw != "3" {
					return errors.New("unexpected raw limit")
				}
				return nil
			},
		}}
	})
	tree := IncludeTree{"reviews": IncludeTree{":limit": "3"}}

	plan, err := ResolvePlan(root, tree, nil, DefaultOptions)
	if err != nil {
		t.Fatalf("declared :limit rejected: %v", err)
	}
	if !validated {
		t.Error("the declared validator was never invoked")
	}
	got := findChild(plan, "reviews").Args["limit"]
	if n, ok := got.(int); !ok || n != 3 {
		t.Errorf("Args[limit] = %#v, want int 3 (built-in coercion still applies)", got)
	}
}

// A :limit on a BARE edge is a contract error: a bare edge fetches all rows.
func TestLimitOnBareEdge_Rejected(t *testing.T) {
	root := policyRoot(func(e *Edge) { e.Bare = true })
	tree := IncludeTree{"reviews": IncludeTree{":limit": "5"}}

	_, err := ResolvePlan(root, tree, nil, DefaultOptions)
	assertInvalidInclude(t, err, "reviews:limit")
}

// A declared arg with a passing validator reaches PlanNode.Args.
func TestEdgeArgValidate_Pass(t *testing.T) {
	root := policyRoot(func(e *Edge) {
		e.Args = []EdgeArg{{
			Name: "status",
			Validate: func(raw any) error {
				if raw == "published" {
					return nil
				}
				return errors.New("unknown status")
			},
		}}
	})
	tree := IncludeTree{"reviews": IncludeTree{":status": "published"}}

	plan, err := ResolvePlan(root, tree, nil, DefaultOptions)
	if err != nil {
		t.Fatalf("valid declared arg rejected: %v", err)
	}
	if got := findChild(plan, "reviews").Args["status"]; got != "published" {
		t.Errorf("Args[status] = %v, want %q", got, "published")
	}
}

// A declared arg whose validator errors fails the plan at "path:arg" — under
// ArgsTolerant too, since the declaration is what is being enforced.
func TestEdgeArgValidate_Fail(t *testing.T) {
	root := policyRoot(func(e *Edge) {
		e.Args = []EdgeArg{{
			Name:     "status",
			Validate: func(any) error { return errors.New("unknown status") },
		}}
	})
	tree := IncludeTree{"reviews": IncludeTree{":status": "draft"}}

	for _, opts := range []Options{
		DefaultOptions,
		{Limits: DefaultLimits, ArgPolicy: ArgsTolerant},
	} {
		_, err := ResolvePlan(root, tree, nil, opts)
		assertInvalidInclude(t, err, "reviews:status")
	}
}

// A declared arg with a nil Validate accepts any value.
func TestEdgeArgValidate_NilAcceptsAnything(t *testing.T) {
	root := policyRoot(func(e *Edge) { e.Args = []EdgeArg{{Name: "status"}} })
	tree := IncludeTree{"reviews": IncludeTree{":status": "whatever"}}

	if _, err := ResolvePlan(root, tree, nil, DefaultOptions); err != nil {
		t.Fatalf("nil Validate must accept any value, got %v", err)
	}
}

// A declaration named "sort" overrides the built-in sort handling: the
// validator decides, the SortCols whitelist is not consulted.
func TestEdgeArgValidate_OverridesBuiltinSort(t *testing.T) {
	root := policyRoot(func(e *Edge) {
		e.SortCols = policySortCols
		e.Args = []EdgeArg{{Name: "sort"}} // accepts anything
	})
	tree := IncludeTree{"reviews": IncludeTree{":sort": "bogus"}}

	if _, err := ResolvePlan(root, tree, nil, DefaultOptions); err != nil {
		t.Fatalf("a declared `sort` arg must win over the built-in check, got %v", err)
	}
}

// Args nested one level down are validated against THEIR inbound edge, and the
// error path is the full dotted path.
func TestArgsValidatedAtNestedPath(t *testing.T) {
	leaf := &tRes{name: "PolicyLeaf"}
	mid := &tRes{
		name: "PolicyMid",
		edges: map[string]Edge{
			"comments": {Target: func() Resource { return leaf }, Many: true, Includable: true},
		},
	}
	root := &tRes{
		name: "PolicyRoot",
		edges: map[string]Edge{
			"reviews": {Target: func() Resource { return mid }, Many: true, Includable: true},
		},
	}
	tree := IncludeTree{"reviews": IncludeTree{"comments": IncludeTree{":tags": "x"}}}

	_, err := ResolvePlan(root, tree, nil, DefaultOptions)
	assertInvalidInclude(t, err, "reviews.comments:tags")
}

// The built-ins are rejected on edge kinds whose loading contract cannot
// honour them — an accepted-and-ignored :limit/:sort would be a silent no-op.
func TestBuiltins_RejectedOnWrongEdgeKinds(t *testing.T) {
	leaf := &tRes{name: "KindLeaf"}
	edgeOf := func(mods func(*Edge)) Resource {
		e := Edge{Target: func() Resource { return leaf }, Includable: true}
		mods(&e)
		return &tRes{name: "KindRoot", edges: map[string]Edge{"child": e}}
	}
	cases := []struct {
		name string
		root Resource
		arg  string
	}{
		{"limit on to-one", edgeOf(func(e *Edge) {}), ":limit"},
		{"sort on to-one", edgeOf(func(e *Edge) {}), ":sort"},
		{"sort on forward-hasMany", edgeOf(func(e *Edge) { e.Many = true }), ":sort"},
		{"limit on computed", edgeOf(func(e *Edge) { e.Computed = true; e.Target = nil }), ":limit"},
		{"sort on computed", edgeOf(func(e *Edge) { e.Computed = true; e.Target = nil }), ":sort"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			tree := IncludeTree{"child": IncludeTree{tc.arg: "5"}}
			_, err := ResolvePlan(tc.root, tree, nil, Options{Limits: DefaultLimits, SortPolicy: SortFallback, ArgPolicy: ArgsTolerant})
			assertInvalidInclude(t, err, "child"+tc.arg)
		})
	}
}

// ...and stays legal where the contract honours it: :limit on forward-hasMany.
func TestBuiltins_LimitLegalOnForwardHasMany(t *testing.T) {
	leaf := &tRes{name: "KindLeaf"}
	root := &tRes{name: "KindRoot", edges: map[string]Edge{
		"child": {Target: func() Resource { return leaf }, Many: true, Includable: true, Limit: 10},
	}}
	plan, err := ResolvePlan(root, IncludeTree{"child": IncludeTree{":limit": "5"}}, nil, DefaultOptions)
	if err != nil {
		t.Fatalf("forward-hasMany :limit: %v", err)
	}
	if got := findChild(plan, "child").Args["limit"]; got != 5 {
		t.Errorf("Args[limit] = %v, want 5", got)
	}
}
