package huma

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"reflect"
	"strings"
	"testing"

	humav2 "github.com/danielgtaylor/huma/v2"
	"github.com/danielgtaylor/huma/v2/humatest"

	"github.com/qrotux/wireleaf/apidoc"
)

func TestNewConfigShape(t *testing.T) {
	c := apidoc.NewComponents()
	cfg := NewConfig("Books", "2.0.0", WithRegistry(c))

	if cfg.OpenAPI == nil || cfg.OpenAPI.OpenAPI != "3.1.0" {
		t.Fatalf("OpenAPI version = %#v", cfg.OpenAPI)
	}
	if cfg.Info == nil || cfg.Info.Title != "Books" || cfg.Info.Version != "2.0.0" {
		t.Errorf("Info = %#v", cfg.Info)
	}
	if cfg.OpenAPIPath != "/openapi" || cfg.DocsPath != "/docs" {
		t.Errorf("paths = %q / %q", cfg.OpenAPIPath, cfg.DocsPath)
	}
	// The $schema link transformer and the /schemas route it points at are
	// deliberately absent.
	if cfg.SchemasPath != "" {
		t.Errorf("SchemasPath = %q, want empty (no link transformer)", cfg.SchemasPath)
	}
	if len(cfg.CreateHooks) != 0 {
		t.Errorf("CreateHooks = %d, want none (that is where DefaultConfig installs the link transformer)", len(cfg.CreateHooks))
	}
	if len(cfg.Transformers) != 0 {
		t.Errorf("Transformers = %d, want none", len(cfg.Transformers))
	}
	if cfg.DefaultFormat != "application/json" {
		t.Errorf("DefaultFormat = %q", cfg.DefaultFormat)
	}
	for _, k := range []string{"application/json", "json"} {
		if _, ok := cfg.Formats[k]; !ok {
			t.Errorf("format %q missing: %v", k, cfg.Formats)
		}
	}
	if len(cfg.Formats) != 2 {
		t.Errorf("Formats = %v, want JSON only", cfg.Formats)
	}

	// The registry IS the bridge over the components we passed.
	b := BridgeOf(cfg.OpenAPI)
	if b.Components() != c {
		t.Errorf("bridge is not over the supplied component set")
	}
	// Library components landed there.
	if _, ok := c.Get(CursorPaginationComponent); !ok {
		t.Errorf("library components not installed by WithRegistry/NewConfig")
	}
}

func TestNewConfigWithoutRegistryUsesItsOwnComponents(t *testing.T) {
	cfg := NewConfig("Books", "1.0.0")
	b := BridgeOf(cfg.OpenAPI)
	if b.Components() == nil {
		t.Fatalf("default config has no component set")
	}
	if _, ok := b.Components().Get(ErrorComponent); !ok {
		t.Errorf("default config did not install the library components")
	}
}

func TestBridgeOfRejectsAForeignRegistry(t *testing.T) {
	cfg := humav2.DefaultConfig("x", "1")
	assertPanics(t, func() { BridgeOf(cfg.OpenAPI) })
}

// ---------------------------------------------------------------------------
// end to end
// ---------------------------------------------------------------------------

type bookOut struct {
	Body BookEnvelope
}

type bookListOut struct {
	Body BookCursorEnvelope
}

// newBookAPI wires the shipped config over a component set that already owns
// the graph-derived Book component.
func newBookAPI(t *testing.T) (humatest.TestAPI, *apidoc.Components, humav2.Config) {
	t.Helper()
	c := apidoc.NewComponents()
	c.Add("Book", apidoc.RawFragment(map[string]any{
		"type":       "object",
		"properties": map[string]any{"title": map[string]any{"type": "string"}},
		"required":   []any{"title"},
	}))
	RegisterNode[BookWire](c, "Book")
	cfg := NewConfig("Books", "1.0.0", WithRegistry(c))
	_, api := humatest.New(t, cfg)
	return api, c, cfg
}

func servedDoc(t *testing.T, api humatest.TestAPI) map[string]any {
	t.Helper()
	resp := api.Get("/openapi.json")
	if resp.Code != http.StatusOK {
		t.Fatalf("GET /openapi.json = %d: %s", resp.Code, resp.Body.String())
	}
	var doc map[string]any
	if err := json.Unmarshal(resp.Body.Bytes(), &doc); err != nil {
		t.Fatalf("served doc is not JSON: %v", err)
	}
	return doc
}

func TestConfigEndToEndDataEnvelope(t *testing.T) {
	api, c, _ := newBookAPI(t)

	humav2.Register(api, humav2.Operation{
		OperationID: "get-book",
		Method:      http.MethodGet,
		Path:        "/books/{id}",
		Parameters: []*humav2.Param{
			{Name: "id", In: "path", Required: true, Schema: &humav2.Schema{Type: "string"}},
		},
	}, func(ctx context.Context, in *struct{}) (*bookOut, error) {
		return &bookOut{Body: BookEnvelope{Data: NodeOf[BookWire](json.RawMessage(`{"title":"Dune"}`))}}, nil
	})

	if err := c.Verify(); err != nil {
		t.Fatalf("component set does not verify after registration: %v", err)
	}

	doc := servedDoc(t, api)
	schemas := dig(t, doc, "components", "schemas")
	env, ok := schemas["BookEnvelope"].(map[string]any)
	if !ok {
		t.Fatalf("derived envelope missing from the served doc: %v", keysOfAny(schemas))
	}
	props, _ := env["properties"].(map[string]any)
	data, _ := props["data"].(map[string]any)
	if data["$ref"] != apidoc.RefPrefix+"Book" {
		t.Fatalf("envelope data = %#v, want $ref Book", data)
	}
	if !sameSet(toStrings(env["required"]), []string{"data"}) {
		t.Errorf("envelope required = %v", env["required"])
	}
	// Book came from the ONE component set.
	if _, ok := schemas["Book"]; !ok {
		t.Fatalf("Book missing from the served doc: %v", keysOfAny(schemas))
	}
	// No $schema property anywhere: the link transformer is not installed.
	if raw, _ := json.Marshal(doc); strings.Contains(string(raw), `"$schema"`) {
		t.Errorf("served doc carries a $schema property (link transformer leaked in)")
	}

	// The response body is the hydrated engine bytes, with no $schema field.
	resp := api.Get("/books/42")
	if resp.Code != http.StatusOK {
		t.Fatalf("GET = %d: %s", resp.Code, resp.Body.String())
	}
	var body map[string]any
	if err := json.Unmarshal(resp.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if _, has := body["$schema"]; has {
		t.Errorf("response body carries $schema: %s", resp.Body.String())
	}
	inner, _ := body["data"].(map[string]any)
	if inner["title"] != "Dune" {
		t.Errorf("hydrated bytes lost: %s", resp.Body.String())
	}
}

func TestConfigEndToEndPaginatedEnvelope(t *testing.T) {
	api, c, _ := newBookAPI(t)

	humav2.Register(api, humav2.Operation{
		OperationID: "list-books",
		Method:      http.MethodGet,
		Path:        "/books",
	}, func(ctx context.Context, in *struct{}) (*bookListOut, error) {
		return &bookListOut{}, nil
	})

	if err := c.Verify(); err != nil {
		t.Fatalf("component set does not verify: %v", err)
	}
	doc := servedDoc(t, api)
	schemas := dig(t, doc, "components", "schemas")
	env, ok := schemas["BookCursorEnvelope"].(map[string]any)
	if !ok {
		t.Fatalf("paginated envelope missing: %v", keysOfAny(schemas))
	}
	props, _ := env["properties"].(map[string]any)
	pag, _ := props["pagination"].(map[string]any)
	if pag["$ref"] != apidoc.RefPrefix+CursorPaginationComponent {
		t.Fatalf("pagination = %#v", pag)
	}
	// The pagination component is reachable, so it survives huma's pruning.
	cp, ok := schemas[CursorPaginationComponent].(map[string]any)
	if !ok {
		t.Fatalf("CursorPagination missing from the served doc: %v", keysOfAny(schemas))
	}
	cprops, _ := cp["properties"].(map[string]any)
	if lim, _ := cprops["limit"].(map[string]any); lim["type"] != "number" {
		t.Errorf("limit must be number-typed in the served doc: %#v", cprops["limit"])
	}
}

// The brief's shape: an ANONYMOUS body struct. It exercises the namer's hint
// branch — the derived envelope has no Go type name to be registered under.
type anonBookOut struct {
	Body struct {
		Data Node[BookWire] `json:"data"`
	}
}

func TestConfigEndToEndAnonymousEnvelopeBody(t *testing.T) {
	api, c, _ := newBookAPI(t)
	humav2.Register(api, humav2.Operation{
		OperationID: "get-anon-book",
		Method:      http.MethodGet,
		Path:        "/anon",
	}, func(ctx context.Context, in *struct{}) (*anonBookOut, error) {
		out := &anonBookOut{}
		out.Body.Data = NodeOf[BookWire](json.RawMessage(`{"title":"Dune"}`))
		return out, nil
	})
	if err := c.Verify(); err != nil {
		t.Fatalf("component set does not verify: %v", err)
	}

	doc := servedDoc(t, api)
	op := dig(t, doc, "paths", "/anon", "get")
	ref, _ := dig(t, op, "responses", "200", "content", "application/json", "schema")["$ref"].(string)
	if ref == "" {
		t.Fatalf("the anonymous body was not served as a $ref: %#v", op)
	}
	env, ok := dig(t, doc, "components", "schemas")[strings.TrimPrefix(ref, apidoc.RefPrefix)].(map[string]any)
	if !ok {
		t.Fatalf("component %q missing from the served doc", ref)
	}
	props, _ := env["properties"].(map[string]any)
	data, _ := props["data"].(map[string]any)
	if data["$ref"] != apidoc.RefPrefix+"Book" {
		t.Fatalf("anonymous envelope = %#v, want {data: $ref Book}", env)
	}
	if !sameSet(toStrings(env["required"]), []string{"data"}) {
		t.Errorf("required = %v", env["required"])
	}

	resp := api.Get("/anon")
	if resp.Code != http.StatusOK {
		t.Fatalf("GET = %d: %s", resp.Code, resp.Body.String())
	}
	if !strings.Contains(resp.Body.String(), `"title":"Dune"`) {
		t.Errorf("hydrated bytes lost: %s", resp.Body.String())
	}
}

// The derivation is idempotent: the SAME body type on two operations is one
// component, registered once (the type binding short-circuits the second
// derivation), not a duplicate-registration panic.
func TestDerivedEnvelopeIsIdempotentAcrossOperations(t *testing.T) {
	api, c, _ := newBookAPI(t)
	for _, path := range []string{"/one", "/two"} {
		humav2.Register(api, humav2.Operation{
			OperationID: "get-book" + strings.ReplaceAll(path, "/", "-"),
			Method:      http.MethodGet,
			Path:        path,
		}, func(ctx context.Context, in *struct{}) (*bookOut, error) {
			return &bookOut{Body: BookEnvelope{Data: NodeOf[BookWire](json.RawMessage(`{"title":"Dune"}`))}}, nil
		})
	}
	if err := c.Verify(); err != nil {
		t.Fatalf("component set does not verify: %v", err)
	}
	doc := servedDoc(t, api)
	schemas := dig(t, doc, "components", "schemas")
	if _, ok := schemas["BookEnvelope"]; !ok {
		t.Fatalf("envelope missing: %v", keysOfAny(schemas))
	}
	for _, path := range []string{"/one", "/two"} {
		op := dig(t, doc, "paths", path, "get")
		ref := dig(t, op, "responses", "200", "content", "application/json", "schema")["$ref"]
		if ref != apidoc.RefPrefix+"BookEnvelope" {
			t.Errorf("%s response schema = %v, want the ONE derived component", path, ref)
		}
	}
}

// A node wrapper OUTSIDE an envelope reaches the canonical reflector, which
// would emit an empty object (the payload lives in an unexported field). That
// is a silently wrong contract, so it stops the wiring instead.
type nodeInPlainStruct struct {
	Book  Node[BookWire] `json:"book"`
	Label string         `json:"label"`
}

type nodeInPlainOut struct {
	Body nodeInPlainStruct
}

func TestNodeOutsideAnEnvelopePanics(t *testing.T) {
	api, _, _ := newBookAPI(t)
	defer func() {
		r := recover()
		if r == nil {
			t.Fatalf("a node wrapper outside an envelope must stop the wiring")
		}
		msg := fmt.Sprint(r)
		if !strings.Contains(msg, "node wrapper") || !strings.Contains(msg, "nodeInPlainStruct.Book") {
			t.Fatalf("panic does not name the offending field: %s", msg)
		}
	}()
	humav2.Register(api, humav2.Operation{
		OperationID: "get-bad",
		Method:      http.MethodGet,
		Path:        "/bad",
	}, func(ctx context.Context, in *struct{}) (*nodeInPlainOut, error) { return nil, nil })
}

// The registry answers huma's INPUT schemas too — the canonical idiom, not
// huma's own reflection.
type bookInput struct {
	Body PlainBody
}

func TestConfigInputSchemasComeFromTheBridge(t *testing.T) {
	api, c, _ := newBookAPI(t)
	humav2.Register(api, humav2.Operation{
		OperationID: "put-plain",
		Method:      http.MethodPut,
		Path:        "/plain",
	}, func(ctx context.Context, in *bookInput) (*struct{}, error) { return nil, nil })

	if _, ok := c.TypeName(reflect.TypeFor[PlainBody]()); !ok {
		t.Fatalf("input body type did not enter the shared component set")
	}
	doc := servedDoc(t, api)
	schemas := dig(t, doc, "components", "schemas")
	pb, ok := schemas["PlainBody"].(map[string]any)
	if !ok {
		t.Fatalf("input component missing: %v", keysOfAny(schemas))
	}
	// The canonical reflector does not close objects.
	if _, has := pb["additionalProperties"]; has {
		t.Errorf("canonical components never carry additionalProperties: %#v", pb)
	}
}

func dig(t *testing.T, doc map[string]any, path ...string) map[string]any {
	t.Helper()
	cur := doc
	for _, k := range path {
		next, ok := cur[k].(map[string]any)
		if !ok {
			t.Fatalf("served doc has no %v", path)
		}
		cur = next
	}
	return cur
}

func keysOfAny(m map[string]any) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}
