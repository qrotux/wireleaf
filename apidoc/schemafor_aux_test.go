package apidoc

// schemafor_aux_test.go — SchemaFor and EmitComponents must AGREE about
// auxiliaries.
//
// The invariant these tests pin: the set of refs SchemaFor emits is a SUBSET of
// the names EmitComponents emits (plus the node names it also emits). Should
// baseObjectIR ever discard the reflector's auxiliary output, a nested-struct
// property would stay a $ref here while EmitComponents inlines it away and
// emits no component of that name — a dangling reference against the very map
// SchemaFor says to resolve against.

import (
	"maps"
	"reflect"
	"slices"
	"testing"

	"github.com/qrotux/wireleaf/include"
)

// metaInner is the plain nested struct: EmitComponents inlines it, so it is
// NEVER a component name, so SchemaFor must not reference it either.
type metaInner struct {
	Kind string `json:"kind"`
}

// metaWire has BOTH aux cases in one node:
//   - "meta": a bare reference → inlined by both sides;
//   - "described": a described reference (the SOLE-REF rule) → survives as a
//     component on both sides.
type metaWire struct {
	ID        string    `json:"id"`
	Meta      metaInner `json:"meta"`
	Described metaInner `json:"described" doc:"the same struct, annotated"`
}

// plainInner is referenced ONLY barely, so EmitComponents inlines every
// reference and DROPS the component entirely — the case where SchemaFor could
// emit a $ref to a name that exists nowhere in the document.
type plainInner struct {
	Kind string `json:"kind"`
}

type plainWire struct {
	ID   string     `json:"id"`
	Meta plainInner `json:"meta"`
}

// schemaForRefs collects every "$ref" target name reachable in a serialized
// SchemaFor result.
func schemaForRefs(m map[string]any) map[string]bool {
	out := map[string]bool{}
	for _, r := range collectRefs(m) {
		out[r] = true
	}
	return out
}

// TestSchemaForRefsAreAllEmittedComponents is the DIRECT cross-consistency
// assertion: every name SchemaFor references must exist in EmitComponents'
// output map. This is the test that fails without the shared inlining.
func TestSchemaForRefsAreAllEmittedComponents(t *testing.T) {
	for _, tc := range []struct {
		name string
		node *emitRes
	}{
		// The aux survives (a described ref keeps it alive), so both sides must
		// name it.
		{"aux survives inlining", &emitRes{name: "Meta", wire: metaWire{}, edges: map[string]include.Edge{}}},
		// The aux is fully inlined and DROPPED: a SchemaFor that did not share
		// the inlining would reference "plainInner", which EmitComponents does
		// not emit at all.
		{"aux fully inlined away", &emitRes{name: "Plain", wire: plainWire{}, edges: map[string]include.Edge{}}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			comps, err := EmitComponents(stubReflector{}, []include.Resource{tc.node})
			if err != nil {
				t.Fatalf("EmitComponents: %v", err)
			}
			s, err := SchemaFor(stubReflector{}, tc.node, include.IncludeTree{}, include.Limits{})
			if err != nil {
				t.Fatalf("SchemaFor: %v", err)
			}
			for name := range schemaForRefs(s.Map()) {
				if _, ok := comps[name]; !ok {
					t.Errorf("SchemaFor emits a $ref to %q, which EmitComponents does NOT emit (it has %v) — a DANGLING reference",
						name, slices.Sorted(maps.Keys(comps)))
				}
			}
		})
	}
}

// TestSchemaForInlinesNestedStructLikeEmit pins the positive half: the bare
// nested-struct property is INLINED in SchemaFor, exactly as in the static
// component, so no "metaInner" ref is produced at all.
func TestSchemaForInlinesNestedStructLikeEmit(t *testing.T) {
	node := &emitRes{name: "Meta", wire: metaWire{}, edges: map[string]include.Edge{}}
	s, err := SchemaFor(stubReflector{}, node, include.IncludeTree{}, include.Limits{})
	if err != nil {
		t.Fatalf("SchemaFor: %v", err)
	}
	got := s.Map()

	meta := prop(t, got, "meta")
	if meta["$ref"] != nil {
		t.Errorf("SchemaFor left the bare nested struct as a $ref (%v); EmitComponents inlines it", meta)
	}
	props, _ := meta["properties"].(map[string]any)
	if _, ok := props["kind"]; !ok {
		t.Errorf("the inlined nested struct lost its properties: %v", meta)
	}
}

// TestSchemaForSoleRefSurvivorIsResolvable pins the other half: a SURVIVING
// auxiliary (the sole-ref rule blocks its inlining) is referenced by BOTH sides
// under the same name, and the name resolves in EmitComponents' map.
func TestSchemaForSoleRefSurvivorIsResolvable(t *testing.T) {
	node := &emitRes{name: "Meta", wire: metaWire{}, edges: map[string]include.Edge{}}

	comps, err := EmitComponents(stubReflector{}, []include.Resource{node})
	if err != nil {
		t.Fatalf("EmitComponents: %v", err)
	}
	s, err := SchemaFor(stubReflector{}, node, include.IncludeTree{}, include.Limits{})
	if err != nil {
		t.Fatalf("SchemaFor: %v", err)
	}

	// EmitComponents keeps the survivor and references it from the static
	// component.
	if _, ok := comps["metaInner"]; !ok {
		t.Fatalf("the sole-ref survivor must be an emitted component; got %v", slices.Sorted(maps.Keys(comps)))
	}
	static := component(t, comps, "Meta")
	if got := prop(t, static, "described")["$ref"]; got != RefPrefix+"metaInner" {
		t.Fatalf("static component: described = %v, want a $ref to metaInner", got)
	}

	// SchemaFor references the SAME name.
	if got := prop(t, s.Map(), "described")["$ref"]; got != RefPrefix+"metaInner" {
		t.Errorf("SchemaFor: described = %v, want the same $ref to metaInner the static component uses", got)
	}
}

// TestSchemaForResolvesAgainstEmitComponentsMap is the Verify-style check: the
// recomputed schema, OVERLAID on EmitComponents' map as one more component,
// must leave the whole set with no dangling reference. That is exactly the
// situation an operation's response schema is in.
func TestSchemaForResolvesAgainstEmitComponentsMap(t *testing.T) {
	g := buildToyGraph()
	node := &emitRes{name: "Meta", wire: metaWire{}, edges: map[string]include.Edge{}}

	for _, tc := range []struct {
		name string
		root *emitRes
		tree include.IncludeTree
		// external names the DOC-EXTERNAL components the application owns.
		// EmitComponents deliberately does not emit their fragments (the toy
		// graph's "Image" is one), so a real document registers them itself;
		// the test does the same before asking Verify to resolve everything.
		external []string
	}{
		{"nested struct, no includes", node, include.IncludeTree{}, nil},
		{"fully inlined aux", &emitRes{name: "Plain", wire: plainWire{}, edges: map[string]include.Edge{}}, include.IncludeTree{}, nil},
		{"toy graph leaf include", g.Book, include.IncludeTree{"author": nil}, []string{"Image"}},
		{"toy graph nested include", g.Book, include.IncludeTree{"author": include.IncludeTree{"books": nil}}, []string{"Image"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			roots := []include.Resource{tc.root}
			comps, err := EmitComponents(stubReflector{}, roots)
			if err != nil {
				t.Fatalf("EmitComponents: %v", err)
			}
			s, err := SchemaFor(stubReflector{}, tc.root, tc.tree, include.Limits{})
			if err != nil {
				t.Fatalf("SchemaFor: %v", err)
			}

			c := NewComponents()
			for name, comp := range comps {
				c.Add(name, comp)
			}
			for _, name := range tc.external {
				c.Add(name, RawFragment(map[string]any{"type": "object"}))
			}
			// The recomputed response schema joins the set under its own name.
			c.Add("MetaResponse", s)
			if err := c.Verify(); err != nil {
				t.Errorf("the recomputed schema does not resolve against EmitComponents' map: %v", err)
			}
		})
	}
}

// nestedNodeHost carries another NODE's wire struct as a plain property (not
// an edge). EmitComponents names that property's schema after the node, so
// SchemaFor must too — or its $ref points at a Go type name no component has.
type nestedNodeWire struct {
	Kind string `json:"kind"`
}

type nestedNodeHost struct {
	ID    string         `json:"id"`
	Inner nestedNodeWire `json:"inner"`
	Again nestedNodeWire `json:"again"`
}

func TestSchemaForNamesNestedNodeLikeEmit(t *testing.T) {
	inner := &emitRes{name: "Inner", wire: nestedNodeWire{}, edges: map[string]include.Edge{}}
	host := &emitRes{name: "Host", wire: nestedNodeHost{}, edges: map[string]include.Edge{
		"link": {Target: func() include.Resource { return inner }, Includable: true},
	}}
	comps, err := EmitComponents(stubReflector{}, []include.Resource{host})
	if err != nil {
		t.Fatalf("EmitComponents: %v", err)
	}
	s, err := SchemaFor(stubReflector{}, host, include.IncludeTree{}, include.Limits{})
	if err != nil {
		t.Fatalf("SchemaFor: %v", err)
	}
	want := prop(t, comps["Host"].Map(), "inner")
	if got := prop(t, s.Map(), "inner"); !reflect.DeepEqual(got, want) {
		t.Errorf("SchemaFor inner = %v, EmitComponents inner = %v", got, want)
	}
	if ref := prop(t, s.Map(), "inner")["$ref"]; ref != RefPrefix+"Inner" {
		t.Errorf("inner $ref = %v, want the node name", ref)
	}
}
