package include

import (
	"encoding/json"
	"testing"
)

// dataEnv is the spec's reference style: {"data": …} + {"data":[…],"pagination":{…}}.
var dataEnv = Envelope{Key: "data", Pagination: "pagination"}

func TestEnvelopePlain(t *testing.T) {
	if !(Envelope{}).Plain() {
		t.Error("zero Envelope must be Plain")
	}
	if (Envelope{Key: "data"}).Plain() {
		t.Error("Envelope with Key must not be Plain")
	}
}

func TestWrapData(t *testing.T) {
	obj := json.RawMessage(`{"id":"b1"}`)
	if got := wrapData(obj, Envelope{}); string(got) != `{"id":"b1"}` {
		t.Errorf("plain: %s", got)
	}
	if got := wrapData(obj, dataEnv); string(got) != `{"data":{"id":"b1"}}` {
		t.Errorf("wrapped: %s", got)
	}
	if got := wrapData(nullRaw, dataEnv); string(got) != `{"data":null}` {
		t.Errorf("null: %s", got)
	}
	if got := wrapData(nil, dataEnv); string(got) != `{"data":null}` {
		t.Errorf("empty raw: %s", got)
	}
	if got := wrapData(obj, Envelope{Key: "values"}); string(got) != `{"values":{"id":"b1"}}` {
		t.Errorf("custom key: %s", got)
	}
}

func TestMarshalEdgeValue(t *testing.T) {
	items := []json.RawMessage{json.RawMessage(`{"id":"b1"}`), json.RawMessage(`{"id":"b2"}`)}
	cases := []struct {
		name    string
		hasMore bool
		cursor  string
		bare    bool
		env     Envelope
		want    string
	}{
		{"plain enveloped", true, "", false, Envelope{}, `{"items":[{"id":"b1"},{"id":"b2"}],"hasMore":true}`},
		{"plain cursor", true, "abc", false, Envelope{}, `{"items":[{"id":"b1"},{"id":"b2"}],"hasMore":true,"nextCursor":"abc"}`},
		{"plain bare", true, "abc", true, Envelope{}, `[{"id":"b1"},{"id":"b2"}]`},
		{"wrapped", true, "", false, dataEnv, `{"data":[{"id":"b1"},{"id":"b2"}],"pagination":{"hasNextPage":true}}`},
		{"wrapped false", false, "", false, dataEnv, `{"data":[{"id":"b1"},{"id":"b2"}],"pagination":{"hasNextPage":false}}`},
		{"wrapped cursor", true, "abc", false, dataEnv, `{"data":[{"id":"b1"},{"id":"b2"}],"pagination":{"hasNextPage":true,"nextCursor":"abc"}}`},
		{"wrapped bare", true, "abc", true, dataEnv, `{"data":[{"id":"b1"},{"id":"b2"}]}`},
		{"wrapped no pagination key", true, "abc", false, Envelope{Key: "data"}, `{"data":[{"id":"b1"},{"id":"b2"}]}`},
		{"custom names", true, "", false, Envelope{Key: "values", Pagination: "meta"}, `{"values":[{"id":"b1"},{"id":"b2"}],"meta":{"hasNextPage":true}}`},
		{"wrapped empty", false, "", false, dataEnv, `{"data":[],"pagination":{"hasNextPage":false}}`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			in := items
			if tc.name == "wrapped empty" {
				in = nil
			}
			got := marshalEdgeValue(in, tc.hasMore, tc.cursor, tc.bare, tc.env)
			if string(got) != tc.want {
				t.Errorf("got  %s\nwant %s", got, tc.want)
			}
		})
	}
}

func TestWrapItems(t *testing.T) {
	items := []json.RawMessage{json.RawMessage(`{"id":"b1"}`)}
	if got := wrapItems(items, Envelope{}); &got[0][0] != &items[0][0] {
		t.Error("plain wrapItems must return the input slice untouched")
	}
	got := wrapItems(items, dataEnv)
	if string(got[0]) != `{"data":{"id":"b1"}}` {
		t.Errorf("wrapped item: %s", got[0])
	}
}

func withEnv(e Edge, env Envelope) Edge { e.Envelope = env; return e }

func TestEnvelope_ToOne_PresentAndNull(t *testing.T) {
	g := buildToyGraph()
	ctx := byteCtx(g.Reg)
	plan := rootWith(g.A, childNode("child", withEnv(g.A.Edges()["child"], dataEnv), g.B))
	docs := []any{
		toyARow{id: "a1", name: "Alpha", childFK: "b1"},
		toyARow{id: "a2", name: "Beta", childFK: ""},    // empty FK → {"data":null}
		toyARow{id: "a3", name: "Gamma", childFK: "zz"}, // dangling, MissingForeignNull → {"data":null}
	}
	out, err := Materialize(plan, docs, ctx)
	if err != nil {
		t.Fatalf("Materialize: %v", err)
	}
	want := []string{
		`{"id":"a1","name":"Alpha","child":{"data":{"id":"b1","label":"L1"}}}`,
		`{"id":"a2","name":"Beta","child":{"data":null}}`,
		`{"id":"a3","name":"Gamma","child":{"data":null}}`,
	}
	for i := range want {
		if string(out[i]) != want[i] {
			t.Errorf("out[%d] =\n  %s\nwant\n  %s", i, out[i], want[i])
		}
	}
}

func TestEnvelope_ToOne_GuardFalse(t *testing.T) {
	g := buildToyGraph()
	ctx := byteCtx(g.Reg)
	e := withEnv(g.A.Edges()["child"], dataEnv)
	e.Guard = func(*Ctx, any) bool { return false }
	plan := rootWith(g.A, childNode("child", e, g.B))
	out, err := Materialize(plan, []any{toyARow{id: "a1", name: "Alpha", childFK: "b1"}}, ctx)
	if err != nil {
		t.Fatalf("Materialize: %v", err)
	}
	if want := `{"id":"a1","name":"Alpha","child":{"data":null}}`; string(out[0]) != want {
		t.Errorf("guard-false =\n  %s\nwant\n  %s", out[0], want)
	}
}

func TestEnvelope_Reverse_Enveloped(t *testing.T) {
	g := buildToyGraph()
	ctx := byteCtx(g.Reg)
	plan := rootWith(g.A, childNode("kids", withEnv(g.A.Edges()["kids"], dataEnv), g.B))
	out, err := Materialize(plan, []any{toyARow{id: "a1", name: "Alpha"}, toyARow{id: "ghost", name: "Nobody"}}, ctx)
	if err != nil {
		t.Fatalf("Materialize: %v", err)
	}
	want0 := `{"id":"a1","name":"Alpha","kids":{"data":[{"data":{"id":"b1","label":"L1"}},{"data":{"id":"b2","label":"L2"}}],"pagination":{"hasNextPage":true}}}`
	if string(out[0]) != want0 {
		t.Errorf("reverse =\n  %s\nwant\n  %s", out[0], want0)
	}
	want1 := `{"id":"ghost","name":"Nobody","kids":{"data":[],"pagination":{"hasNextPage":false}}}`
	if string(out[1]) != want1 {
		t.Errorf("empty reverse =\n  %s\nwant\n  %s", out[1], want1)
	}
}

func TestEnvelope_Reverse_GuardFalse_EmptyWrapped(t *testing.T) {
	g := buildToyGraph()
	ctx := byteCtx(g.Reg)
	e := withEnv(g.A.Edges()["kids"], dataEnv)
	e.Guard = func(*Ctx, any) bool { return false }
	out, err := Materialize(rootWith(g.A, childNode("kids", e, g.B)), []any{toyARow{id: "a1", name: "Alpha"}}, ctx)
	if err != nil {
		t.Fatalf("Materialize: %v", err)
	}
	if want := `{"id":"a1","name":"Alpha","kids":{"data":[],"pagination":{"hasNextPage":false}}}`; string(out[0]) != want {
		t.Errorf("guard-false reverse =\n  %s\nwant\n  %s", out[0], want)
	}
}

func TestEnvelope_Reverse_Bare_And_NoPaginationKey(t *testing.T) {
	g := buildToyGraph()
	ctx := byteCtx(g.Reg)
	bare := Edge{Target: func() Resource { return g.B }, Many: true, Backref: "parentID", Includable: true, Bare: true, Envelope: dataEnv}
	out, err := Materialize(rootWith(g.A, childNode("kids", bare, g.B)), []any{toyARow{id: "a1", name: "Alpha"}}, ctx)
	if err != nil {
		t.Fatalf("Materialize: %v", err)
	}
	want := `{"id":"a1","name":"Alpha","kids":{"data":[{"data":{"id":"b1","label":"L1"}},{"data":{"id":"b2","label":"L2"}},{"data":{"id":"b3","label":"L3"}}]}}`
	if string(out[0]) != want {
		t.Errorf("bare wrapped =\n  %s\nwant\n  %s", out[0], want)
	}

	noPag := withEnv(g.A.Edges()["kids"], Envelope{Key: "data"}) // Limit 2, HasMore dropped
	out, err = Materialize(rootWith(g.A, childNode("kids", noPag, g.B)), []any{toyARow{id: "a1", name: "Alpha"}}, ctx)
	if err != nil {
		t.Fatalf("Materialize: %v", err)
	}
	want = `{"id":"a1","name":"Alpha","kids":{"data":[{"data":{"id":"b1","label":"L1"}},{"data":{"id":"b2","label":"L2"}}]}}`
	if string(out[0]) != want {
		t.Errorf("no-pagination wrapped =\n  %s\nwant\n  %s", out[0], want)
	}
}

func TestEnvelope_Reverse_NextCursorInsidePagination(t *testing.T) {
	g := buildToyGraph()
	reg := &stubParentsReg{result: map[string]ParentRows{
		"p1": {Rows: bRows("b1"), HasMore: true, NextCursor: "abc"},
		"p2": {Rows: bRows("b2"), HasMore: true},
	}}
	ctx := byteCtx(reg)
	plan := rootWith(g.A, childNode("kids", withEnv(g.A.Edges()["kids"], dataEnv), g.B))
	out, err := Materialize(plan, []any{toyARow{id: "p1"}, toyARow{id: "p2"}}, ctx)
	if err != nil {
		t.Fatalf("Materialize: %v", err)
	}
	want0 := `{"id":"p1","name":"","kids":{"data":[{"data":{"id":"b1","label":"Lb1"}}],"pagination":{"hasNextPage":true,"nextCursor":"abc"}}}`
	if string(out[0]) != want0 {
		t.Errorf("cursor =\n  %s\nwant\n  %s", out[0], want0)
	}
	want1 := `{"id":"p2","name":"","kids":{"data":[{"data":{"id":"b2","label":"Lb2"}}],"pagination":{"hasNextPage":true}}}`
	if string(out[1]) != want1 {
		t.Errorf("no cursor =\n  %s\nwant\n  %s", out[1], want1)
	}
}

func TestEnvelope_Reverse_CustomNames(t *testing.T) {
	g := buildToyGraph()
	ctx := byteCtx(g.Reg)
	custom := withEnv(g.A.Edges()["kids"], Envelope{Key: "values", Pagination: "meta"})
	out, err := Materialize(rootWith(g.A, childNode("kids", custom, g.B)), []any{toyARow{id: "a1", name: "Alpha"}}, ctx)
	if err != nil {
		t.Fatalf("Materialize: %v", err)
	}
	want := `{"id":"a1","name":"Alpha","kids":{"values":[{"values":{"id":"b1","label":"L1"}},{"values":{"id":"b2","label":"L2"}}],"meta":{"hasNextPage":true}}}`
	if string(out[0]) != want {
		t.Errorf("custom names =\n  %s\nwant\n  %s", out[0], want)
	}
}

func TestEnvelope_ForwardHasMany(t *testing.T) {
	g := buildToyGraph()
	ctx := byteCtx(g.Reg)
	type manyRow struct {
		id   string
		bIDs []string
	}
	mres := &toyRes{
		name: "Many", slug: "many", fields: []string{"id"},
		idOf: func(doc any) string { return doc.(manyRow).id },
		serialize: func(doc any, _ *Ctx) any {
			return toyAWire{ID: doc.(manyRow).id}
		},
	}
	many := Edge{Target: func() Resource { return g.B }, Many: true, Includable: true, Limit: 2, Envelope: dataEnv,
		ForeignKeys: func(a any) []string { return a.(manyRow).bIDs }}
	out, err := Materialize(rootWith(mres, childNode("bs", many, g.B)), []any{manyRow{id: "m1", bIDs: []string{"b3", "b1", "b2"}}}, ctx)
	if err != nil {
		t.Fatalf("Materialize: %v", err)
	}
	want := `{"id":"m1","name":"","bs":{"data":[{"data":{"id":"b3","label":"L3"}},{"data":{"id":"b1","label":"L1"}}],"pagination":{"hasNextPage":true}}}`
	if string(out[0]) != want {
		t.Errorf("forward has-many =\n  %s\nwant\n  %s", out[0], want)
	}
}

func TestEnvelope_InArray(t *testing.T) {
	g := buildToyGraph()
	ctx := byteCtx(g.Reg)
	plan := rootWith(inArrRes(), childNode("attendees", inArrEdge(g.B, func(e *Edge) { e.Envelope = dataEnv }), g.B))
	docs := []any{inArrRow{id: "p1", arr: json.RawMessage(`[{"role":"a"},{"role":"b"}]`), userIDs: []string{"b1", "zz"}}}
	out, err := Materialize(plan, docs, ctx)
	if err != nil {
		t.Fatalf("Materialize: %v", err)
	}
	want := `{"id":"p1","participants":[{"role":"a","user":{"data":{"id":"b1","label":"L1"}}},{"role":"b","user":{"data":null}}]}`
	if string(out[0]) != want {
		t.Errorf("in-array =\n  %s\nwant\n  %s", out[0], want)
	}
}

func TestEnvelope_NestedLevelsCompose(t *testing.T) {
	g := buildToyGraph()
	ctx := byteCtx(g.Reg)
	self := withEnv(g.A.Edges()["self"], dataEnv)
	child := withEnv(g.A.Edges()["child"], dataEnv)
	plan := rootWith(g.A, childNode("self", self, g.A, childNode("child", child, g.B)))
	out, err := Materialize(plan, []any{toyARow{id: "a1", name: "Alpha", selfFK: "a2"}}, ctx)
	if err != nil {
		t.Fatalf("Materialize: %v", err)
	}
	want := `{"id":"a1","name":"Alpha","self":{"data":{"id":"a2","name":"Beta","child":{"data":{"id":"b2","label":"L2"}}}}}`
	if string(out[0]) != want {
		t.Errorf("nested =\n  %s\nwant\n  %s", out[0], want)
	}
}
