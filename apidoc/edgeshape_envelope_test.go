package apidoc

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"

	"github.com/qrotux/wireleaf/include"
)

var dataEnv = include.Envelope{Key: "data", Pagination: "pagination"}

func irMap(t *testing.T, n *IRNode) map[string]any {
	t.Helper()
	raw, err := n.MarshalJSON()
	if err != nil {
		t.Fatalf("MarshalJSON: %v", err)
	}
	var m map[string]any
	if err := json.Unmarshal(raw, &m); err != nil {
		t.Fatalf("Unmarshal %s: %v", raw, err)
	}
	return m
}

func obj(props map[string]any, required ...string) map[string]any {
	out := map[string]any{"type": "object", "properties": props}
	if len(required) > 0 {
		req := make([]any, len(required))
		for i, r := range required {
			req[i] = r
		}
		out["required"] = req
	}
	return out
}

func arr(items map[string]any) map[string]any { return map[string]any{"type": "array", "items": items} }

func wrapped(key string, inner map[string]any) map[string]any {
	return obj(map[string]any{key: inner}, key)
}

func paginationFrag() map[string]any {
	return obj(map[string]any{
		"hasNextPage": map[string]any{"type": "boolean"},
		"nextCursor":  map[string]any{"type": "string"},
	}, "hasNextPage")
}

func TestWrappedEdgeShape(t *testing.T) {
	target := func() include.Resource { return nil }
	cases := []struct {
		name string
		edge include.Edge
		want map[string]any
	}{
		{"to-one", include.Edge{Target: target, Envelope: dataEnv},
			wrapped("data", anyOfNull(ref("T")))},
		{"to-one required", include.Edge{Target: target, Required: true, Envelope: dataEnv},
			wrapped("data", ref("T"))},
		{"reverse", include.Edge{Many: true, Backref: "p", Envelope: dataEnv},
			obj(map[string]any{
				"data":       arr(wrapped("data", ref("T"))),
				"pagination": paginationFrag(),
			}, "data", "pagination")},
		{"to-many custom names", include.Edge{Many: true, Envelope: include.Envelope{Key: "values", Pagination: "meta"}},
			obj(map[string]any{
				"values": arr(wrapped("values", ref("T"))),
				"meta":   paginationFrag(),
			}, "values", "meta")},
		{"reverse bare", include.Edge{Many: true, Backref: "p", Bare: true, Envelope: dataEnv},
			wrapped("data", arr(wrapped("data", ref("T"))))},
		{"reverse no pagination key", include.Edge{Many: true, Backref: "p", Envelope: include.Envelope{Key: "data"}},
			wrapped("data", arr(wrapped("data", ref("T"))))},
		{"in-array", include.Edge{Many: true, ArrayPath: "g", SubField: "s", Envelope: dataEnv},
			anyOfNull(arr(obj(map[string]any{"s": wrapped("data", anyOfNull(ref("T")))}, "s")))},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := irMap(t, edgeShape(tc.edge, newRef("T")))
			if !reflect.DeepEqual(got, tc.want) {
				g, _ := json.Marshal(got)
				w, _ := json.Marshal(tc.want)
				t.Errorf("shape =\n  %s\nwant\n  %s", g, w)
			}
		})
	}
}

func TestWrappedEdgeShapeInlinedMirrorsRef(t *testing.T) {
	inner := newObject([]Prop{{Name: "x", Schema: newScalar("string"), Required: true}})
	cases := []struct {
		name string
		edge include.Edge
	}{
		{"to-one", include.Edge{Target: func() include.Resource { return nil }, Envelope: dataEnv}},
		{"bare", include.Edge{Many: true, Backref: "p", Bare: true, Envelope: dataEnv}},
		{"enveloped", include.Edge{Many: true, Backref: "p", Envelope: dataEnv}},
		{"in-array", include.Edge{Many: true, ArrayPath: "g", SubField: "s", Envelope: dataEnv}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			withRef := edgeShape(tc.edge, newRef("T"))
			withInner := edgeShape(tc.edge, inner)
			refJSON, _ := withRef.MarshalJSON()
			innerJSON, _ := withInner.MarshalJSON()
			innerBody, _ := inner.MarshalJSON()
			swapped := strings.ReplaceAll(string(refJSON), `{"$ref":"`+RefPrefix+`T"}`, string(innerBody))
			if swapped != string(innerJSON) {
				t.Fatalf("inlined shape drifted:\n ref    = %s\n inline = %s\n want   = %s", refJSON, innerJSON, swapped)
			}
		})
	}
}

// The plain shapes are untouched by the envelope branch.
func TestPlainEdgeShapeUnchangedByEnvelopeField(t *testing.T) {
	e := include.Edge{Many: true, Backref: "p"}
	got := irMap(t, edgeShape(e, newRef("T")))
	want := anyOfNull(obj(map[string]any{
		"items":      arr(ref("T")),
		"hasMore":    map[string]any{"type": "boolean"},
		"nextCursor": map[string]any{"type": "string"},
	}, "items", "hasMore"))
	if !reflect.DeepEqual(got, want) {
		t.Errorf("plain reverse shape changed: %v", got)
	}
}

// SchemaFor over a graph whose included edge carries an Envelope AND
// a sub-include, so the target is INLINED rather than $ref-ed. The wrapper must
// survive inlining at the edge site, and the inlined target's own enveloped
// edge must be wrapped in turn.
func TestSchemaForWrapsInlinedEdge(t *testing.T) {
	g := buildToyGraph()
	// Declare the envelope on both the outer to-one and the nested to-many.
	author := g.Book.edges["author"]
	author.Envelope = dataEnv
	g.Book.edges["author"] = author
	books := g.Author.edges["books"]
	books.Envelope = dataEnv
	g.Author.edges["books"] = books

	s, err := SchemaFor(stubReflector{}, g.Book, include.IncludeTree{
		"author": include.IncludeTree{"books": nil},
	}, include.Limits{})
	if err != nil {
		t.Fatalf("SchemaFor: %v", err)
	}
	got := s.Map()

	// The edge site is the wrapper object itself — no outer null arm.
	site := prop(t, got, "author")
	if _, isAnyOf := site["anyOf"]; isAnyOf {
		t.Fatalf("wrapped edge site must not carry an outer null arm: %v", site)
	}
	if site["type"] != "object" {
		t.Fatalf("wrapped edge site type = %v, want object", site["type"])
	}
	if req := requiredKeys(t, site); !reflect.DeepEqual(req, []string{"data"}) {
		t.Fatalf("wrapped edge site required = %v, want [data]", req)
	}

	// Inside "data" sits the nullable (non-required to-one) inlined target.
	data := prop(t, site, "data")
	arms, ok := data["anyOf"].([]any)
	if !ok || len(arms) != 2 {
		t.Fatalf("data fragment = %v, want anyOf[inlined, null]", data)
	}
	if want := map[string]any{"type": "null"}; !reflect.DeepEqual(arms[1], want) {
		t.Fatalf("second arm = %v, want %v", arms[1], want)
	}
	inner, ok := arms[0].(map[string]any)
	if !ok {
		t.Fatalf("inlined arm = %T", arms[0])
	}
	if _, isRef := inner["$ref"]; isRef {
		t.Fatalf("non-empty subtree must inline the target, got a $ref: %v", inner)
	}
	if req := requiredKeys(t, inner); !contains(req, "books") {
		t.Errorf("inlined Author required = %v, want it to carry books", req)
	}

	// The inlined target's own enveloped to-many is wrapped per ITS Envelope.
	wantBooks := obj(map[string]any{
		"data":       arr(wrapped("data", ref("Book"))),
		"pagination": paginationFrag(),
	}, "data", "pagination")
	if got := prop(t, inner, "books"); !reflect.DeepEqual(got, wantBooks) {
		t.Errorf("inlined Author.books = %v, want %v", got, wantBooks)
	}
}
