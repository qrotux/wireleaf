package include

import (
	"encoding/json"
	"errors"
	"testing"
)

// ---------------------------------------------------------------------------
// Toy graph for the engine (materialize) tests.
//
// The nodes are INLINE Resource implementations (toyRes) with pluggable
// serialize/idOf/enrich closures rather than graph.Define constructions: the
// graph package imports include, so a test file in package include that
// imported it would create an import cycle.
//
// Type names are deliberately distinct from resolve_test.go's `tRes`.
// ---------------------------------------------------------------------------

// ------------------------------------------------------------------ row + wire types

// toyARow is the "DB row" for the root node.
type toyARow struct {
	id   string
	name string
	// childFK is the forward to-one FK into toyB (empty → null child).
	childFK string
	// selfFK is the forward to-one self-FK into toyA.
	selfFK string
}

// toyBRow is the "DB row" for the leaf node.
type toyBRow struct {
	id    string
	label string
	// parentID is the reverse backref: which toyA this B belongs to (for `kids`).
	parentID string
}

// toyAWire is the scalar wire struct emitted by toyA.Serialize.
// Field order here defines the JSON key order the assembler must preserve.
type toyAWire struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

// toyBWire is the scalar wire struct emitted by toyB.Serialize.
type toyBWire struct {
	ID    string `json:"id"`
	Label string `json:"label"`
}

// ------------------------------------------------------------------ toyRes

// toyRes is a single configurable inline Resource used for every toy node.
// The per-node behavior (scalar shape, id extraction, optional enrich) is
// injected as closures so one type covers both toyA and toyB.
type toyRes struct {
	name      string
	slug      string
	fields    []string
	defaults  []string
	edges     map[string]Edge
	serialize func(doc any, ctx *Ctx) any
	idOf      func(doc any) string
	enrich    func(docs []any, ctx *Ctx) error
}

func (r *toyRes) Name() string           { return r.name }
func (r *toyRes) Slug() string           { return r.slug }
func (r *toyRes) Fields() []string       { return r.fields }
func (r *toyRes) Defaults() []string     { return r.defaults }
func (r *toyRes) Edges() map[string]Edge { return r.edges }

// setEdges is a late-binding setter letting the toy graph declare all node vars
// first, then wire cyclic edges (self, child, kids).
func (r *toyRes) setEdges(m map[string]Edge) { r.edges = m }

func (r *toyRes) IDOf(doc any) string { return r.idOf(doc) }

func (r *toyRes) Serialize(doc any, ctx *Ctx) any { return r.serialize(doc, ctx) }

func (r *toyRes) Enrich(docs []any, ctx *Ctx) error {
	if r.enrich == nil {
		return nil
	}
	return r.enrich(docs, ctx)
}

// Compile-time assertion.
var _ Resource = (*toyRes)(nil)

// ------------------------------------------------------------------ fakeReg

// fakeReg is an in-memory Registry backed by canned rows. It lets the engine be
// exercised without a database.
type fakeReg struct {
	// byID maps a node Name() → (row id → row) for FetchByIDs lookups.
	byID map[string]map[string]any
	// children maps a node Name() → (parent id → ordered child rows) for
	// FetchByParents lookups.
	children map[string]map[string][]any
}

var _ Registry = (*fakeReg)(nil)

// fakeReg has node-level fetchers only.
func (f *fakeReg) FetchByEdge(Resource, string) (FetchByParents, bool) { return nil, false }

func newFakeReg() *fakeReg {
	return &fakeReg{
		byID:     map[string]map[string]any{},
		children: map[string]map[string][]any{},
	}
}

// putByID registers a forward-fetchable row under node, keyed by id.
func (f *fakeReg) putByID(node Resource, id string, row any) {
	m := f.byID[node.Name()]
	if m == nil {
		m = map[string]any{}
		f.byID[node.Name()] = m
	}
	m[id] = row
}

// putChildren registers the ordered children of parentID under node.
func (f *fakeReg) putChildren(node Resource, parentID string, rows []any) {
	m := f.children[node.Name()]
	if m == nil {
		m = map[string][]any{}
		f.children[node.Name()] = m
	}
	m[parentID] = rows
}

// FetchByIDs returns a fetcher over node's canned rows. It returns rows whose id
// ∈ ids in INPUT-ID ORDER, silently dropping ids with no canned row (simulating
// an access-dropped / missing record).
func (f *fakeReg) FetchByIDs(node Resource) (FetchByIDs, bool) {
	table, ok := f.byID[node.Name()]
	if !ok {
		return nil, false
	}
	return func(_ *Ctx, ids []string) ([]any, error) {
		out := make([]any, 0, len(ids))
		for _, id := range ids {
			if row, ok := table[id]; ok {
				out = append(out, row)
			}
		}
		return out, nil
	}, true
}

// FetchByParents returns the BATCHED reverse fetcher over node's canned
// children: one call covers every requested parent id.
//   - q.Limit > 0: envelope probe — the fetcher itself does the limit+1 work,
//     reporting hasMore when the canned set exceeds the limit and truncating
//     the returned slice to it.
//   - q.Limit <= 0: bare / fetch-all — every child, hasMore=false.
func (f *fakeReg) FetchByParents(node Resource) (FetchByParents, bool) {
	table, ok := f.children[node.Name()]
	if !ok {
		return nil, false
	}
	return func(_ *Ctx, parentIDs []string, q EdgeQuery) (map[string]ParentRows, error) {
		out := make(map[string]ParentRows, len(parentIDs))
		for _, parentID := range parentIDs {
			all := table[parentID]
			if q.Limit <= 0 {
				// bare / fetch-all
				rows := make([]any, len(all))
				copy(rows, all)
				out[parentID] = ParentRows{Rows: rows}
				continue
			}
			n := q.Limit
			if n > len(all) {
				n = len(all)
			}
			rows := make([]any, n)
			copy(rows, all[:n])
			out[parentID] = ParentRows{Rows: rows, HasMore: len(all) > q.Limit}
		}
		return out, nil
	}, true
}

// ------------------------------------------------------------------ graph builder

// toyGraph bundles the assembled nodes and a registry preloaded with canned rows.
type toyGraph struct {
	A   *toyRes // root {id,name}
	B   *toyRes // leaf {id,label}
	Reg *fakeReg
}

// buildToyGraph assembles the concrete toy graph:
//
//	toyA (root, scalar {id,name}):
//	  child → toyB   (includable to-one, FK=childFK)
//	  kids  → toyB   (includable to-many reverse, Backref="parentID")
//	  self  → toyA   (includable + DEFAULT to-one self-edge, FK=selfFK; cycle test)
//	toyB (leaf, scalar {id,label})
//
// Edges are wired via setEdges AFTER both node vars exist, so the cyclic self
// and cross edges compile without declaration-order gymnastics.
//
// The registry is preloaded with a few canned toyA + toyB rows so materialize
// tests can fetch real data:
//   - toyA byID: a1 (child=b1, self=a2), a2 (child=b2, self="")
//   - toyB byID: b1{label L1}, b2{label L2}, b3{label L3}
//   - toyB children of a1 (for `kids`): [b1, b2, b3]
func buildToyGraph() *toyGraph {
	A := &toyRes{
		name:   "ToyA",
		slug:   "toyA",
		fields: []string{"id", "name"},
		// self is a default edge (exercises the default-cycle guard).
		defaults: []string{"self"},
		idOf:     func(doc any) string { return doc.(toyARow).id },
		serialize: func(doc any, _ *Ctx) any {
			r := doc.(toyARow)
			return toyAWire{ID: r.id, Name: r.name}
		},
	}
	B := &toyRes{
		name:   "ToyB",
		slug:   "toyB",
		fields: []string{"id", "label"},
		idOf:   func(doc any) string { return doc.(toyBRow).id },
		serialize: func(doc any, _ *Ctx) any {
			r := doc.(toyBRow)
			return toyBWire{ID: r.id, Label: r.label}
		},
	}

	// Late-bind edges now that both A and B exist.
	A.setEdges(map[string]Edge{
		// includable to-one forward edge; parent holds childFK. ForeignKey reads the
		// forward FK VALUE off the typed toyARow (the engine's to-one id source).
		"child": {Target: func() Resource { return B }, Includable: true, ForeignKey: func(a any) string { return a.(toyARow).childFK }},
		// includable to-many reverse edge; child holds parentID backref (no ForeignKey —
		// reverse fetches by the parent's own id via FetchByParents).
		"kids": {Target: func() Resource { return B }, Many: true, Backref: "parentID", Includable: true, Limit: 2},
		// default + includable to-one self-edge (cycle terminates via the guard).
		// ForeignKey reads selfFK (a1→a2, a2→"" which terminates the to-one recursion).
		"self": {Target: func() Resource { return A }, Includable: true, ForeignKey: func(a any) string { return a.(toyARow).selfFK }},
		// includable COMPUTED edge: no Target, value produced by application
		// code, so the engine plans it and then skips it at materialize time.
		// It declares one client arg to exercise computed arg validation.
		"stats": {
			Computed:   true,
			Includable: true,
			Args: []EdgeArg{{Name: "kind", Validate: func(raw any) error {
				if raw == "bad" {
					return errors.New("bad kind")
				}
				return nil
			}}},
		},
		// a NON-includable computed edge (deny-by-default gate regression).
		"secretStats": {Computed: true},
	})
	B.setEdges(map[string]Edge{}) // leaf: no edges

	reg := newFakeReg()
	// toyA rows.
	reg.putByID(A, "a1", toyARow{id: "a1", name: "Alpha", childFK: "b1", selfFK: "a2"})
	reg.putByID(A, "a2", toyARow{id: "a2", name: "Beta", childFK: "b2", selfFK: ""})
	// toyB rows.
	reg.putByID(B, "b1", toyBRow{id: "b1", label: "L1", parentID: "a1"})
	reg.putByID(B, "b2", toyBRow{id: "b2", label: "L2", parentID: "a1"})
	reg.putByID(B, "b3", toyBRow{id: "b3", label: "L3", parentID: "a1"})
	// reverse children of a1 (for the `kids` to-many edge): three B rows.
	reg.putChildren(B, "a1", []any{
		toyBRow{id: "b1", label: "L1", parentID: "a1"},
		toyBRow{id: "b2", label: "L2", parentID: "a1"},
		toyBRow{id: "b3", label: "L3", parentID: "a1"},
	})

	return &toyGraph{A: A, B: B, Reg: reg}
}

// ------------------------------------------------------------------ smoke test

// TestToyGraphWellFormed exercises the builder + fakeReg so this file compiles
// and its behavior (boxing, id-order fetch, access-drop, envelope probe, bare
// fetch-all) is pinned for the engine tests that depend on it.
func TestToyGraphWellFormed(t *testing.T) {
	g := buildToyGraph()

	// --- graph shape ---
	if g.A.Name() != "ToyA" || g.B.Name() != "ToyB" {
		t.Fatalf("node names = %q/%q", g.A.Name(), g.B.Name())
	}
	if got := g.A.Defaults(); len(got) != 1 || got[0] != "self" {
		t.Fatalf("toyA defaults = %v, want [self]", got)
	}
	edges := g.A.Edges()
	for _, k := range []string{"child", "kids", "self"} {
		if _, ok := edges[k]; !ok {
			t.Fatalf("toyA missing edge %q", k)
		}
	}
	if !edges["child"].Includable || edges["child"].Many {
		t.Errorf("child edge = %+v, want includable to-one", edges["child"])
	}
	if EdgeKind(edges["kids"]) != KindReverse {
		t.Errorf("kids EdgeKind = %v, want reverse", EdgeKind(edges["kids"]))
	}
	if edges["self"].Target() != g.A {
		t.Error("self edge Target should resolve to toyA")
	}
	if len(g.B.Edges()) != 0 {
		t.Errorf("toyB should be a leaf, got edges %v", g.B.Edges())
	}

	// --- Serialize produces the scalar wire struct (JSON-assertable) ---
	raw, err := json.Marshal(g.A.Serialize(toyARow{id: "a1", name: "Alpha"}, nil))
	if err != nil {
		t.Fatalf("marshal toyA wire: %v", err)
	}
	if string(raw) != `{"id":"a1","name":"Alpha"}` {
		t.Fatalf("toyA wire JSON = %s", raw)
	}
	if g.A.IDOf(toyARow{id: "a1"}) != "a1" {
		t.Error("toyA IDOf")
	}

	ctx := &Ctx{Registry: g.Reg}

	// --- FetchByIDs: input-id order + access-drop of unknown ids ---
	fi, ok := g.Reg.FetchByIDs(g.B)
	if !ok {
		t.Fatal("FetchByIDs(toyB) missing")
	}
	rows, err := fi(ctx, []string{"b2", "nope", "b1"})
	if err != nil {
		t.Fatalf("FetchByIDs err: %v", err)
	}
	if len(rows) != 2 {
		t.Fatalf("got %d rows, want 2 (unknown dropped)", len(rows))
	}
	if rows[0].(toyBRow).id != "b2" || rows[1].(toyBRow).id != "b1" {
		t.Fatalf("FetchByIDs order = %q,%q, want b2,b1", rows[0].(toyBRow).id, rows[1].(toyBRow).id)
	}

	// --- FetchByParents envelope probe: limit 2 over 3 canned → hasMore, truncated ---
	fp, ok := g.Reg.FetchByParents(g.B)
	if !ok {
		t.Fatal("FetchByParents(toyB) missing")
	}
	res, err := fp(ctx, []string{"a1"}, EdgeQuery{Limit: 2})
	if err != nil {
		t.Fatalf("FetchByParents err: %v", err)
	}
	kids := res["a1"]
	if !kids.HasMore {
		t.Error("hasMore should be true (3 canned > limit 2)")
	}
	if len(kids.Rows) != 2 || kids.Rows[0].(toyBRow).id != "b1" || kids.Rows[1].(toyBRow).id != "b2" {
		t.Fatalf("enveloped kids = %v, want [b1 b2]", kids.Rows)
	}

	// --- FetchByParents bare / fetch-all: limit <= 0 → all, hasMore=false ---
	resAll, err := fp(ctx, []string{"a1"}, EdgeQuery{Limit: 0})
	if err != nil {
		t.Fatalf("bare FetchByParents err: %v", err)
	}
	if resAll["a1"].HasMore {
		t.Error("bare fetch must report hasMore=false")
	}
	if len(resAll["a1"].Rows) != 3 {
		t.Fatalf("bare fetch got %d, want 3", len(resAll["a1"].Rows))
	}

	// --- unknown parent → empty, no error ---
	resNone, err := fp(ctx, []string{"ghost"}, EdgeQuery{Limit: 5})
	if err != nil || len(resNone["ghost"].Rows) != 0 {
		t.Fatalf("unknown parent: rows=%v err=%v", resNone["ghost"].Rows, err)
	}
}
