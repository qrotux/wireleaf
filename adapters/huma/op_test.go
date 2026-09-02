package huma

import (
	"context"
	"encoding/json"
	"net/http"
	"reflect"
	"strings"
	"testing"

	humav2 "github.com/danielgtaylor/huma/v2"

	"github.com/qrotux/wireleaf/apidoc"
	"github.com/qrotux/wireleaf/include"
)

// ---------------------------------------------------------------------------
// a toy graph for IncludeParam
// ---------------------------------------------------------------------------

type toyResource struct {
	name  string
	edges map[string]include.Edge
}

func (r toyResource) Name() string                          { return r.name }
func (r toyResource) Slug() string                          { return r.name }
func (r toyResource) Fields() []string                      { return nil }
func (r toyResource) Defaults() []string                    { return nil }
func (r toyResource) Edges() map[string]include.Edge        { return r.edges }
func (r toyResource) IDOf(doc any) string                   { return "" }
func (r toyResource) Serialize(doc any, _ *include.Ctx) any { return doc }
func (r toyResource) Enrich(_ []any, _ *include.Ctx) error  { return nil }

func toyGraph() include.Resource {
	author := toyResource{name: "author"}
	return toyResource{name: "book", edges: map[string]include.Edge{
		"author":  {Target: func() include.Resource { return author }, Includable: true},
		"reviews": {Target: func() include.Resource { return author }, Includable: true},
		"hidden":  {Target: func() include.Resource { return author }},
	}}
}

// ---------------------------------------------------------------------------
// Op decorators
// ---------------------------------------------------------------------------

func TestOpErrorsGroupsByStatus(t *testing.T) {
	op := Op(humav2.Operation{OperationID: "x"}, Errors(
		ErrorDef{Status: 404, Code: "BOOK_NOT_FOUND", Message: "no such book"},
		ErrorDef{Status: 403, Code: "FORBIDDEN", Message: "nope"},
		ErrorDef{Status: 404, Code: "AUTHOR_NOT_FOUND", Message: "no such author"},
	))

	if len(op.Responses) != 2 {
		t.Fatalf("responses = %v, want one per STATUS", keysOfResponses(op.Responses))
	}
	got := op.Responses["404"]
	if got == nil {
		t.Fatalf("no 404 response: %v", keysOfResponses(op.Responses))
	}
	want := "BOOK_NOT_FOUND (HTTP 404): no such book; AUTHOR_NOT_FOUND (HTTP 404): no such author"
	if got.Description != want {
		t.Errorf("404 description = %q, want %q", got.Description, want)
	}
	mt := got.Content["application/json"]
	if mt == nil || mt.Schema == nil || mt.Schema.Ref != apidoc.RefPrefix+ErrorComponent {
		t.Errorf("404 body = %#v, want $ref Error", mt)
	}
	if op.Responses["403"].Description != "FORBIDDEN (HTTP 403): nope" {
		t.Errorf("403 description = %q", op.Responses["403"].Description)
	}
}

func TestOpErrorsEmptyIsANoOp(t *testing.T) {
	op := Op(humav2.Operation{OperationID: "x"}, Errors())
	if op.Responses != nil {
		t.Errorf("no defs must leave Responses alone: %v", op.Responses)
	}
}

func TestOpIncludeParam(t *testing.T) {
	op := Op(humav2.Operation{OperationID: "x"}, IncludeParam(toyGraph()))
	if len(op.Parameters) != 1 {
		t.Fatalf("parameters = %#v", op.Parameters)
	}
	p := op.Parameters[0]
	if p.Name != "include" || p.In != "query" || p.Required {
		t.Errorf("param = %#v, want an optional include query param", p)
	}
	if p.Schema == nil || p.Schema.Type != "string" {
		t.Fatalf("include schema = %#v, want a string", p.Schema)
	}
	paths := toStrings(p.Schema.Extensions[apidoc.XIncludePaths])
	if !sameSet(paths, []string{"author", "reviews"}) {
		t.Errorf("x-include-paths = %v, want the includable edges only", p.Schema.Extensions[apidoc.XIncludePaths])
	}
}

// IncludeParamWithLimits bounds the enumerated paths by the caller's Limits,
// so an application that raised MaxDepth documents what it really accepts.
func TestIncludeParamWithLimits(t *testing.T) {
	node := toyResource{name: "node"}
	node.edges = map[string]include.Edge{
		"self": {Target: func() include.Resource { return node }, Includable: true},
	}
	shallow := Op(humav2.Operation{}, IncludeParamWithLimits(node, include.Limits{MaxDepth: 1, MaxNodes: 50}))
	if got := toStrings(shallow.Parameters[0].Schema.Extensions[apidoc.XIncludePaths]); !sameSet(got, []string{"self"}) {
		t.Errorf("MaxDepth 1: x-include-paths = %v, want [self]", got)
	}
	deep := Op(humav2.Operation{}, IncludeParamWithLimits(node, include.Limits{MaxDepth: 3, MaxNodes: 50}))
	if got := toStrings(deep.Parameters[0].Schema.Extensions[apidoc.XIncludePaths]); !sameSet(got, []string{"self", "self.self", "self.self.self"}) {
		t.Errorf("MaxDepth 3: x-include-paths = %v", got)
	}
	// Zero Limits → DefaultLimits, identical to IncludeParam.
	a := Op(humav2.Operation{}, IncludeParam(node))
	b := Op(humav2.Operation{}, IncludeParamWithLimits(node, include.Limits{}))
	if !sameSet(toStrings(a.Parameters[0].Schema.Extensions[apidoc.XIncludePaths]), toStrings(b.Parameters[0].Schema.Extensions[apidoc.XIncludePaths])) {
		t.Error("zero Limits must equal IncludeParam")
	}
}

// A shared base TEMPLATE must not carry one route's decorations into the next.
// Both reference fields are the hazard: the Responses map is shared by a
// shallow copy, and Parameters' spare capacity is written in place by append.
func TestOpDoesNotAliasTheBaseTemplate(t *testing.T) {
	params := make([]*humav2.Param, 1, 4) // spare capacity: append would write in place
	params[0] = &humav2.Param{Name: "id", In: "path", Required: true}
	base := humav2.Operation{
		Responses: map[string]*humav2.Response{
			"401": {Description: "unauthorized"},
		},
		Parameters: params,
	}

	first := Op(base,
		Errors(ErrorDef{Status: 404, Code: "NOT_FOUND", Message: "gone"}),
		IncludeParam(toyGraph()),
	)
	second := Op(base, Errors(ErrorDef{Status: 409, Code: "CONFLICT", Message: "clash"}))

	// The template is untouched.
	if len(base.Responses) != 1 {
		t.Errorf("base.Responses leaked: %v", keysOfResponses(base.Responses))
	}
	if len(base.Parameters) != 1 {
		t.Errorf("base.Parameters leaked: %#v", base.Parameters)
	}
	// And neither operation sees the other's decorations.
	if _, leaked := first.Responses["409"]; leaked {
		t.Errorf("first saw the second's error: %v", keysOfResponses(first.Responses))
	}
	if _, leaked := second.Responses["404"]; leaked {
		t.Errorf("second saw the first's error: %v", keysOfResponses(second.Responses))
	}
	if len(second.Parameters) != 1 {
		t.Errorf("second inherited the first's include param: %#v", second.Parameters)
	}
	if second.Parameters[0].Name != "id" {
		t.Errorf("the shared path param was overwritten: %#v", second.Parameters[0])
	}
	// The inherited response survives in both.
	for i, op := range []humav2.Operation{first, second} {
		if op.Responses["401"] == nil {
			t.Errorf("op %d lost the template's 401", i)
		}
	}
}

func TestIncludeParamIsIdempotent(t *testing.T) {
	op := Op(humav2.Operation{
		Parameters: []*humav2.Param{{Name: "include", In: "query", Description: "hand-written"}},
	}, IncludeParam(toyGraph()))
	if len(op.Parameters) != 1 {
		t.Fatalf("a second include param was appended: %#v", op.Parameters)
	}
	if op.Parameters[0].Description != "hand-written" {
		t.Errorf("the existing declaration was replaced: %#v", op.Parameters[0])
	}
	twice := Op(humav2.Operation{}, IncludeParam(toyGraph()), IncludeParam(toyGraph()))
	if len(twice.Parameters) != 1 {
		t.Errorf("IncludeParam twice = %d params", len(twice.Parameters))
	}
}

func TestOpDecoratorsCompose(t *testing.T) {
	base := humav2.Operation{
		OperationID: "get-book",
		Parameters:  []*humav2.Param{{Name: "id", In: "path", Required: true}},
	}
	op := Op(base,
		Errors(ErrorDef{Status: 404, Code: "NOT_FOUND", Message: "gone"}),
		IncludeParam(toyGraph()),
	)
	if len(op.Parameters) != 2 || op.Parameters[1].Name != "include" {
		t.Errorf("IncludeParam must APPEND: %#v", op.Parameters)
	}
	if base.Responses != nil {
		t.Errorf("Op must not mutate the caller's base operation: %v", base.Responses)
	}
}

// The decorated operation reaches the served document.
func TestOpDecorationsAreServed(t *testing.T) {
	api, _, _ := newBookAPI(t)
	humav2.Register(api, Op(humav2.Operation{
		OperationID: "get-book-decorated",
		Method:      http.MethodGet,
		Path:        "/books/{id}",
		Parameters: []*humav2.Param{
			{Name: "id", In: "path", Required: true, Schema: &humav2.Schema{Type: "string"}},
		},
	},
		Errors(ErrorDef{Status: 404, Code: "BOOK_NOT_FOUND", Message: "no such book"}),
		IncludeParam(toyGraph()),
	), func(ctx context.Context, in *struct{}) (*bookOut, error) {
		return &bookOut{Body: BookEnvelope{Data: NodeOf[BookWire](json.RawMessage(`{"title":"Dune"}`))}}, nil
	})

	doc := servedDoc(t, api)
	op := dig(t, doc, "paths", "/books/{id}", "get")
	resp404 := dig(t, op, "responses", "404")
	if resp404["description"] != "BOOK_NOT_FOUND (HTTP 404): no such book" {
		t.Errorf("404 description not served: %#v", resp404)
	}
	if ref := dig(t, resp404, "content", "application/json", "schema")["$ref"]; ref != apidoc.RefPrefix+ErrorComponent {
		t.Errorf("404 schema = %v, want $ref Error", ref)
	}
	// The Error component is reachable from the operation, so huma's pruning
	// keeps it in the served document.
	if _, ok := dig(t, doc, "components", "schemas")[ErrorComponent]; !ok {
		t.Errorf("Error component missing from the served doc")
	}
	params, _ := op["parameters"].([]any)
	var found map[string]any
	for _, p := range params {
		pm, _ := p.(map[string]any)
		if pm["name"] == "include" {
			found = pm
		}
	}
	if found == nil {
		t.Fatalf("include parameter not served: %#v", params)
	}
	schema, _ := found["schema"].(map[string]any)
	if !sameSet(toStrings(schema[apidoc.XIncludePaths]), []string{"author", "reviews"}) {
		t.Errorf("served include schema = %#v", schema)
	}
}

// ---------------------------------------------------------------------------
// post-registration fixes
// ---------------------------------------------------------------------------

// WriteBody is a plain write body: no provider, so its schema comes from the
// bridge.
type WriteBody struct {
	Title string `json:"title"`
}

// DeclaredWriteBody declares its own whole requestBody.
type DeclaredWriteBody struct{}

func (DeclaredWriteBody) RequestBodySchema() apidoc.Schema {
	return apidoc.OneOf(
		apidoc.RefTo("Book"),
		apidoc.RawFragment(map[string]any{"type": "object"}),
	)
}

// rawInput is the wireleaf write-path input shape: the handler owns the bytes,
// so huma emits an application/octet-stream body that the doc pass replaces.
type rawInput struct {
	RawBody []byte
}

func registerRawOp(t *testing.T, id, path string) (humav2.Config, *humav2.OpenAPI) {
	t.Helper()
	api, _, cfg := newBookAPI(t)
	humav2.Register(api, humav2.Operation{
		OperationID: id,
		Method:      http.MethodPost,
		Path:        path,
	}, func(ctx context.Context, in *rawInput) (*struct{}, error) { return nil, nil })
	return cfg, cfg.OpenAPI
}

func TestApplyRequestBodyDocFromTheBridge(t *testing.T) {
	_, oapi := registerRawOp(t, "post-book", "/books")

	// huma's auto body is the raw one.
	pre := oapi.Paths["/books"].Post.RequestBody
	if pre == nil || pre.Content["application/octet-stream"] == nil {
		t.Fatalf("expected huma's raw-body requestBody, got %#v", pre)
	}

	ApplyRequestBodyDoc(oapi, http.MethodPost, "/books", reflect.TypeFor[WriteBody]())

	rb := oapi.Paths["/books"].Post.RequestBody
	if rb.Required {
		t.Errorf("a documented write body is never Required")
	}
	if _, still := rb.Content["application/octet-stream"]; still {
		t.Errorf("the raw body was not replaced: %#v", rb.Content)
	}
	mt := rb.Content["application/json"]
	if mt == nil || mt.Schema == nil || mt.Schema.Ref != apidoc.RefPrefix+"WriteBody" {
		t.Fatalf("requestBody schema = %#v, want $ref WriteBody", mt)
	}
	// The type entered the SHARED set on the way.
	b := BridgeOf(oapi)
	if _, ok := b.Components().Get("WriteBody"); !ok {
		t.Errorf("the write body did not enter the shared component set")
	}
}

func TestApplyRequestBodyDocHonorsTheProvider(t *testing.T) {
	_, oapi := registerRawOp(t, "post-declared", "/declared")
	ApplyRequestBodyDoc(oapi, http.MethodPost, "/declared", reflect.TypeFor[DeclaredWriteBody]())

	mt := oapi.Paths["/declared"].Post.RequestBody.Content["application/json"]
	if mt == nil || mt.Schema == nil || len(mt.Schema.OneOf) != 2 {
		t.Fatalf("declared requestBody = %#v, want the provider's oneOf", mt)
	}
}

// The naming hint is DERIVED FROM THE OPERATION: a fixed one would give two
// operations' unrelated anonymous bodies the same component name.
func TestRequestBodyHintIsPerOperation(t *testing.T) {
	withID := requestBodyHint(&humav2.Operation{OperationID: "post-alpha"}, http.MethodPost, "/alpha")
	other := requestBodyHint(&humav2.Operation{OperationID: "post-beta"}, http.MethodPost, "/beta")
	if withID == other {
		t.Fatalf("two operations share the hint %q", withID)
	}
	if withID != "post-alphaRequestBody" {
		t.Errorf("hint = %q, want the operation id", withID)
	}
	// No operation id: method+path is unique by construction.
	a := requestBodyHint(&humav2.Operation{}, http.MethodPost, "/alpha")
	b := requestBodyHint(&humav2.Operation{}, http.MethodPost, "/beta")
	if a == b || a == "" {
		t.Errorf("id-less hints = %q / %q", a, b)
	}
}

// Two operations, two body types, two components — and both reachable.
func TestApplyRequestBodyDocNamesBodiesPerOperation(t *testing.T) {
	api, c, cfg := newBookAPI(t)
	for _, op := range []struct{ id, path string }{
		{"post-alpha", "/alpha"},
		{"post-beta", "/beta"},
	} {
		humav2.Register(api, humav2.Operation{
			OperationID: op.id,
			Method:      http.MethodPost,
			Path:        op.path,
		}, func(ctx context.Context, in *rawInput) (*struct{}, error) { return nil, nil })
	}
	ApplyRequestBodyDoc(cfg.OpenAPI, http.MethodPost, "/alpha", reflect.TypeFor[WriteBody]())
	ApplyRequestBodyDoc(cfg.OpenAPI, http.MethodPost, "/beta", reflect.TypeFor[PlainBody]())

	refOf := func(path string) string {
		mt := cfg.Paths[path].Post.RequestBody.Content["application/json"]
		if mt == nil || mt.Schema == nil {
			t.Fatalf("%s has no json request body", path)
		}
		return mt.Schema.Ref
	}
	a, bRef := refOf("/alpha"), refOf("/beta")
	if a == bRef {
		t.Fatalf("two bodies collapsed onto one component: %q", a)
	}
	for _, ref := range []string{a, bRef} {
		if _, ok := c.Get(ref[len(apidoc.RefPrefix):]); !ok {
			t.Errorf("component %q not in the shared set", ref)
		}
	}
	if err := c.Verify(); err != nil {
		t.Fatalf("shared set does not verify: %v", err)
	}
}

// LIMITATION, pinned: NO anonymous struct can be reflected into a component,
// wherever it appears. jsonschema-go inlines a root anonymous struct instead of
// defining it, so the reflector never produces the requested name and the bridge
// stops loudly, naming the workaround. Requests and responses alike; the ONE
// anonymous shape that works is a body the ENVELOPE DERIVATION recognises, which
// answers before the reflector is reached (see
// TestConfigEndToEndAnonymousEnvelopeBody).
const anonReflectorHint = "use a named body type"

func TestAnonymousRequestBodyIsUnsupported(t *testing.T) {
	_, oapi := registerRawOp(t, "post-anon", "/anon-body")
	anon := reflect.TypeOf(struct {
		Alpha string `json:"alpha"`
	}{})
	assertPanicsWith(t, anonReflectorHint, func() {
		ApplyRequestBodyDoc(oapi, http.MethodPost, "/anon-body", anon)
	})
}

// The same constraint on the RESPONSE side: an anonymous body that is not an
// envelope panics at huma.Register.
type anonPlainOut struct {
	Body struct {
		Alpha string `json:"alpha"`
	}
}

func TestAnonymousResponseBodyIsUnsupported(t *testing.T) {
	api, _, _ := newBookAPI(t)
	assertPanicsWith(t, anonReflectorHint, func() {
		humav2.Register(api, humav2.Operation{
			OperationID: "get-anon-plain",
			Method:      http.MethodGet,
			Path:        "/anon-plain",
		}, func(ctx context.Context, in *struct{}) (*anonPlainOut, error) { return nil, nil })
	})
}

func TestApplyRequestBodyDocUnknownOperationPanics(t *testing.T) {
	_, oapi := registerRawOp(t, "post-book2", "/books")
	assertPanics(t, func() {
		ApplyRequestBodyDoc(oapi, http.MethodPost, "/nope", reflect.TypeFor[WriteBody]())
	})
}

func TestApplyRequestBodyDocNeedsTheBridge(t *testing.T) {
	cfg := humav2.DefaultConfig("x", "1")
	assertPanics(t, func() {
		ApplyRequestBodyDoc(cfg.OpenAPI, http.MethodPost, "/books", reflect.TypeFor[WriteBody]())
	})
}

func TestApplyResponseHeaders(t *testing.T) {
	api, _, cfg := newBookAPI(t)
	humav2.Register(api, Op(humav2.Operation{
		OperationID: "get-headered",
		Method:      http.MethodGet,
		Path:        "/headered",
	}, Errors(ErrorDef{Status: 429, Code: "RATE_LIMITED", Message: "slow down"})),
		func(ctx context.Context, in *struct{}) (*struct{}, error) { return nil, nil })

	ApplyResponseHeaders(cfg.OpenAPI, http.MethodGet, "/headered", map[int][]ResponseHeader{
		204: {{Name: "X-Trace-Id", Description: "trace", Schema: map[string]any{"type": "string"}}},
		429: {{Name: "Retry-After", Schema: map[string]any{"type": "integer"}, Required: true}},
		503: {{Name: "Retry-After", Schema: map[string]any{"type": "integer"}}},
	})

	op := cfg.Paths["/headered"].Get
	h := op.Responses["204"].Headers["X-Trace-Id"]
	if h == nil || h.Description != "trace" || h.Schema.Extensions["type"] != "string" {
		t.Fatalf("204 header = %#v", h)
	}
	// A header attaches to an EXISTING response without disturbing it.
	if op.Responses["429"].Description != "RATE_LIMITED (HTTP 429): slow down" {
		t.Errorf("429 response was clobbered: %#v", op.Responses["429"])
	}
	if !op.Responses["429"].Headers["Retry-After"].Required {
		t.Errorf("required flag lost")
	}
	// A status with no response yet gets a bare, header-only one.
	if op.Responses["503"] == nil || op.Responses["503"].Headers["Retry-After"] == nil {
		t.Fatalf("header-only response missing: %#v", op.Responses["503"])
	}

	// The header object marshals as an OpenAPI Header: no name/in.
	raw, err := json.Marshal(op.Responses["204"].Headers["X-Trace-Id"])
	if err != nil {
		t.Fatal(err)
	}
	var hm map[string]any
	if err := json.Unmarshal(raw, &hm); err != nil {
		t.Fatal(err)
	}
	if _, has := hm["name"]; has {
		t.Errorf("a response header must not carry name/in: %s", raw)
	}
}

// A *Response inherited from a base template is SHARED by pointer. Attaching a
// header to one operation's copy must not write through it.
func TestApplyResponseHeadersDoesNotWriteThroughSharedResponses(t *testing.T) {
	api, _, cfg := newBookAPI(t)
	shared := &humav2.Response{Description: "unauthorized"}
	template := humav2.Operation{
		Responses: map[string]*humav2.Response{"401": shared},
	}
	for _, path := range []string{"/a", "/b"} {
		op := Op(template)
		op.OperationID = "get" + strings.ReplaceAll(path, "/", "-")
		op.Method = http.MethodGet
		op.Path = path
		humav2.Register(api, op, func(ctx context.Context, in *struct{}) (*struct{}, error) { return nil, nil })
	}

	ApplyResponseHeaders(cfg.OpenAPI, http.MethodGet, "/a", map[int][]ResponseHeader{
		401: {{Name: "WWW-Authenticate", Schema: map[string]any{"type": "string"}}},
	})

	// The template's own response is untouched...
	if shared.Headers != nil {
		t.Errorf("the shared template response was written through: %#v", shared.Headers)
	}
	// ...and so is the other operation's.
	if h := cfg.Paths["/b"].Get.Responses["401"].Headers; h != nil {
		t.Errorf("the header leaked onto /b: %#v", h)
	}
	a := cfg.Paths["/a"].Get.Responses["401"]
	if a.Headers["WWW-Authenticate"] == nil {
		t.Fatalf("/a did not get its header: %#v", a)
	}
	if a.Description != "unauthorized" {
		t.Errorf("the copied response lost its description: %#v", a)
	}
}

func TestApplyResponseHeadersEmptyIsANoOp(t *testing.T) {
	_, oapi := registerRawOp(t, "post-noop", "/noop")
	ApplyResponseHeaders(oapi, http.MethodPost, "/missing", nil) // no panic: nothing to do
}

func keysOfResponses(m map[string]*humav2.Response) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}
