package huma

import (
	"encoding/json"
	"reflect"
	"testing"

	humav2 "github.com/danielgtaylor/huma/v2"

	"github.com/qrotux/wireleaf/apidoc"
)

func f64(v float64) *float64 { return &v }
func ip(v int) *int          { return &v }

// The per-field table IS the losslessness spec: one IR fixture per typed field,
// asserting the huma.Schema field it lands in.
func TestToHumaFieldByField(t *testing.T) {
	cases := []struct {
		name  string
		in    *apidoc.IRNode
		check func(*testing.T, *humav2.Schema)
	}{
		{"type scalar", &apidoc.IRNode{Kind: apidoc.KindScalar, Types: []string{"string"}},
			func(t *testing.T, s *humav2.Schema) {
				if s.Type != "string" || s.Nullable {
					t.Fatalf("type=%q nullable=%v", s.Type, s.Nullable)
				}
			}},
		{"type nullable scalar", &apidoc.IRNode{Kind: apidoc.KindScalar, Types: []string{"integer", "null"}},
			func(t *testing.T, s *humav2.Schema) {
				if s.Type != "integer" || !s.Nullable {
					t.Fatalf("type=%q nullable=%v", s.Type, s.Nullable)
				}
			}},
		{"type bare null", &apidoc.IRNode{Kind: apidoc.KindScalar, Types: []string{"null"}},
			func(t *testing.T, s *humav2.Schema) {
				// huma's validator has no "null" case, so the type alone would
				// accept everything; an enum of exactly [null] enforces it.
				if s.Type != "null" || s.Nullable {
					t.Fatalf("type=%q nullable=%v", s.Type, s.Nullable)
				}
				if !reflect.DeepEqual(s.Enum, []any{nil}) {
					t.Fatalf("enum=%#v", s.Enum)
				}
			}},
		{"type null enum untouched", &apidoc.IRNode{Kind: apidoc.KindEnum, Types: []string{"string", "null"},
			Enum: []any{"a", nil}},
			func(t *testing.T, s *humav2.Schema) {
				if !reflect.DeepEqual(s.Enum, []any{"a", nil}) {
					t.Fatalf("enum=%#v", s.Enum)
				}
			}},
		{"empty object keeps a properties map", &apidoc.IRNode{Kind: apidoc.KindObject, Types: []string{"object"}, Props: []apidoc.Prop{}},
			func(t *testing.T, s *humav2.Schema) {
				// huma's SchemaLinkTransformer writes "$schema" into this map.
				if s.Properties == nil {
					t.Fatalf("an object schema must carry a non-nil properties map")
				}
			}},
		{"ref", &apidoc.IRNode{Kind: apidoc.KindRef, Ref: "Thing"},
			func(t *testing.T, s *humav2.Schema) {
				if s.Ref != apidoc.RefPrefix+"Thing" {
					t.Fatalf("ref=%q", s.Ref)
				}
			}},
		{"props+required", &apidoc.IRNode{Kind: apidoc.KindObject, Types: []string{"object"}, Props: []apidoc.Prop{
			{Name: "id", Schema: &apidoc.IRNode{Kind: apidoc.KindScalar, Types: []string{"string"}}, Required: true},
			{Name: "note", Schema: &apidoc.IRNode{Kind: apidoc.KindScalar, Types: []string{"string"}}},
		}},
			func(t *testing.T, s *humav2.Schema) {
				if len(s.Properties) != 2 || s.Properties["id"].Type != "string" {
					t.Fatalf("properties=%#v", s.Properties)
				}
				if !reflect.DeepEqual(s.Required, []string{"id"}) {
					t.Fatalf("required=%v", s.Required)
				}
			}},
		{"items", &apidoc.IRNode{Kind: apidoc.KindArray, Types: []string{"array"},
			Items: &apidoc.IRNode{Kind: apidoc.KindScalar, Types: []string{"number"}}},
			func(t *testing.T, s *humav2.Schema) {
				if s.Items == nil || s.Items.Type != "number" {
					t.Fatalf("items=%#v", s.Items)
				}
			}},
		{"anyOf", &apidoc.IRNode{Kind: apidoc.KindCombinator, AnyOf: []*apidoc.IRNode{
			{Kind: apidoc.KindRef, Ref: "Thing"}, {Kind: apidoc.KindScalar, Types: []string{"null"}},
		}},
			func(t *testing.T, s *humav2.Schema) {
				if len(s.AnyOf) != 2 || s.AnyOf[0].Ref != apidoc.RefPrefix+"Thing" || s.AnyOf[1].Type != "null" {
					t.Fatalf("anyOf=%#v", s.AnyOf)
				}
			}},
		{"oneOf", &apidoc.IRNode{Kind: apidoc.KindCombinator, OneOf: []*apidoc.IRNode{
			{Kind: apidoc.KindScalar, Types: []string{"string"}},
		}},
			func(t *testing.T, s *humav2.Schema) {
				if len(s.OneOf) != 1 || s.OneOf[0].Type != "string" {
					t.Fatalf("oneOf=%#v", s.OneOf)
				}
			}},
		{"allOf", &apidoc.IRNode{Kind: apidoc.KindCombinator, AllOf: []*apidoc.IRNode{
			{Kind: apidoc.KindRef, Ref: "A"},
		}},
			func(t *testing.T, s *humav2.Schema) {
				if len(s.AllOf) != 1 || s.AllOf[0].Ref != apidoc.RefPrefix+"A" {
					t.Fatalf("allOf=%#v", s.AllOf)
				}
			}},
		{"not", &apidoc.IRNode{Kind: apidoc.KindScalar, Types: []string{"string"},
			Not: &apidoc.IRNode{Kind: apidoc.KindScalar, Types: []string{"null"}}},
			func(t *testing.T, s *humav2.Schema) {
				if s.Not == nil || s.Not.Type != "null" {
					t.Fatalf("not=%#v", s.Not)
				}
			}},
		{"additionalProperties bool", &apidoc.IRNode{Kind: apidoc.KindObject, Types: []string{"object"},
			Props: []apidoc.Prop{}, AdditionalProperties: false},
			func(t *testing.T, s *humav2.Schema) {
				if v, ok := s.AdditionalProperties.(bool); !ok || v {
					t.Fatalf("additionalProperties=%#v", s.AdditionalProperties)
				}
			}},
		{"additionalProperties schema", &apidoc.IRNode{Kind: apidoc.KindObject, Types: []string{"object"},
			Props: []apidoc.Prop{}, AdditionalProperties: &apidoc.IRNode{Kind: apidoc.KindScalar, Types: []string{"string"}}},
			func(t *testing.T, s *humav2.Schema) {
				sub, ok := s.AdditionalProperties.(*humav2.Schema)
				if !ok || sub.Type != "string" {
					t.Fatalf("additionalProperties=%#v", s.AdditionalProperties)
				}
			}},
		{"additionalProperties nil", &apidoc.IRNode{Kind: apidoc.KindObject, Types: []string{"object"}, Props: []apidoc.Prop{}},
			func(t *testing.T, s *humav2.Schema) {
				if s.AdditionalProperties != nil {
					t.Fatalf("additionalProperties=%#v", s.AdditionalProperties)
				}
			}},
		{"enum", &apidoc.IRNode{Kind: apidoc.KindEnum, Types: []string{"string"}, Enum: []any{"a", "b"}},
			func(t *testing.T, s *humav2.Schema) {
				if !reflect.DeepEqual(s.Enum, []any{"a", "b"}) {
					t.Fatalf("enum=%v", s.Enum)
				}
			}},
		{"const", &apidoc.IRNode{Kind: apidoc.KindScalar, Types: []string{"string"}, Const: "fixed"},
			func(t *testing.T, s *humav2.Schema) {
				if s.Const != "fixed" {
					t.Fatalf("const=%v", s.Const)
				}
			}},
		{"default", &apidoc.IRNode{Kind: apidoc.KindScalar, Types: []string{"string"}, Default: "d"},
			func(t *testing.T, s *humav2.Schema) {
				if s.Default != "d" {
					t.Fatalf("default=%v", s.Default)
				}
			}},
		{"examples", &apidoc.IRNode{Kind: apidoc.KindScalar, Types: []string{"string"}, Examples: []any{"x"}},
			func(t *testing.T, s *humav2.Schema) {
				if !reflect.DeepEqual(s.Examples, []any{"x"}) {
					t.Fatalf("examples=%v", s.Examples)
				}
			}},
		{"minimum", &apidoc.IRNode{Kind: apidoc.KindScalar, Types: []string{"number"}, Minimum: f64(1)},
			func(t *testing.T, s *humav2.Schema) {
				if s.Minimum == nil || *s.Minimum != 1 {
					t.Fatalf("minimum=%v", s.Minimum)
				}
			}},
		{"maximum", &apidoc.IRNode{Kind: apidoc.KindScalar, Types: []string{"number"}, Maximum: f64(2)},
			func(t *testing.T, s *humav2.Schema) {
				if s.Maximum == nil || *s.Maximum != 2 {
					t.Fatalf("maximum=%v", s.Maximum)
				}
			}},
		{"exclusiveMinimum", &apidoc.IRNode{Kind: apidoc.KindScalar, Types: []string{"number"}, ExclusiveMinimum: f64(3)},
			func(t *testing.T, s *humav2.Schema) {
				if s.ExclusiveMinimum == nil || *s.ExclusiveMinimum != 3 {
					t.Fatalf("exclusiveMinimum=%v", s.ExclusiveMinimum)
				}
			}},
		{"exclusiveMaximum", &apidoc.IRNode{Kind: apidoc.KindScalar, Types: []string{"number"}, ExclusiveMaximum: f64(4)},
			func(t *testing.T, s *humav2.Schema) {
				if s.ExclusiveMaximum == nil || *s.ExclusiveMaximum != 4 {
					t.Fatalf("exclusiveMaximum=%v", s.ExclusiveMaximum)
				}
			}},
		{"multipleOf", &apidoc.IRNode{Kind: apidoc.KindScalar, Types: []string{"number"}, MultipleOf: f64(5)},
			func(t *testing.T, s *humav2.Schema) {
				if s.MultipleOf == nil || *s.MultipleOf != 5 {
					t.Fatalf("multipleOf=%v", s.MultipleOf)
				}
			}},
		{"minLength", &apidoc.IRNode{Kind: apidoc.KindScalar, Types: []string{"string"}, MinLength: ip(1)},
			func(t *testing.T, s *humav2.Schema) {
				if s.MinLength == nil || *s.MinLength != 1 {
					t.Fatalf("minLength=%v", s.MinLength)
				}
			}},
		{"maxLength", &apidoc.IRNode{Kind: apidoc.KindScalar, Types: []string{"string"}, MaxLength: ip(2)},
			func(t *testing.T, s *humav2.Schema) {
				if s.MaxLength == nil || *s.MaxLength != 2 {
					t.Fatalf("maxLength=%v", s.MaxLength)
				}
			}},
		{"minItems", &apidoc.IRNode{Kind: apidoc.KindArray, Types: []string{"array"},
			Items: &apidoc.IRNode{Kind: apidoc.KindScalar, Types: []string{"string"}}, MinItems: ip(3)},
			func(t *testing.T, s *humav2.Schema) {
				if s.MinItems == nil || *s.MinItems != 3 {
					t.Fatalf("minItems=%v", s.MinItems)
				}
			}},
		{"maxItems", &apidoc.IRNode{Kind: apidoc.KindArray, Types: []string{"array"},
			Items: &apidoc.IRNode{Kind: apidoc.KindScalar, Types: []string{"string"}}, MaxItems: ip(4)},
			func(t *testing.T, s *humav2.Schema) {
				if s.MaxItems == nil || *s.MaxItems != 4 {
					t.Fatalf("maxItems=%v", s.MaxItems)
				}
			}},
		{"minProperties", &apidoc.IRNode{Kind: apidoc.KindObject, Types: []string{"object"}, Props: []apidoc.Prop{}, MinProperties: ip(5)},
			func(t *testing.T, s *humav2.Schema) {
				if s.MinProperties == nil || *s.MinProperties != 5 {
					t.Fatalf("minProperties=%v", s.MinProperties)
				}
			}},
		{"maxProperties", &apidoc.IRNode{Kind: apidoc.KindObject, Types: []string{"object"}, Props: []apidoc.Prop{}, MaxProperties: ip(6)},
			func(t *testing.T, s *humav2.Schema) {
				if s.MaxProperties == nil || *s.MaxProperties != 6 {
					t.Fatalf("maxProperties=%v", s.MaxProperties)
				}
			}},
		{"uniqueItems", &apidoc.IRNode{Kind: apidoc.KindArray, Types: []string{"array"},
			Items: &apidoc.IRNode{Kind: apidoc.KindScalar, Types: []string{"string"}}, UniqueItems: true},
			func(t *testing.T, s *humav2.Schema) {
				if !s.UniqueItems {
					t.Fatalf("uniqueItems lost")
				}
			}},
		{"pattern", &apidoc.IRNode{Kind: apidoc.KindScalar, Types: []string{"string"}, Pattern: "^a$"},
			func(t *testing.T, s *humav2.Schema) {
				if s.Pattern != "^a$" {
					t.Fatalf("pattern=%q", s.Pattern)
				}
			}},
		{"format", &apidoc.IRNode{Kind: apidoc.KindScalar, Types: []string{"string"}, Format: "date-time"},
			func(t *testing.T, s *humav2.Schema) {
				if s.Format != "date-time" {
					t.Fatalf("format=%q", s.Format)
				}
			}},
		{"title", &apidoc.IRNode{Kind: apidoc.KindScalar, Types: []string{"string"}, Title: "T"},
			func(t *testing.T, s *humav2.Schema) {
				if s.Title != "T" {
					t.Fatalf("title=%q", s.Title)
				}
			}},
		{"description", &apidoc.IRNode{Kind: apidoc.KindScalar, Types: []string{"string"}, Description: "D"},
			func(t *testing.T, s *humav2.Schema) {
				if s.Description != "D" {
					t.Fatalf("description=%q", s.Description)
				}
			}},
		{"contentEncoding", &apidoc.IRNode{Kind: apidoc.KindScalar, Types: []string{"string"}, ContentEncoding: "base64"},
			func(t *testing.T, s *humav2.Schema) {
				if s.ContentEncoding != "base64" {
					t.Fatalf("contentEncoding=%q", s.ContentEncoding)
				}
			}},
		{"contentMediaType", &apidoc.IRNode{Kind: apidoc.KindScalar, Types: []string{"string"}, ContentMediaType: "image/png"},
			func(t *testing.T, s *humav2.Schema) {
				// huma.Schema has no field: the inline extension key is the
				// correct OAS output.
				if s.Extensions["contentMediaType"] != "image/png" {
					t.Fatalf("extensions=%v", s.Extensions)
				}
			}},
		{"dependentRequired", &apidoc.IRNode{Kind: apidoc.KindObject, Types: []string{"object"}, Props: []apidoc.Prop{},
			DependentRequired: map[string][]string{"a": {"b"}}},
			func(t *testing.T, s *humav2.Schema) {
				if !reflect.DeepEqual(s.DependentRequired, map[string][]string{"a": {"b"}}) {
					t.Fatalf("dependentRequired=%v", s.DependentRequired)
				}
			}},
		{"readOnly", &apidoc.IRNode{Kind: apidoc.KindScalar, Types: []string{"string"}, ReadOnly: true},
			func(t *testing.T, s *humav2.Schema) {
				if !s.ReadOnly {
					t.Fatalf("readOnly lost")
				}
			}},
		{"writeOnly", &apidoc.IRNode{Kind: apidoc.KindScalar, Types: []string{"string"}, WriteOnly: true},
			func(t *testing.T, s *humav2.Schema) {
				if !s.WriteOnly {
					t.Fatalf("writeOnly lost")
				}
			}},
		{"deprecated", &apidoc.IRNode{Kind: apidoc.KindScalar, Types: []string{"string"}, Deprecated: true},
			func(t *testing.T, s *humav2.Schema) {
				if !s.Deprecated {
					t.Fatalf("deprecated lost")
				}
			}},
		{"extensions", &apidoc.IRNode{Kind: apidoc.KindScalar, Types: []string{"string"},
			Extensions: map[string]any{"x-thing": "v"}},
			func(t *testing.T, s *humav2.Schema) {
				if s.Extensions["x-thing"] != "v" {
					t.Fatalf("extensions=%v", s.Extensions)
				}
			}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := toHuma(tc.in)
			if err != nil {
				t.Fatal(err)
			}
			tc.check(t, got)
		})
	}
}

// A 3-member type array has no huma representation: Type is one string plus a
// Nullable flag. The converter refuses instead of dropping a member.
func TestToHumaThreeMemberTypeErrors(t *testing.T) {
	_, err := toHuma(&apidoc.IRNode{Kind: apidoc.KindScalar, Types: []string{"string", "integer", "null"}})
	if err == nil {
		t.Fatalf("3-member type array must error")
	}
	// A 2-member array whose second member is not "null" is equally
	// unrepresentable.
	if _, err := toHuma(&apidoc.IRNode{Kind: apidoc.KindScalar, Types: []string{"string", "integer"}}); err == nil {
		t.Fatalf("non-null 2-member type array must error")
	}
}

// The Extensions bridge: an Opaque node's bytes survive the conversion because
// a zero typed Schema carrying them as extensions marshals to exactly them.
func TestToHumaOpaqueByteFidelity(t *testing.T) {
	raw := json.RawMessage(`{"type":"object","patternProperties":{"^x":{"type":"string"}},"$ref":"#/components/schemas/Other"}`)
	s, err := apidoc.OpaqueFragment(raw)
	if err != nil {
		t.Fatal(err)
	}
	got, err := toHuma(s.IR())
	if err != nil {
		t.Fatal(err)
	}
	b, err := json.Marshal(got)
	if err != nil {
		t.Fatal(err)
	}
	var want, have map[string]any
	if err := json.Unmarshal(raw, &want); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(b, &have); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(want, have) {
		t.Fatalf("opaque fragment lost:\n got %s\nwant %s", b, raw)
	}
}

// The converter recurses: a nested opaque property keeps its keywords too.
func TestToHumaNestedRecursion(t *testing.T) {
	inner, err := apidoc.OpaqueFragment(json.RawMessage(`{"if":{"type":"string"}}`))
	if err != nil {
		t.Fatal(err)
	}
	n := &apidoc.IRNode{Kind: apidoc.KindObject, Types: []string{"object"}, Props: []apidoc.Prop{
		{Name: "weird", Schema: inner.IR(), Required: true},
	}}
	got, err := toHuma(n)
	if err != nil {
		t.Fatal(err)
	}
	b, err := json.Marshal(got.Properties["weird"])
	if err != nil {
		t.Fatal(err)
	}
	if string(b) != `{"if":{"type":"string"}}` {
		t.Fatalf("nested opaque = %s", b)
	}
}

// A conversion error from any depth surfaces instead of being swallowed.
func TestToHumaNestedErrorPropagates(t *testing.T) {
	n := &apidoc.IRNode{Kind: apidoc.KindObject, Types: []string{"object"}, Props: []apidoc.Prop{
		{Name: "bad", Schema: &apidoc.IRNode{Kind: apidoc.KindScalar, Types: []string{"a", "b", "c"}}},
	}}
	if _, err := toHuma(n); err == nil {
		t.Fatalf("nested error must propagate")
	}
}

// PrecomputeMessages runs on every converted schema: huma's validator reads the
// precomputed strings, and an unprepared schema reports empty messages.
func TestToHumaPrecomputesValidationMessages(t *testing.T) {
	n := &apidoc.IRNode{Kind: apidoc.KindObject, Types: []string{"object"}, Props: []apidoc.Prop{
		{Name: "id", Schema: &apidoc.IRNode{Kind: apidoc.KindScalar, Types: []string{"string"}}, Required: true},
	}}
	s, err := toHuma(n)
	if err != nil {
		t.Fatal(err)
	}
	reg := humav2.NewMapRegistry(apidoc.RefPrefix, humav2.DefaultSchemaNamer)
	pb := humav2.NewPathBuffer([]byte(""), 0)
	res := &humav2.ValidateResult{}
	humav2.Validate(reg, s, pb, humav2.ModeReadFromServer, map[string]any{}, res)
	if len(res.Errors) != 1 {
		t.Fatalf("expected one required error, got %v", res.Errors)
	}
	if msg := res.Errors[0].Error(); msg == "" {
		t.Fatalf("empty validation message: PrecomputeMessages was not called")
	}
}
