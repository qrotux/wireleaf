package include

import "testing"

// A minimal, self-contained Resource implementation used ONLY by resolve_test
// and policy_test. It deliberately does not depend on toygraph_test.go.

type tRes struct {
	name     string
	defaults []string
	edges    map[string]Edge
}

func (r *tRes) Name() string             { return r.name }
func (r *tRes) Slug() string             { return r.name }
func (r *tRes) Fields() []string         { return nil }
func (r *tRes) Defaults() []string       { return r.defaults }
func (r *tRes) Edges() map[string]Edge   { return r.edges }
func (r *tRes) IDOf(any) string          { return "" }
func (r *tRes) Serialize(any, *Ctx) any  { return nil }
func (r *tRes) Enrich([]any, *Ctx) error { return nil }

// testOptions is a generous default used by scenarios that don't probe a cap.
var testOptions = Options{Limits: Limits{MaxDepth: 4, MaxNodes: 50}}

// childKeys collects the edgeKey of every direct child of a plan node.
func childKeys(p *PlanNode) []string {
	keys := make([]string, 0, len(p.Children))
	for _, c := range p.Children {
		keys = append(keys, c.EdgeKey)
	}
	return keys
}

// findChild returns the direct child with the given edge key, or nil.
func findChild(p *PlanNode, key string) *PlanNode {
	for _, c := range p.Children {
		if c.EdgeKey == key {
			return c
		}
	}
	return nil
}

// errCode extracts the Error code from err, or "" if not an *Error.
func errCode(err error) Code {
	if e, ok := err.(*Error); ok {
		return e.Code
	}
	return ""
}

// Empty client tree → only the root's defaults are expanded.
func TestResolveEmptyTreeExpandsOnlyDefaults(t *testing.T) {
	B := &tRes{name: "B"}
	A := &tRes{
		name:     "A",
		defaults: []string{"child"},
		edges: map[string]Edge{
			// child is a default + includable to-one → B.
			"child": {Target: func() Resource { return B }, Includable: true},
			// secret is present but NOT a default and NOT includable.
			"secret": {Target: func() Resource { return B }},
		},
	}

	plan, err := ResolvePlan(A, IncludeTree{}, nil, testOptions)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	got := childKeys(plan)
	want := A.Defaults() // ["child"]
	if len(got) != len(want) {
		t.Fatalf("child keys = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("child keys = %v, want %v", got, want)
		}
	}
	// Root metadata sanity.
	if plan.Path != "" {
		t.Errorf("root path = %q, want empty", plan.Path)
	}
	if plan.Resource != A {
		t.Errorf("root resource mismatch")
	}
	// The expanded default child carries its path + resource + edge.
	c := findChild(plan, "child")
	if c == nil {
		t.Fatalf("default child 'child' missing")
	}
	if c.Path != "child" {
		t.Errorf("child path = %q, want %q", c.Path, "child")
	}
	if c.Resource != B {
		t.Errorf("child resource mismatch")
	}
	if c.Edge == nil {
		t.Errorf("child edge not carried")
	}
}

// A client include naming an unknown edge → INVALID_INCLUDE.
func TestResolveClientUnknownEdge(t *testing.T) {
	A := &tRes{name: "A", edges: map[string]Edge{}}
	_, err := ResolvePlan(A, IncludeTree{"nope": IncludeTree{}}, nil, testOptions)
	if errCode(err) != INVALID_INCLUDE {
		t.Fatalf("err = %v, want INVALID_INCLUDE", err)
	}
}

// A client include on an existing-but-non-includable edge → INVALID_INCLUDE.
func TestResolveClientNonIncludableEdge(t *testing.T) {
	B := &tRes{name: "B"}
	A := &tRes{
		name: "A",
		edges: map[string]Edge{
			"secret": {Target: func() Resource { return B }}, // not includable
		},
	}
	_, err := ResolvePlan(A, IncludeTree{"secret": IncludeTree{}}, nil, testOptions)
	if errCode(err) != INVALID_INCLUDE {
		t.Fatalf("err = %v, want INVALID_INCLUDE", err)
	}
}

// A client include deeper than MaxDepth → INCLUDE_TOO_DEEP.
// Self-edge chain a.a.a with MaxDepth:2.
func TestResolveClientTooDeep(t *testing.T) {
	A := &tRes{name: "A"}
	A.edges = map[string]Edge{
		"a": {Target: func() Resource { return A }, Includable: true},
	}
	// a.a.a = clientDepth 3 > MaxDepth 2.
	tree := IncludeTree{"a": IncludeTree{"a": IncludeTree{"a": IncludeTree{}}}}
	_, err := ResolvePlan(A, tree, nil, Options{Limits: Limits{MaxDepth: 2, MaxNodes: 50}})
	if errCode(err) != INCLUDE_TOO_DEEP {
		t.Fatalf("err = %v, want INCLUDE_TOO_DEEP", err)
	}
}

// More client sibling edges than MaxNodes → INCLUDE_TOO_DEEP.
func TestResolveMaxNodesExceeded(t *testing.T) {
	B := &tRes{name: "B"}
	A := &tRes{
		name: "A",
		edges: map[string]Edge{
			"x": {Target: func() Resource { return B }, Includable: true},
			"y": {Target: func() Resource { return B }, Includable: true},
			"z": {Target: func() Resource { return B }, Includable: true},
		},
	}
	tree := IncludeTree{"x": IncludeTree{}, "y": IncludeTree{}, "z": IncludeTree{}}
	_, err := ResolvePlan(A, tree, nil, Options{Limits: Limits{MaxDepth: 4, MaxNodes: 2}})
	if errCode(err) != INCLUDE_TOO_DEEP {
		t.Fatalf("err = %v, want INCLUDE_TOO_DEEP", err)
	}
}

// A DEFAULT self-cycle terminates cleanly (no error, no infinite recursion).
// A.defaults=[self], self→A (includable). The default-chain is seeded with "A",
// so the self default re-entering A is cut at the first hop.
func TestResolveDefaultCycleTerminates(t *testing.T) {
	A := &tRes{name: "A", defaults: []string{"self"}}
	A.edges = map[string]Edge{
		"self": {Target: func() Resource { return A }, Includable: true},
	}
	plan, err := ResolvePlan(A, IncludeTree{}, nil, testOptions)
	if err != nil {
		t.Fatalf("unexpected error (cycle should terminate): %v", err)
	}
	// The self default re-enters A (already in seenDefaults) → pruned at the first hop.
	if findChild(plan, "self") != nil {
		t.Fatalf("self default should be cut by the cycle guard, got child %q", "self")
	}
}

// Excluding an unknown path → INVALID_INCLUDE.
func TestResolveExcludeUnknownPath(t *testing.T) {
	B := &tRes{name: "B"}
	A := &tRes{
		name:     "A",
		defaults: []string{"child"},
		edges: map[string]Edge{
			"child": {Target: func() Resource { return B }, Includable: true},
		},
	}
	_, err := ResolvePlan(A, IncludeTree{}, [][]string{{"ghost"}}, testOptions)
	if errCode(err) != INVALID_INCLUDE {
		t.Fatalf("err = %v, want INVALID_INCLUDE", err)
	}
}

// Excluding a real default child removes it from the plan.
func TestResolveExcludeRemovesDefaultChild(t *testing.T) {
	B := &tRes{name: "B"}
	A := &tRes{
		name:     "A",
		defaults: []string{"child"},
		edges: map[string]Edge{
			"child": {Target: func() Resource { return B }, Includable: true},
		},
	}
	plan, err := ResolvePlan(A, IncludeTree{}, [][]string{{"child"}}, testOptions)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if findChild(plan, "child") != nil {
		t.Fatalf("excluded default child 'child' should be absent")
	}
}

// Args are carried through, `:`-prefix stripped, and a nested child survives.
func TestResolveArgsCarriedThrough(t *testing.T) {
	C := &tRes{name: "C"}
	B := &tRes{
		name: "B",
		edges: map[string]Edge{
			"cover": {Target: func() Resource { return C }, Includable: true},
		},
	}
	A := &tRes{
		name: "A",
		edges: map[string]Edge{
			"book": {Target: func() Resource { return B }, Many: true, Includable: true},
		},
	}
	// {"book":{":limit":"5","cover":{}}}
	tree := IncludeTree{
		"book": IncludeTree{
			":limit": "5",
			"cover":  IncludeTree{},
		},
	}
	plan, err := ResolvePlan(A, tree, nil, testOptions)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	book := findChild(plan, "book")
	if book == nil {
		t.Fatalf("book child missing")
	}
	if !book.Many {
		t.Errorf("book.Many = false, want true (ToMany edge)")
	}
	if v, ok := book.Args["limit"].(int); !ok || v != 5 {
		t.Errorf("book.Args = %v, want {limit:5} (int, prefix stripped)", book.Args)
	}
	if _, leaked := book.Args[":limit"]; leaked {
		t.Errorf("book.Args still contains the :-prefixed key: %v", book.Args)
	}
	cover := findChild(book, "cover")
	if cover == nil {
		t.Fatalf("nested 'cover' child missing")
	}
	if cover.Path != "book.cover" {
		t.Errorf("cover.Path = %q, want %q", cover.Path, "book.cover")
	}
}

// A multi-value arg ([]string) is passed through unchanged. The edge DECLARES
// `tags`, so the pass-through holds under the default ArgsStrict policy.
func TestResolveArgsMultiValue(t *testing.T) {
	B := &tRes{name: "B"}
	A := &tRes{
		name: "A",
		edges: map[string]Edge{
			"book": {Target: func() Resource { return B }, Many: true, Includable: true, Args: []EdgeArg{{Name: "tags"}}},
		},
	}
	tree := IncludeTree{"book": IncludeTree{":tags": []string{"a", "b"}}}
	plan, err := ResolvePlan(A, tree, nil, testOptions)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	book := findChild(plan, "book")
	got, ok := book.Args["tags"].([]string)
	if !ok || len(got) != 2 || got[0] != "a" || got[1] != "b" {
		t.Errorf("book.Args[tags] = %#v, want []string{a,b}", book.Args["tags"])
	}
}

// A "stale default" (default key with no matching edge) is silently skipped.
func TestResolveStaleDefaultSkipped(t *testing.T) {
	A := &tRes{name: "A", defaults: []string{"ghost"}, edges: map[string]Edge{}}
	plan, err := ResolvePlan(A, IncludeTree{}, nil, testOptions)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(plan.Children) != 0 {
		t.Errorf("stale default produced children: %v", childKeys(plan))
	}
}

// A diamond (two sibling default edges to the same resource) is NOT pruned:
// the cycle guard only cuts a re-entrant default chain, not distinct siblings.
func TestResolveDiamondDefaultsSurvive(t *testing.T) {
	U := &tRes{name: "U"}
	A := &tRes{
		name:     "A",
		defaults: []string{"author", "editor"},
		edges: map[string]Edge{
			"author": {Target: func() Resource { return U }, Includable: true},
			"editor": {Target: func() Resource { return U }, Includable: true},
		},
	}
	plan, err := ResolvePlan(A, IncludeTree{}, nil, testOptions)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if findChild(plan, "author") == nil || findChild(plan, "editor") == nil {
		t.Fatalf("both sibling defaults must survive; got %v", childKeys(plan))
	}
}

// Tolerant exclude: a prefix-overlapping exclude (remove ancestor, then a
// descendant of the removed ancestor) is a silent no-op, not a 400.
func TestResolveExcludeTolerantPrefix(t *testing.T) {
	C := &tRes{name: "C"}
	B := &tRes{
		name:     "B",
		defaults: []string{"author"},
		edges: map[string]Edge{
			"author": {Target: func() Resource { return C }, Includable: true},
		},
	}
	A := &tRes{
		name:     "A",
		defaults: []string{"reviews"},
		edges: map[string]Edge{
			"reviews": {Target: func() Resource { return B }, Many: true, Includable: true},
		},
	}
	// Remove "reviews"; then ["reviews","author"] walks into the removed node → no-op.
	plan, err := ResolvePlan(A, IncludeTree{}, [][]string{{"reviews"}, {"reviews", "author"}}, testOptions)
	if err != nil {
		t.Fatalf("prefix-overlapping exclude should not error: %v", err)
	}
	if findChild(plan, "reviews") != nil {
		t.Fatalf("'reviews' should be removed")
	}
}

// Root-level args (a ":"-prefixed key in the root subtree) → INVALID_INCLUDE.
// The root carries no edge, so it accepts no arguments; covers the
// programmatic-IncludeTree contract gap.
func TestResolveRootArgsRejected(t *testing.T) {
	A := &tRes{name: "A", edges: map[string]Edge{}}
	_, err := ResolvePlan(A, IncludeTree{":foo": "bar"}, nil, testOptions)
	if errCode(err) != INVALID_INCLUDE {
		t.Fatalf("err = %v, want INVALID_INCLUDE", err)
	}
}

// Multi-key client include yields a STABLE, sorted PlanNode.Children order.
// Two includable sibling edges supplied as {z:{}, a:{}} must always resolve to
// child EdgeKey order [a, z] (client-only keys are sorted), regardless of Go's
// randomized map iteration. Run the resolve repeatedly to smoke out flakiness.
func TestResolveClientChildOrderStable(t *testing.T) {
	B := &tRes{name: "B"}
	A := &tRes{
		name: "A",
		edges: map[string]Edge{
			"a": {Target: func() Resource { return B }, Includable: true},
			"z": {Target: func() Resource { return B }, Includable: true},
		},
	}
	tree := IncludeTree{"z": IncludeTree{}, "a": IncludeTree{}}
	want := []string{"a", "z"}
	for i := 0; i < 50; i++ {
		plan, err := ResolvePlan(A, tree, nil, testOptions)
		if err != nil {
			t.Fatalf("iter %d: unexpected error: %v", i, err)
		}
		got := childKeys(plan)
		if len(got) != len(want) {
			t.Fatalf("iter %d: child keys = %v, want %v", i, got, want)
		}
		for j := range want {
			if got[j] != want[j] {
				t.Fatalf("iter %d: child keys = %v, want sorted %v", i, got, want)
			}
		}
	}
}

// Defaults keep declaration-order slice position; client-only keys come after,
// sorted. defaults=[m]; client={z,a} → order [m, a, z].
func TestResolveDefaultsThenSortedClient(t *testing.T) {
	B := &tRes{name: "B"}
	A := &tRes{
		name:     "A",
		defaults: []string{"m"},
		edges: map[string]Edge{
			"m": {Target: func() Resource { return B }, Includable: true},
			"a": {Target: func() Resource { return B }, Includable: true},
			"z": {Target: func() Resource { return B }, Includable: true},
		},
	}
	tree := IncludeTree{"z": IncludeTree{}, "a": IncludeTree{}}
	want := []string{"m", "a", "z"}
	for i := 0; i < 50; i++ {
		plan, err := ResolvePlan(A, tree, nil, testOptions)
		if err != nil {
			t.Fatalf("iter %d: unexpected error: %v", i, err)
		}
		got := childKeys(plan)
		if len(got) != len(want) {
			t.Fatalf("iter %d: child keys = %v, want %v", i, got, want)
		}
		for j := range want {
			if got[j] != want[j] {
				t.Fatalf("iter %d: child keys = %v, want %v", i, got, want)
			}
		}
	}
}

// DefaultLimits exposes the engine-wide {4,50}; DefaultOptions is strict on
// both policies.
func TestDefaultOptionsValues(t *testing.T) {
	want := Limits{MaxDepth: 4, MaxNodes: 50, MaxCost: 5000, MaxRows: 50000, MaxFilterDepth: 4, MaxFilterMany: 2, MaxFilterNodes: 32, MaxFilterSubqueries: 8}
	if DefaultLimits != want {
		t.Errorf("DefaultLimits = %+v, want %+v", DefaultLimits, want)
	}
	if DefaultOptions.Limits != DefaultLimits {
		t.Errorf("DefaultOptions.Limits = %+v, want %+v", DefaultOptions.Limits, DefaultLimits)
	}
	if DefaultOptions.SortPolicy != SortStrict || DefaultOptions.ArgPolicy != ArgsStrict {
		t.Errorf("DefaultOptions policies = %v/%v, want SortStrict/ArgsStrict",
			DefaultOptions.SortPolicy, DefaultOptions.ArgPolicy)
	}
}

// A computed edge has NO Target: its value is produced by application code.
// It still occupies the ordinary edge namespace and passes through the same
// Includable gate, arg validation and node accounting — but it is a plan LEAF
// (a child segment under it is a client error) and the engine never fetches it.

// computedGraph returns a tRes root with:
//
//	child  → to-one B (includable)
//	stats  → computed (includable, declares arg "kind" rejecting "bad")
//	hidden → computed, NOT includable
func computedGraph() (*tRes, *tRes) {
	B := &tRes{name: "B"}
	A := &tRes{
		name: "A",
		edges: map[string]Edge{
			"child": {Target: func() Resource { return B }, Includable: true},
			"stats": {
				Computed:   true,
				Includable: true,
				Args: []EdgeArg{{Name: "kind", Validate: func(raw any) error {
					if raw == "bad" {
						return errBadComputedKind
					}
					return nil
				}}},
			},
			"hidden": {Computed: true},
		},
	}
	return A, B
}

var errBadComputedKind = errorString("bad kind")

type errorString string

func (e errorString) Error() string { return string(e) }

// A computed edge plans like any other edge, sets PlanNode.Computed, carries a
// nil Resource (no target) and classifies as KindComputed.
func TestComputedEdgePlans(t *testing.T) {
	A, _ := computedGraph()

	plan, err := ResolvePlan(A, IncludeTree{"stats": IncludeTree{}}, nil, testOptions)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	node := findChild(plan, "stats")
	if node == nil {
		t.Fatalf("stats missing from plan (children %v)", childKeys(plan))
	}
	if !node.Computed {
		t.Error("PlanNode.Computed = false, want true")
	}
	if node.Edge == nil || !node.Edge.Computed {
		t.Fatal("plan node lost its computed Edge")
	}
	if got := EdgeKind(*node.Edge); got != KindComputed {
		t.Errorf("EdgeKind = %v, want %v", got, KindComputed)
	}
	if node.Resource != nil {
		t.Errorf("computed node Resource = %v, want nil (no target)", node.Resource)
	}
	if node.Path != "stats" {
		t.Errorf("path = %q, want %q", node.Path, "stats")
	}
	if len(node.Children) != 0 {
		t.Errorf("computed node has children %v, want none", childKeys(node))
	}
}

// Deny-by-default applies to computed edges too.
func TestComputedNotIncludableRejected(t *testing.T) {
	A, _ := computedGraph()

	_, err := ResolvePlan(A, IncludeTree{"hidden": IncludeTree{}}, nil, testOptions)
	if errCode(err) != INVALID_INCLUDE {
		t.Fatalf("err = %v (code %v), want INVALID_INCLUDE", err, errCode(err))
	}
	if e, ok := err.(*Error); ok && e.Path != "hidden" {
		t.Errorf("path = %q, want %q", e.Path, "hidden")
	}
}

// A child segment under a computed key is INVALID_INCLUDE at the CHILD path.
func TestComputedChildSegmentRejected(t *testing.T) {
	A, _ := computedGraph()

	_, err := ResolvePlan(A, IncludeTree{"stats": IncludeTree{"user": IncludeTree{}}}, nil, testOptions)
	if errCode(err) != INVALID_INCLUDE {
		t.Fatalf("err = %v (code %v), want INVALID_INCLUDE", err, errCode(err))
	}
	e, ok := err.(*Error)
	if !ok {
		t.Fatalf("err type %T, want *Error", err)
	}
	if e.Path != "stats.user" {
		t.Errorf("path = %q, want %q", e.Path, "stats.user")
	}
}

// Declared args on a computed edge are validated; an undeclared one fails
// under ArgsStrict and passes through under ArgsTolerant.
func TestComputedArgsValidated(t *testing.T) {
	A, _ := computedGraph()

	// declared + accepted
	plan, err := ResolvePlan(A, IncludeTree{"stats": IncludeTree{":kind": "good"}}, nil, testOptions)
	if err != nil {
		t.Fatalf("declared arg rejected: %v", err)
	}
	node := findChild(plan, "stats")
	if node == nil || node.Args["kind"] != "good" {
		t.Fatalf("args = %v, want kind=good", node.Args)
	}

	// declared + validator rejects
	_, err = ResolvePlan(A, IncludeTree{"stats": IncludeTree{":kind": "bad"}}, nil, testOptions)
	if errCode(err) != INVALID_INCLUDE {
		t.Fatalf("bad value err = %v, want INVALID_INCLUDE", err)
	}
	if e, ok := err.(*Error); ok && e.Path != "stats:kind" {
		t.Errorf("path = %q, want %q", e.Path, "stats:kind")
	}

	// undeclared under ArgsStrict → rejected
	_, err = ResolvePlan(A, IncludeTree{"stats": IncludeTree{":nope": "1"}}, nil, testOptions)
	if errCode(err) != INVALID_INCLUDE {
		t.Fatalf("undeclared arg err = %v, want INVALID_INCLUDE", err)
	}
	if e, ok := err.(*Error); ok && e.Path != "stats:nope" {
		t.Errorf("path = %q, want %q", e.Path, "stats:nope")
	}

	// undeclared under ArgsTolerant → passed through
	tolerant := Options{Limits: testOptions.Limits, ArgPolicy: ArgsTolerant}
	plan, err = ResolvePlan(A, IncludeTree{"stats": IncludeTree{":nope": "1"}}, nil, tolerant)
	if err != nil {
		t.Fatalf("tolerant policy rejected undeclared arg: %v", err)
	}
	if node := findChild(plan, "stats"); node == nil || node.Args["nope"] != "1" {
		t.Fatalf("tolerant args = %v, want nope=1", node.Args)
	}
}

// A computed edge counts toward MaxNodes like any other client edge.
func TestComputedCountsTowardMaxNodes(t *testing.T) {
	A, _ := computedGraph()
	opts := Options{Limits: Limits{MaxDepth: 4, MaxNodes: 1}}

	_, err := ResolvePlan(A, IncludeTree{"child": IncludeTree{}, "stats": IncludeTree{}}, nil, opts)
	if errCode(err) != INCLUDE_TOO_DEEP {
		t.Fatalf("err = %v (code %v), want INCLUDE_TOO_DEEP", err, errCode(err))
	}
}

// Excluding a computed key removes it from the plan (ordinary exclude mechanics).
func TestComputedExcluded(t *testing.T) {
	A, _ := computedGraph()

	plan, err := ResolvePlan(A,
		IncludeTree{"child": IncludeTree{}, "stats": IncludeTree{}},
		[][]string{{"stats"}}, testOptions)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if findChild(plan, "stats") != nil {
		t.Errorf("stats still present after exclude (children %v)", childKeys(plan))
	}
	if findChild(plan, "child") == nil {
		t.Errorf("exclude removed the wrong child (children %v)", childKeys(plan))
	}
}

// Get returns the DIRECT child with the given edge key, nil otherwise — the
// typed sibling of HasInclude.
func TestPlanGet(t *testing.T) {
	C := &tRes{name: "C"}
	B := &tRes{name: "B", edges: map[string]Edge{
		"leaf": {Target: func() Resource { return C }, Includable: true},
	}}
	A := &tRes{name: "A", edges: map[string]Edge{
		"child": {Target: func() Resource { return B }, Includable: true},
		"stats": {Computed: true, Includable: true},
	}}

	plan, err := ResolvePlan(A,
		IncludeTree{"child": IncludeTree{"leaf": IncludeTree{}}, "stats": IncludeTree{}},
		nil, testOptions)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	child := plan.Get("child")
	if child == nil || child.EdgeKey != "child" {
		t.Fatalf("Get(child) = %v, want the child node", child)
	}
	if got := plan.Get("stats"); got == nil || !got.Computed {
		t.Errorf("Get(stats) = %v, want the computed node", got)
	}
	// direct children ONLY: a grandchild key is not found at the root.
	if got := plan.Get("leaf"); got != nil {
		t.Errorf("Get(leaf) at root = %v, want nil (grandchild)", got)
	}
	if got := child.Get("leaf"); got == nil {
		t.Error("Get(leaf) on the child node = nil, want the grandchild")
	}
	if got := plan.Get("bogus"); got != nil {
		t.Errorf("Get(bogus) = %v, want nil", got)
	}
	// Get agrees with HasInclude on direct children.
	for _, k := range []string{"child", "stats", "leaf", "bogus"} {
		if (plan.Get(k) != nil) != HasInclude(plan, k) {
			t.Errorf("Get(%q)/HasInclude(%q) disagree", k, k)
		}
	}
	// nil receiver is safe (a missing lookup chains).
	var nilNode *PlanNode
	if got := nilNode.Get("child"); got != nil {
		t.Errorf("nil.Get = %v, want nil", got)
	}
}

// inArrayGraph returns a root with a single includable IN-ARRAY edge that also
// (illegally, for a hand-built edge) declares a limit ceiling and a sort
// whitelist — the plan must reject the client args regardless.
func inArrayGraph() *tRes {
	B := &tRes{name: "B"}
	return &tRes{
		name: "A",
		edges: map[string]Edge{
			"attendees": {Target: func() Resource { return B }, Many: true, ArrayPath: "participants", SubField: "user", Includable: true, Limit: 5, SortCols: map[string]string{"name": "name"}, ForeignKeys: func(any) []string { return nil }},
		},
	}
}

// An in-array edge has no EdgeQuery: a client :limit on one is INVALID_INCLUDE.
func TestInArrayClientLimitRejected(t *testing.T) {
	A := inArrayGraph()

	_, err := ResolvePlan(A,
		IncludeTree{"attendees": IncludeTree{":limit": "2"}}, nil, testOptions)
	if err == nil {
		t.Fatal("expected INVALID_INCLUDE for :limit on an in-array edge")
	}
	if errCode(err) != INVALID_INCLUDE {
		t.Errorf("code = %v, want INVALID_INCLUDE", errCode(err))
	}
	if e, ok := err.(*Error); ok && e.Path != "attendees:limit" {
		t.Errorf("path = %q, want %q", e.Path, "attendees:limit")
	}
}

// Same for :sort — even with a SortCols whitelist that would otherwise accept it.
func TestInArrayClientSortRejected(t *testing.T) {
	A := inArrayGraph()

	for _, policy := range []SortPolicy{SortStrict, SortFallback} {
		opts := testOptions
		opts.SortPolicy = policy
		_, err := ResolvePlan(A,
			IncludeTree{"attendees": IncludeTree{":sort": "name"}}, nil, opts)
		if err == nil {
			t.Fatalf("policy %v: expected INVALID_INCLUDE for :sort on an in-array edge", policy)
		}
		if errCode(err) != INVALID_INCLUDE {
			t.Errorf("policy %v: code = %v, want INVALID_INCLUDE", policy, errCode(err))
		}
		if e, ok := err.(*Error); ok && e.Path != "attendees:sort" {
			t.Errorf("policy %v: path = %q, want %q", policy, e.Path, "attendees:sort")
		}
	}
}

// A computed key listed in Defaults() plans as a computed LEAF with no client
// include at all: the default expansion must not dereference its (absent)
// Target, and the default-cycle guard must skip it safely.
func TestDefaultComputedEdgePlans(t *testing.T) {
	B := &tRes{name: "B"}
	A := &tRes{
		name:     "A",
		defaults: []string{"stats", "child"},
		edges: map[string]Edge{
			"stats": {Computed: true}, // NOT includable: a default needs no gate
			"child": {Target: func() Resource { return B }, Includable: true},
		},
	}

	plan, err := ResolvePlan(A, IncludeTree{}, nil, testOptions)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	got := childKeys(plan)
	if len(got) != 2 || got[0] != "stats" || got[1] != "child" {
		t.Fatalf("child keys = %v, want [stats child]", got)
	}
	node := findChild(plan, "stats")
	if node == nil || !node.Computed {
		t.Fatalf("stats not planned as computed: %+v", node)
	}
	if node.Resource != nil {
		t.Errorf("computed node Resource = %v, want nil", node.Resource)
	}
	if len(node.Children) != 0 {
		t.Errorf("computed node is not a leaf: %v", childKeys(node))
	}
}

// A DECLARED edge arg named "limit"/"sort" must NOT let the in-array rejection
// evaporate: the built-in guard runs BEFORE the declared-arg short-circuit.
// (graph.Compile rejects such declarations on the builder path; this pins the
// engine-level behaviour for a hand-built edge.)
func TestInArrayDeclaredBuiltinArgsStillRejected(t *testing.T) {
	B := &tRes{name: "B"}
	A := &tRes{
		name: "A",
		edges: map[string]Edge{
			"attendees": {
				Target:     func() Resource { return B },
				Many:       true,
				ArrayPath:  "participants",
				SubField:   "user",
				Includable: true,
				SortCols:   map[string]string{"name": "name"},
				// Declared args of the BUILT-IN names, accepting anything.
				Args:        []EdgeArg{{Name: "limit"}, {Name: "sort"}},
				ForeignKeys: func(any) []string { return nil },
			},
		},
	}

	for _, name := range []string{"limit", "sort"} {
		_, err := ResolvePlan(A,
			IncludeTree{"attendees": IncludeTree{":" + name: "2"}}, nil, testOptions)
		if err == nil {
			t.Fatalf(":%s on an in-array edge with a declared %q arg was accepted", name, name)
		}
		if errCode(err) != INVALID_INCLUDE {
			t.Errorf(":%s code = %v, want INVALID_INCLUDE", name, errCode(err))
		}
		if e, ok := err.(*Error); ok && e.Path != "attendees:"+name {
			t.Errorf(":%s path = %q, want %q", name, e.Path, "attendees:"+name)
		}
	}
}

// An exclude naming a REQUIRED edge is silently suppressed — the document
// declares the key always present, so the plan keeps it; sub-paths beneath it
// stay excludable, and the path still validates (an unknown path errors).
func TestExcludeOfRequiredEdgeIsSuppressed(t *testing.T) {
	leaf := &tRes{name: "XLeaf", edges: map[string]Edge{}}
	mid := &tRes{name: "XMid"}
	root := &tRes{name: "XRoot", edges: map[string]Edge{
		"author": {Target: func() Resource { return mid }, Includable: true, Required: true},
	}}
	mid.edges = map[string]Edge{
		"avatar": {Target: func() Resource { return leaf }, Includable: true},
	}

	tree := IncludeTree{"author": IncludeTree{"avatar": IncludeTree{}}}
	plan, err := ResolvePlan(root, tree, [][]string{{"author"}, {"author", "avatar"}}, testOptions)
	if err != nil {
		t.Fatalf("ResolvePlan: %v", err)
	}
	author := findChild(plan, "author")
	if author == nil {
		t.Fatalf("required edge author was excluded from the plan; children = %v", childKeys(plan))
	}
	if findChild(author, "avatar") != nil {
		t.Errorf("non-required sub-path author.avatar must still be excludable")
	}
}

// Under ExcludeRequiredStrict the same exclude is refused instead of suppressed, at
// the excluded path; a non-required path still prunes normally.
func TestExcludeStrictRejectsRequiredEdge(t *testing.T) {
	leaf := &tRes{name: "XLeaf", edges: map[string]Edge{}}
	mid := &tRes{name: "XMid"}
	root := &tRes{name: "XRoot", edges: map[string]Edge{
		"author": {Target: func() Resource { return mid }, Includable: true, Required: true},
	}}
	mid.edges = map[string]Edge{
		"avatar": {Target: func() Resource { return leaf }, Includable: true},
	}
	tree := IncludeTree{"author": IncludeTree{"avatar": IncludeTree{}}}
	strict := Options{Limits: DefaultLimits, ExcludeRequiredPolicy: ExcludeRequiredStrict}

	_, err := ResolvePlan(root, tree, [][]string{{"author"}}, strict)
	assertInvalidInclude(t, err, "author")

	// Nothing was mutated by the refusal, and a legal exclude still prunes.
	plan, err := ResolvePlan(root, tree, [][]string{{"author", "avatar"}}, strict)
	if err != nil {
		t.Fatalf("non-required exclude under ExcludeRequiredStrict: %v", err)
	}
	if findChild(findChild(plan, "author"), "avatar") != nil {
		t.Errorf("author.avatar should have been pruned")
	}
}

// costGraph: A --a(reverse, Limit 20)--> A, plus optional extra edges.
func costGraph(extra map[string]Edge) *tRes {
	A := &tRes{name: "A"}
	A.edges = map[string]Edge{
		"a": {Target: func() Resource { return A }, Many: true, Backref: "aID", Includable: true, Limit: 20},
	}
	for k, e := range extra {
		A.edges[k] = e
	}
	return A
}

func TestResolveCostChain(t *testing.T) {
	A := costGraph(nil)
	cases := []struct {
		tree IncludeTree
		want int
	}{
		{IncludeTree{}, 1},
		{IncludeTree{"a": nil}, 21},
		{IncludeTree{"a": IncludeTree{"a": nil}}, 421},
		{IncludeTree{"a": IncludeTree{":limit": "5", "a": nil}}, 106},
	}
	for _, c := range cases {
		plan, err := ResolvePlan(A, c.tree, nil, testOptions)
		if err != nil {
			t.Fatalf("%v: %v", c.tree, err)
		}
		if plan.Cost != c.want {
			t.Errorf("%v: Cost = %d, want %d", c.tree, plan.Cost, c.want)
		}
	}
	// Subtree cost on a child node.
	plan, _ := ResolvePlan(A, IncludeTree{"a": IncludeTree{"a": nil}}, nil, testOptions)
	if got := findChild(plan, "a").Cost; got != 420 {
		t.Errorf("child Cost = %d, want 420", got)
	}
}

func TestResolveCostTooExpensive(t *testing.T) {
	A := costGraph(nil)
	deep := IncludeTree{"a": IncludeTree{"a": IncludeTree{"a": nil}}} // 8421
	_, err := ResolvePlan(A, deep, nil, Options{Limits: Limits{MaxDepth: 4, MaxNodes: 50, MaxCost: 5000}})
	if errCode(err) != INCLUDE_TOO_EXPENSIVE {
		t.Fatalf("err = %v, want INCLUDE_TOO_EXPENSIVE", err)
	}
	// Zero MaxCost → default 5000: same outcome.
	_, err = ResolvePlan(A, deep, nil, Options{Limits: Limits{MaxDepth: 4, MaxNodes: 50}})
	if errCode(err) != INCLUDE_TOO_EXPENSIVE {
		t.Fatalf("zero MaxCost: err = %v, want INCLUDE_TOO_EXPENSIVE", err)
	}
	// Raised ceiling lets it through.
	plan, err := ResolvePlan(A, deep, nil, Options{Limits: Limits{MaxDepth: 4, MaxNodes: 50, MaxCost: 10000}})
	if err != nil {
		t.Fatalf("MaxCost 10000: %v", err)
	}
	if plan.MaxRows != DefaultLimits.MaxRows {
		t.Errorf("root MaxRows = %d, want default %d", plan.MaxRows, DefaultLimits.MaxRows)
	}
}

func TestResolveCostEdgeKinds(t *testing.T) {
	B := &tRes{name: "B"}
	A := costGraph(map[string]Edge{
		"one":  {Target: func() Resource { return B }, Includable: true},                                         // to-one ×1
		"bare": {Target: func() Resource { return B }, Many: true, Backref: "aID", Includable: true, Bare: true}, // ×100
		"est":  {Target: func() Resource { return B }, Many: true, Backref: "aID", Includable: true, Bare: true, EstimatedRows: 7},
		"arr":  {Target: func() Resource { return B }, Many: true, ArrayPath: "ids", Includable: true}, // in-array ×100
		"comp": {Computed: true, Includable: true},
	})
	cases := map[string]int{"one": 2, "bare": 101, "est": 8, "arr": 101, "comp": 1}
	for key, want := range cases {
		plan, err := ResolvePlan(A, IncludeTree{key: nil}, nil, testOptions)
		if err != nil {
			t.Fatalf("%s: %v", key, err)
		}
		if plan.Cost != want {
			t.Errorf("%s: Cost = %d, want %d", key, plan.Cost, want)
		}
	}
}

func TestResolveCostCountsDefaults(t *testing.T) {
	// A default edge to ANOTHER node (a default re-entering the root is cut
	// by the default-cycle guard at the first hop, so it would cost nothing).
	B := &tRes{name: "B"}
	A := costGraph(map[string]Edge{
		"kids": {Target: func() Resource { return B }, Many: true, Backref: "aID", Limit: 20},
	})
	A.defaults = []string{"kids"}
	plan, err := ResolvePlan(A, IncludeTree{}, nil, testOptions)
	if err != nil {
		t.Fatal(err)
	}
	if plan.Cost != 21 {
		t.Errorf("Cost with default reverse edge = %d, want 21", plan.Cost)
	}
}
