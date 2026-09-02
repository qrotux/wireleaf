package huma

import (
	"context"
	"encoding/json"
	"net/http"
	"reflect"
	"strings"
	"testing"

	humav2 "github.com/danielgtaylor/huma/v2"
	"github.com/danielgtaylor/huma/v2/humatest"

	"github.com/qrotux/wireleaf/apidoc"
	"github.com/qrotux/wireleaf/reflector"
)

// E2EBody / E2EAux are exported so the reflector's component name (the Go type
// name) is the doc-facing one.
type E2EBody struct {
	Name string `json:"name"`
	Aux  E2EAux `json:"aux"`
	// A pointer WITHOUT omitempty is the nullable-reference case: the canonical
	// IR is anyOf[$ref, {"type":"null"}].
	Maybe *E2EAux `json:"maybe"`
}

type E2EAux struct {
	Label string `json:"label"`
}

// E2EEmpty exercises the empty-object path: huma's SchemaLinkTransformer writes
// "$schema" into every served object's property map.
type E2EEmpty struct{}

type e2eInput struct {
	Body E2EBody
}

type e2eOutput struct {
	Body E2EBody
}

type e2eEmptyInput struct {
	Body E2EEmpty
}

type e2eEmptyOutput struct {
	Body E2EEmpty
}

// newE2EAPI wires a REAL huma API on humav2.DefaultConfig — transformers
// included — whose registry is the bridge. DefaultConfig is what an application
// uses, and it is where a nil property map or an unenforced null arm shows up.
func newE2EAPI(t *testing.T) (humatest.TestAPI, *apidoc.Components, *Registry, humav2.Config) {
	t.Helper()
	c := apidoc.NewComponents()
	bridge := NewRegistry(c, &reflector.Reflector{}).(*Registry)
	cfg := humav2.DefaultConfig("test", "1.0.0")
	cfg.Components = &humav2.Components{Schemas: bridge}
	_, api := humatest.New(t, cfg)
	return api, c, bridge, cfg
}

// The bridge stands in for huma's own registry end to end: huma reflects the
// operation's input through it (so the body components enter the SHARED set),
// serves them in the document, and validates requests against the converted
// schemas.
//
// The full config/operation layer is Task 19; this stays at "the bridge is a
// working humav2.Registry under DefaultConfig".
func TestBridgeDrivesHumaEndToEnd(t *testing.T) {
	api, c, bridge, cfg := newE2EAPI(t)

	humav2.Register(api, humav2.Operation{
		OperationID: "put-thing",
		Method:      http.MethodPut,
		Path:        "/thing",
	}, func(ctx context.Context, in *e2eInput) (*e2eOutput, error) {
		return &e2eOutput{Body: in.Body}, nil
	})

	// Registering the operation put the body components into the SHARED set.
	if _, ok := c.TypeName(reflect.TypeFor[E2EBody]()); !ok {
		t.Fatalf("input body type did not enter the shared component index")
	}
	if err := c.Verify(); err != nil {
		t.Fatalf("shared set does not verify after huma registration: %v", err)
	}
	if _, ok := bridge.Map()["E2EAux"]; !ok {
		t.Fatalf("nested component missing: %v", keysOf(bridge.Map()))
	}

	// The served document carries them.
	doc, err := cfg.YAML()
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(doc), "E2EAux") {
		t.Fatalf("document does not mention the nested component:\n%s", doc)
	}

	// A valid request round-trips.
	resp := api.Put("/thing", map[string]any{
		"name": "n", "aux": map[string]any{"label": "l"}, "maybe": nil,
	})
	if resp.Code != http.StatusOK {
		t.Fatalf("valid request = %d: %s", resp.Code, resp.Body.String())
	}
	var out map[string]any
	if err := json.Unmarshal(resp.Body.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	if out["name"] != "n" {
		t.Fatalf("body lost: %v", out)
	}

	// An invalid request is REJECTED by the converted schema: huma validates
	// against wireleaf's components, precomputed messages and all.
	bad := api.Put("/thing", map[string]any{"aux": map[string]any{"label": "l"}, "maybe": nil})
	if bad.Code != http.StatusUnprocessableEntity {
		t.Fatalf("missing required property = %d: %s", bad.Code, bad.Body.String())
	}
	if !strings.Contains(bad.Body.String(), "name") {
		t.Fatalf("validation error does not name the missing property: %s", bad.Body.String())
	}
}

// The nullable-reference idiom anyOf[$ref, {"type":"null"}] must ENFORCE both
// arms. huma's validator has no "null" type case, so without the enum-of-null
// the null arm would match ANY value and every nullable reference in the
// document would be unvalidated.
func TestBridgeNullableRefIsEnforced(t *testing.T) {
	api, _, _, _ := newE2EAPI(t)
	humav2.Register(api, humav2.Operation{
		OperationID: "put-nullable",
		Method:      http.MethodPut,
		Path:        "/nullable",
	}, func(ctx context.Context, in *e2eInput) (*e2eOutput, error) {
		return &e2eOutput{Body: in.Body}, nil
	})

	base := func(maybe any) map[string]any {
		return map[string]any{"name": "n", "aux": map[string]any{"label": "l"}, "maybe": maybe}
	}

	// null: accepted by the null arm.
	if r := api.Put("/nullable", base(nil)); r.Code != http.StatusOK {
		t.Fatalf("null = %d: %s", r.Code, r.Body.String())
	}
	// a valid object: accepted by the $ref arm.
	if r := api.Put("/nullable", base(map[string]any{"label": "x"})); r.Code != http.StatusOK {
		t.Fatalf("valid object = %d: %s", r.Code, r.Body.String())
	}
	// an object missing its required property: matches NEITHER arm. This is the
	// regression — it used to slip through the null arm with a 200.
	r := api.Put("/nullable", base(map[string]any{}))
	if r.Code != http.StatusUnprocessableEntity {
		t.Fatalf("invalid object under a nullable ref = %d (must be 422): %s", r.Code, r.Body.String())
	}
	// so does a wrong-typed value.
	if r := api.Put("/nullable", base("not-an-object")); r.Code != http.StatusUnprocessableEntity {
		t.Fatalf("string under a nullable ref = %d (must be 422): %s", r.Code, r.Body.String())
	}
}

// An empty struct body under DefaultConfig: the SchemaLinkTransformer writes
// "$schema" into the served object's property map, which panics on a nil map.
func TestBridgeEmptyObjectBodyUnderDefaultConfig(t *testing.T) {
	api, _, bridge, _ := newE2EAPI(t)
	humav2.Register(api, humav2.Operation{
		OperationID: "put-empty",
		Method:      http.MethodPut,
		Path:        "/empty",
	}, func(ctx context.Context, in *e2eEmptyInput) (*e2eEmptyOutput, error) {
		return &e2eEmptyOutput{}, nil
	})
	if s := bridge.SchemaFromRef(apidoc.RefPrefix + "E2EEmpty"); s == nil || s.Properties == nil {
		t.Fatalf("empty object must keep a non-nil property map: %#v", s)
	}
	// The response path is where the transformer runs.
	if r := api.Put("/empty", map[string]any{}); r.Code != http.StatusOK {
		t.Fatalf("empty body = %d: %s", r.Code, r.Body.String())
	}
}

// ---------------------------------------------------------------------------
// response-position serving: huma's SchemaLinkTransformer needs a Go type back
// ---------------------------------------------------------------------------

// NodeWire is the wire type behind a Node[W] response body.
type NodeWire struct {
	V string `json:"v"`
}

type e2eNodeOutput struct {
	Body Node[NodeWire]
}

type e2eTypedAppOutput struct {
	Body AppTyped
}

// AppTyped stands for a component the application assembled itself AND bound to
// its wire type.
type AppTyped struct {
	ID string `json:"id"`
}

// THE round-3 regression: an application-registered component used as a RESPONSE
// body through the Node[W] idiom. Serving it typed makes huma's
// SchemaLinkTransformer accept it ("type":"object") and then dereference
// TypeFromRef — outside its own recover() — so a component with no Go type
// panicked huma.Register.
func TestNodeResponseBodyWithAppRegisteredComponent(t *testing.T) {
	c := apidoc.NewComponents()
	// The application owns this component: hand-assembled, no wire type behind
	// it. RegisterNode binds the WRAPPER, which is not a component type.
	c.Add("NodeWireComponentE2E", apidoc.RawFragment(map[string]any{
		"type":       "object",
		"properties": map[string]any{"v": map[string]any{"type": "string"}},
	}))
	RegisterNodeWireE2E(c)

	bridge := NewRegistry(c, &reflector.Reflector{})
	cfg := humav2.DefaultConfig("test", "1.0.0")
	cfg.Components = &humav2.Components{Schemas: bridge}
	_, api := humatest.New(t, cfg)

	// This is the call that used to panic.
	humav2.Register(api, humav2.Operation{
		OperationID: "get-node",
		Method:      http.MethodGet,
		Path:        "/node",
	}, func(ctx context.Context, in *struct{}) (*e2eNodeOutput, error) {
		return &e2eNodeOutput{Body: NodeOf[NodeWire](json.RawMessage(`{"v":"hydrated"}`))}, nil
	})

	resp := api.Get("/node")
	if resp.Code != http.StatusOK {
		t.Fatalf("node response = %d: %s", resp.Code, resp.Body.String())
	}
	if !strings.Contains(resp.Body.String(), `"hydrated"`) {
		t.Fatalf("engine bytes lost: %s", resp.Body.String())
	}
	doc, err := cfg.YAML()
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(doc), "NodeWireComponentE2E") {
		t.Fatalf("document does not carry the component $ref:\n%s", doc)
	}
}

// RegisterNodeWireE2E keeps the generic call out of the table above.
func RegisterNodeWireE2E(c *apidoc.Components) {
	c.RegisterWrapperType(reflect.TypeFor[Node[NodeWire]](), "NodeWireComponentE2E")
	c.RegisterWrapperType(reflect.TypeFor[apidoc.Node[NodeWire]](), "NodeWireComponentE2E")
}

// An application component registered WITH its wire type is served typed: the
// transformer runs to completion and TypeFromRef answers.
func TestAppComponentWithTypeServedTyped(t *testing.T) {
	c := apidoc.NewComponents()
	c.Add("AppTyped", apidoc.RawFragment(map[string]any{
		"type":       "object",
		"properties": map[string]any{"id": map[string]any{"type": "string"}},
		"required":   []any{"id"},
	}))
	c.RegisterType(reflect.TypeFor[AppTyped](), "AppTyped")

	bridge := NewRegistry(c, &reflector.Reflector{}).(*Registry)
	cfg := humav2.DefaultConfig("test", "1.0.0")
	cfg.Components = &humav2.Components{Schemas: bridge}
	_, api := humatest.New(t, cfg)

	humav2.Register(api, humav2.Operation{
		OperationID: "get-typed",
		Method:      http.MethodGet,
		Path:        "/typed",
	}, func(ctx context.Context, in *struct{}) (*e2eTypedAppOutput, error) {
		return &e2eTypedAppOutput{Body: AppTyped{ID: "x"}}, nil
	})

	s := bridge.SchemaFromRef(apidoc.RefPrefix + "AppTyped")
	if s.Type != "object" || s.Properties["id"] == nil {
		t.Fatalf("bound component must be served typed: %#v", s)
	}
	if got := bridge.TypeFromRef(apidoc.RefPrefix + "AppTyped"); got != reflect.TypeFor[AppTyped]() {
		t.Fatalf("TypeFromRef = %v", got)
	}
	// The transformer ran: it writes "$schema" into the served object.
	if s.Properties["$schema"] == nil {
		t.Fatalf("SchemaLinkTransformer did not run over the typed component")
	}
	if r := api.Get("/typed"); r.Code != http.StatusOK {
		t.Fatalf("typed response = %d: %s", r.Code, r.Body.String())
	}
}

// A component with NO type binding is served opaque — Type == "" makes huma's
// transformer bail one line before the dereference.
func TestAppComponentWithoutTypeServedOpaque(t *testing.T) {
	c := apidoc.NewComponents()
	c.Add("Typeless", apidoc.RawFragment(map[string]any{
		"type":       "object",
		"properties": map[string]any{"id": map[string]any{"type": "string"}},
	}))
	bridge := NewRegistry(c, &reflector.Reflector{}).(*Registry)

	s := bridge.SchemaFromRef(apidoc.RefPrefix + "Typeless")
	if s.Type != "" {
		t.Fatalf("a typeless component must not be served as a typed object: %#v", s)
	}
	if bridge.TypeFromRef(apidoc.RefPrefix+"Typeless") != nil {
		t.Fatalf("a typeless component has no type")
	}
	// The document is unchanged by the degradation.
	raw, err := json.Marshal(s)
	if err != nil {
		t.Fatal(err)
	}
	if string(raw) != `{"properties":{"id":{"type":"string"}},"type":"object"}` {
		t.Fatalf("document drifted: %s", raw)
	}
}

// TypeFromRef across all three provenances.
func TestTypeFromRefProvenances(t *testing.T) {
	c := apidoc.NewComponents()
	bridge := NewRegistry(c, &reflector.Reflector{}).(*Registry)

	// reflected by the bridge — top AND auxiliary.
	bridge.Schema(reflect.TypeFor[E2EBody](), true, "body")
	if got := bridge.TypeFromRef(apidoc.RefPrefix + "E2EBody"); got != reflect.TypeFor[E2EBody]() {
		t.Fatalf("reflected top = %v", got)
	}
	if got := bridge.TypeFromRef(apidoc.RefPrefix + "E2EAux"); got != reflect.TypeFor[E2EAux]() {
		t.Fatalf("reflected auxiliary = %v", got)
	}
	// application-registered WITH a type binding — found through Components.TypeOf.
	c.Add("AppTyped", apidoc.RawFragment(map[string]any{"type": "object"}))
	c.RegisterType(reflect.TypeFor[AppTyped](), "AppTyped")
	if got := bridge.TypeFromRef(apidoc.RefPrefix + "AppTyped"); got != reflect.TypeFor[AppTyped]() {
		t.Fatalf("app-bound = %v", got)
	}
	// unbound.
	c.Add("Unbound", apidoc.RawFragment(map[string]any{"type": "object"}))
	if got := bridge.TypeFromRef(apidoc.RefPrefix + "Unbound"); got != nil {
		t.Fatalf("unbound = %v", got)
	}
	if got := bridge.TypeFromRef(apidoc.RefPrefix + "NeverHeardOf"); got != nil {
		t.Fatalf("unknown = %v", got)
	}
}
