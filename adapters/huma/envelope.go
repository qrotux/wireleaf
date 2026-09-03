package huma

// envelope.go — the success-envelope data convention plus the library
// components it references.
//
// WHERE IT RUNS. The derivation is part of the BRIDGE's Schema(): huma asks the
// registry for the schema of an operation's body type, so the convention has to
// answer there. There is no post-registration doc pass (the v0 prototype's
// successFragment ran after huma.Register and patched the document); inversion
// means the envelope IS the component huma was handed.
//
// THE CONVENTION (port of platform-go's successFragment). A body struct
// qualifies when:
//
//	field 0 is json:"data" AND its type is a registered node wrapper (or a slice
//	of one, or a type providing its own EnvelopeSchema), and either
//	  - it is the ONLY field                                  → {data}
//	  - field 1 is json:"pagination" typed CursorPagination /
//	    CursorPaginationTotal and there are EXACTLY two fields → {data,pagination}
//	  - field 1 is json:"pagination" typed PagePagination      → {data,pagination}
//	    plus any further fields, each documented and required (an OPEN envelope:
//	    page envelopes never carry additionalProperties)
//
// A type providing a whole BodySchema short-circuits all of it. Anything else
// is not an envelope and reflects canonically.

import (
	"encoding/json"
	"fmt"
	"reflect"
	"strings"

	"github.com/qrotux/wireleaf/apidoc"
)

// ---------------------------------------------------------------------------
// library components
// ---------------------------------------------------------------------------

// CursorPagination is the cursor page block. The wire shape is verbatim the
// platform-go contract (httpx/envelope.go): nullable cursors, boolean flags, an
// int limit.
type CursorPagination struct {
	NextCursor  *string `json:"nextCursor"`
	PrevCursor  *string `json:"prevCursor"`
	HasNextPage bool    `json:"hasNextPage"`
	HasPrevPage bool    `json:"hasPrevPage"`
	Limit       int     `json:"limit"`
}

// CursorPaginationTotal is the cursor page that ALSO carries totalDocs.
// TotalDocs is OPTIONAL: an empty resolve omits it, so the document marks it
// optional too.
type CursorPaginationTotal struct {
	NextCursor  *string `json:"nextCursor"`
	PrevCursor  *string `json:"prevCursor"`
	HasNextPage bool    `json:"hasNextPage"`
	HasPrevPage bool    `json:"hasPrevPage"`
	Limit       int     `json:"limit"`
	TotalDocs   *int    `json:"totalDocs,omitempty"`
}

// PagePagination is the offset page block. Every field is required.
type PagePagination struct {
	Page        int  `json:"page"`
	TotalPages  int  `json:"totalPages"`
	TotalDocs   int  `json:"totalDocs"`
	HasNextPage bool `json:"hasNextPage"`
	HasPrevPage bool `json:"hasPrevPage"`
}

// Component names of the library components. They are the names the derived
// envelopes $ref, so they are part of the public contract.
const (
	CursorPaginationComponent      = "CursorPagination"
	CursorPaginationTotalComponent = "CursorPaginationTotal"
	PagePaginationComponent        = "PagePagination"
	ErrorComponent                 = "Error"
)

// The DOC schemas are hand-built and NUMBER-typed on purpose: a Go int reflects
// to {"type":"integer","format":"int64"}, which is a drifted contract against
// the TypeScript client. The Go structs exist for the operation layer to
// SERIALIZE; the schemas here are what the document says.
func cursorPaginationFragment(withTotal bool) map[string]any {
	props := map[string]any{
		"nextCursor":  map[string]any{"type": []any{"string", "null"}},
		"prevCursor":  map[string]any{"type": []any{"string", "null"}},
		"hasNextPage": map[string]any{"type": "boolean"},
		"hasPrevPage": map[string]any{"type": "boolean"},
		"limit":       map[string]any{"type": "number"},
	}
	if withTotal {
		props["totalDocs"] = map[string]any{"type": "number"} // OPTIONAL
	}
	return map[string]any{
		"type":       "object",
		"properties": props,
		"required":   []any{"nextCursor", "prevCursor", "hasNextPage", "hasPrevPage", "limit"},
	}
}

func pagePaginationFragment() map[string]any {
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"page":        map[string]any{"type": "number"},
			"totalPages":  map[string]any{"type": "number"},
			"totalDocs":   map[string]any{"type": "number"},
			"hasNextPage": map[string]any{"type": "boolean"},
			"hasPrevPage": map[string]any{"type": "boolean"},
		},
		"required": []any{"page", "totalPages", "totalDocs", "hasNextPage", "hasPrevPage"},
	}
}

func errorFragment() map[string]any {
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"code":    map[string]any{"type": "string"},
			"message": map[string]any{"type": "string"},
		},
		"required": []any{"code", "message"},
	}
}

// registerLibraryComponents installs the pagination blocks and the Error
// component on c. NewConfig calls it at wiring time, because a derived envelope
// $refs a pagination component and Errors() $refs Error — a document that uses
// either must own it.
//
// Registration goes through RegisterReflected, which is IDEMPOTENT for an
// identical re-registration (two WithRegistry installs over one Components) and
// an ERROR for a conflicting one (an application component squatting the name).
// The pagination components are bound to their Go wire types so the bridge can
// serve them typed; Error has no wire type and is served as one opaque fragment.
// libraryComponents are the names registerLibraryComponents owns; New refuses
// a graph node that would take one of them.
var libraryComponents = []string{CursorPaginationComponent, CursorPaginationTotalComponent, PagePaginationComponent, ErrorComponent}

func registerLibraryComponents(c *apidoc.Components) {
	install := func(name string, frag map[string]any, t reflect.Type) {
		if err := c.RegisterReflected(name, apidoc.RawFragment(frag).IR(), t); err != nil {
			panic(fmt.Sprintf("adapters/huma: installing library component %q: %v", name, err))
		}
	}
	install(CursorPaginationComponent, cursorPaginationFragment(false), reflect.TypeFor[CursorPagination]())
	install(CursorPaginationTotalComponent, cursorPaginationFragment(true), reflect.TypeFor[CursorPaginationTotal]())
	install(PagePaginationComponent, pagePaginationFragment(), reflect.TypeFor[PagePagination]())
	install(ErrorComponent, errorFragment(), nil)
}

// ---------------------------------------------------------------------------
// derivation
// ---------------------------------------------------------------------------

var (
	cursorPaginationType      = reflect.TypeFor[CursorPagination]()
	cursorPaginationTotalType = reflect.TypeFor[CursorPaginationTotal]()
	pagePaginationType        = reflect.TypeFor[PagePagination]()

	bodySchemaProviderType     = reflect.TypeFor[apidoc.BodySchemaProvider]()
	envelopeSchemaProviderType = reflect.TypeFor[apidoc.EnvelopeSchemaProvider]()
	requestBodyProviderType    = reflect.TypeFor[apidoc.RequestBodyProvider]()
)

// envelopeIR returns the derived envelope IR for t, or (nil,false) when t is
// not a data-convention body.
func (b *Registry) envelopeIR(t reflect.Type, hint string) (*apidoc.IRNode, bool) {
	// A whole-body provider declares everything; checked first, because such a
	// type need not be a {data} struct at all.
	if s, ok := bodySchemaOf(t); ok {
		return s.IR(), true
	}
	if t.Kind() != reflect.Struct || t.NumField() == 0 {
		return nil, false
	}
	f0 := t.Field(0)
	if jsonKey(f0) != "data" {
		return nil, false
	}
	data, ok := b.dataFieldSchema(f0.Type)
	if !ok {
		return nil, false
	}
	props := map[string]any{"data": data}
	required := []any{"data"}
	env := map[string]any{"type": "object", "properties": props}

	if t.NumField() == 1 {
		env["required"] = required
		return apidoc.RawFragment(env).IR(), true
	}

	f1 := t.Field(1)
	if jsonKey(f1) != "pagination" {
		return nil, false
	}
	switch f1.Type {
	case cursorPaginationType, cursorPaginationTotalType:
		if t.NumField() != 2 {
			return nil, false
		}
		name := CursorPaginationComponent
		if f1.Type == cursorPaginationTotalType {
			name = CursorPaginationTotalComponent
		}
		props["pagination"] = map[string]any{"$ref": apidoc.RefPrefix + name}
		env["required"] = append(required, "pagination")
		return apidoc.RawFragment(env).IR(), true

	case pagePaginationType:
		props["pagination"] = map[string]any{"$ref": apidoc.RefPrefix + PagePaginationComponent}
		required = append(required, "pagination")
		// An OPEN page envelope: every further field is documented and
		// required, and nothing writes additionalProperties.
		for i := 2; i < t.NumField(); i++ {
			fi := t.Field(i)
			key := jsonKey(fi)
			if key == "" || key == "-" {
				return nil, false
			}
			props[key] = b.fieldFragment(fi.Type, hint+"-"+key)
			required = append(required, key)
		}
		env["required"] = required
		return apidoc.RawFragment(env).IR(), true
	}
	return nil, false
}

// dataFieldSchema documents the data field: a slice of, or a single, node
// wrapper ($ref to the component W was registered under) or provider type
// (its own inline schema).
//
// ORDER: the EnvelopeSchemaProvider is honored BEFORE the node lookup, so a
// type that both provides a composed schema and is a registered node wrapper
// documents the composition it declared.
func (b *Registry) dataFieldSchema(t reflect.Type) (map[string]any, bool) {
	if t.Kind() == reflect.Slice {
		items, ok := b.dataValueSchema(t.Elem())
		if !ok {
			return nil, false
		}
		return map[string]any{"type": "array", "items": items}, true
	}
	return b.dataValueSchema(t)
}

func (b *Registry) dataValueSchema(t reflect.Type) (map[string]any, bool) {
	if s, ok := envelopeSchemaOf(t); ok {
		return s.Map(), true
	}
	if name, ok := b.c.NodeComponent(t); ok {
		return map[string]any{"$ref": apidoc.RefPrefix + name}, true
	}
	return nil, false
}

// fieldFragment documents one EXTRA field of an open page envelope. It recurses
// through the bridge — a component-worthy struct becomes a $ref (and enters the
// shared set on the way), everything else is huma's inline schema — and the
// result is folded back into the fragment as JSON. That keeps one reflection
// authority for the whole envelope.
func (b *Registry) fieldFragment(t reflect.Type, hint string) map[string]any {
	s := b.Schema(t, true, hint)
	raw, err := json.Marshal(s)
	if err != nil {
		panic(fmt.Sprintf("adapters/huma: documenting envelope field %v: %v", t, err))
	}
	var m map[string]any
	if err := json.Unmarshal(raw, &m); err != nil {
		panic(fmt.Sprintf("adapters/huma: documenting envelope field %v: %v", t, err))
	}
	// Page envelopes stay OPEN — an inline object schema must not close the
	// door the envelope deliberately leaves ajar.
	delete(m, "additionalProperties")
	return m
}

// ---------------------------------------------------------------------------
// provider lookups
// ---------------------------------------------------------------------------

// CONTRACT: providers use a VALUE receiver. A pointer type is dereferenced
// first, and both the value and the pointer method sets are consulted.
func providerOf[T any](t reflect.Type, iface reflect.Type) (T, bool) {
	var zero T
	t = apidoc.DerefType(t)
	if t == nil {
		return zero, false
	}
	if t.Implements(iface) {
		v, ok := reflect.Zero(t).Interface().(T)
		return v, ok
	}
	if reflect.PointerTo(t).Implements(iface) {
		v, ok := reflect.New(t).Interface().(T)
		return v, ok
	}
	return zero, false
}

func bodySchemaOf(t reflect.Type) (apidoc.Schema, bool) {
	p, ok := providerOf[apidoc.BodySchemaProvider](t, bodySchemaProviderType)
	if !ok {
		return apidoc.Schema{}, false
	}
	return p.BodySchema(), true
}

func envelopeSchemaOf(t reflect.Type) (apidoc.Schema, bool) {
	p, ok := providerOf[apidoc.EnvelopeSchemaProvider](t, envelopeSchemaProviderType)
	if !ok {
		return apidoc.Schema{}, false
	}
	return p.EnvelopeSchema(), true
}

func requestBodySchemaOf(t reflect.Type) (apidoc.Schema, bool) {
	p, ok := providerOf[apidoc.RequestBodyProvider](t, requestBodyProviderType)
	if !ok {
		return apidoc.Schema{}, false
	}
	return p.RequestBodySchema(), true
}

// jsonKey is the field's json name (up to the first comma), "" without a tag.
func jsonKey(f reflect.StructField) string {
	return strings.Split(f.Tag.Get("json"), ",")[0]
}
