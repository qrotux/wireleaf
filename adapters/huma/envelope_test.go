package huma

import (
	"reflect"
	"testing"

	"github.com/qrotux/wireleaf/apidoc"
	"github.com/qrotux/wireleaf/reflector"
)

// BookWire is the wire type behind the Book component used by the envelope
// tests.
type BookWire struct {
	Title string `json:"title"`
}

// newEnvelopeBridge wires a bridge whose shared set already owns the Book
// component (a stand-in for a graph-emitted component) and the library
// components a WithRegistry install would add.
func newEnvelopeBridge(t *testing.T) (*Registry, *apidoc.Components) {
	t.Helper()
	c := apidoc.NewComponents()
	c.Add("Book", apidoc.RawFragment(map[string]any{
		"type":       "object",
		"properties": map[string]any{"title": map[string]any{"type": "string"}},
		"required":   []any{"title"},
	}))
	RegisterNode[BookWire](c, "Book")
	registerLibraryComponents(c)
	return NewRegistry(c, &reflector.Reflector{}).(*Registry), c
}

// derive asks the bridge for t's schema (as huma does for a body type) and
// returns the component it registered, in map form.
func derive(t *testing.T, b *Registry, c *apidoc.Components, rt reflect.Type, hint string) map[string]any {
	t.Helper()
	s := b.Schema(rt, true, hint)
	if s.Ref == "" {
		t.Fatalf("expected a $ref for %v, got %#v", rt, s)
	}
	name := s.Ref[len(apidoc.RefPrefix):]
	entry, ok := c.Get(name)
	if !ok {
		t.Fatalf("component %q was not registered", name)
	}
	return entry.Map()
}

// ---------------------------------------------------------------------------
// library components
// ---------------------------------------------------------------------------

func TestLibraryComponentsRegistered(t *testing.T) {
	c := apidoc.NewComponents()
	registerLibraryComponents(c)

	for _, name := range []string{"CursorPagination", "CursorPaginationTotal", "PagePagination", "Error"} {
		if _, ok := c.Get(name); !ok {
			t.Fatalf("library component %q not registered", name)
		}
	}
	// idempotent: a second install (two WithRegistry calls on one set) is fine.
	registerLibraryComponents(c)

	cursor, _ := c.Get("CursorPagination")
	got := cursor.Map()
	props, _ := got["properties"].(map[string]any)
	if props == nil {
		t.Fatalf("CursorPagination has no properties: %#v", got)
	}
	if lim, _ := props["limit"].(map[string]any); lim["type"] != "number" {
		t.Errorf("limit must be number-typed, got %#v", props["limit"])
	}
	next, _ := props["nextCursor"].(map[string]any)
	types, _ := next["type"].([]any)
	if len(types) != 2 || types[0] != "string" || types[1] != "null" {
		t.Errorf("nextCursor must be a nullable string, got %#v", next)
	}
	req := toStrings(got["required"])
	want := []string{"nextCursor", "prevCursor", "hasNextPage", "hasPrevPage", "limit"}
	if !sameSet(req, want) {
		t.Errorf("required = %v, want %v", req, want)
	}

	total, _ := c.Get("CursorPaginationTotal")
	tm := total.Map()
	tprops, _ := tm["properties"].(map[string]any)
	if _, ok := tprops["totalDocs"]; !ok {
		t.Errorf("CursorPaginationTotal must carry totalDocs: %#v", tm)
	}
	if contains(toStrings(tm["required"]), "totalDocs") {
		t.Errorf("totalDocs must stay OPTIONAL: %v", tm["required"])
	}

	page, _ := c.Get("PagePagination")
	pm := page.Map()
	if !sameSet(toStrings(pm["required"]),
		[]string{"page", "totalPages", "totalDocs", "hasNextPage", "hasPrevPage"}) {
		t.Errorf("PagePagination required = %v", pm["required"])
	}

	errC, _ := c.Get("Error")
	em := errC.Map()
	if !sameSet(toStrings(em["required"]), []string{"code", "message"}) {
		t.Errorf("Error required = %v", em["required"])
	}

	// The pagination components are bound to their wire types, so the bridge
	// serves them TYPED (huma can build a Go type back from the $ref).
	if n, ok := c.TypeName(reflect.TypeFor[CursorPagination]()); !ok || n != "CursorPagination" {
		t.Errorf("CursorPagination wire type not bound: %q %v", n, ok)
	}
}

// ---------------------------------------------------------------------------
// envelope derivation
// ---------------------------------------------------------------------------

type BookEnvelope struct {
	Data Node[BookWire] `json:"data"`
}

type BookListEnvelope struct {
	Data []Node[BookWire] `json:"data"`
}

type BookCursorEnvelope struct {
	Data       []Node[BookWire] `json:"data"`
	Pagination CursorPagination `json:"pagination"`
}

type BookCursorTotalEnvelope struct {
	Data       []Node[BookWire]      `json:"data"`
	Pagination CursorPaginationTotal `json:"pagination"`
}

type BookPageEnvelope struct {
	Data       []Node[BookWire] `json:"data"`
	Pagination PagePagination   `json:"pagination"`
}

type Facets struct {
	Tags []string `json:"tags"`
}

type BookOpenPageEnvelope struct {
	Data       []Node[BookWire] `json:"data"`
	Pagination PagePagination   `json:"pagination"`
	Facets     Facets           `json:"facets"`
}

// A struct that does not follow the data convention reflects plainly.
type PlainBody struct {
	Name string `json:"name"`
}

// A struct whose first field is "data" but not a node type is NOT an envelope.
type NotANodeEnvelope struct {
	Data string `json:"data"`
}

func TestEnvelopeDataNode(t *testing.T) {
	b, c := newEnvelopeBridge(t)
	got := derive(t, b, c, reflect.TypeFor[BookEnvelope](), "BookEnvelope")
	props, _ := got["properties"].(map[string]any)
	data, _ := props["data"].(map[string]any)
	if data["$ref"] != apidoc.RefPrefix+"Book" {
		t.Fatalf("data must be a $ref to Book: %#v", got)
	}
	if !sameSet(toStrings(got["required"]), []string{"data"}) {
		t.Errorf("required = %v", got["required"])
	}
}

func TestEnvelopeDataNodeSlice(t *testing.T) {
	b, c := newEnvelopeBridge(t)
	got := derive(t, b, c, reflect.TypeFor[BookListEnvelope](), "BookListEnvelope")
	props, _ := got["properties"].(map[string]any)
	data, _ := props["data"].(map[string]any)
	if data["type"] != "array" {
		t.Fatalf("data must be an array: %#v", got)
	}
	items, _ := data["items"].(map[string]any)
	if items["$ref"] != apidoc.RefPrefix+"Book" {
		t.Fatalf("items must $ref Book: %#v", got)
	}
}

func TestEnvelopeCursorPagination(t *testing.T) {
	b, c := newEnvelopeBridge(t)
	for _, tc := range []struct {
		rt   reflect.Type
		want string
	}{
		{reflect.TypeFor[BookCursorEnvelope](), "CursorPagination"},
		{reflect.TypeFor[BookCursorTotalEnvelope](), "CursorPaginationTotal"},
		{reflect.TypeFor[BookPageEnvelope](), "PagePagination"},
	} {
		got := derive(t, b, c, tc.rt, tc.rt.Name())
		props, _ := got["properties"].(map[string]any)
		pag, _ := props["pagination"].(map[string]any)
		if pag["$ref"] != apidoc.RefPrefix+tc.want {
			t.Errorf("%s: pagination = %#v, want $ref %s", tc.rt.Name(), pag, tc.want)
		}
		if !sameSet(toStrings(got["required"]), []string{"data", "pagination"}) {
			t.Errorf("%s: required = %v", tc.rt.Name(), got["required"])
		}
		if _, has := got["additionalProperties"]; has {
			t.Errorf("%s: envelopes stay open", tc.rt.Name())
		}
	}
}

func TestEnvelopeOpenPageWithExtraFields(t *testing.T) {
	b, c := newEnvelopeBridge(t)
	got := derive(t, b, c, reflect.TypeFor[BookOpenPageEnvelope](), "BookOpenPageEnvelope")
	props, _ := got["properties"].(map[string]any)
	if _, ok := props["facets"]; !ok {
		t.Fatalf("extra field missing: %#v", got)
	}
	if !sameSet(toStrings(got["required"]), []string{"data", "pagination", "facets"}) {
		t.Errorf("required = %v", got["required"])
	}
	if _, has := got["additionalProperties"]; has {
		t.Errorf("page envelopes never carry additionalProperties: %#v", got)
	}
	if err := c.Verify(); err != nil {
		t.Fatalf("extra-field component left a dangling ref: %v", err)
	}
}

// A cursor envelope with a THIRD field is not a cursor envelope (exactly 2).
type BookCursorExtra struct {
	Data       []Node[BookWire] `json:"data"`
	Pagination CursorPagination `json:"pagination"`
	Extra      string           `json:"extra"`
}

func TestEnvelopeCursorRejectsExtraFields(t *testing.T) {
	b, _ := newEnvelopeBridge(t)
	// White-box: a 3-field CURSOR struct is not a cursor envelope (exactly 2),
	// and the plain-reflection fallback for a Node-typed field is not
	// meaningful to assert on.
	if _, ok := b.envelopeIR(reflect.TypeFor[BookCursorExtra](), "BookCursorExtra"); ok {
		t.Fatalf("a 3-field cursor struct must NOT derive an envelope")
	}
}

func TestNonEnvelopeStructsReflectPlainly(t *testing.T) {
	b, c := newEnvelopeBridge(t)
	got := derive(t, b, c, reflect.TypeFor[PlainBody](), "PlainBody")
	props, _ := got["properties"].(map[string]any)
	if _, ok := props["name"]; !ok {
		t.Fatalf("plain struct did not reflect: %#v", got)
	}

	got2 := derive(t, b, c, reflect.TypeFor[NotANodeEnvelope](), "NotANodeEnvelope")
	props2, _ := got2["properties"].(map[string]any)
	data, _ := props2["data"].(map[string]any)
	if data["$ref"] != nil {
		t.Fatalf("a non-node data field must not become a $ref envelope: %#v", got2)
	}
}

// ---------------------------------------------------------------------------
// providers
// ---------------------------------------------------------------------------

// WholeBody declares its ENTIRE response body.
type WholeBody struct {
	Data Node[BookWire] `json:"data"`
}

func (WholeBody) BodySchema() apidoc.Schema {
	return apidoc.AnyOf(apidoc.RefTo("Book"), apidoc.RawFragment(map[string]any{"type": "null"}))
}

func TestBodySchemaProviderWins(t *testing.T) {
	b, c := newEnvelopeBridge(t)
	got := derive(t, b, c, reflect.TypeFor[WholeBody](), "WholeBody")
	if _, ok := got["anyOf"]; !ok {
		t.Fatalf("BodySchemaProvider must supply the WHOLE body: %#v", got)
	}
}

// ComboData supplies its own inline data schema.
type ComboData struct{}

func (ComboData) EnvelopeSchema() apidoc.Schema {
	return apidoc.AllOf(apidoc.RefTo("Book"), apidoc.RawFragment(map[string]any{"type": "object"}))
}

type ComboEnvelope struct {
	Data ComboData `json:"data"`
}

type ComboListEnvelope struct {
	Data []ComboData `json:"data"`
}

func TestEnvelopeSchemaProvider(t *testing.T) {
	b, c := newEnvelopeBridge(t)
	got := derive(t, b, c, reflect.TypeFor[ComboEnvelope](), "ComboEnvelope")
	props, _ := got["properties"].(map[string]any)
	data, _ := props["data"].(map[string]any)
	if _, ok := data["allOf"]; !ok {
		t.Fatalf("data must carry the provider's schema: %#v", got)
	}

	got2 := derive(t, b, c, reflect.TypeFor[ComboListEnvelope](), "ComboListEnvelope")
	props2, _ := got2["properties"].(map[string]any)
	data2, _ := props2["data"].(map[string]any)
	items, _ := data2["items"].(map[string]any)
	if _, ok := items["allOf"]; !ok {
		t.Fatalf("items must carry the provider's schema: %#v", got2)
	}
}

// ---------------------------------------------------------------------------
// small helpers
// ---------------------------------------------------------------------------

func toStrings(v any) []string {
	var out []string
	switch vs := v.(type) {
	case []string:
		return vs
	case []any:
		for _, e := range vs {
			s, _ := e.(string)
			out = append(out, s)
		}
	}
	return out
}

func sameSet(got, want []string) bool {
	if len(got) != len(want) {
		return false
	}
	seen := map[string]int{}
	for _, g := range got {
		seen[g]++
	}
	for _, w := range want {
		seen[w]--
	}
	for _, n := range seen {
		if n != 0 {
			return false
		}
	}
	return true
}

func contains(hay []string, needle string) bool {
	for _, h := range hay {
		if h == needle {
			return true
		}
	}
	return false
}
