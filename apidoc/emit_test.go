package apidoc

// emit_test.go — the emission spec, in-core over the stub reflector.
//
// Fixtures are hand-built include.Edge values plus emitRes/bareRes stubs
// (testhelpers_test.go): package apidoc cannot import graph (import cycle), and
// include.Edge is public engine API that needs no constructor. The
// reflector-integration copies of these cases live in the adapter module.

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"

	"github.com/qrotux/wireleaf/include"
)

// ---------------------------------------------------------------------------
// ported adapter-module emission spec
// ---------------------------------------------------------------------------

func TestEmitComponentsStitchesEdgeKinds(t *testing.T) {
	g := buildToyGraph()
	out, err := EmitComponents(stubReflector{}, []include.Resource{g.Book})
	if err != nil {
		t.Fatalf("EmitComponents: %v", err)
	}

	book := component(t, out, "Book")

	if got, want := prop(t, book, "author"), anyOfNull(ref("Author")); !reflect.DeepEqual(got, want) {
		t.Errorf("author fragment = %v, want %v", got, want)
	}
	if got, want := prop(t, book, "reviews"), envelopeOf(ref("Review")); !reflect.DeepEqual(got, want) {
		t.Errorf("reviews fragment = %v, want %v", got, want)
	}
	wantSimilar := anyOfNull(map[string]any{"type": "array", "items": ref("Book")})
	if got := prop(t, book, "similar"); !reflect.DeepEqual(got, wantSimilar) {
		t.Errorf("similar fragment = %v, want %v", got, wantSimilar)
	}
	wantImages := anyOfNull(map[string]any{
		"type": "array",
		"items": map[string]any{
			"type":       "object",
			"properties": map[string]any{"image": anyOfNull(ref("Image"))},
			"required":   []any{"image"},
		},
	})
	if got := prop(t, book, "images"); !reflect.DeepEqual(got, wantImages) {
		t.Errorf("images fragment = %v, want %v", got, wantImages)
	}

	if !hasProp(book, "id") || !hasProp(book, "title") {
		t.Errorf("Book lost its scalar properties: %v", book["properties"])
	}
	if hasProp(book, "notes") {
		t.Errorf("non-includable edge %q leaked into properties", "notes")
	}

	req := requiredKeys(t, book)
	for _, key := range []string{"author", "reviews", "similar", "images"} {
		if contains(req, key) {
			t.Errorf("edge %q must not be required, got required=%v", key, req)
		}
	}
	if !contains(req, "id") {
		t.Errorf("scalar id should stay required, got %v", req)
	}

	// The cycle Book↔Author is walked once and both nodes are emitted.
	author := component(t, out, "Author")
	if got, want := prop(t, author, "books"), envelopeOf(ref("Book")); !reflect.DeepEqual(got, want) {
		t.Errorf("Author.books fragment = %v, want %v", got, want)
	}
	if _, ok := out["Review"]; !ok {
		t.Errorf("Review component missing; emitted %v", keysOf(out))
	}
}

// A DocExternal node is skipped by the reachability walk (its fragment is owned
// elsewhere) yet the $ref pointing at it is still stitched.
func TestEmitComponentsSkipsDocExternalTargetButStitchesRef(t *testing.T) {
	g := buildToyGraph()
	out, err := EmitComponents(stubReflector{}, []include.Resource{g.Book})
	if err != nil {
		t.Fatalf("EmitComponents: %v", err)
	}
	if _, emitted := out["Image"]; emitted {
		t.Errorf("DocExternal node Image must not be emitted; components = %v", keysOf(out))
	}
	images := prop(t, component(t, out, "Book"), "images")
	arms, ok := images["anyOf"].([]any)
	if !ok || len(arms) != 2 {
		t.Fatalf("images fragment = %v", images)
	}
	rows, _ := arms[0].(map[string]any)
	item, _ := rows["items"].(map[string]any)
	itemProps, _ := item["properties"].(map[string]any)
	if got, want := itemProps["image"], anyOfNull(ref("Image")); !reflect.DeepEqual(got, want) {
		t.Errorf("in-array subField fragment = %v, want %v", got, want)
	}
}

func TestEmitComponentsRejectsNodeWithoutWireSample(t *testing.T) {
	if _, err := EmitComponents(stubReflector{}, []include.Resource{&bareRes{name: "Bare"}}); err == nil {
		t.Fatal("expected an error for a node without WireSample()")
	}
}

func TestEmitComponentsRejectsDuplicateNodeName(t *testing.T) {
	g := buildToyGraph()
	clone := buildToyGraph()
	if _, err := EmitComponents(stubReflector{}, []include.Resource{g.Book, clone.Book}); err == nil {
		t.Fatal("expected an error for two distinct resources sharing a name")
	}
}

func TestEmitComponentsRejectsInArrayEdgeWithoutSubField(t *testing.T) {
	g := buildToyGraph()
	g.Book.edges = map[string]include.Edge{
		"images": {
			Target:     func() include.Resource { return g.Image },
			Many:       true,
			Includable: true,
			ArrayPath:  "gallery",
		},
	}
	_, err := EmitComponents(stubReflector{}, []include.Resource{g.Book})
	if err == nil {
		t.Fatal("EmitComponents: want an error for a blank SubField, got nil")
	}
	if want := `apidoc: node "Book" edge "images": in-array edge requires SubField`; err.Error() != want {
		t.Errorf("err = %q, want %q", err, want)
	}
}

// ---------------------------------------------------------------------------
// envelope: nextCursor is OPTIONAL
// ---------------------------------------------------------------------------

func TestEnvelopeHasNextCursorOptional(t *testing.T) {
	g := buildToyGraph()
	out, err := EmitComponents(stubReflector{}, []include.Resource{g.Book})
	if err != nil {
		t.Fatal(err)
	}
	env, _ := prop(t, component(t, out, "Book"), "reviews")["anyOf"].([]any)[0].(map[string]any)
	props, _ := env["properties"].(map[string]any)
	nc, ok := props["nextCursor"].(map[string]any)
	if !ok {
		t.Fatalf("envelope has no nextCursor: %v", env)
	}
	if nc["type"] != "string" {
		t.Errorf("nextCursor = %v, want a string schema", nc)
	}
	req := requiredKeys(t, env)
	if contains(req, "nextCursor") {
		t.Errorf("nextCursor must NOT be required (the engine omits the key when there is no token): %v", req)
	}
	if !contains(req, "items") || !contains(req, "hasMore") {
		t.Errorf("items and hasMore must stay required, got %v", req)
	}
}

// ---------------------------------------------------------------------------
// computed edges
// ---------------------------------------------------------------------------

type computedWire struct {
	ID string `json:"id"`
}

// A computed edge's Target is a NIL FUNC — every walk must branch on Computed
// before touching it, and its declared ComputedSchema is spliced verbatim.
func TestComputedEdgeStitched(t *testing.T) {
	n := &emitRes{name: "Widget", wire: computedWire{}, edges: map[string]include.Edge{
		"score": {
			Includable:     true,
			Computed:       true,
			ComputedSchema: RawFragment(map[string]any{"type": "integer", "description": "0..100"}),
		},
	}}
	name, s, err := EmitComponent(stubReflector{}, n)
	if err != nil {
		t.Fatalf("EmitComponent: %v", err)
	}
	if name != "Widget" {
		t.Fatalf("name = %q", name)
	}
	got := prop(t, s.Map(), "score")
	want := map[string]any{"type": "integer", "description": "0..100"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("computed edge fragment = %v, want %v", got, want)
	}
}

func TestComputedEdgeWithoutSchemaErrors(t *testing.T) {
	n := &emitRes{name: "Widget", wire: computedWire{}, edges: map[string]include.Edge{
		"score": {Includable: true, Computed: true},
	}}
	_, _, err := EmitComponent(stubReflector{}, n)
	if err == nil || !strings.Contains(err.Error(), "ComputedSchema") {
		t.Fatalf("err = %v, want a ComputedSchema complaint", err)
	}
}

// ---------------------------------------------------------------------------
// auxiliary inlining
// ---------------------------------------------------------------------------

type auxInner struct {
	Zip string `json:"zip"`
}

type auxWire struct {
	ID   string   `json:"id"`
	Home auxInner `json:"home"`
	Work auxInner `json:"work" doc:"where they work"`
}

// An auxiliary struct component is inlined into its referent; a SOLE-REF (a
// $ref carrying a sibling keyword) is not, and keeps the auxiliary alive as its
// own component.
func TestEmitComponentInlinesAux(t *testing.T) {
	n := &emitRes{name: "Person", wire: auxWire{}, edges: map[string]include.Edge{}}
	out, err := EmitComponents(stubReflector{}, []include.Resource{n})
	if err != nil {
		t.Fatalf("EmitComponents: %v", err)
	}
	person := component(t, out, "Person")

	home := prop(t, person, "home")
	if home["$ref"] != nil {
		t.Errorf("bare $ref to an auxiliary must be inlined, got %v", home)
	}
	if _, ok := home["properties"].(map[string]any)["zip"]; !ok {
		t.Errorf("inlined auxiliary lost its properties: %v", home)
	}

	work := prop(t, person, "work")
	if work["$ref"] != RefPrefix+"auxInner" {
		t.Errorf("sole-ref (a described $ref) must NOT be inlined, got %v", work)
	}
	if work["description"] != "where they work" {
		t.Errorf("sole-ref lost its sibling keyword: %v", work)
	}

	if _, ok := out["auxInner"]; !ok {
		t.Errorf("an auxiliary still referenced must survive as a component; emitted %v", keysOf(out))
	}
}

type soloInner struct {
	Zip string `json:"zip"`
}

type soloWire struct {
	ID   string    `json:"id"`
	Home soloInner `json:"home"`
}

// With every reference inlined, the auxiliary is dropped entirely.
func TestFullyInlinedAuxIsDropped(t *testing.T) {
	n := &emitRes{name: "Solo", wire: soloWire{}, edges: map[string]include.Edge{}}
	out, err := EmitComponents(stubReflector{}, []include.Resource{n})
	if err != nil {
		t.Fatalf("EmitComponents: %v", err)
	}
	if _, ok := out["soloInner"]; ok {
		t.Errorf("a fully-inlined auxiliary must not surface as a component; emitted %v", keysOf(out))
	}
	if len(out) != 1 {
		t.Errorf("expected only the node component, got %v", keysOf(out))
	}
}

type cycA struct {
	B *cycB `json:"b"`
}

type cycB struct {
	A *cycA `json:"a"`
}

type cycWire struct {
	ID string `json:"id"`
	A  *cycA  `json:"a"`
}

type selfAux struct {
	Next *selfAux `json:"next"`
	Zip  string   `json:"zip"`
}

type selfWire struct {
	ID   string   `json:"id"`
	Root *selfAux `json:"root"`
}

// A cyclic auxiliary DEGRADES: it cannot be inlined (substitution would grow
// forever), so it is emitted as its own component and referenced by $ref —
// exactly the survival path a sole-ref takes. A self-referential struct is an
// ordinary Go shape, not a document error.
func TestEmitAuxMutualCycleDegradesToComponents(t *testing.T) {
	n := &emitRes{name: "Cyc", wire: cycWire{}, edges: map[string]include.Edge{}}
	out, err := EmitComponents(stubReflector{}, []include.Resource{n})
	if err != nil {
		t.Fatalf("a cyclic auxiliary must degrade, not error: %v", err)
	}
	// The mutually-referential pair survives as two components...
	for _, want := range []string{"cycA", "cycB"} {
		if _, ok := out[want]; !ok {
			t.Fatalf("pinned auxiliary %q must be emitted; got %v", want, keysOf(out))
		}
	}
	// ...and every mention of them is a byte-correct $ref.
	if got := prop(t, component(t, out, "Cyc"), "a"); !reflect.DeepEqual(got, ref("cycA")) {
		t.Errorf("Cyc.a = %v, want %v", got, ref("cycA"))
	}
	if got := prop(t, component(t, out, "cycA"), "b"); !reflect.DeepEqual(got, ref("cycB")) {
		t.Errorf("cycA.b = %v, want %v", got, ref("cycB"))
	}
	if got := prop(t, component(t, out, "cycB"), "a"); !reflect.DeepEqual(got, ref("cycA")) {
		t.Errorf("cycB.a = %v, want %v", got, ref("cycA"))
	}
	assertComponentsVerify(t, out)
}

// A SELF-loop is the degenerate cycle and takes the same path. The non-cyclic
// part of a pinned auxiliary still gets its own acyclic refs inlined.
func TestEmitAuxSelfLoopDegradesToComponent(t *testing.T) {
	n := &emitRes{name: "Self", wire: selfWire{}, edges: map[string]include.Edge{}}
	out, err := EmitComponents(stubReflector{}, []include.Resource{n})
	if err != nil {
		t.Fatalf("a self-referential auxiliary must degrade, not error: %v", err)
	}
	if _, ok := out["selfAux"]; !ok {
		t.Fatalf("pinned auxiliary selfAux must be emitted; got %v", keysOf(out))
	}
	if got := prop(t, component(t, out, "Self"), "root"); !reflect.DeepEqual(got, ref("selfAux")) {
		t.Errorf("Self.root = %v, want %v", got, ref("selfAux"))
	}
	aux := component(t, out, "selfAux")
	if got := prop(t, aux, "next"); !reflect.DeepEqual(got, ref("selfAux")) {
		t.Errorf("selfAux.next = %v, want a self $ref, got %v", got, ref("selfAux"))
	}
	if !hasProp(aux, "zip") {
		t.Errorf("pinned auxiliary lost its scalars: %v", aux)
	}
	assertComponentsVerify(t, out)
}

type mixedLeaf struct {
	Zip string `json:"zip"`
}

type mixedFeeder struct {
	Leaf mixedLeaf `json:"leaf"`
	Ring *cycA     `json:"ring"`
}

type mixedWire struct {
	ID   string      `json:"id"`
	Feed mixedFeeder `json:"feed"`
}

// Pinning is per-auxiliary, not per-component-set: an ACYCLIC auxiliary that
// merely POINTS INTO a cycle is still inlined, and only the cyclic members stay
// $refs. This is what makes the post-pre-pass substitution set acyclic.
func TestEmitAuxAcyclicFeederIntoCycleStillInlines(t *testing.T) {
	n := &emitRes{name: "Mixed", wire: mixedWire{}, edges: map[string]include.Edge{}}
	out, err := EmitComponents(stubReflector{}, []include.Resource{n})
	if err != nil {
		t.Fatalf("EmitComponents: %v", err)
	}
	if _, ok := out["mixedFeeder"]; ok {
		t.Errorf("the acyclic feeder must be inlined, not emitted: %v", keysOf(out))
	}
	if _, ok := out["mixedLeaf"]; ok {
		t.Errorf("the acyclic leaf must be inlined, not emitted: %v", keysOf(out))
	}
	feed := prop(t, component(t, out, "Mixed"), "feed")
	leaf, _ := feed["properties"].(map[string]any)["leaf"].(map[string]any)
	if _, ok := leaf["properties"].(map[string]any)["zip"]; !ok {
		t.Errorf("nested acyclic auxiliary was not inlined: %v", feed)
	}
	if got := feed["properties"].(map[string]any)["ring"]; !reflect.DeepEqual(got, ref("cycA")) {
		t.Errorf("the ref into the cycle must stay a $ref, got %v", got)
	}
	assertComponentsVerify(t, out)
}

// assertComponentsVerify registers an emitted set and checks it has no dangling
// $ref — the contract between EmitComponents and Components.Verify.
func assertComponentsVerify(t *testing.T, out map[string]Schema) {
	t.Helper()
	c := NewComponents()
	for _, name := range sortedKeys(out) {
		c.Add(name, out[name])
	}
	if err := c.Verify(); err != nil {
		t.Fatalf("emitted set must verify clean: %v", err)
	}
}

type opqAux struct{}

type opqWire struct {
	ID   string `json:"id"`
	Blob opqAux `json:"blob" raw:"1"`
}

// An Opaque component is held BYTE-EXACT; inlining it would mean reshaping the
// one thing the IR promises not to touch. It always stays a $ref.
func TestOpaqueComponentNeverInlined(t *testing.T) {
	n := &emitRes{name: "Holder", wire: opqWire{}, edges: map[string]include.Edge{}}
	out, err := EmitComponents(stubReflector{}, []include.Resource{n})
	if err != nil {
		t.Fatalf("EmitComponents: %v", err)
	}
	blob := prop(t, component(t, out, "Holder"), "blob")
	if blob["$ref"] != RefPrefix+"opqAux" {
		t.Fatalf("an Opaque auxiliary must stay a $ref, got %v", blob)
	}
	aux, ok := out["opqAux"]
	if !ok {
		t.Fatalf("the Opaque auxiliary must survive as a component; emitted %v", keysOf(out))
	}
	b, err := aux.IR().MarshalJSON()
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(b), "patternProperties") {
		t.Fatalf("Opaque bytes were reshaped: %s", b)
	}
}

// ---------------------------------------------------------------------------
// edge-shape builders, directly
// ---------------------------------------------------------------------------

func TestEdgeShapeInlinedMirrorsRef(t *testing.T) {
	inner := newObject([]Prop{{Name: "x", Schema: newScalar("string"), Required: true}})
	cases := []struct {
		name string
		edge include.Edge
	}{
		{"to-one", include.Edge{Target: func() include.Resource { return nil }}},
		{"bare", include.Edge{Many: true, Backref: "p", Bare: true}},
		{"enveloped", include.Edge{Many: true, Backref: "p"}},
		{"in-array", include.Edge{Many: true, ArrayPath: "g", SubField: "s"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			withRef := edgeShape(tc.edge, newRef("T"))
			withInner := edgeShape(tc.edge, inner)
			refJSON, _ := withRef.MarshalJSON()
			innerJSON, _ := withInner.MarshalJSON()
			// Same shape, different leaf: swapping the $ref text for the inlined
			// object must recover the other rendering exactly.
			innerBody, _ := inner.MarshalJSON()
			swapped := strings.ReplaceAll(string(refJSON), `{"$ref":"`+RefPrefix+`T"}`, string(innerBody))
			if swapped != string(innerJSON) {
				t.Fatalf("inlined shape drifted:\n ref    = %s\n inline = %s\n want   = %s", refJSON, innerJSON, swapped)
			}
		})
	}
}

// The shape table has no branch that fits a computed edge; reaching it means a
// caller skipped the Computed check and would silently get a to-many envelope.
func TestEdgeShapePanicsOnComputedEdge(t *testing.T) {
	assertPanics(t, func() {
		edgeShape(include.Edge{Computed: true, ComputedSchema: RefTo("X")}, newRef("T"))
	})
}

// ---------------------------------------------------------------------------
// serialization sanity: emitted components survive a byte round trip
// ---------------------------------------------------------------------------

func TestEmittedComponentIsValidJSON(t *testing.T) {
	g := buildToyGraph()
	out, err := EmitComponents(stubReflector{}, []include.Resource{g.Book})
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range keysOf(out) {
		b, err := out[name].IR().MarshalJSON()
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if !json.Valid(b) {
			t.Fatalf("%s: invalid JSON: %s", name, b)
		}
	}
}

// ---------------------------------------------------------------------------
// required to-one edges (spec §1: graph.Required() → bare $ref, in `required`)
// ---------------------------------------------------------------------------

// requireAuthor marks the toy graph's Book.author to-one edge as Required.
func requireAuthor(g *toyGraph) {
	e := g.Book.edges["author"]
	e.Required = true
	g.Book.edges["author"] = e
}

func TestEmitComponentsRequiredToOneIsBareRefAndRequired(t *testing.T) {
	g := buildToyGraph()
	requireAuthor(g)

	out, err := EmitComponents(stubReflector{}, []include.Resource{g.Book})
	if err != nil {
		t.Fatalf("EmitComponents: %v", err)
	}
	book := component(t, out, "Book")

	// The VALUE loses the null arm …
	if got, want := prop(t, book, "author"), ref("Author"); !reflect.DeepEqual(got, want) {
		t.Errorf("required to-one fragment = %v, want a bare %v", got, want)
	}
	// … and the KEY is listed in required.
	req := requiredKeys(t, book)
	if !contains(req, "author") {
		t.Errorf("required to-one edge missing from required = %v", req)
	}
	// Every other edge kind is untouched: still optional, still null-able.
	for _, key := range []string{"reviews", "similar", "images"} {
		if contains(req, key) {
			t.Errorf("edge %q must not be required, got required=%v", key, req)
		}
	}
	if !contains(req, "id") {
		t.Errorf("scalar id should stay required, got %v", req)
	}

	// The set still assembles and verifies. Image is DocExternal — its fragment
	// is owned elsewhere — so the $ref to it is whitelisted, as any assembler of
	// this graph must.
	c := NewComponents()
	c.ExternalRefs("Image")
	for _, name := range keysOf(out) {
		c.Add(name, out[name])
	}
	if err := c.Verify(); err != nil {
		t.Fatalf("Verify: %v", err)
	}
}

// Regression: a to-one edge WITHOUT Required keeps anyOf[$ref,null] and stays
// optional — the flag is the only thing that changes the shape.
func TestEmitComponentsNonRequiredToOneUnchanged(t *testing.T) {
	g := buildToyGraph()
	out, err := EmitComponents(stubReflector{}, []include.Resource{g.Book})
	if err != nil {
		t.Fatalf("EmitComponents: %v", err)
	}
	book := component(t, out, "Book")
	if got, want := prop(t, book, "author"), anyOfNull(ref("Author")); !reflect.DeepEqual(got, want) {
		t.Errorf("to-one fragment = %v, want %v", got, want)
	}
	if req := requiredKeys(t, book); contains(req, "author") {
		t.Errorf("a non-required to-one edge must stay optional, required = %v", req)
	}
}

// SchemaFor shares the shape table, so a REQUESTED required to-one leaf is the
// same bare $ref. (Requiredness is not observably different there: SchemaFor
// promotes every requested key already.)
func TestSchemaForRequiredToOneLeafIsBareRef(t *testing.T) {
	g := buildToyGraph()
	requireAuthor(g)

	s, err := SchemaFor(stubReflector{}, g.Book, include.IncludeTree{"author": nil}, include.Limits{})
	if err != nil {
		t.Fatalf("SchemaFor: %v", err)
	}
	m := s.Map()
	if got, want := prop(t, m, "author"), ref("Author"); !reflect.DeepEqual(got, want) {
		t.Errorf("SchemaFor required to-one = %v, want a bare %v", got, want)
	}
	if req := requiredKeys(t, m); !contains(req, "author") {
		t.Errorf("requested edge missing from required = %v", req)
	}
}
