package apidoc

import (
	"encoding/json"
	"testing"
)

func irJSON(t *testing.T, n *IRNode) string {
	t.Helper()
	// MarshalJSON directly: json.Marshal would re-escape <, > and & in the
	// result, which breaks byte-exactness of Opaque fragments.
	b, err := n.MarshalJSON()
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if !json.Valid(b) {
		t.Fatalf("output is not valid JSON: %s", b)
	}
	return string(b)
}

func TestIRSerializeBasics(t *testing.T) {
	f := func(v float64) *float64 { return &v }
	i := func(v int) *int { return &v }

	cases := []struct {
		name string
		node *IRNode
		want string
	}{
		{
			"scalar single type",
			newScalar("string"),
			`{"type":"string"}`,
		},
		{
			"nullable scalar type array",
			irNullable(newScalar("string")),
			`{"type":["string","null"]}`,
		},
		{
			"canonical nullable ref",
			irNullableRef("User"),
			`{"anyOf":[{"$ref":"#/components/schemas/User"},{"type":"null"}]}`,
		},
		{
			"plain ref",
			newRef("User"),
			`{"$ref":"#/components/schemas/User"}`,
		},
		{
			"ref with siblings",
			func() *IRNode {
				n := newRef("User")
				n.Description = "the user"
				n.Deprecated = true
				return n
			}(),
			`{"$ref":"#/components/schemas/User","description":"the user","deprecated":true}`,
		},
		{
			"enum with null",
			newEnum("string", true, "a", "b"),
			`{"type":["string","null"],"enum":["a","b",null]}`,
		},
		{
			"object ordered props and required",
			newObject([]Prop{
				{Name: "zeta", Schema: newScalar("string"), Required: true},
				{Name: "alpha", Schema: newScalar("integer")},
				{Name: "mid", Schema: newRef("User"), Required: true},
			}),
			`{"type":"object","properties":{"zeta":{"type":"string"},"alpha":{"type":"integer"},"mid":{"$ref":"#/components/schemas/User"}},"required":["zeta","mid"]}`,
		},
		{
			"object with no required",
			newObject([]Prop{{Name: "a", Schema: newScalar("string")}}),
			`{"type":"object","properties":{"a":{"type":"string"}}}`,
		},
		{
			"array",
			newArray(newRef("User")),
			`{"type":"array","items":{"$ref":"#/components/schemas/User"}}`,
		},
		{
			"nullable array",
			irNullable(newArray(newScalar("string"))),
			`{"type":["array","null"],"items":{"type":"string"}}`,
		},
		{
			"oneOf",
			newOneOf(newRef("A"), newRef("B")),
			`{"oneOf":[{"$ref":"#/components/schemas/A"},{"$ref":"#/components/schemas/B"}]}`,
		},
		{
			"allOf",
			newAllOf(newRef("A"), newRef("B")),
			`{"allOf":[{"$ref":"#/components/schemas/A"},{"$ref":"#/components/schemas/B"}]}`,
		},
		{
			"validations in field order",
			func() *IRNode {
				n := newScalar("integer")
				n.Maximum = f(10)
				n.Minimum = f(1)
				n.MultipleOf = f(2)
				n.ExclusiveMaximum = f(11)
				n.ExclusiveMinimum = f(0)
				return n
			}(),
			`{"type":"integer","minimum":1,"maximum":10,"exclusiveMinimum":0,"exclusiveMaximum":11,"multipleOf":2}`,
		},
		{
			"string validations",
			func() *IRNode {
				n := newScalar("string")
				n.MinLength = i(1)
				n.MaxLength = i(5)
				n.Pattern = "^a"
				n.Format = "email"
				n.ContentEncoding = "base64"
				n.ContentMediaType = "image/png"
				return n
			}(),
			`{"type":"string","minLength":1,"maxLength":5,"pattern":"^a","format":"email","contentEncoding":"base64","contentMediaType":"image/png"}`,
		},
		{
			"array and object validations",
			func() *IRNode {
				n := newArray(newScalar("string"))
				n.MinItems = i(1)
				n.MaxItems = i(3)
				n.UniqueItems = true
				return n
			}(),
			`{"type":"array","minItems":1,"maxItems":3,"uniqueItems":true,"items":{"type":"string"}}`,
		},
		{
			"object validations and dependentRequired sorted",
			func() *IRNode {
				n := newObject([]Prop{{Name: "a", Schema: newScalar("string")}})
				n.MinProperties = i(1)
				n.MaxProperties = i(4)
				n.DependentRequired = map[string][]string{"z": {"a"}, "a": {"z"}}
				n.AdditionalProperties = false
				return n
			}(),
			`{"type":"object","minProperties":1,"maxProperties":4,"dependentRequired":{"a":["z"],"z":["a"]},"properties":{"a":{"type":"string"}},"additionalProperties":false}`,
		},
		{
			"additionalProperties schema",
			func() *IRNode {
				n := newObject(nil)
				n.AdditionalProperties = newScalar("string")
				return n
			}(),
			// Zero Props → no "properties" key at all (Task 13 decision).
			`{"type":"object","additionalProperties":{"type":"string"}}`,
		},
		{
			"const and default and examples",
			func() *IRNode {
				n := newScalar("string")
				n.Const = "x"
				n.Default = "y"
				n.Examples = []any{"a", 1}
				return n
			}(),
			`{"type":"string","const":"x","default":"y","examples":["a",1]}`,
		},
		{
			"annotations and flags",
			func() *IRNode {
				n := newScalar("string")
				n.Description = "d"
				n.Title = "T"
				n.ReadOnly = true
				n.WriteOnly = true
				n.Deprecated = true
				return n
			}(),
			`{"type":"string","description":"d","title":"T","readOnly":true,"writeOnly":true,"deprecated":true}`,
		},
		{
			"extensions inlined sorted",
			func() *IRNode {
				n := newScalar("string")
				n.Extensions = map[string]any{"x-z": 1, "x-a": map[string]any{"k": "v"}}
				return n
			}(),
			`{"type":"string","x-a":{"k":"v"},"x-z":1}`,
		},
		{
			"not",
			func() *IRNode {
				n := newScalar("string")
				n.Not = newEnum("string", false, "bad")
				return n
			}(),
			`{"type":"string","not":{"type":"string","enum":["bad"]}}`,
		},
		{
			"nil node",
			nil,
			`null`,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := irJSON(t, tc.node); got != tc.want {
				t.Fatalf("\ngot:  %s\nwant: %s", got, tc.want)
			}
		})
	}
}

func TestIRSerializeOpaqueByteExact(t *testing.T) {
	raws := []string{
		`{"type":"string","description":"a & b <c> \"d\""}`,
		`{"title":"é中文","x":1}`,
		`{"b":1,"a":2}`,
		`[1,2,3]`,
		`"plain string"`,
	}
	for _, raw := range raws {
		n, err := newOpaque(json.RawMessage(raw))
		if err != nil {
			t.Fatalf("newOpaque(%s): %v", raw, err)
		}
		if got := irJSON(t, n); got != raw {
			t.Fatalf("\ngot:  %s\nwant: %s", got, raw)
		}
	}
}

func TestIRSerializeNestedOpaque(t *testing.T) {
	op, err := newOpaque(json.RawMessage(`{"x-raw":"a<b>&c"}`))
	if err != nil {
		t.Fatal(err)
	}
	n := newObject([]Prop{{Name: "p", Schema: op, Required: true}})
	want := `{"type":"object","properties":{"p":{"x-raw":"a<b>&c"}},"required":["p"]}`
	if got := irJSON(t, n); got != want {
		t.Fatalf("\ngot:  %s\nwant: %s", got, want)
	}
}

func TestIRSerializeRoundTrip(t *testing.T) {
	n := newObject([]Prop{
		{Name: "id", Schema: newScalar("string"), Required: true},
		{Name: "owner", Schema: irNullableRef("User")},
		{Name: "tags", Schema: newArray(newEnum("string", false, "a", "b"))},
	})
	b, err := json.Marshal(n)
	if err != nil {
		t.Fatal(err)
	}
	var back map[string]any
	if err := json.Unmarshal(b, &back); err != nil {
		t.Fatalf("output is not valid JSON: %v", err)
	}
	m := n.mapAny()
	if m == nil {
		t.Fatal("mapAny returned nil")
	}
	if m["type"] != "object" {
		t.Fatalf("mapAny type: %v", m["type"])
	}
	props, ok := m["properties"].(map[string]any)
	if !ok || len(props) != 3 {
		t.Fatalf("mapAny properties: %v", m["properties"])
	}
	req, ok := m["required"].([]any)
	if !ok || len(req) != 1 || req[0] != "id" {
		t.Fatalf("mapAny required: %v", m["required"])
	}
	if (*IRNode)(nil).mapAny() != nil {
		t.Fatal("nil mapAny should be nil")
	}
}
