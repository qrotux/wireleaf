package include

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"
)

// timeAfter is the cycle-termination watchdog for TestMaterialize_SelfCycle:
// a generous 5s deadline that only trips if Materialize fails to terminate.
func timeAfter() <-chan time.Time { return time.After(5 * time.Second) }

// ---------------------------------------------------------------------------
// Engine (Materialize) tests. These assert on RAW json.RawMessage BYTES
// directly, so ctx here comes from byteCtx, which installs MarshalNoEscape:
// the byte-exact expectations below are JSON.stringify-parity bytes, not
// HTML-escaped ones. The one exception is the paired test that pins the
// default (escaping) marshal.
// They reuse the toy graph (toygraph_test.go) rows + fakeReg, and hand-build
// small PlanNode variants so individual edge kinds can be isolated (e.g. a
// to-one WITHOUT the toyA `self` default in the way).
// ---------------------------------------------------------------------------

// byteCtx is the Ctx used by every byte-pinning test in this file.
func byteCtx(reg Registry) *Ctx { return &Ctx{Registry: reg, Marshal: MarshalNoEscape} }

// ------------------------------------------------------------------ plan helpers

// leafPlan builds a childless plan node for res (a resolved-plan leaf).
func leafPlan(res Resource) *PlanNode {
	return &PlanNode{Path: "", Resource: res, Children: nil}
}

// rootWith builds a root plan over res whose only inbound-less children are the
// given child plan nodes (already carrying their EdgeKey + Edge).
func rootWith(res Resource, children ...*PlanNode) *PlanNode {
	return &PlanNode{Path: "", Resource: res, Children: children}
}

// childNode builds a child plan node under a root. The inbound edge `e` is
// passed explicitly rather than looked up by key on the parent resource, so
// tests can inject bare/guard variants.
func childNode(key string, e Edge, target Resource, grandchildren ...*PlanNode) *PlanNode {
	ce := e
	return &PlanNode{
		Path:     key,
		Resource: target,
		Many:     e.Many,
		EdgeKey:  key,
		Edge:     &ce,
		Children: grandchildren,
	}
}

// ------------------------------------------------------------------ spy registry

// spyReg decorates a Registry, recording every id set requested via FetchByIDs
// and every BATCHED reverse call (parent id list + resolved EdgeQuery), keyed
// by resource Name. It is NON-INVASIVE — it wraps fakeReg without modifying
// it, so toygraph_test.go stays reusable by other tests. Used to prove
// guard-before-fetch (a guarded-out parent must contribute NO id to any batch)
// and one-call-per-level batching.
type spyReg struct {
	inner       Registry
	requestedID map[string][]string     // resource Name → flattened requested ids
	parentCalls map[string][]parentCall // resource Name → one entry per reverse call
}

// parentCall records one FetchByParents invocation.
type parentCall struct {
	ids []string
	q   EdgeQuery
}

var _ Registry = (*spyReg)(nil)

func (s *spyReg) FetchByEdge(p Resource, k string) (FetchByParents, bool) {
	return s.inner.FetchByEdge(p, k)
}

func newSpyReg(inner Registry) *spyReg {
	return &spyReg{
		inner:       inner,
		requestedID: map[string][]string{},
		parentCalls: map[string][]parentCall{},
	}
}

func (s *spyReg) FetchByIDs(r Resource) (FetchByIDs, bool) {
	inner, ok := s.inner.FetchByIDs(r)
	if !ok {
		return nil, false
	}
	name := r.Name()
	return func(c *Ctx, ids []string) ([]any, error) {
		s.requestedID[name] = append(s.requestedID[name], ids...)
		return inner(c, ids)
	}, true
}

func (s *spyReg) FetchByParents(r Resource) (FetchByParents, bool) {
	inner, ok := s.inner.FetchByParents(r)
	if !ok {
		return nil, false
	}
	name := r.Name()
	return func(c *Ctx, parentIDs []string, q EdgeQuery) (map[string]ParentRows, error) {
		ids := append([]string(nil), parentIDs...)
		s.parentCalls[name] = append(s.parentCalls[name], parentCall{ids: ids, q: q})
		return inner(c, parentIDs, q)
	}, true
}

// asked reports whether id was ever requested for resource name.
func (s *spyReg) asked(name, id string) bool {
	for _, got := range s.requestedID[name] {
		if got == id {
			return true
		}
	}
	return false
}

// ------------------------------------------------------------------ (a) to-one present + input order

func TestMaterialize_ToOnePresent_InputOrder(t *testing.T) {
	g := buildToyGraph()
	ctx := byteCtx(g.Reg)

	// Root plan over toyA with only the `child` to-one edge (no `self` default,
	// so we can pin exact bytes). Two docs: a1 (child=b1) and a2 (child=b2).
	plan := rootWith(g.A,
		childNode("child", g.A.Edges()["child"], g.B),
	)
	docs := []any{
		toyARow{id: "a1", name: "Alpha", childFK: "b1"},
		toyARow{id: "a2", name: "Beta", childFK: "b2"},
	}

	out, err := Materialize(plan, docs, ctx)
	if err != nil {
		t.Fatalf("Materialize: %v", err)
	}
	if len(out) != 2 {
		t.Fatalf("got %d outputs, want 2", len(out))
	}
	want0 := `{"id":"a1","name":"Alpha","child":{"id":"b1","label":"L1"}}`
	want1 := `{"id":"a2","name":"Beta","child":{"id":"b2","label":"L2"}}`
	if string(out[0]) != want0 {
		t.Errorf("out[0] =\n  %s\nwant\n  %s", out[0], want0)
	}
	if string(out[1]) != want1 {
		t.Errorf("out[1] =\n  %s\nwant\n  %s", out[1], want1)
	}
}

// ------------------------------------------------------------------ (a2) scalar HTML chars stay raw

// A scalar string containing `&`/`<`/`>` reaches the wire UNESCAPED when
// ctx.Marshal is MarshalNoEscape (explicit opt-in of the same function the
// default now uses).
func TestMaterialize_ScalarHTMLChars_RawBytes(t *testing.T) {
	g := buildToyGraph()
	ctx := byteCtx(g.Reg)

	out, err := Materialize(leafPlan(g.A), []any{
		toyARow{id: "a1", name: "Sense & <Sensibility>"},
	}, ctx)
	if err != nil {
		t.Fatalf("Materialize: %v", err)
	}
	want := `{"id":"a1","name":"Sense & <Sensibility>"}`
	if string(out[0]) != want {
		t.Errorf("out[0] =\n  %s\nwant\n  %s", out[0], want)
	}
}

// The v1 DEFAULT (ctx.Marshal nil) is MarshalNoEscape: `&`/`<`/`>` stay raw
// without any opt-in. This is the flip of the v0 behavior (stdlib escaping).
func TestMarshalDefaultIsNoEscape(t *testing.T) {
	g := buildToyGraph()
	ctx := &Ctx{Registry: g.Reg} // Marshal nil → the engine default

	out, err := Materialize(leafPlan(g.A), []any{
		toyARow{id: "a1", name: "Sense & <Sensibility>"},
	}, ctx)
	if err != nil {
		t.Fatalf("Materialize: %v", err)
	}
	want := `{"id":"a1","name":"Sense & <Sensibility>"}`
	if string(out[0]) != want {
		t.Errorf("out[0] =\n  %s\nwant\n  %s", out[0], want)
	}

	// Direct unit check of the fallback itself.
	b, err := (&Ctx{}).marshal("a & b <c>")
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if string(b) != `"a & b <c>"` {
		t.Errorf("(&Ctx{}).marshal = %s, want %q", b, `"a & b <c>"`)
	}
}

// MarshalStd is the OPT-IN back to encoding/json's HTML escaping — the
// contrast pins that Materialize honours ctx.marshal rather than hardcoding one.
func TestMarshalStdEscapes(t *testing.T) {
	g := buildToyGraph()
	ctx := &Ctx{Registry: g.Reg, Marshal: MarshalStd}

	out, err := Materialize(leafPlan(g.A), []any{
		toyARow{id: "a1", name: "Sense & <Sensibility>"},
	}, ctx)
	if err != nil {
		t.Fatalf("Materialize: %v", err)
	}
	want := "{\"id\":\"a1\",\"name\":\"Sense \\u0026 \\u003cSensibility\\u003e\"}"
	if string(out[0]) != want {
		t.Errorf("out[0] =\n  %s\nwant\n  %s", out[0], want)
	}

	// MarshalStd is byte-identical to json.Marshal.
	got, err := MarshalStd(map[string]string{"k": "a & b"})
	if err != nil {
		t.Fatalf("MarshalStd: %v", err)
	}
	std, err := json.Marshal(map[string]string{"k": "a & b"})
	if err != nil {
		t.Fatalf("json.Marshal: %v", err)
	}
	if string(got) != string(std) {
		t.Errorf("MarshalStd = %s, want %s", got, std)
	}
}

// ------------------------------------------------------------------ (b) access → null (missing id)

func TestMaterialize_ToOne_MissingID_Null(t *testing.T) {
	g := buildToyGraph()
	ctx := byteCtx(g.Reg)

	plan := rootWith(g.A, childNode("child", g.A.Edges()["child"], g.B))
	// childFK "ghost" has no canned toyB row → the fetcher drops it → null.
	docs := []any{toyARow{id: "a1", name: "Alpha", childFK: "ghost"}}

	out, err := Materialize(plan, docs, ctx)
	if err != nil {
		t.Fatalf("Materialize: %v", err)
	}
	want := `{"id":"a1","name":"Alpha","child":null}`
	if string(out[0]) != want {
		t.Errorf("out[0] = %s, want %s", out[0], want)
	}
}

// An empty ("") FK is a legitimate empty relation → null, and contributes no id
// to the batch.
func TestMaterialize_ToOne_EmptyFK_Null(t *testing.T) {
	g := buildToyGraph()
	spy := newSpyReg(g.Reg)
	ctx := byteCtx(spy)

	plan := rootWith(g.A, childNode("child", g.A.Edges()["child"], g.B))
	docs := []any{toyARow{id: "a1", name: "Alpha", childFK: ""}}

	out, err := Materialize(plan, docs, ctx)
	if err != nil {
		t.Fatalf("Materialize: %v", err)
	}
	if string(out[0]) != `{"id":"a1","name":"Alpha","child":null}` {
		t.Errorf("empty-FK out = %s", out[0])
	}
	if len(spy.requestedID["ToyB"]) != 0 {
		t.Errorf("empty FK must not issue a batch id, got %v", spy.requestedID["ToyB"])
	}
}

// ------------------------------------------------------------------ (c) guard → null + no fetch

func TestMaterialize_ToOne_GuardFalse_NullAndNoFetch(t *testing.T) {
	g := buildToyGraph()
	spy := newSpyReg(g.Reg)
	ctx := byteCtx(spy)

	// A guarded `child` edge that ALWAYS denies. Even though childFK=b1 is a real
	// row, the guard must short-circuit to null WITHOUT asking the fetcher for b1.
	guarded := Edge{Target: func() Resource { return g.B }, Includable: true, ForeignKey: func(a any) string { return a.(toyARow).childFK }, Guard: func(_ *Ctx, _ any) bool { return false }}
	plan := rootWith(g.A, childNode("child", guarded, g.B))
	docs := []any{toyARow{id: "a1", name: "Alpha", childFK: "b1"}}

	out, err := Materialize(plan, docs, ctx)
	if err != nil {
		t.Fatalf("Materialize: %v", err)
	}
	if string(out[0]) != `{"id":"a1","name":"Alpha","child":null}` {
		t.Errorf("guarded out = %s, want child:null", out[0])
	}
	// The critical assertion: b1 (a valid, fetchable id) was NEVER requested.
	if spy.asked("ToyB", "b1") {
		t.Errorf("guard-before-fetch violated: fetcher was asked for b1 %v", spy.requestedID["ToyB"])
	}
	if len(spy.requestedID["ToyB"]) != 0 {
		t.Errorf("guarded edge must issue no batch ids, got %v", spy.requestedID["ToyB"])
	}
}

// ------------------------------------------------------------------ (d) reverse envelope + hasMore

func TestMaterialize_Reverse_Envelope_HasMore(t *testing.T) {
	g := buildToyGraph()
	ctx := byteCtx(g.Reg)

	// `kids` reverse edge, limit 2 over 3 canned children of a1 → hasMore, trimmed.
	plan := rootWith(g.A, childNode("kids", g.A.Edges()["kids"], g.B))
	docs := []any{toyARow{id: "a1", name: "Alpha"}}

	out, err := Materialize(plan, docs, ctx)
	if err != nil {
		t.Fatalf("Materialize: %v", err)
	}
	want := `{"id":"a1","name":"Alpha","kids":{"items":[{"id":"b1","label":"L1"},{"id":"b2","label":"L2"}],"hasMore":true}}`
	if string(out[0]) != want {
		t.Errorf("reverse envelope =\n  %s\nwant\n  %s", out[0], want)
	}
}

// An unknown parent → {items:[],hasMore:false}, never null.
func TestMaterialize_Reverse_EmptyEnvelope(t *testing.T) {
	g := buildToyGraph()
	ctx := byteCtx(g.Reg)

	plan := rootWith(g.A, childNode("kids", g.A.Edges()["kids"], g.B))
	docs := []any{toyARow{id: "ghost", name: "Nobody"}} // no canned children

	out, err := Materialize(plan, docs, ctx)
	if err != nil {
		t.Fatalf("Materialize: %v", err)
	}
	want := `{"id":"ghost","name":"Nobody","kids":{"items":[],"hasMore":false}}`
	if string(out[0]) != want {
		t.Errorf("empty reverse = %s, want %s", out[0], want)
	}
}

// ------------------------------------------------------------------ (e) bare reverse → flat array

func TestMaterialize_Reverse_Bare_FlatArray(t *testing.T) {
	g := buildToyGraph()
	ctx := byteCtx(g.Reg)

	// Bare reverse: flat array, fetch-all (limit ≤ 0), no envelope. a1 → [b1,b2,b3].
	bareKids := Edge{Target: func() Resource { return g.B }, Many: true, Backref: "parentID", Includable: true, Bare: true}
	plan := rootWith(g.A, childNode("kids", bareKids, g.B))
	docs := []any{toyARow{id: "a1", name: "Alpha"}}

	out, err := Materialize(plan, docs, ctx)
	if err != nil {
		t.Fatalf("Materialize: %v", err)
	}
	want := `{"id":"a1","name":"Alpha","kids":[{"id":"b1","label":"L1"},{"id":"b2","label":"L2"},{"id":"b3","label":"L3"}]}`
	if string(out[0]) != want {
		t.Errorf("bare reverse =\n  %s\nwant\n  %s", out[0], want)
	}

	// Empty bare reverse must be [] (NOT null).
	docsEmpty := []any{toyARow{id: "ghost", name: "Nobody"}}
	out2, err := Materialize(plan, docsEmpty, ctx)
	if err != nil {
		t.Fatalf("Materialize (empty bare): %v", err)
	}
	want2 := `{"id":"ghost","name":"Nobody","kids":[]}`
	if string(out2[0]) != want2 {
		t.Errorf("empty bare reverse = %s, want %s", out2[0], want2)
	}
}

// ------------------------------------------------------------------ reverse guard-false → EMPTY node

// A guarded-out reverse edge yields the EMPTY node, NOT null: enveloped →
// {items:[],hasMore:false}; bare → []. Same empty shape a guard-pass with zero
// rows produces. (Only to-one returns null on guard-false.)
func TestMaterialize_Reverse_GuardFalse_Empty(t *testing.T) {
	g := buildToyGraph()
	ctx := byteCtx(g.Reg)

	deny := func(_ *Ctx, _ any) bool { return false }

	// Enveloped guarded reverse: a1 HAS 3 canned children, but the guard denies →
	// {items:[],hasMore:false}, never null and never the (would-be hasMore:true) set.
	envGuarded := Edge{Target: func() Resource { return g.B }, Many: true, Backref: "parentID", Includable: true, Limit: 2, Guard: deny}
	planEnv := rootWith(g.A, childNode("kids", envGuarded, g.B))
	docs := []any{toyARow{id: "a1", name: "Alpha"}}

	outEnv, err := Materialize(planEnv, docs, ctx)
	if err != nil {
		t.Fatalf("Materialize (enveloped guarded): %v", err)
	}
	wantEnv := `{"id":"a1","name":"Alpha","kids":{"items":[],"hasMore":false}}`
	if string(outEnv[0]) != wantEnv {
		t.Errorf("guarded reverse (enveloped) = %s, want %s", outEnv[0], wantEnv)
	}

	// Bare guarded reverse → [] (NOT null).
	bareGuarded := Edge{Target: func() Resource { return g.B }, Many: true, Backref: "parentID", Includable: true, Bare: true, Guard: deny}
	planBare := rootWith(g.A, childNode("kids", bareGuarded, g.B))
	outBare, err := Materialize(planBare, docs, ctx)
	if err != nil {
		t.Fatalf("Materialize (bare guarded): %v", err)
	}
	wantBare := `{"id":"a1","name":"Alpha","kids":[]}`
	if string(outBare[0]) != wantBare {
		t.Errorf("guarded reverse (bare) = %s, want %s", outBare[0], wantBare)
	}
}

// ------------------------------------------------------------------ (f) key order byte-exact (2+ edges)

func TestMaterialize_KeyOrder_ByteExact(t *testing.T) {
	g := buildToyGraph()
	ctx := byteCtx(g.Reg)

	// Two edges in a DELIBERATE plan order: `child` (to-one) then `kids` (reverse).
	// The assembler must emit: scalar fields (id,name) first, THEN child, THEN kids.
	plan := rootWith(g.A,
		childNode("child", g.A.Edges()["child"], g.B),
		childNode("kids", g.A.Edges()["kids"], g.B),
	)
	docs := []any{toyARow{id: "a1", name: "Alpha", childFK: "b1"}}

	out, err := Materialize(plan, docs, ctx)
	if err != nil {
		t.Fatalf("Materialize: %v", err)
	}
	want := `{"id":"a1","name":"Alpha",` +
		`"child":{"id":"b1","label":"L1"},` +
		`"kids":{"items":[{"id":"b1","label":"L1"},{"id":"b2","label":"L2"}],"hasMore":true}}`
	if string(out[0]) != want {
		t.Errorf("key order =\n  %s\nwant\n  %s", out[0], want)
	}

	// Reverse the plan order → the edges must swap position (proves plan-order, not
	// map/alpha order, drives assembly).
	planRev := rootWith(g.A,
		childNode("kids", g.A.Edges()["kids"], g.B),
		childNode("child", g.A.Edges()["child"], g.B),
	)
	outRev, err := Materialize(planRev, docs, ctx)
	if err != nil {
		t.Fatalf("Materialize (rev): %v", err)
	}
	wantRev := `{"id":"a1","name":"Alpha",` +
		`"kids":{"items":[{"id":"b1","label":"L1"},{"id":"b2","label":"L2"}],"hasMore":true},` +
		`"child":{"id":"b1","label":"L1"}}`
	if string(outRev[0]) != wantRev {
		t.Errorf("reversed key order =\n  %s\nwant\n  %s", outRev[0], wantRev)
	}
}

// ------------------------------------------------------------------ (g) cycle terminates (self default)

func TestMaterialize_SelfCycle_Terminates(t *testing.T) {
	g := buildToyGraph()
	ctx := byteCtx(g.Reg)

	// The `self` edge (toyA→toyA) is self-referential. The DEFAULT expansion is
	// cut at hop 0 by ResolvePlan's default-cycle guard (root ToyA is pre-seeded),
	// so a defaults-only plan is a bare root. To actually EXERCISE self-recursion
	// AND prove termination, drive a CLIENT `self.self` chain — client edges are
	// bounded by MaxDepth (not cycle-guarded), so the plan is a finite self chain.
	plan, err := ResolvePlan(g.A, IncludeTree{"self": IncludeTree{"self": IncludeTree{}}}, nil, DefaultOptions)
	if err != nil {
		t.Fatalf("ResolvePlan: %v", err)
	}

	// a1.self = a2 (a real row); a2.self = "" → the 2nd self hop is null → the
	// recursion terminates. The plan is 2 self levels deep, so a2's serialized
	// form carries a `self` key whose value is null.
	docs := []any{toyARow{id: "a1", name: "Alpha", selfFK: "a2"}}

	done := make(chan struct{})
	var out []json.RawMessage
	go func() {
		out, err = Materialize(plan, docs, ctx)
		close(done)
	}()
	select {
	case <-done:
	case <-timeAfter():
		t.Fatal("Materialize did not terminate (cycle not bounded)")
	}
	if err != nil {
		t.Fatalf("Materialize: %v", err)
	}
	// root(a1).self = a2 ; a2.self = "" → null (2nd hop terminates the chain).
	want := `{"id":"a1","name":"Alpha","self":{"id":"a2","name":"Beta","self":null}}`
	if string(out[0]) != want {
		t.Errorf("self cycle result =\n  %s\nwant\n  %s", out[0], want)
	}
}

// ------------------------------------------------------------------ forward-hasMany (extra coverage)

// The toy graph has no forward-hasMany edge; build a local one so the ForeignKeys /
// union / per-parent reorder / envelope path is exercised too. Parent holds an
// FK id array into toyB.
func TestMaterialize_ForwardHasMany_EnvelopeReorderLimit(t *testing.T) {
	g := buildToyGraph()
	ctx := byteCtx(g.Reg)

	// A toyARow variant carrying a slice of toyB FK ids, stashed in a wrapper row
	// type local to this test so ForeignKeys can read it.
	type manyRow struct {
		id   string
		name string
		bIDs []string
	}
	// Local resource mirroring toyA's scalar shape but reading manyRow.
	mres := &toyRes{
		name:   "ToyA",
		slug:   "toyA",
		fields: []string{"id", "name"},
		idOf:   func(doc any) string { return doc.(manyRow).id },
		serialize: func(doc any, _ *Ctx) any {
			r := doc.(manyRow)
			return toyAWire{ID: r.id, Name: r.name}
		},
	}
	many := Edge{Target: func() Resource { return g.B }, Many: true, Includable: true, Limit: 2, ForeignKeys: func(a any) []string { return a.(manyRow).bIDs }}
	plan := rootWith(mres, childNode("bs", many, g.B))

	// bIDs in a DELIBERATE non-DB order [b3,b1,b2]; limit 2 → trim to [b3,b1],
	// hasMore=true (3 > 2). The batch returns INPUT-ID order, and attach reorders
	// by the parent's trimmed fk order → items must be b3 THEN b1.
	docs := []any{manyRow{id: "a1", name: "Alpha", bIDs: []string{"b3", "b1", "b2"}}}
	out, err := Materialize(plan, docs, ctx)
	if err != nil {
		t.Fatalf("Materialize: %v", err)
	}
	want := `{"id":"a1","name":"Alpha","bs":{"items":[{"id":"b3","label":"L3"},{"id":"b1","label":"L1"}],"hasMore":true}}`
	if string(out[0]) != want {
		t.Errorf("forward-hasMany =\n  %s\nwant\n  %s", out[0], want)
	}
}

// ------------------------------------------------------------------ batch reverse contract

// stubParentsReg is a Registry whose reverse fetcher returns a CANNED result
// map regardless of what was asked, recording the calls. It exists to pin the
// engine's behaviour against a hostile/sloppy fetcher (unknown ids, over-limit
// rows, a cursor).
type stubParentsReg struct {
	result map[string]ParentRows
	calls  []parentCall
}

var _ Registry = (*stubParentsReg)(nil)

func (s *stubParentsReg) FetchByIDs(Resource) (FetchByIDs, bool) { return nil, false }
func (s *stubParentsReg) FetchByEdge(Resource, string) (FetchByParents, bool) {
	return nil, false
}

func (s *stubParentsReg) FetchByParents(Resource) (FetchByParents, bool) {
	return func(_ *Ctx, parentIDs []string, q EdgeQuery) (map[string]ParentRows, error) {
		s.calls = append(s.calls, parentCall{ids: append([]string(nil), parentIDs...), q: q})
		return s.result, nil
	}, true
}

// bRows boxes toyB rows into the engine's []any.
func bRows(ids ...string) []any {
	out := make([]any, len(ids))
	for i, id := range ids {
		out[i] = toyBRow{id: id, label: "L" + id}
	}
	return out
}

// TestReverse_BatchOnceForLevel: three parent docs → EXACTLY ONE fetcher call,
// carrying the deduped parent ids in doc order.
func TestReverse_BatchOnceForLevel(t *testing.T) {
	g := buildToyGraph()
	spy := newSpyReg(g.Reg)
	ctx := byteCtx(spy)

	plan := rootWith(g.A, childNode("kids", g.A.Edges()["kids"], g.B))
	docs := []any{
		toyARow{id: "p1"},
		toyARow{id: "p2"},
		toyARow{id: "p1"}, // duplicate parent id → deduped in the request
		toyARow{id: "p3"},
	}

	if _, err := Materialize(plan, docs, ctx); err != nil {
		t.Fatalf("Materialize: %v", err)
	}
	calls := spy.parentCalls[g.B.Name()]
	if len(calls) != 1 {
		t.Fatalf("FetchByParents calls = %d, want exactly 1 for the level", len(calls))
	}
	if want := []string{"p1", "p2", "p3"}; !reflect.DeepEqual(calls[0].ids, want) {
		t.Errorf("parentIDs = %v, want %v (doc order, deduped)", calls[0].ids, want)
	}
	// The query names the edge and its owner, so a node with several inbound
	// reverse edges can pick the join.
	if want := (EdgeRef{Parent: g.A.Name(), Key: "kids"}); calls[0].q.Edge != want {
		t.Errorf("EdgeQuery.Edge = %v, want %v", calls[0].q.Edge, want)
	}
}

// edgeReg overrides a Registry's per-edge reverse fetchers.
type edgeReg struct {
	Registry
	byEdge map[EdgeRef]FetchByParents
}

func (r *edgeReg) FetchByEdge(parent Resource, key string) (FetchByParents, bool) {
	fn, ok := r.byEdge[EdgeRef{Parent: parent.Name(), Key: key}]
	return fn, ok
}

// A per-edge fetcher wins over the target's node-level one; an edge without
// one falls back to the node-level fetcher.
func TestReverse_PerEdgeFetcherWinsOverNodeLevel(t *testing.T) {
	g := buildToyGraph()
	nodeCalls, edgeCalls := 0, 0
	counting := &countingReg{Registry: g.Reg, onParents: func() { nodeCalls++ }}
	reg := &edgeReg{Registry: counting, byEdge: map[EdgeRef]FetchByParents{
		{Parent: g.A.Name(), Key: "kids"}: func(c *Ctx, parentIDs []string, q EdgeQuery) (map[string]ParentRows, error) {
			edgeCalls++
			if q.Edge != (EdgeRef{Parent: g.A.Name(), Key: "kids"}) {
				t.Errorf("EdgeQuery.Edge = %v", q.Edge)
			}
			out := map[string]ParentRows{}
			for _, id := range parentIDs {
				out[id] = ParentRows{Rows: []any{toyBRow{id: "edge-" + id}}}
			}
			return out, nil
		},
	}}
	ctx := byteCtx(reg)
	plan := rootWith(g.A, childNode("kids", g.A.Edges()["kids"], g.B))

	items, err := Materialize(plan, []any{toyARow{id: "p1"}}, ctx)
	if err != nil {
		t.Fatalf("Materialize: %v", err)
	}
	if edgeCalls != 1 || nodeCalls != 0 {
		t.Errorf("edge fetcher calls = %d, node fetcher calls = %d; want 1 / 0", edgeCalls, nodeCalls)
	}
	if !strings.Contains(string(items[0]), `"id":"edge-p1"`) {
		t.Errorf("rows did not come from the per-edge fetcher: %s", items[0])
	}

	// No per-edge bind for this edge → node-level fetcher.
	reg.byEdge = nil
	if _, err := Materialize(plan, []any{toyARow{id: "p1"}}, byteCtx(reg)); err != nil {
		t.Fatalf("Materialize (fallback): %v", err)
	}
	if nodeCalls != 1 {
		t.Errorf("node fetcher calls after fallback = %d, want 1", nodeCalls)
	}
}

// countingReg counts node-level FetchByParents lookups that get CALLED.
type countingReg struct {
	Registry
	onParents func()
}

func (r *countingReg) FetchByParents(res Resource) (FetchByParents, bool) {
	inner, ok := r.Registry.FetchByParents(res)
	if !ok {
		return nil, false
	}
	return func(c *Ctx, ids []string, q EdgeQuery) (map[string]ParentRows, error) {
		r.onParents()
		return inner(c, ids, q)
	}, true
}

// A guarded-out parent contributes NO id to the batch (guard-before-fetch).
func TestReverse_GuardedParentNotInBatch(t *testing.T) {
	g := buildToyGraph()
	spy := newSpyReg(g.Reg)
	ctx := byteCtx(spy)

	guarded := Edge{Target: func() Resource { return g.B }, Many: true, Backref: "parentID", Includable: true, Limit: 2, Guard: func(_ *Ctx, doc any) bool { return doc.(toyARow).id != "p2" }}
	plan := rootWith(g.A, childNode("kids", guarded, g.B))
	docs := []any{toyARow{id: "p1"}, toyARow{id: "p2"}, toyARow{id: "p3"}}

	if _, err := Materialize(plan, docs, ctx); err != nil {
		t.Fatalf("Materialize: %v", err)
	}
	calls := spy.parentCalls[g.B.Name()]
	if len(calls) != 1 {
		t.Fatalf("FetchByParents calls = %d, want 1", len(calls))
	}
	if want := []string{"p1", "p3"}; !reflect.DeepEqual(calls[0].ids, want) {
		t.Errorf("parentIDs = %v, want %v (guarded parent excluded)", calls[0].ids, want)
	}
}

// A parent absent from the result map renders the EMPTY collection.
func TestReverse_AbsentParentEmptyEnvelope(t *testing.T) {
	g := buildToyGraph()
	reg := &stubParentsReg{result: map[string]ParentRows{
		"p1": {Rows: bRows("b1")},
		// p2 deliberately absent
	}}
	ctx := byteCtx(reg)

	plan := rootWith(g.A, childNode("kids", g.A.Edges()["kids"], g.B))
	out, err := Materialize(plan, []any{toyARow{id: "p1"}, toyARow{id: "p2"}}, ctx)
	if err != nil {
		t.Fatalf("Materialize: %v", err)
	}
	if want := `{"id":"p1","name":"","kids":{"items":[{"id":"b1","label":"Lb1"}],"hasMore":false}}`; string(out[0]) != want {
		t.Errorf("present parent =\n  %s\nwant\n  %s", out[0], want)
	}
	if want := `{"id":"p2","name":"","kids":{"items":[],"hasMore":false}}`; string(out[1]) != want {
		t.Errorf("absent parent =\n  %s\nwant\n  %s", out[1], want)
	}

	// Bare variant: absent parent → [], never null.
	bare := Edge{Target: func() Resource { return g.B }, Many: true, Backref: "parentID", Includable: true, Bare: true}
	outBare, err := Materialize(rootWith(g.A, childNode("kids", bare, g.B)), []any{toyARow{id: "p2"}}, ctx)
	if err != nil {
		t.Fatalf("Materialize (bare): %v", err)
	}
	if want := `{"id":"p2","name":"","kids":[]}`; string(outBare[0]) != want {
		t.Errorf("absent parent (bare) = %s, want %s", outBare[0], want)
	}
}

// Rows keyed by a parent id that was never requested are dropped.
func TestReverse_UnrequestedIDDropped(t *testing.T) {
	g := buildToyGraph()
	reg := &stubParentsReg{result: map[string]ParentRows{
		"p1":     {Rows: bRows("b1")},
		"ghost":  {Rows: bRows("b9")},
		"other":  {Rows: bRows("b8")},
		"p1-not": {Rows: bRows("b7")},
	}}
	ctx := byteCtx(reg)

	plan := rootWith(g.A, childNode("kids", g.A.Edges()["kids"], g.B))
	out, err := Materialize(plan, []any{toyARow{id: "p1"}}, ctx)
	if err != nil {
		t.Fatalf("Materialize: %v", err)
	}
	want := `{"id":"p1","name":"","kids":{"items":[{"id":"b1","label":"Lb1"}],"hasMore":false}}`
	if string(out[0]) != want {
		t.Errorf("out =\n  %s\nwant\n  %s (unrequested keys dropped)", out[0], want)
	}
}

// A fetcher returning MORE rows than q.Limit is truncated defensively.
func TestReverse_OverLimitTruncated(t *testing.T) {
	g := buildToyGraph()
	reg := &stubParentsReg{result: map[string]ParentRows{
		"p1": {Rows: bRows("b1", "b2", "b3", "b4", "b5")},
	}}
	ctx := byteCtx(reg)

	// The toy `kids` edge declares Limit 2.
	plan := rootWith(g.A, childNode("kids", g.A.Edges()["kids"], g.B))
	out, err := Materialize(plan, []any{toyARow{id: "p1"}}, ctx)
	if err != nil {
		t.Fatalf("Materialize: %v", err)
	}
	want := `{"id":"p1","name":"","kids":{"items":[{"id":"b1","label":"Lb1"},{"id":"b2","label":"Lb2"}],"hasMore":false}}`
	if string(out[0]) != want {
		t.Errorf("over-limit =\n  %s\nwant\n  %s (truncated to Limit 2)", out[0], want)
	}

	// A bare edge (Limit 0 = fetch-all) is NEVER truncated.
	bare := Edge{Target: func() Resource { return g.B }, Many: true, Backref: "parentID", Includable: true, Bare: true}
	outBare, err := Materialize(rootWith(g.A, childNode("kids", bare, g.B)), []any{toyARow{id: "p1"}}, ctx)
	if err != nil {
		t.Fatalf("Materialize (bare): %v", err)
	}
	if got := len(reg.calls); got == 0 {
		t.Fatal("no fetcher call recorded")
	}
	var doc struct {
		Kids []struct {
			ID string `json:"id"`
		} `json:"kids"`
	}
	if err := json.Unmarshal(outBare[0], &doc); err != nil {
		t.Fatalf("decode bare: %v (%s)", err, outBare[0])
	}
	if len(doc.Kids) != 5 {
		t.Errorf("bare items = %d, want 5 (no truncation when Limit ≤ 0)", len(doc.Kids))
	}
}

// A non-empty NextCursor is copied VERBATIM into the envelope, after hasMore.
func TestReverse_NextCursorEmitted(t *testing.T) {
	g := buildToyGraph()
	reg := &stubParentsReg{result: map[string]ParentRows{
		"p1": {Rows: bRows("b1"), HasMore: true, NextCursor: "abc"},
		"p2": {Rows: bRows("b2"), HasMore: true}, // no cursor → key omitted
	}}
	ctx := byteCtx(reg)

	plan := rootWith(g.A, childNode("kids", g.A.Edges()["kids"], g.B))
	out, err := Materialize(plan, []any{toyARow{id: "p1"}, toyARow{id: "p2"}}, ctx)
	if err != nil {
		t.Fatalf("Materialize: %v", err)
	}
	want := `{"id":"p1","name":"","kids":{"items":[{"id":"b1","label":"Lb1"}],"hasMore":true,"nextCursor":"abc"}}`
	if string(out[0]) != want {
		t.Errorf("cursor envelope =\n  %s\nwant\n  %s", out[0], want)
	}
	want2 := `{"id":"p2","name":"","kids":{"items":[{"id":"b2","label":"Lb2"}],"hasMore":true}}`
	if string(out[1]) != want2 {
		t.Errorf("no-cursor envelope =\n  %s\nwant\n  %s", out[1], want2)
	}
}

// A BARE edge never emits an envelope: hasMore and nextCursor are both ignored.
func TestReverse_BareIgnoresCursorAndHasMore(t *testing.T) {
	g := buildToyGraph()
	reg := &stubParentsReg{result: map[string]ParentRows{
		"p1": {Rows: bRows("b1"), HasMore: true, NextCursor: "abc"},
	}}
	ctx := byteCtx(reg)

	bare := Edge{Target: func() Resource { return g.B }, Many: true, Backref: "parentID", Includable: true, Bare: true}
	out, err := Materialize(rootWith(g.A, childNode("kids", bare, g.B)), []any{toyARow{id: "p1"}}, ctx)
	if err != nil {
		t.Fatalf("Materialize: %v", err)
	}
	want := `{"id":"p1","name":"","kids":[{"id":"b1","label":"Lb1"}]}`
	if string(out[0]) != want {
		t.Errorf("bare =\n  %s\nwant\n  %s", out[0], want)
	}
	if len(reg.calls) != 1 || reg.calls[0].q.Limit != 0 {
		t.Errorf("bare EdgeQuery = %+v, want Limit 0 (fetch-all)", reg.calls)
	}
}

// emptyReg is a Registry with NOTHING registered: every lookup reports absent.
type emptyReg struct{}

var _ Registry = emptyReg{}

func (emptyReg) FetchByIDs(Resource) (FetchByIDs, bool)         { return nil, false }
func (emptyReg) FetchByParents(Resource) (FetchByParents, bool) { return nil, false }
func (emptyReg) FetchByEdge(Resource, string) (FetchByParents, bool) {
	return nil, false
}

// A reverse edge with at least one guard-passing parent and NO registered
// fetcher is a wiring error, surfaced as such.
func TestReverse_MissingFetcher_Errors(t *testing.T) {
	g := buildToyGraph()
	ctx := byteCtx(emptyReg{})

	plan := rootWith(g.A, childNode("kids", g.A.Edges()["kids"], g.B))
	_, err := Materialize(plan, []any{toyARow{id: "p1"}}, ctx)
	if err == nil {
		t.Fatal("missing FetchByParents must error")
	}
	if !strings.Contains(err.Error(), "no FetchByParents registered") {
		t.Errorf("err = %v, want it to name the missing FetchByParents", err)
	}
}

// The mirror image: when EVERY parent is guarded out there is no fetch to make,
// so a missing fetcher is not reached — the empty shapes still render.
func TestReverse_MissingFetcher_AllGuarded_NoError(t *testing.T) {
	g := buildToyGraph()
	ctx := byteCtx(emptyReg{})
	deny := func(_ *Ctx, _ any) bool { return false }

	env := Edge{Target: func() Resource { return g.B }, Many: true, Backref: "parentID", Includable: true, Limit: 2, Guard: deny}
	out, err := Materialize(rootWith(g.A, childNode("kids", env, g.B)), []any{toyARow{id: "p1"}}, ctx)
	if err != nil {
		t.Fatalf("a fully guarded-out level must not touch the registry: %v", err)
	}
	if want := `{"id":"p1","name":"","kids":{"items":[],"hasMore":false}}`; string(out[0]) != want {
		t.Errorf("out = %s, want %s", out[0], want)
	}

	bare := Edge{Target: func() Resource { return g.B }, Many: true, Backref: "parentID", Includable: true, Bare: true, Guard: deny}
	outBare, err := Materialize(rootWith(g.A, childNode("kids", bare, g.B)), []any{toyARow{id: "p1"}}, ctx)
	if err != nil {
		t.Fatalf("bare guarded-out: %v", err)
	}
	if want := `{"id":"p1","name":"","kids":[]}`; string(outBare[0]) != want {
		t.Errorf("bare out = %s, want %s", outBare[0], want)
	}
}

// TestLimitClamp pins q.Limit resolution: no client limit → Edge.Limit; a
// client limit above the ceiling clamps to it; below it wins.
func TestLimitClamp(t *testing.T) {
	cases := []struct {
		name        string
		clientLimit any // nil = no client :limit
		want        int
	}{
		{"no client limit → Edge.Limit", nil, 10},
		{"client above ceiling → clamped", 50, 10},
		{"client below ceiling → client", 3, 3},
		{"client equal ceiling", 10, 10},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			g := buildToyGraph()
			reg := &stubParentsReg{result: map[string]ParentRows{}}
			ctx := byteCtx(reg)

			e := Edge{Target: func() Resource { return g.B }, Many: true, Backref: "parentID", Includable: true, Limit: 10}
			child := childNode("kids", e, g.B)
			if tc.clientLimit != nil {
				child.Args = map[string]any{"limit": tc.clientLimit}
			}
			if _, err := Materialize(rootWith(g.A, child), []any{toyARow{id: "p1"}}, ctx); err != nil {
				t.Fatalf("Materialize: %v", err)
			}
			if len(reg.calls) != 1 {
				t.Fatalf("calls = %d, want 1", len(reg.calls))
			}
			if got := reg.calls[0].q.Limit; got != tc.want {
				t.Errorf("q.Limit = %d, want %d", got, tc.want)
			}
		})
	}
}

// EdgeQuery.Args carries the declared args MINUS the built-ins.
func TestReverse_EdgeQueryArgsExcludeBuiltins(t *testing.T) {
	g := buildToyGraph()
	reg := &stubParentsReg{result: map[string]ParentRows{}}
	ctx := byteCtx(reg)

	e := Edge{Target: func() Resource { return g.B }, Many: true, Backref: "parentID", Includable: true, Limit: 5, Sort: "label", SortCols: map[string]string{"label": "label_col"}}
	child := childNode("kids", e, g.B)
	child.Args = map[string]any{"limit": 2, "sort": "label", "status": "published"}

	if _, err := Materialize(rootWith(g.A, child), []any{toyARow{id: "p1"}}, ctx); err != nil {
		t.Fatalf("Materialize: %v", err)
	}
	q := reg.calls[0].q
	if q.Limit != 2 || q.Sort != "label_col" {
		t.Errorf("q = %+v, want Limit 2 / Sort label_col", q)
	}
	if !reflect.DeepEqual(q.Args, map[string]any{"status": "published"}) {
		t.Errorf("q.Args = %#v, want only the non-built-in args", q.Args)
	}
}

// A client :limit trims the forward-hasMany FK slice with the SAME resolved limit.
func TestForwardHasMany_ClientLimitTrims(t *testing.T) {
	g := buildToyGraph()
	ctx := byteCtx(g.Reg)

	type manyRow struct {
		id   string
		bIDs []string
	}
	mres := &toyRes{
		name:   "ToyA",
		slug:   "toyA",
		fields: []string{"id", "name"},
		idOf:   func(doc any) string { return doc.(manyRow).id },
		serialize: func(doc any, _ *Ctx) any {
			return toyAWire{ID: doc.(manyRow).id}
		},
	}
	many := Edge{Target: func() Resource { return g.B }, Many: true, Includable: true, Limit: 3, ForeignKeys: func(a any) []string { return a.(manyRow).bIDs }}
	child := childNode("bs", many, g.B)
	child.Args = map[string]any{"limit": 1} // below the edge ceiling → wins
	docs := []any{manyRow{id: "a1", bIDs: []string{"b1", "b2", "b3"}}}

	out, err := Materialize(rootWith(mres, child), docs, ctx)
	if err != nil {
		t.Fatalf("Materialize: %v", err)
	}
	want := `{"id":"a1","name":"","bs":{"items":[{"id":"b1","label":"L1"}],"hasMore":true}}`
	if string(out[0]) != want {
		t.Errorf("client-limited forward-hasMany =\n  %s\nwant\n  %s", out[0], want)
	}
}

// ------------------------------------------------------------------ assembleObject unit tests (raw bytes)

func TestAssembleObject_RawBytes(t *testing.T) {
	scalar := json.RawMessage(`{"id":"1","name":"a"}`)

	// no edges → scalar unchanged.
	if got := assembleObject(scalar, nil); !reflect.DeepEqual([]byte(got), []byte(scalar)) {
		t.Errorf("no-edge = %s, want %s", got, scalar)
	}

	// one edge appended after scalar fields.
	got := assembleObject(scalar, []kv{{Key: "child", Val: json.RawMessage(`{"id":"b1"}`)}})
	if string(got) != `{"id":"1","name":"a","child":{"id":"b1"}}` {
		t.Errorf("one-edge = %s", got)
	}

	// two edges in order; null edge uses the literal null.
	got2 := assembleObject(scalar, []kv{
		{Key: "child", Val: nullRaw},
		{Key: "kids", Val: json.RawMessage(`[]`)},
	})
	if string(got2) != `{"id":"1","name":"a","child":null,"kids":[]}` {
		t.Errorf("two-edge = %s", got2)
	}

	// empty edge value defaults to null.
	got3 := assembleObject(scalar, []kv{{Key: "child", Val: nil}})
	if string(got3) != `{"id":"1","name":"a","child":null}` {
		t.Errorf("empty-val = %s", got3)
	}

	// empty scalar object {} → first edge has NO leading comma.
	gotEmpty := assembleObject(json.RawMessage(`{}`), []kv{
		{Key: "a", Val: json.RawMessage(`1`)},
		{Key: "b", Val: json.RawMessage(`2`)},
	})
	if string(gotEmpty) != `{"a":1,"b":2}` {
		t.Errorf("empty-scalar = %s, want {\"a\":1,\"b\":2}", gotEmpty)
	}
}

// ------------------------------------------------------------------ computed edges: planned, then skipped

// A computed edge survives planning (PlanNode.Computed) but is skipped
// ENTIRELY by the engine: its key never appears in the response bytes (the
// application splices the value in afterwards).
func TestComputedEdgePlansAndSkips(t *testing.T) {
	g := buildToyGraph()

	// include=stats. toyA's only default (`self`) is cut by the default-cycle
	// guard at the root, so `stats` is the plan's sole child and the bytes are
	// pinnable.
	plan, err := ResolvePlan(g.A, IncludeTree{"stats": IncludeTree{}}, nil,
		Options{Limits: Limits{MaxDepth: 4, MaxNodes: 50}})
	if err != nil {
		t.Fatalf("ResolvePlan: %v", err)
	}
	node := plan.Get("stats")
	if node == nil || !node.Computed {
		t.Fatalf("stats not planned as computed: %+v", node)
	}

	for _, ctx := range []*Ctx{byteCtx(g.Reg), {Registry: g.Reg}} {
		out, err := Materialize(plan, []any{toyARow{id: "a1", name: "Alpha", childFK: "b1"}}, ctx)
		if err != nil {
			t.Fatalf("Materialize: %v", err)
		}
		want := `{"id":"a1","name":"Alpha"}`
		if string(out[0]) != want {
			t.Errorf("out[0] =\n  %s\nwant\n  %s", out[0], want)
		}
		if strings.Contains(string(out[0]), "stats") {
			t.Errorf("computed key leaked into bytes: %s", out[0])
		}
	}
}

// A computed edge sitting BETWEEN two real edges must not disturb the key
// order (or the values) of the edges around it.
func TestComputedEdgeSkipKeepsSiblingOrder(t *testing.T) {
	g := buildToyGraph()
	ctx := byteCtx(g.Reg)

	edges := g.A.Edges()
	plan := rootWith(g.A,
		childNode("child", edges["child"], g.B),
		&PlanNode{Path: "stats", EdgeKey: "stats", Edge: &Edge{Computed: true, Includable: true}, Computed: true},
		childNode("kids", edges["kids"], g.B),
	)

	out, err := Materialize(plan, []any{toyARow{id: "a1", name: "Alpha", childFK: "b1"}}, ctx)
	if err != nil {
		t.Fatalf("Materialize: %v", err)
	}
	want := `{"id":"a1","name":"Alpha","child":{"id":"b1","label":"L1"},` +
		`"kids":{"items":[{"id":"b1","label":"L1"},{"id":"b2","label":"L2"}],"hasMore":true}}`
	if string(out[0]) != want {
		t.Errorf("out[0] =\n  %s\nwant\n  %s", out[0], want)
	}
}

// ------------------------------------------------------------------ in-array edges

// An in-array edge splices the hydrated target INTO each element of an array
// that already lives in the parent's own scalar bytes — it adds no top-level
// key of its own. These fixtures model a meeting whose `participants` array
// carries a per-element role plus (via ForeignKeys, in array order) the user ids.

// inArrRow is the "DB row": the raw wire bytes of the array member plus the
// typed id harvest ForeignKeys reads.
type inArrRow struct {
	id      string
	arr     json.RawMessage // raw bytes of the "participants" member ("" → absent)
	userIDs []string
}

// inArrWire is the scalar wire struct: the array member is passed through
// VERBATIM (omitempty → an empty RawMessage drops the key entirely).
type inArrWire struct {
	ID    string          `json:"id"`
	Parts json.RawMessage `json:"participants,omitempty"`
}

// inArrRes builds the parent resource for the in-array fixtures.
func inArrRes() *toyRes {
	return &toyRes{
		name:   "Meeting",
		slug:   "meeting",
		fields: []string{"id", "participants"},
		idOf:   func(d any) string { return d.(inArrRow).id },
		serialize: func(d any, _ *Ctx) any {
			r := d.(inArrRow)
			return inArrWire{ID: r.id, Parts: r.arr}
		},
	}
}

// inArrEdge builds the in-array edge. NOTE the edge KEY ("attendees", chosen by
// the caller) deliberately differs from the array FIELD name ("participants").
func inArrEdge(target Resource, mods ...func(*Edge)) Edge {
	e := Edge{
		Target:      func() Resource { return target },
		Many:        true,
		ArrayPath:   "participants",
		SubField:    "user",
		Includable:  true,
		ForeignKeys: func(a any) []string { return a.(inArrRow).userIDs },
	}
	for _, m := range mods {
		m(&e)
	}
	return e
}

// The happy path: each element gains `user` (the hydrated target), order
// preserved, every other element byte untouched, no top-level edge key.
func TestInArrayStitch(t *testing.T) {
	g := buildToyGraph()
	spy := newSpyReg(g.Reg)
	ctx := byteCtx(spy)

	plan := rootWith(inArrRes(), childNode("attendees", inArrEdge(g.B), g.B))
	docs := []any{
		inArrRow{id: "p1", arr: json.RawMessage(`[{"role":"a"},{"role":"b"}]`), userIDs: []string{"b1", "b2"}},
		inArrRow{id: "p2", arr: json.RawMessage(`[{"role":"c"}]`), userIDs: []string{"b1"}},
	}
	out, err := Materialize(plan, docs, ctx)
	if err != nil {
		t.Fatalf("Materialize: %v", err)
	}
	want0 := `{"id":"p1","participants":[{"role":"a","user":{"id":"b1","label":"L1"}},` +
		`{"role":"b","user":{"id":"b2","label":"L2"}}]}`
	if string(out[0]) != want0 {
		t.Errorf("out[0] =\n  %s\nwant\n  %s", out[0], want0)
	}
	want1 := `{"id":"p2","participants":[{"role":"c","user":{"id":"b1","label":"L1"}}]}`
	if string(out[1]) != want1 {
		t.Errorf("out[1] =\n  %s\nwant\n  %s", out[1], want1)
	}
	// The whole level is ONE deduped forward batch: b1 asked exactly once.
	got := spy.requestedID["ToyB"]
	if len(got) != 2 || got[0] != "b1" || got[1] != "b2" {
		t.Errorf("requested ids = %v, want one deduped batch [b1 b2]", got)
	}
}

// An id the fetch does not return (access-dropped / missing) → that element's
// SubField is null; its siblings still hydrate.
func TestInArrayMissingIDNull(t *testing.T) {
	g := buildToyGraph()
	ctx := byteCtx(g.Reg)

	plan := rootWith(inArrRes(), childNode("attendees", inArrEdge(g.B), g.B))
	docs := []any{inArrRow{
		id:      "p1",
		arr:     json.RawMessage(`[{"role":"a"},{"role":"b"}]`),
		userIDs: []string{"ghost", "b2"},
	}}
	out, err := Materialize(plan, docs, ctx)
	if err != nil {
		t.Fatalf("Materialize: %v", err)
	}
	want := `{"id":"p1","participants":[{"role":"a","user":null},` +
		`{"role":"b","user":{"id":"b2","label":"L2"}}]}`
	if string(out[0]) != want {
		t.Errorf("out[0] =\n  %s\nwant\n  %s", out[0], want)
	}
}

// A guarded-out parent: every element gets SubField null and NO id reaches the
// batch (guard runs before the harvest).
func TestInArrayGuardFalse(t *testing.T) {
	g := buildToyGraph()
	spy := newSpyReg(g.Reg)
	ctx := byteCtx(spy)

	edge := inArrEdge(g.B, func(e *Edge) {
		e.Guard = func(_ *Ctx, parent any) bool { return parent.(inArrRow).id != "p1" }
	})
	plan := rootWith(inArrRes(), childNode("attendees", edge, g.B))
	docs := []any{
		inArrRow{id: "p1", arr: json.RawMessage(`[{"role":"a"},{"role":"b"}]`), userIDs: []string{"b1", "b2"}},
		inArrRow{id: "p2", arr: json.RawMessage(`[{"role":"c"}]`), userIDs: []string{"b3"}},
	}
	out, err := Materialize(plan, docs, ctx)
	if err != nil {
		t.Fatalf("Materialize: %v", err)
	}
	want0 := `{"id":"p1","participants":[{"role":"a","user":null},{"role":"b","user":null}]}`
	if string(out[0]) != want0 {
		t.Errorf("guarded out[0] =\n  %s\nwant\n  %s", out[0], want0)
	}
	want1 := `{"id":"p2","participants":[{"role":"c","user":{"id":"b3","label":"L3"}}]}`
	if string(out[1]) != want1 {
		t.Errorf("out[1] =\n  %s\nwant\n  %s", out[1], want1)
	}
	if spy.asked("ToyB", "b1") || spy.asked("ToyB", "b2") {
		t.Errorf("guarded parent contributed ids to the batch: %v", spy.requestedID["ToyB"])
	}
}

// A guarded-out parent's array length is NOT cross-checked (it contributes no
// ids at all), so a stale ForeignKeys harvest cannot fail the request.
func TestInArrayGuardFalseSkipsLengthCheck(t *testing.T) {
	g := buildToyGraph()
	ctx := byteCtx(g.Reg)

	edge := inArrEdge(g.B, func(e *Edge) { e.Guard = func(*Ctx, any) bool { return false } })
	plan := rootWith(inArrRes(), childNode("attendees", edge, g.B))
	docs := []any{inArrRow{
		id:      "p1",
		arr:     json.RawMessage(`[{"role":"a"},{"role":"b"}]`),
		userIDs: []string{"b1"}, // deliberately the WRONG length
	}}
	out, err := Materialize(plan, docs, ctx)
	if err != nil {
		t.Fatalf("Materialize: %v", err)
	}
	want := `{"id":"p1","participants":[{"role":"a","user":null},{"role":"b","user":null}]}`
	if string(out[0]) != want {
		t.Errorf("out[0] =\n  %s\nwant\n  %s", out[0], want)
	}
}

// The harvest must line up 1:1 with the wire array — a mismatch is a developer
// error reported verbatim.
func TestInArrayLengthMismatch(t *testing.T) {
	g := buildToyGraph()
	ctx := byteCtx(g.Reg)

	plan := rootWith(inArrRes(), childNode("attendees", inArrEdge(g.B), g.B))
	docs := []any{inArrRow{
		id:      "p1",
		arr:     json.RawMessage(`[{"role":"a"},{"role":"b"}]`),
		userIDs: []string{"b1"},
	}}
	_, err := Materialize(plan, docs, ctx)
	if err == nil {
		t.Fatal("expected a length-mismatch error")
	}
	want := "include: in-array length mismatch on attendees: 1 ids, 2 elements"
	if err.Error() != want {
		t.Errorf("err = %q, want %q", err, want)
	}
}

// The fetched target docs go through the CHILD plan, so nested includes under
// an in-array edge hydrate exactly as they do under a forward edge.
func TestInArrayNestedInclude(t *testing.T) {
	g := buildToyGraph()
	ctx := byteCtx(g.Reg)

	edges := g.A.Edges()
	// target = toyA, and under it the to-one `child` → toyB.
	plan := rootWith(inArrRes(),
		childNode("attendees", inArrEdge(g.A), g.A,
			childNode("child", edges["child"], g.B)))
	docs := []any{inArrRow{
		id:      "p1",
		arr:     json.RawMessage(`[{"role":"a"}]`),
		userIDs: []string{"a1"},
	}}
	out, err := Materialize(plan, docs, ctx)
	if err != nil {
		t.Fatalf("Materialize: %v", err)
	}
	want := `{"id":"p1","participants":[{"role":"a","user":` +
		`{"id":"a1","name":"Alpha","child":{"id":"b1","label":"L1"}}}]}`
	if string(out[0]) != want {
		t.Errorf("out[0] =\n  %s\nwant\n  %s", out[0], want)
	}
}

// An absent or null array member → the edge is simply skipped for that parent
// (no error, bytes untouched).
func TestInArrayNullArraySkipped(t *testing.T) {
	g := buildToyGraph()
	ctx := byteCtx(g.Reg)

	plan := rootWith(inArrRes(), childNode("attendees", inArrEdge(g.B), g.B))
	docs := []any{
		inArrRow{id: "p1", arr: json.RawMessage(`null`)}, // explicit null
		inArrRow{id: "p2"}, // member absent entirely
	}
	out, err := Materialize(plan, docs, ctx)
	if err != nil {
		t.Fatalf("Materialize: %v", err)
	}
	if string(out[0]) != `{"id":"p1","participants":null}` {
		t.Errorf("null array out[0] = %s", out[0])
	}
	if string(out[1]) != `{"id":"p2"}` {
		t.Errorf("absent array out[1] = %s", out[1])
	}
}

// A non-object array element cannot carry a SubField → error.
func TestInArrayNonObjectElement(t *testing.T) {
	g := buildToyGraph()
	ctx := byteCtx(g.Reg)

	plan := rootWith(inArrRes(), childNode("attendees", inArrEdge(g.B), g.B))
	docs := []any{inArrRow{
		id:      "p1",
		arr:     json.RawMessage(`["a"]`),
		userIDs: []string{"b1"},
	}}
	_, err := Materialize(plan, docs, ctx)
	if err == nil {
		t.Fatal("expected an error on a non-object element")
	}
	if !strings.Contains(err.Error(), "attendees") {
		t.Errorf("err = %q, want it to name the edge key", err)
	}
}

// A hand-built COMPUTED plan node (Computed set on the NODE, no Edge.Computed)
// is skipped by the engine just like a resolved one.
func TestComputedNodeWithoutEdgeFlagSkipped(t *testing.T) {
	g := buildToyGraph()
	ctx := byteCtx(g.Reg)

	edges := g.A.Edges()
	plan := rootWith(g.A,
		childNode("child", edges["child"], g.B),
		// Computed on the NODE only: the inbound Edge does not carry the flag.
		&PlanNode{Path: "stats", EdgeKey: "stats", Edge: &Edge{Includable: true}, Computed: true},
	)
	out, err := Materialize(plan, []any{toyARow{id: "a1", name: "Alpha", childFK: "b1"}}, ctx)
	if err != nil {
		t.Fatalf("Materialize: %v", err)
	}
	want := `{"id":"a1","name":"Alpha","child":{"id":"b1","label":"L1"}}`
	if string(out[0]) != want {
		t.Errorf("out[0] =\n  %s\nwant\n  %s", out[0], want)
	}
}

// An element that ALREADY carries the SubField has that member REPLACED in
// place: the member keeps its position (role, user, z) and every other byte —
// including the sibling members around it — survives verbatim.
func TestInArrayReplacesExistingSubField(t *testing.T) {
	g := buildToyGraph()
	ctx := byteCtx(g.Reg)

	plan := rootWith(inArrRes(), childNode("attendees", inArrEdge(g.B), g.B))
	docs := []any{inArrRow{
		id:      "p1",
		arr:     json.RawMessage(`[{"role":"a","user":"stale","z":1},{"role":"b"}]`),
		userIDs: []string{"b1", "b2"},
	}}
	out, err := Materialize(plan, docs, ctx)
	if err != nil {
		t.Fatalf("Materialize: %v", err)
	}
	want := `{"id":"p1","participants":[` +
		`{"role":"a","user":{"id":"b1","label":"L1"},"z":1},` +
		`{"role":"b","user":{"id":"b2","label":"L2"}}]}`
	if string(out[0]) != want {
		t.Errorf("out[0] =\n  %s\nwant\n  %s", out[0], want)
	}
}

// ------------------------------------------------------------------ MissingRequiredPolicy

// requiredChildPlan builds a root plan over toyA with ONE required to-one
// child ("child" → toyB, ForeignKey reads childFK), carrying the given
// per-edge override (nil inherits Ctx.Policies).
func requiredChildPlan(g *toyGraph, override *MissingRequiredPolicy) *PlanNode {
	e := g.A.Edges()["child"]
	e.Required = true
	e.MissingRequired = override
	return rootWith(g.A, childNode("child", e, g.B))
}

// requiredChildReg registers exactly one toyB row, so a parent pointing at any
// other id models "the fetcher did not return it".
func requiredChildReg() *fakeReg {
	g := buildToyGraph()
	reg := newFakeReg()
	reg.putByID(g.B, "b1", toyBRow{id: "b1", label: "L"})
	return reg
}

// The default policy emits null on BOTH missing paths (empty FK, absent row).
func TestRequiredMissing_NullIsTheDefault(t *testing.T) {
	g := buildToyGraph()
	ctx := byteCtx(requiredChildReg())
	plan := requiredChildPlan(g, nil) // zero Ctx.Policies → MissingRequiredNull

	for _, fk := range []string{"", "gone"} {
		out, err := Materialize(plan, []any{toyARow{id: "a1", childFK: fk}}, ctx)
		if err != nil {
			t.Fatalf("childFK=%q: default policy must not error, got %v", fk, err)
		}
		if !strings.Contains(string(out[0]), `"child":null`) {
			t.Errorf("childFK=%q: want a null child, got %s", fk, out[0])
		}
	}
}

// Under MissingRequiredError each missing path fails the request, and the
// message names WHICH path it was.
func TestRequiredMissing_ErrorNamesThePath(t *testing.T) {
	g := buildToyGraph()
	ctx := byteCtx(requiredChildReg())
	ctx.Policies.MissingRequired = MissingRequiredError
	plan := requiredChildPlan(g, nil)

	for _, tc := range []struct{ fk, want string }{
		{"", "empty FK"},
		{"gone", "did not return id"},
	} {
		_, err := Materialize(plan, []any{toyARow{id: "a1", childFK: tc.fk}}, ctx)
		if err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("childFK=%q: want an error mentioning %q, got %v", tc.fk, tc.want, err)
		}
	}
	// A resolvable target is unaffected.
	if _, err := Materialize(plan, []any{toyARow{id: "a1", childFK: "b1"}}, ctx); err != nil {
		t.Errorf("a present target must not error, got %v", err)
	}
}

// A NON-required edge nulls out under both policies: the policy is scoped to
// Required edges only.
func TestRequiredMissing_IgnoredOnOptionalEdge(t *testing.T) {
	g := buildToyGraph()
	ctx := byteCtx(requiredChildReg())
	ctx.Policies.MissingRequired = MissingRequiredError
	child := childNode("child", g.A.Edges()["child"], g.B) // Required stays false
	plan := rootWith(g.A, child)

	out, err := Materialize(plan, []any{toyARow{id: "a1", childFK: "gone"}}, ctx)
	if err != nil {
		t.Fatalf("optional edge must ignore MissingRequiredError, got %v", err)
	}
	if !strings.Contains(string(out[0]), `"child":null`) {
		t.Errorf("want a null child, got %s", out[0])
	}
}

// The edge override wins over the engine-wide Ctx.Policies fallback, in both
// directions, observed on the wire: a dangling FK either errors or nulls out.
func TestRequiredMissing_EdgeOverrideWinsOverCtx(t *testing.T) {
	strict, lenient := MissingRequiredError, MissingRequiredNull
	g := buildToyGraph()

	cases := []struct {
		name     string
		override *MissingRequiredPolicy
		fallback MissingRequiredPolicy
		wantErr  bool
	}{
		{"inherit tolerant ctx", nil, MissingRequiredNull, false},
		{"inherit strict ctx", nil, MissingRequiredError, true},
		{"strict edge beats tolerant ctx", &strict, MissingRequiredNull, true},
		{"tolerant edge beats strict ctx", &lenient, MissingRequiredError, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			ctx := byteCtx(requiredChildReg())
			ctx.Policies.MissingRequired = tc.fallback
			_, err := Materialize(requiredChildPlan(g, tc.override),
				[]any{toyARow{id: "a1", childFK: "gone"}}, ctx)
			if (err != nil) != tc.wantErr {
				t.Errorf("err = %v, wantErr = %v", err, tc.wantErr)
			}
		})
	}
}

// ------------------------------------------------------------------ MissingForeignPolicy

// A dangling FK (non-empty id the fetcher did not return) fails the request
// under MissingForeignError, on every kind that reads a parent-side FK.
func TestMissingForeign_ErrorOnDanglingFK(t *testing.T) {
	g := buildToyGraph()
	ctx := byteCtx(requiredChildReg()) // only b1 exists
	ctx.Policies.MissingForeign = MissingForeignError

	t.Run("to-one", func(t *testing.T) {
		child := childNode("child", g.A.Edges()["child"], g.B)
		_, err := Materialize(rootWith(g.A, child), []any{toyARow{id: "a1", childFK: "gone"}}, ctx)
		if err == nil || !strings.Contains(err.Error(), "dangling foreign key") {
			t.Errorf("want a dangling-FK error, got %v", err)
		}
	})

	t.Run("to-one, empty FK is not dangling", func(t *testing.T) {
		child := childNode("child", g.A.Edges()["child"], g.B)
		out, err := Materialize(rootWith(g.A, child), []any{toyARow{id: "a1", childFK: ""}}, ctx)
		if err != nil {
			t.Fatalf("an empty FK is the absence of a reference, not a dangling one: %v", err)
		}
		if !strings.Contains(string(out[0]), `"child":null`) {
			t.Errorf("want a null child, got %s", out[0])
		}
	})

	t.Run("forward-hasMany", func(t *testing.T) {
		e := Edge{
			Target: func() Resource { return g.B }, Many: true, Includable: true,
			ForeignKeys: func(any) []string { return []string{"b1", "gone"} },
		}
		child := childNode("many", e, g.B)
		_, err := Materialize(rootWith(g.A, child), []any{toyARow{id: "a1"}}, ctx)
		if err == nil || !strings.Contains(err.Error(), "dangling foreign key") {
			t.Errorf("a silently dropped list item must error, got %v", err)
		}
	})
}

// The default keeps the v0 shapes: null for to-one, a dropped item for to-many.
func TestMissingForeign_NullIsTheDefault(t *testing.T) {
	g := buildToyGraph()
	ctx := byteCtx(requiredChildReg())

	child := childNode("child", g.A.Edges()["child"], g.B)
	out, err := Materialize(rootWith(g.A, child), []any{toyARow{id: "a1", childFK: "gone"}}, ctx)
	if err != nil {
		t.Fatalf("default policy must not error, got %v", err)
	}
	if !strings.Contains(string(out[0]), `"child":null`) {
		t.Errorf("want a null child, got %s", out[0])
	}

	e := Edge{
		Target: func() Resource { return g.B }, Many: true, Includable: true,
		ForeignKeys: func(any) []string { return []string{"b1", "gone"} },
	}
	out, err = Materialize(rootWith(g.A, childNode("many", e, g.B)), []any{toyARow{id: "a1"}}, ctx)
	if err != nil {
		t.Fatalf("default policy must not error, got %v", err)
	}
	if strings.Count(string(out[0]), `"id":"b1"`) != 1 || strings.Contains(string(out[0]), "gone") {
		t.Errorf("want the resolvable item only, got %s", out[0])
	}
}

// A REVERSE edge reads no parent-side FK, so no absence there is dangling.
func TestMissingForeign_ReverseUnaffected(t *testing.T) {
	g := buildToyGraph()
	reg := newFakeReg()
	reg.putChildren(g.B, "a1", nil) // registered, but this parent has none
	ctx := byteCtx(reg)

	ctx.Policies.MissingForeign = MissingForeignError
	child := childNode("kids", g.A.Edges()["kids"], g.B)
	out, err := Materialize(rootWith(g.A, child), []any{toyARow{id: "a1"}}, ctx)
	if err != nil {
		t.Fatalf("a reverse edge with no children is not a dangling reference: %v", err)
	}
	if !strings.Contains(string(out[0]), `"items":[]`) {
		t.Errorf("want an empty envelope, got %s", out[0])
	}
}

// The edge override wins over the engine-wide Ctx.Policies fallback, in both
// directions, observed on the wire: a dangling FK either errors or nulls out.
func TestMissingForeign_EdgeOverrideWinsOverCtx(t *testing.T) {
	strict, lenient := MissingForeignError, MissingForeignNull
	g := buildToyGraph()

	for _, tc := range []struct {
		name     string
		override *MissingForeignPolicy
		fallback MissingForeignPolicy
		wantErr  bool
	}{
		{"inherit tolerant ctx", nil, MissingForeignNull, false},
		{"inherit strict ctx", nil, MissingForeignError, true},
		{"strict edge beats tolerant ctx", &strict, MissingForeignNull, true},
		{"tolerant edge beats strict ctx", &lenient, MissingForeignError, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx := byteCtx(requiredChildReg())
			ctx.Policies.MissingForeign = tc.fallback
			e := g.A.Edges()["child"]
			e.MissingForeign = tc.override
			plan := rootWith(g.A, childNode("child", e, g.B))
			_, err := Materialize(plan, []any{toyARow{id: "a1", childFK: "gone"}}, ctx)
			if (err != nil) != tc.wantErr {
				t.Errorf("err = %v, wantErr = %v", err, tc.wantErr)
			}
		})
	}
}

// ------------------------------------------------------------------ FetcherContractPolicy

// reversePlan builds a root→kids reverse plan; contract strictness comes from
// Ctx.Policies.FetcherContract (see strictCtx below).
func reversePlan(g *toyGraph, limit int, bare bool) *PlanNode {
	e := g.A.Edges()["kids"]
	e.Limit = limit
	e.Bare = bare
	return rootWith(g.A, childNode("kids", e, g.B))
}

// strictCtx is byteCtx with the strict fetcher contract installed engine-wide.
func strictCtx(reg Registry) *Ctx {
	ctx := byteCtx(reg)
	ctx.Policies.FetcherContract = FetcherContractStrict
	return ctx
}

// Under FetcherContractStrict each silent correction becomes a failed request
// naming the edge and the violation; the default stays tolerant.
func TestFetcherContract_Reverse(t *testing.T) {
	g := buildToyGraph()

	t.Run("over-limit rows", func(t *testing.T) {
		// fakeReg honours q.Limit (a correct fetcher), so a broken one is
		// stubbed by hand: three rows against Limit 2.
		reg := &stubParentsReg{result: map[string]ParentRows{
			"a1": {Rows: []any{toyBRow{id: "b1"}, toyBRow{id: "b2"}, toyBRow{id: "b3"}}},
		}}
		_, err := Materialize(reversePlan(g, 2, false), []any{toyARow{id: "a1"}}, strictCtx(reg))
		if err == nil || !strings.Contains(err.Error(), "returned 3 rows") {
			t.Errorf("want an over-limit violation, got %v", err)
		}

		// tolerant (the zero-Ctx default): same data, silent truncation
		out, err := Materialize(reversePlan(g, 2, false), []any{toyARow{id: "a1"}}, byteCtx(reg))
		if err != nil || strings.Count(string(out[0]), `"id"`) != 3 { // a1 + b1 + b2
			t.Errorf("tolerant must truncate: %s / %v", out, err)
		}
	})

	t.Run("unrequested parent id", func(t *testing.T) {
		reg := &stubParentsReg{result: map[string]ParentRows{
			"a1":    {Rows: []any{toyBRow{id: "b1"}}},
			"ghost": {Rows: []any{toyBRow{id: "b9"}}},
		}}
		_, err := Materialize(reversePlan(g, 2, false), []any{toyARow{id: "a1"}}, strictCtx(reg))
		if err == nil || !strings.Contains(err.Error(), `parent id "ghost"`) {
			t.Errorf("want an unrequested-parent violation, got %v", err)
		}
	})

	t.Run("HasMore on a bare edge", func(t *testing.T) {
		reg := &stubParentsReg{result: map[string]ParentRows{
			"a1": {Rows: []any{toyBRow{id: "b1"}}, HasMore: true},
		}}
		_, err := Materialize(reversePlan(g, 0, true), []any{toyARow{id: "a1"}}, strictCtx(reg))
		if err == nil || !strings.Contains(err.Error(), "HasMore") {
			t.Errorf("want a bare-HasMore violation, got %v", err)
		}
	})
}

// FetchIDs returning a row that was never requested violates the forward
// contract under strict; tolerant leaves it unread.
func TestFetcherContract_ForwardUnrequestedRow(t *testing.T) {
	g := buildToyGraph()
	reg := &extraRowsReg{inner: newFakeReg(), extra: toyBRow{id: "stowaway"}}
	reg.inner.putByID(g.B, "b1", toyBRow{id: "b1"})

	e := g.A.Edges()["child"]
	child := childNode("child", e, g.B)
	_, err := Materialize(rootWith(g.A, child), []any{toyARow{id: "a1", childFK: "b1"}}, strictCtx(reg))
	if err == nil || !strings.Contains(err.Error(), `"stowaway"`) {
		t.Errorf("want an unrequested-row violation, got %v", err)
	}

	// tolerant (the zero-Ctx default) leaves the extra row unread
	if _, err := Materialize(rootWith(g.A, child), []any{toyARow{id: "a1", childFK: "b1"}}, byteCtx(reg)); err != nil {
		t.Errorf("tolerant must ignore the extra row, got %v", err)
	}
}

// extraRowsReg decorates a fakeReg so every FetchIDs answer smuggles one extra
// row the engine never asked for.
type extraRowsReg struct {
	inner *fakeReg
	extra any
}

var _ Registry = (*extraRowsReg)(nil)

func (r *extraRowsReg) FetchByEdge(p Resource, k string) (FetchByParents, bool) {
	return r.inner.FetchByEdge(p, k)
}

func (r *extraRowsReg) FetchByIDs(res Resource) (FetchByIDs, bool) {
	inner, ok := r.inner.FetchByIDs(res)
	if !ok {
		return nil, false
	}
	return func(c *Ctx, ids []string) ([]any, error) {
		rows, err := inner(c, ids)
		return append(rows, r.extra), err
	}, true
}

func (r *extraRowsReg) FetchByParents(res Resource) (FetchByParents, bool) {
	return r.inner.FetchByParents(res)
}

// ------------------------------------------------------------------ row budget

func TestMaterialize_CountsRows(t *testing.T) {
	g := buildToyGraph()
	reg := newFakeReg()
	reg.putChildren(g.B, "a1", []any{toyBRow{id: "b1", parentID: "a1"}, toyBRow{id: "b2", parentID: "a1"}})
	ctx := byteCtx(reg)
	child := childNode("kids", g.A.Edges()["kids"], g.B)
	if _, err := Materialize(rootWith(g.A, child), []any{toyARow{id: "a1"}}, ctx); err != nil {
		t.Fatal(err)
	}
	if ctx.Rows() != 3 {
		t.Errorf("Rows() = %d, want 3 (1 root + 2 kids)", ctx.Rows())
	}
}

func TestMaterialize_BudgetExceeded(t *testing.T) {
	g := buildToyGraph()
	reg := newFakeReg()
	reg.putChildren(g.B, "a1", []any{toyBRow{id: "b1", parentID: "a1"}, toyBRow{id: "b2", parentID: "a1"}})
	ctx := byteCtx(reg)
	child := childNode("kids", g.A.Edges()["kids"], g.B)
	root := rootWith(g.A, child)
	root.MaxRows = 2
	_, err := Materialize(root, []any{toyARow{id: "a1"}}, ctx)
	if errCode(err) != INCLUDE_BUDGET_EXCEEDED {
		t.Fatalf("err = %v, want INCLUDE_BUDGET_EXCEEDED", err)
	}
	// MaxRows 0 on a hand-built root: no budget.
	root.MaxRows = 0
	if _, err := Materialize(root, []any{toyARow{id: "a1"}}, byteCtx(reg)); err != nil {
		t.Fatalf("no budget: %v", err)
	}
}

func TestMaterialize_CancelledContext(t *testing.T) {
	g := buildToyGraph()
	ctx := byteCtx(newFakeReg())
	c, cancel := context.WithCancel(context.Background())
	cancel()
	ctx.Context = c
	_, err := Materialize(leafPlan(g.A), []any{toyARow{id: "a1"}}, ctx)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v, want context.Canceled", err)
	}
}
