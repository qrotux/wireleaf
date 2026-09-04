package huma

import (
	"encoding/json"
	"reflect"
	"testing"
	"time"

	humav2 "github.com/danielgtaylor/huma/v2"

	"github.com/qrotux/wireleaf/apidoc"
	"github.com/qrotux/wireleaf/reflector"
)

type BridgeAux struct {
	Label string `json:"label"`
}

type BridgeBody struct {
	ID    string      `json:"id"`
	Aux   BridgeAux   `json:"aux"`
	Extra *BridgeAux  `json:"extra,omitempty"`
	Tags  []string    `json:"tags,omitempty"`
	When  time.Time   `json:"when"`
	List  []BridgeAux `json:"list,omitempty"`
}

type BridgeOther struct {
	N int `json:"n"`
}

// AppOwned is the wire type behind a hand-assembled component.
type AppOwned struct {
	ID string `json:"id"`
}

// newTestBridge wires a bridge over a fresh Components and the REAL reflector:
// reflector integration is the point of the adapter suite.
func newTestBridge() (*Registry, *apidoc.Components) {
	c := apidoc.NewComponents()
	return NewRegistry(c, &reflector.Reflector{}).(*Registry), c
}

// A type huma has never seen is reflected on demand, and EVERY component the
// reflector returned — the top and its auxiliaries — lands in the shared set.
func TestBridgeReflectsOnDemand(t *testing.T) {
	b, c := newTestBridge()
	s := b.Schema(reflect.TypeFor[BridgeBody](), true, "body")
	if s.Ref != apidoc.RefPrefix+"BridgeBody" {
		t.Fatalf("ref = %q", s.Ref)
	}
	m := b.Map()
	for _, want := range []string{"BridgeBody", "BridgeAux"} {
		if _, ok := m[want]; !ok {
			t.Fatalf("component %q missing from %v", want, keysOf(m))
		}
	}
	if err := c.Verify(); err != nil {
		t.Fatalf("shared set does not verify: %v", err)
	}
	// The type index is filled, so a later BuildInto/RegisterNode sees it.
	if name, ok := c.TypeName(reflect.TypeFor[BridgeBody]()); !ok || name != "BridgeBody" {
		t.Fatalf("type index = %q, %v", name, ok)
	}
}

// The same type is asked for many times over a document's assembly: an
// identical re-registration is a no-op, not a duplicate-name panic.
func TestBridgeIdempotentReRequest(t *testing.T) {
	b, c := newTestBridge()
	first := b.Schema(reflect.TypeFor[BridgeBody](), false, "body")
	second := b.Schema(reflect.TypeFor[BridgeBody](), false, "body")
	if !reflect.DeepEqual(first, second) {
		t.Fatalf("second call differs")
	}
	if n := len(b.Map()); n != len(b.Map()) || n == 0 {
		t.Fatalf("unstable component set")
	}
	if err := c.Verify(); err != nil {
		t.Fatal(err)
	}
}

// A name the APPLICATION registered first is not automatically a conflict: what
// decides is the IR. An identical schema is an idempotent re-request (the
// application and the reflector simply agree about the type); a different one is
// two writers for one fact and must stop assembly.
func TestBridgeSharedNameIdempotentVsConflicting(t *testing.T) {
	// Identical: seed the component with the very IR the reflector produces.
	{
		b, c := newTestBridge()
		comps, err := (&reflector.Reflector{}).ReflectComponents(
			[]reflect.Type{reflect.TypeFor[BridgeOther]()}, nil)
		if err != nil {
			t.Fatal(err)
		}
		if err := c.RegisterReflected("BridgeOther", comps["BridgeOther"], nil); err != nil {
			t.Fatal(err)
		}
		s := b.Schema(reflect.TypeFor[BridgeOther](), true, "other")
		if s.Ref != apidoc.RefPrefix+"BridgeOther" {
			t.Fatalf("ref = %q", s.Ref)
		}
		if n := len(b.Map()); n != 1 {
			t.Fatalf("an identical re-registration must not add a component: %v", keysOf(b.Map()))
		}
		if err := c.Verify(); err != nil {
			t.Fatal(err)
		}
	}
	// Different: another writer already owns the name.
	{
		b, c := newTestBridge()
		c.Add("BridgeOther", apidoc.RawFragment(map[string]any{"type": "string"}))
		assertPanics(t, func() { b.Schema(reflect.TypeFor[BridgeOther](), true, "other") })
	}
}

// allowRef picks between a reference and the full converted schema.
func TestBridgeAllowRef(t *testing.T) {
	b, _ := newTestBridge()
	ref := b.Schema(reflect.TypeFor[BridgeAux](), true, "aux")
	if ref.Ref == "" || ref.Type != "" {
		t.Fatalf("allowRef=true must yield a bare $ref, got %#v", ref)
	}
	full := b.Schema(reflect.TypeFor[BridgeAux](), false, "aux")
	if full.Ref != "" || full.Type != "object" || full.Properties["label"] == nil {
		t.Fatalf("allowRef=false must yield the full schema, got %#v", full)
	}
}

// SchemaFromRef / TypeFromRef / Map round-trip.
func TestBridgeRefRoundTrip(t *testing.T) {
	b, _ := newTestBridge()
	ref := b.Schema(reflect.TypeFor[BridgeBody](), true, "body")
	s := b.SchemaFromRef(ref.Ref)
	if s == nil || s.Type != "object" {
		t.Fatalf("SchemaFromRef = %#v", s)
	}
	if got := b.TypeFromRef(ref.Ref); got != reflect.TypeFor[BridgeBody]() {
		t.Fatalf("TypeFromRef = %v", got)
	}
	if b.SchemaFromRef("#/nope/Thing") != nil || b.SchemaFromRef(apidoc.RefPrefix+"Nope") != nil {
		t.Fatalf("unknown refs must resolve to nil")
	}
	// An auxiliary carries its owning type too: huma needs one back for any
	// component it serves as a response body, and a nested struct here is a
	// body of its own elsewhere.
	if got := b.TypeFromRef(apidoc.RefPrefix + "BridgeAux"); got != reflect.TypeFor[BridgeAux]() {
		t.Fatalf("auxiliary type = %v", got)
	}
	if _, ok := b.Map()["BridgeBody"]; !ok {
		t.Fatalf("Map missing the top component")
	}
}

// Non-struct types never become components: huma generates them inline, and the
// bridge stays out of the reflector's way (it only reflects structs).
func TestBridgeInlinesNonComponentTypes(t *testing.T) {
	b, c := newTestBridge()
	if s := b.Schema(reflect.TypeFor[string](), true, "s"); s.Type != "string" {
		t.Fatalf("string = %#v", s)
	}
	if s := b.Schema(reflect.TypeFor[time.Time](), true, "t"); s.Type != "string" || s.Format != "date-time" {
		t.Fatalf("time.Time = %#v", s)
	}
	if s := b.Schema(reflect.TypeFor[[]BridgeAux](), true, "list"); s.Type != "array" || s.Items.Ref != apidoc.RefPrefix+"BridgeAux" {
		t.Fatalf("[]BridgeAux = %#v", s)
	}
	// The element type still landed in the shared set.
	if _, ok := b.Map()["BridgeAux"]; !ok {
		t.Fatalf("element component not registered")
	}
	if err := c.Verify(); err != nil {
		t.Fatal(err)
	}
}

// A pointer decays to its element, and an alias redirects.
func TestBridgePointerAndAlias(t *testing.T) {
	b, _ := newTestBridge()
	if s := b.Schema(reflect.TypeFor[*BridgeAux](), true, "aux"); s.Ref != apidoc.RefPrefix+"BridgeAux" {
		t.Fatalf("pointer = %#v", s)
	}
	b.RegisterTypeAlias(reflect.TypeFor[BridgeOther](), reflect.TypeFor[BridgeAux]())
	if s := b.Schema(reflect.TypeFor[BridgeOther](), true, "other"); s.Ref != apidoc.RefPrefix+"BridgeAux" {
		t.Fatalf("alias = %#v", s)
	}
}

// MarshalJSON is how huma serves components/schemas: it must emit every
// component in the shared set, the bridge's and the application's alike, with
// the same key set a plain huma registry would have produced for its own.
func TestBridgeMarshalJSONEmitsEveryComponent(t *testing.T) {
	b, c := newTestBridge()
	b.Schema(reflect.TypeFor[BridgeBody](), true, "body")
	c.Add("AppOwned", apidoc.RawFragment(map[string]any{"type": "object"}))

	raw, err := json.Marshal(b)
	if err != nil {
		t.Fatal(err)
	}
	var got map[string]json.RawMessage
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"BridgeBody", "BridgeAux", "AppOwned"} {
		if _, ok := got[want]; !ok {
			t.Fatalf("component %q missing from the marshalled registry: %s", want, raw)
		}
	}
	if len(got) != len(b.Map()) {
		t.Fatalf("MarshalJSON and Map disagree: %d vs %d", len(got), len(b.Map()))
	}
	// The wireleaf side of the doc is shaped like huma's own: the reference form
	// a mapRegistry produces for the same nested type is what the bridge emits.
	var body struct {
		Properties map[string]struct {
			Ref string `json:"$ref"`
		} `json:"properties"`
	}
	if err := json.Unmarshal(got["BridgeBody"], &body); err != nil {
		t.Fatal(err)
	}
	if body.Properties["aux"].Ref != apidoc.RefPrefix+"BridgeAux" {
		t.Fatalf("nested component is not referenced: %s", got["BridgeBody"])
	}
}

// An application-registered component reaches huma as a TYPED schema — the
// shared set holds IR whoever wrote it, so huma can validate against it exactly
// as against a reflected one — PROVIDED its wire type is bound, which is what
// lets huma serve it in response position (see schemaFor). Only a genuinely
// opaque component rides the Extensions bridge.
func TestBridgeServesApplicationComponentsTyped(t *testing.T) {
	b, c := newTestBridge()
	c.Add("AppOwned", apidoc.RawFragment(map[string]any{
		"type":       "object",
		"properties": map[string]any{"id": map[string]any{"type": "string"}},
		"required":   []any{"id"},
	}))
	c.RegisterType(reflect.TypeFor[AppOwned](), "AppOwned")
	s := b.SchemaFromRef(apidoc.RefPrefix + "AppOwned")
	if s == nil {
		t.Fatalf("application component not visible to huma")
	}
	if s.Type != "object" || s.Properties["id"] == nil || len(s.Extensions) != 0 {
		t.Fatalf("application component is not typed: %#v", s)
	}
	// Typed means ENFORCED: the required property is validated, which the
	// Extensions bridge cannot do.
	res := &humav2.ValidateResult{}
	humav2.Validate(b, s, humav2.NewPathBuffer([]byte(""), 0), humav2.ModeWriteToServer, map[string]any{}, res)
	if len(res.Errors) == 0 {
		t.Fatalf("an application component must validate like any other")
	}

	// A genuinely opaque component still comes through byte for byte.
	opaque, err := apidoc.OpaqueFragment(json.RawMessage(`{"if":{"type":"string"},"then":{"maxLength":3}}`))
	if err != nil {
		t.Fatal(err)
	}
	c.Add("AppOpaque", opaque)
	raw, err := json.Marshal(b.SchemaFromRef(apidoc.RefPrefix + "AppOpaque"))
	if err != nil {
		t.Fatal(err)
	}
	if string(raw) != `{"if":{"type":"string"},"then":{"maxLength":3}}` {
		t.Fatalf("opaque component lost: %s", raw)
	}
}

func TestNewRegistryRejectsNilArguments(t *testing.T) {
	assertPanics(t, func() { NewRegistry(nil, &reflector.Reflector{}) })
	assertPanics(t, func() { NewRegistry(apidoc.NewComponents(), nil) })
}

func keysOf(m map[string]*humav2.Schema) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}

// BridgeEmbed is bound in the shared set under a name that is NOT its Go type
// name — the normal case for a graph node (Book vs BookWire) — and BridgeHost
// nests it.
type BridgeEmbed struct {
	URL string `json:"url"`
}

type BridgeHost struct {
	ID    string      `json:"id"`
	Embed BridgeEmbed `json:"embed"`
}

// A nested type the set already binds under its own component name is
// referenced under THAT name: the reflector must not rename it after its Go
// type, and the set's component stays the authority (it is not re-registered).
func TestBridgeNestedTypeKeepsExistingBinding(t *testing.T) {
	b, c := newTestBridge()
	c.Add("BridgeEmbedNode", apidoc.RawFragment(map[string]any{"type": "object", "description": "graph-owned"}))
	c.RegisterType(reflect.TypeFor[BridgeEmbed](), "BridgeEmbedNode")

	s := b.Schema(reflect.TypeFor[BridgeHost](), true, "body")
	if s.Ref != apidoc.RefPrefix+"BridgeHost" {
		t.Fatalf("ref = %q", s.Ref)
	}
	m := b.Map()
	if got := keysOf(m); len(got) != 2 {
		t.Fatalf("components = %v, want [BridgeEmbedNode BridgeHost]", got)
	}
	if got := m["BridgeHost"].Properties["embed"].Ref; got != apidoc.RefPrefix+"BridgeEmbedNode" {
		t.Fatalf("embed ref = %q, want the bound name", got)
	}
	if got := m["BridgeEmbedNode"].Description; got != "graph-owned" {
		t.Fatalf("the set's component was replaced: %+v", m["BridgeEmbedNode"])
	}
	if err := c.Verify(); err != nil {
		t.Fatal(err)
	}
}
