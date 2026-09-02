package apidoc

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"
)

func TestIRInvariants(t *testing.T) {
	ptrInt := func(i int) *int { return &i }

	cases := []struct {
		name string
		node *IRNode
		want string // substring expected in the error; "" means no error
	}{
		// --- Object ---
		{"object ok", &IRNode{Kind: KindObject, Props: []Prop{{Name: "a", Schema: &IRNode{Kind: KindScalar, Types: []string{"string"}}}}}, ""},
		{"object empty props ok", &IRNode{Kind: KindObject, Props: []Prop{}}, ""},
		{"object nil props", &IRNode{Kind: KindObject}, "object"},
		{"object with ref", &IRNode{Kind: KindObject, Props: []Prop{}, Ref: "X"}, "object"},
		{"object with items", &IRNode{Kind: KindObject, Props: []Prop{}, Items: &IRNode{Kind: KindScalar, Types: []string{"string"}}}, "object"},
		{"object with anyOf", &IRNode{Kind: KindObject, Props: []Prop{}, AnyOf: []*IRNode{{Kind: KindScalar, Types: []string{"null"}}}}, "object"},
		{"object with enum", &IRNode{Kind: KindObject, Props: []Prop{}, Enum: []any{1}}, "object"},
		{"object prop without schema", &IRNode{Kind: KindObject, Props: []Prop{{Name: "a"}}}, "object"},
		{"object prop without name", &IRNode{Kind: KindObject, Props: []Prop{{Schema: &IRNode{Kind: KindScalar, Types: []string{"string"}}}}}, "object"},

		// --- Ref ---
		{"ref ok", &IRNode{Kind: KindRef, Ref: "User"}, ""},
		{"ref with annotations ok", &IRNode{Kind: KindRef, Ref: "User", Description: "d", MinLength: ptrInt(1), Extensions: map[string]any{"x-a": 1}}, ""},
		{"ref missing name", &IRNode{Kind: KindRef}, "ref"},
		{"ref with types", &IRNode{Kind: KindRef, Ref: "U", Types: []string{"object"}}, "ref"},
		{"ref with props", &IRNode{Kind: KindRef, Ref: "U", Props: []Prop{}}, "ref"},
		{"ref with items", &IRNode{Kind: KindRef, Ref: "U", Items: &IRNode{Kind: KindScalar, Types: []string{"string"}}}, "ref"},
		{"ref with oneOf", &IRNode{Kind: KindRef, Ref: "U", OneOf: []*IRNode{{Kind: KindScalar, Types: []string{"null"}}}}, "ref"},
		{"ref with enum", &IRNode{Kind: KindRef, Ref: "U", Enum: []any{1}}, "ref"},

		// --- Array ---
		{"array ok", &IRNode{Kind: KindArray, Items: &IRNode{Kind: KindScalar, Types: []string{"string"}}}, ""},
		{"array typed ok", &IRNode{Kind: KindArray, Types: []string{"array"}, Items: &IRNode{Kind: KindScalar, Types: []string{"string"}}}, ""},
		{"array nullable ok", &IRNode{Kind: KindArray, Types: []string{"array", "null"}, Items: &IRNode{Kind: KindScalar, Types: []string{"string"}}}, ""},
		{"array missing items", &IRNode{Kind: KindArray}, "array"},
		{"array bad types", &IRNode{Kind: KindArray, Types: []string{"string"}, Items: &IRNode{Kind: KindScalar, Types: []string{"string"}}}, "array"},
		{"array with ref", &IRNode{Kind: KindArray, Items: &IRNode{Kind: KindScalar, Types: []string{"string"}}, Ref: "U"}, "array"},
		{"array with props", &IRNode{Kind: KindArray, Items: &IRNode{Kind: KindScalar, Types: []string{"string"}}, Props: []Prop{}}, "array"},

		// --- Combinator ---
		{"combinator anyOf ok", &IRNode{Kind: KindCombinator, AnyOf: []*IRNode{{Kind: KindRef, Ref: "U"}}}, ""},
		{"combinator none", &IRNode{Kind: KindCombinator}, "combinator"},
		{"combinator two arms", &IRNode{Kind: KindCombinator, AnyOf: []*IRNode{{Kind: KindRef, Ref: "U"}}, OneOf: []*IRNode{{Kind: KindRef, Ref: "V"}}}, "combinator"},
		{"combinator with types", &IRNode{Kind: KindCombinator, AllOf: []*IRNode{{Kind: KindRef, Ref: "U"}}, Types: []string{"object"}}, "combinator"},
		{"combinator with ref", &IRNode{Kind: KindCombinator, AllOf: []*IRNode{{Kind: KindRef, Ref: "U"}}, Ref: "Z"}, "combinator"},
		{"combinator with props", &IRNode{Kind: KindCombinator, AllOf: []*IRNode{{Kind: KindRef, Ref: "U"}}, Props: []Prop{}}, "combinator"},
		{"combinator with items", &IRNode{Kind: KindCombinator, AllOf: []*IRNode{{Kind: KindRef, Ref: "U"}}, Items: &IRNode{Kind: KindScalar, Types: []string{"string"}}}, "combinator"},
		{"combinator with enum", &IRNode{Kind: KindCombinator, AllOf: []*IRNode{{Kind: KindRef, Ref: "U"}}, Enum: []any{1}}, "combinator"},
		{"combinator nil arm", &IRNode{Kind: KindCombinator, AnyOf: []*IRNode{nil}}, "combinator"},

		// --- Scalar ---
		{"scalar ok", &IRNode{Kind: KindScalar, Types: []string{"string"}}, ""},
		{"scalar nullable ok", &IRNode{Kind: KindScalar, Types: []string{"string", "null"}}, ""},
		{"scalar missing types", &IRNode{Kind: KindScalar}, "scalar"},
		{"scalar with ref", &IRNode{Kind: KindScalar, Types: []string{"string"}, Ref: "U"}, "scalar"},
		{"scalar with enum", &IRNode{Kind: KindScalar, Types: []string{"string"}, Enum: []any{"a"}}, "scalar"},
		{"scalar with props", &IRNode{Kind: KindScalar, Types: []string{"string"}, Props: []Prop{}}, "scalar"},

		// --- Enum ---
		{"enum ok", &IRNode{Kind: KindEnum, Types: []string{"string"}, Enum: []any{"a", "b"}}, ""},
		{"enum missing values", &IRNode{Kind: KindEnum, Types: []string{"string"}}, "enum"},
		{"enum missing types", &IRNode{Kind: KindEnum, Enum: []any{"a"}}, "enum"},
		{"enum with ref", &IRNode{Kind: KindEnum, Types: []string{"string"}, Enum: []any{"a"}, Ref: "U"}, "enum"},
		{"enum with items", &IRNode{Kind: KindEnum, Types: []string{"string"}, Enum: []any{"a"}, Items: &IRNode{Kind: KindScalar, Types: []string{"string"}}}, "enum"},

		// --- Opaque ---
		{"opaque ok", &IRNode{Kind: KindOpaque, Opaque: json.RawMessage(`{"a":1}`)}, ""},
		{"opaque missing raw", &IRNode{Kind: KindOpaque}, "opaque"},
		{"opaque with types", &IRNode{Kind: KindOpaque, Opaque: json.RawMessage(`{}`), Types: []string{"string"}}, "opaque"},
		{"opaque with description", &IRNode{Kind: KindOpaque, Opaque: json.RawMessage(`{}`), Description: "d"}, "opaque"},
		{"opaque with extensions", &IRNode{Kind: KindOpaque, Opaque: json.RawMessage(`{}`), Extensions: map[string]any{"x-a": 1}}, "opaque"},
		{"opaque with props", &IRNode{Kind: KindOpaque, Opaque: json.RawMessage(`{}`), Props: []Prop{}}, "opaque"},

		// --- Extensions boundary ---
		{"extensions vendor key ok", &IRNode{Kind: KindScalar, Types: []string{"string"}, Extensions: map[string]any{"x-vendor": 1}}, ""},
		{"extensions unknown key ok", &IRNode{Kind: KindScalar, Types: []string{"string"}, Extensions: map[string]any{"weirdKeyword": 1}}, ""},
		{"extensions standard keyword", &IRNode{Kind: KindScalar, Types: []string{"string"}, Extensions: map[string]any{"description": "d"}}, `standard keyword "description" must not ride in Extensions`},
		{"extensions standard keyword ref", &IRNode{Kind: KindObject, Props: []Prop{}, Extensions: map[string]any{"$ref": "x"}}, `standard keyword "$ref"`},
		{"extensions standard keyword on ref node", &IRNode{Kind: KindRef, Ref: "U", Extensions: map[string]any{"properties": map[string]any{}}}, `standard keyword "properties"`},

		// --- unknown kind ---
		{"zero kind", &IRNode{}, "kind"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := tc.node.checkInvariants()
			if tc.want == "" {
				if err != nil {
					t.Fatalf("unexpected error: %v", err)
				}
				return
			}
			if err == nil {
				t.Fatalf("expected error containing %q, got nil", tc.want)
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("error %q does not contain %q", err.Error(), tc.want)
			}
		})
	}
}

// deepCopyIR clones a node deeply, so a test can prove an operation left its
// argument untouched.
func deepCopyIR(n *IRNode) *IRNode {
	if n == nil {
		return nil
	}
	c := *n
	c.Types = append([]string(nil), n.Types...)
	c.Enum = append([]any(nil), n.Enum...)
	c.Examples = append([]any(nil), n.Examples...)
	c.Opaque = append(json.RawMessage(nil), n.Opaque...)
	c.opaqueRefs = append([]string(nil), n.opaqueRefs...)
	if n.Props != nil {
		c.Props = make([]Prop, len(n.Props))
		for i, p := range n.Props {
			p.Schema = deepCopyIR(p.Schema)
			c.Props[i] = p
		}
	}
	copyArms := func(arms []*IRNode) []*IRNode {
		if arms == nil {
			return nil
		}
		out := make([]*IRNode, len(arms))
		for i, a := range arms {
			out[i] = deepCopyIR(a)
		}
		return out
	}
	c.AnyOf, c.OneOf, c.AllOf = copyArms(n.AnyOf), copyArms(n.OneOf), copyArms(n.AllOf)
	c.Items = deepCopyIR(n.Items)
	c.Not = deepCopyIR(n.Not)
	if sub, ok := n.AdditionalProperties.(*IRNode); ok {
		c.AdditionalProperties = deepCopyIR(sub)
	}
	return &c
}

func TestIRConstructors(t *testing.T) {
	t.Run("newScalar", func(t *testing.T) {
		n := newScalar("string")
		if n.Kind != KindScalar || !reflect.DeepEqual(n.Types, []string{"string"}) {
			t.Fatalf("got %+v", n)
		}
		if err := n.checkInvariants(); err != nil {
			t.Fatal(err)
		}
	})

	t.Run("newObject preserves order", func(t *testing.T) {
		n := newObject([]Prop{
			{Name: "b", Schema: newScalar("string"), Required: true},
			{Name: "a", Schema: newScalar("integer")},
		})
		if n.Kind != KindObject || len(n.Props) != 2 || n.Props[0].Name != "b" || n.Props[1].Name != "a" {
			t.Fatalf("got %+v", n)
		}
	})

	t.Run("newRef", func(t *testing.T) {
		n := newRef("User")
		if n.Kind != KindRef || n.Ref != "User" {
			t.Fatalf("got %+v", n)
		}
	})

	t.Run("newArray", func(t *testing.T) {
		n := newArray(newScalar("string"))
		if n.Kind != KindArray || n.Items == nil {
			t.Fatalf("got %+v", n)
		}
		if err := n.checkInvariants(); err != nil {
			t.Fatal(err)
		}
	})

	t.Run("newEnum non-nullable", func(t *testing.T) {
		n := newEnum("string", false, "a", "b")
		if n.Kind != KindEnum || !reflect.DeepEqual(n.Types, []string{"string"}) {
			t.Fatalf("got %+v", n)
		}
		if !reflect.DeepEqual(n.Enum, []any{"a", "b"}) {
			t.Fatalf("enum: %+v", n.Enum)
		}
	})

	t.Run("newEnum nullable widens Types and Enum", func(t *testing.T) {
		n := newEnum("string", true, "a")
		if !reflect.DeepEqual(n.Types, []string{"string", "null"}) {
			t.Fatalf("types: %+v", n.Types)
		}
		if !reflect.DeepEqual(n.Enum, []any{"a", nil}) {
			t.Fatalf("enum: %+v", n.Enum)
		}
		if err := n.checkInvariants(); err != nil {
			t.Fatal(err)
		}
	})

	t.Run("newEnum nullable does not double the null member", func(t *testing.T) {
		n := newEnum("string", true, "a", nil)
		if !reflect.DeepEqual(n.Enum, []any{"a", nil}) {
			t.Fatalf("enum: %+v", n.Enum)
		}
	})

	t.Run("combinators", func(t *testing.T) {
		a, b := newRef("A"), newRef("B")
		if n := newAnyOf(a, b); n.Kind != KindCombinator || len(n.AnyOf) != 2 {
			t.Fatalf("anyOf: %+v", n)
		}
		if n := newOneOf(a, b); len(n.OneOf) != 2 || n.AnyOf != nil {
			t.Fatalf("oneOf: %+v", n)
		}
		if n := newAllOf(a, b); len(n.AllOf) != 2 || n.AnyOf != nil {
			t.Fatalf("allOf: %+v", n)
		}
	})

	t.Run("nullableRef canonical shape", func(t *testing.T) {
		n := irNullableRef("User")
		if n.Kind != KindCombinator || len(n.AnyOf) != 2 {
			t.Fatalf("got %+v", n)
		}
		if n.AnyOf[0].Kind != KindRef || n.AnyOf[0].Ref != "User" {
			t.Fatalf("arm0: %+v", n.AnyOf[0])
		}
		if n.AnyOf[1].Kind != KindScalar || !reflect.DeepEqual(n.AnyOf[1].Types, []string{"null"}) {
			t.Fatalf("arm1: %+v", n.AnyOf[1])
		}
		if err := n.checkInvariants(); err != nil {
			t.Fatal(err)
		}
	})

	t.Run("nullable scalar", func(t *testing.T) {
		n := irNullable(newScalar("string"))
		if !reflect.DeepEqual(n.Types, []string{"string", "null"}) {
			t.Fatalf("got %+v", n.Types)
		}
	})

	t.Run("nullable scalar idempotent", func(t *testing.T) {
		n := irNullable(irNullable(newScalar("string")))
		if !reflect.DeepEqual(n.Types, []string{"string", "null"}) {
			t.Fatalf("got %+v", n.Types)
		}
	})

	t.Run("nullable enum widens Types and Enum", func(t *testing.T) {
		n := irNullable(newEnum("string", false, "a"))
		if !reflect.DeepEqual(n.Types, []string{"string", "null"}) {
			t.Fatalf("types: %+v", n.Types)
		}
		if !reflect.DeepEqual(n.Enum, []any{"a", nil}) {
			t.Fatalf("enum: %+v", n.Enum)
		}
	})

	t.Run("nullable enum idempotent", func(t *testing.T) {
		n := irNullable(irNullable(newEnum("string", false, "a")))
		if !reflect.DeepEqual(n.Types, []string{"string", "null"}) {
			t.Fatalf("types: %+v", n.Types)
		}
		if !reflect.DeepEqual(n.Enum, []any{"a", nil}) {
			t.Fatalf("enum: %+v", n.Enum)
		}
	})

	t.Run("nullable enum already nullable in Types gains the null member", func(t *testing.T) {
		in := &IRNode{Kind: KindEnum, Types: []string{"string", "null"}, Enum: []any{"a"}}
		out := irNullable(in)
		if !reflect.DeepEqual(out.Enum, []any{"a", nil}) {
			t.Fatalf("enum: %+v", out.Enum)
		}
		if !reflect.DeepEqual(in.Enum, []any{"a"}) {
			t.Fatalf("argument mutated: %+v", in.Enum)
		}
	})

	t.Run("nullable ref wraps in anyOf", func(t *testing.T) {
		n := irNullable(newRef("User"))
		if n.Kind != KindCombinator || len(n.AnyOf) != 2 || n.AnyOf[0].Ref != "User" {
			t.Fatalf("got %+v", n)
		}
	})

	t.Run("nullable combinator with null arm unchanged", func(t *testing.T) {
		in := irNullableRef("User")
		out := irNullable(in)
		if out != in {
			t.Fatalf("expected identical node back, got %+v", out)
		}
		if len(out.AnyOf) != 2 {
			t.Fatalf("arms changed: %+v", out.AnyOf)
		}
	})

	t.Run("nullable combinator without null arm gains one", func(t *testing.T) {
		in := newAnyOf(newRef("A"), newRef("B"))
		out := irNullable(in)
		if len(out.AnyOf) != 3 || out.AnyOf[2].Kind != KindScalar {
			t.Fatalf("got %+v", out)
		}
	})

	t.Run("nullable oneOf gains a null arm", func(t *testing.T) {
		in := newOneOf(newRef("A"), newRef("B"))
		out := irNullable(in)
		if out.Kind != KindCombinator || len(out.OneOf) != 3 || !out.OneOf[2].isNullType() {
			t.Fatalf("got %+v", out)
		}
		if len(out.AnyOf) != 0 {
			t.Fatalf("anyOf leaked: %+v", out.AnyOf)
		}
		if len(in.OneOf) != 2 {
			t.Fatalf("argument mutated: %+v", in.OneOf)
		}
		if err := out.checkInvariants(); err != nil {
			t.Fatal(err)
		}
	})

	t.Run("nullable oneOf idempotent", func(t *testing.T) {
		in := newOneOf(newRef("A"), newRef("B"))
		once := irNullable(in)
		twice := irNullable(once)
		if twice != once || len(twice.OneOf) != 3 {
			t.Fatalf("got %+v", twice)
		}
	})

	t.Run("nullable allOf wraps instead of appending", func(t *testing.T) {
		in := newAllOf(newRef("A"), newRef("B"))
		out := irNullable(in)
		if out.Kind != KindCombinator || len(out.AnyOf) != 2 {
			t.Fatalf("got %+v", out)
		}
		if out.AnyOf[0] != in {
			t.Fatalf("first arm should be the original allOf, got %+v", out.AnyOf[0])
		}
		if !out.AnyOf[1].isNullType() {
			t.Fatalf("second arm should be null, got %+v", out.AnyOf[1])
		}
		if len(in.AllOf) != 2 {
			t.Fatalf("allOf gained an arm (unsatisfiable): %+v", in.AllOf)
		}
		if err := out.checkInvariants(); err != nil {
			t.Fatal(err)
		}
	})

	t.Run("nullable allOf idempotent", func(t *testing.T) {
		once := irNullable(newAllOf(newRef("A")))
		twice := irNullable(once)
		if twice != once || len(twice.AnyOf) != 2 {
			t.Fatalf("got %+v", twice)
		}
	})

	t.Run("irNullable does not mutate its argument", func(t *testing.T) {
		originals := []*IRNode{
			newScalar("string"),
			newEnum("string", false, "a", "b"),
			newArray(newScalar("string")),
			newObject([]Prop{{Name: "a", Schema: newScalar("string"), Required: true}}),
			newAnyOf(newRef("A"), newRef("B")),
			newOneOf(newRef("A")),
			newAllOf(newRef("A")),
			newRef("User"),
		}
		for _, in := range originals {
			before := deepCopyIR(in)
			out := irNullable(in)
			if !reflect.DeepEqual(in, before) {
				t.Fatalf("argument mutated: before %+v, after %+v", before, in)
			}
			if out == in && in.Kind != KindOpaque {
				// Only legal when the node was already nullable; none here are.
				t.Fatalf("expected a new node for %v", in.Kind)
			}
			if err := out.checkInvariants(); err != nil {
				t.Fatalf("%v: %v", in.Kind, err)
			}
		}
	})

	t.Run("irNullable does not share appended-to backing arrays", func(t *testing.T) {
		base := newScalar("string")
		base.Types = append(make([]string, 0, 4), "string") // spare capacity
		a := irNullable(base)
		b := irNullable(newScalar("integer"))
		_ = b
		if !reflect.DeepEqual(base.Types, []string{"string"}) {
			t.Fatalf("base mutated: %+v", base.Types)
		}
		if !reflect.DeepEqual(a.Types, []string{"string", "null"}) {
			t.Fatalf("result: %+v", a.Types)
		}
	})

	t.Run("nullable array", func(t *testing.T) {
		n := irNullable(newArray(newScalar("string")))
		if !reflect.DeepEqual(n.Types, []string{"array", "null"}) {
			t.Fatalf("got %+v", n.Types)
		}
		if err := n.checkInvariants(); err != nil {
			t.Fatal(err)
		}
	})

	t.Run("nullable nil", func(t *testing.T) {
		if irNullable(nil) != nil {
			t.Fatal("expected nil")
		}
	})
}

func TestIROpaque(t *testing.T) {
	t.Run("invalid json", func(t *testing.T) {
		if _, err := newOpaque(json.RawMessage(`{"a":`)); err == nil {
			t.Fatal("expected error")
		}
	})

	t.Run("empty", func(t *testing.T) {
		if _, err := newOpaque(nil); err == nil {
			t.Fatal("expected error")
		}
	})

	t.Run("collects nested refs", func(t *testing.T) {
		n, err := newOpaque(json.RawMessage(`{"a":{"$ref":"#/components/schemas/X"}}`))
		if err != nil {
			t.Fatal(err)
		}
		if !reflect.DeepEqual(n.opaqueRefs, []string{"X"}) {
			t.Fatalf("got %+v", n.opaqueRefs)
		}
		if err := n.checkInvariants(); err != nil {
			t.Fatal(err)
		}
	})

	t.Run("collects refs in arrays and dedups", func(t *testing.T) {
		raw := json.RawMessage(`{"anyOf":[{"$ref":"#/components/schemas/X"},{"items":{"$ref":"#/components/schemas/Y"}},{"$ref":"#/components/schemas/X"}]}`)
		n, err := newOpaque(raw)
		if err != nil {
			t.Fatal(err)
		}
		if !reflect.DeepEqual(n.opaqueRefs, []string{"X", "Y"}) {
			t.Fatalf("got %+v", n.opaqueRefs)
		}
	})

	t.Run("external ref kept verbatim", func(t *testing.T) {
		n, err := newOpaque(json.RawMessage(`{"$ref":"https://example.com/s.json"}`))
		if err != nil {
			t.Fatal(err)
		}
		if !reflect.DeepEqual(n.opaqueRefs, []string{"https://example.com/s.json"}) {
			t.Fatalf("got %+v", n.opaqueRefs)
		}
	})

	t.Run("no refs", func(t *testing.T) {
		n, err := newOpaque(json.RawMessage(`{"type":"string"}`))
		if err != nil {
			t.Fatal(err)
		}
		if len(n.opaqueRefs) != 0 {
			t.Fatalf("got %+v", n.opaqueRefs)
		}
	})

	t.Run("non-string $ref ignored", func(t *testing.T) {
		n, err := newOpaque(json.RawMessage(`{"$ref":123}`))
		if err != nil {
			t.Fatal(err)
		}
		if len(n.opaqueRefs) != 0 {
			t.Fatalf("got %+v", n.opaqueRefs)
		}
	})

	t.Run("collected refs strip the prefix", func(t *testing.T) {
		n, err := newOpaque(json.RawMessage(`{"$ref":"#/components/schemas/X"}`))
		if err != nil {
			t.Fatal(err)
		}
		if !reflect.DeepEqual(n.opaqueRefs, []string{"X"}) {
			t.Fatalf("got %+v", n.opaqueRefs)
		}
	})
}
