package apidoc

// The ONE place the package's shared test helpers live. Do not redefine them
// elsewhere in the package (redeclaration is a compile error).

import (
	"maps"
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/qrotux/wireleaf/include"
)

// ---------------------------------------------------------------------------
// generic-fragment builders (assertion side only)
//
// These moved out of schemafor.go when SchemaFor was rewritten over the IR
// (Task 14): the production code builds IR nodes, so the map forms survive
// only as the shape the tests compare Schema.Map() against.
// ---------------------------------------------------------------------------

// ref builds a bare $ref fragment to component `name`.
func ref(name string) map[string]any {
	return map[string]any{"$ref": RefPrefix + name}
}

// anyOfNull wraps a fragment as anyOf[frag, {"type":"null"}].
func anyOfNull(frag map[string]any) map[string]any {
	return map[string]any{
		"anyOf": []any{frag, map[string]any{"type": "null"}},
	}
}

func assertPanics(t *testing.T, fn func()) {
	t.Helper()
	defer func() {
		if recover() == nil {
			t.Fatalf("expected panic")
		}
	}()
	fn()
}

// ---------------------------------------------------------------------------
// stubReflector
// ---------------------------------------------------------------------------

// stubReflector renders a struct into a flat IR object keyed by json field
// names. It stands in for a real reflector wherever the assertion is about
// apidoc's own composition (arm order, name selection, edge stitching,
// auxiliary inlining), not about reflection fidelity.
//
// Field rules, deliberately small:
//   - json tag names the property; ",omitempty" makes it optional, everything
//     else is required;
//   - a struct (or pointer-to-struct) field becomes a $ref to that Go type's
//     name, and the type is emitted as an AUXILIARY component alongside the
//     requested tops (recursively, cycle-safe) — this is what the inlining
//     tests need;
//   - a `doc:"..."` tag puts a Description on the property node, which is how a
//     test builds a SOLE-REF (a $ref with a sibling keyword);
//   - `raw:"1"` makes the field's component an Opaque node instead of an object
//     (for the never-inlined guarantee);
//   - everything else is {"type":"string"}.
type stubReflector struct {
	err error
}

func (s stubReflector) ReflectComponents(types []reflect.Type, overrides map[reflect.Type]string) (map[string]*IRNode, error) {
	if s.err != nil {
		return nil, s.err
	}
	out := map[string]*IRNode{}
	for _, t := range types {
		s.reflectType(DerefType(t), overrides, out)
	}
	return out, nil
}

// reflectType emits t's component into out under its override (or Go type)
// name, recursing into struct-typed fields. A type already in out is skipped,
// which is what makes a self-referential or mutually-referential struct
// terminate.
func (s stubReflector) reflectType(t reflect.Type, overrides map[reflect.Type]string, out map[string]*IRNode) string {
	name, ok := overrides[t]
	if !ok {
		name = t.Name()
	}
	if _, done := out[name]; done {
		return name
	}
	out[name] = nil // reserve the name so a cycle stops here

	props := make([]Prop, 0, t.NumField())
	for i := 0; i < t.NumField(); i++ {
		f := t.Field(i)
		key := f.Name
		required := true
		if tag := f.Tag.Get("json"); tag != "" && tag != "-" {
			parts := strings.Split(tag, ",")
			if parts[0] != "" {
				key = parts[0]
			}
			for _, opt := range parts[1:] {
				if opt == "omitempty" {
					required = false
				}
			}
		}
		var value *IRNode
		ft := DerefType(f.Type)
		switch {
		case f.Tag.Get("raw") != "":
			auxName := s.emitOpaqueAux(ft, overrides, out)
			value = newRef(auxName)
		case ft != nil && ft.Kind() == reflect.Struct:
			value = newRef(s.reflectType(ft, overrides, out))
		default:
			value = newScalar("string")
		}
		if doc := f.Tag.Get("doc"); doc != "" {
			value = value.shallowClone()
			value.Description = doc
		}
		props = append(props, Prop{Name: key, Schema: value, Required: required})
	}
	out[name] = newObject(props)
	return name
}

// emitOpaqueAux registers ft as an OPAQUE auxiliary component.
func (s stubReflector) emitOpaqueAux(ft reflect.Type, overrides map[reflect.Type]string, out map[string]*IRNode) string {
	name, ok := overrides[ft]
	if !ok {
		name = ft.Name()
	}
	if _, done := out[name]; !done {
		n, err := newOpaque([]byte(`{"type":"object","patternProperties":{"^x":{"type":"string"}}}`))
		if err != nil {
			panic(err)
		}
		out[name] = n
	}
	return name
}

var _ Reflector = stubReflector{}

// ---------------------------------------------------------------------------
// include.Resource stubs
// ---------------------------------------------------------------------------

// emitRes is a hand-built include.Resource for the emission tests: a name, a
// wire sample and a bag of edges. Building the fixtures by hand (rather than
// through the graph builder) is deliberate — package apidoc tests cannot import
// graph (import cycle), and include.Edge is public engine API that needs no
// constructor.
type emitRes struct {
	name        string
	wire        any
	edges       map[string]include.Edge
	defaults    []string
	docExternal bool
}

func (r *emitRes) Name() string                            { return r.name }
func (r *emitRes) Slug() string                            { return strings.ToLower(r.name) }
func (r *emitRes) Fields() []string                        { return nil }
func (r *emitRes) Defaults() []string                      { return r.defaults }
func (r *emitRes) Edges() map[string]include.Edge          { return r.edges }
func (r *emitRes) IDOf(doc any) string                     { return "" }
func (r *emitRes) Serialize(doc any, c *include.Ctx) any   { return doc }
func (r *emitRes) Enrich(docs []any, c *include.Ctx) error { return nil }

// WireSample and IsDocExternal are the two DUCK-TYPED seams apidoc reads off a
// compiled graph node; a compiled graph node implements the same pair.
func (r *emitRes) WireSample() any     { return r.wire }
func (r *emitRes) IsDocExternal() bool { return r.docExternal }

var _ include.Resource = (*emitRes)(nil)

// bareRes is a Resource that deliberately provides NO WireSample().
type bareRes struct{ name string }

func (r *bareRes) Name() string                            { return r.name }
func (r *bareRes) Slug() string                            { return r.name }
func (r *bareRes) Fields() []string                        { return nil }
func (r *bareRes) Defaults() []string                      { return nil }
func (r *bareRes) Edges() map[string]include.Edge          { return nil }
func (r *bareRes) IDOf(doc any) string                     { return "" }
func (r *bareRes) Serialize(doc any, c *include.Ctx) any   { return doc }
func (r *bareRes) Enrich(docs []any, c *include.Ctx) error { return nil }

var _ include.Resource = (*bareRes)(nil)

// ---------------------------------------------------------------------------
// shared fixtures & fragment assertions (relocated from emit_test.go, Task 14)
//
// The toy graph and the generic-fragment accessors are used by emit_test.go,
// schemafor_test.go and build_test.go alike, so they belong in the ONE shared
// home this file claims. Pure relocation: no behavior changed.
// ---------------------------------------------------------------------------

// ---------------------------------------------------------------------------
// toy graph
//
//	Book    author  → Author  (to-one)
//	        reviews → Review  (reverse, enveloped)
//	        similar → Book    (reverse, BARE; cycle)
//	        images  → Image   (in-array, subField "image"; DOC-EXTERNAL target)
//	        notes   → Author  (NOT includable)
//	Author  books   → Book    (reverse, enveloped; closes the Book↔Author cycle)
//	Review  leaf
//	Image   leaf, DocExternal
// ---------------------------------------------------------------------------

type bookWire struct {
	ID    string `json:"id"`
	Title string `json:"title"`
}

type authorWire struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

type reviewWire struct {
	ID   string `json:"id"`
	Body string `json:"body"`
}

type imageWire struct {
	ID  string `json:"id"`
	URL string `json:"url"`
}

type toyGraph struct{ Book, Author, Review, Image *emitRes }

func buildToyGraph() *toyGraph {
	book := &emitRes{name: "Book", wire: bookWire{}}
	author := &emitRes{name: "Author", wire: authorWire{}}
	review := &emitRes{name: "Review", wire: reviewWire{}, edges: map[string]include.Edge{}}
	image := &emitRes{name: "Image", wire: imageWire{}, edges: map[string]include.Edge{}, docExternal: true}

	book.edges = map[string]include.Edge{
		"author": {
			Target: func() include.Resource { return author }, Includable: true,
		},
		"reviews": {
			Target: func() include.Resource { return review }, Many: true, Includable: true,
			Backref: "bookId", Limit: 10,
		},
		"similar": {
			Target: func() include.Resource { return book }, Many: true, Includable: true,
			Backref: "similarTo", Bare: true,
		},
		"images": {
			Target: func() include.Resource { return image }, Many: true, Includable: true,
			ArrayPath: "gallery", SubField: "image",
		},
		"notes": {Target: func() include.Resource { return author }},
	}
	author.edges = map[string]include.Edge{
		"books": {
			Target: func() include.Resource { return book }, Many: true, Includable: true,
			Backref: "authorId",
		},
	}
	return &toyGraph{Book: book, Author: author, Review: review, Image: image}
}

// ---------------------------------------------------------------------------
// helpers
// ---------------------------------------------------------------------------

func component(t *testing.T, out map[string]Schema, name string) map[string]any {
	t.Helper()
	s, ok := out[name]
	if !ok {
		t.Fatalf("component %q missing (have %v)", name, keysOf(out))
	}
	return s.Map()
}

func prop(t *testing.T, comp map[string]any, key string) map[string]any {
	t.Helper()
	props, ok := comp["properties"].(map[string]any)
	if !ok {
		t.Fatalf("component has no properties map: %v", comp)
	}
	p, ok := props[key].(map[string]any)
	if !ok {
		t.Fatalf("property %q missing (got %T)", key, props[key])
	}
	return p
}

func hasProp(comp map[string]any, key string) bool {
	props, _ := comp["properties"].(map[string]any)
	_, ok := props[key]
	return ok
}

func requiredKeys(t *testing.T, comp map[string]any) []string {
	t.Helper()
	raw, ok := comp["required"].([]any)
	if !ok {
		return nil
	}
	out := make([]string, 0, len(raw))
	for _, v := range raw {
		s, ok := v.(string)
		if !ok {
			t.Fatalf("required entry %v is %T, want string", v, v)
		}
		out = append(out, s)
	}
	return out
}

func contains(hay []string, needle string) bool {
	for _, s := range hay {
		if s == needle {
			return true
		}
	}
	return false
}

func keysOf[V any](m map[string]V) []string { return slices.Sorted(maps.Keys(m)) }

// envelopeOf is the enveloped-to-many shape as a generic fragment, including
// the OPTIONAL nextCursor.
func envelopeOf(target map[string]any) map[string]any {
	return anyOfNull(map[string]any{
		"type": "object",
		"properties": map[string]any{
			"items":      map[string]any{"type": "array", "items": target},
			"hasMore":    map[string]any{"type": "boolean"},
			"nextCursor": map[string]any{"type": "string"},
		},
		"required": []any{"items", "hasMore"},
	})
}
