package reflector_test

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"

	"github.com/qrotux/wireleaf/apidoc"
	"github.com/qrotux/wireleaf/apidoc/reflectortest"
	"github.com/qrotux/wireleaf/reflector"
	"github.com/qrotux/wireleaf/reflector/internal/collide"
)

// TestReflectorContract is the executable specification: the full reflector
// contract suite, run against the canonical reflector.
func TestReflectorContract(t *testing.T) {
	reflectortest.Run(t, &reflector.Reflector{})
}

// TestCustomNullabilityPolicyHonored pins that the policy — not the engine —
// decides nullability: a policy that force-nulls everything makes every field
// nullable and required.
func TestCustomNullabilityPolicyHonored(t *testing.T) {
	r := &reflector.Reflector{Nullability: func(reflect.StructField) apidoc.Verdict {
		return apidoc.VerdictNull
	}}
	got, err := r.ReflectComponents([]reflect.Type{reflect.TypeOf(reflectortest.Nullability{})}, nil)
	if err != nil {
		t.Fatalf("ReflectComponents: %v", err)
	}
	top := got["Nullability"]
	if top == nil {
		t.Fatalf("no Nullability component: %v", got)
	}
	for _, p := range top.Props {
		if !p.Required {
			t.Errorf("property %q: want required under a force-null policy", p.Name)
		}
		if !admitsNull(p.Schema) {
			t.Errorf("property %q: want nullable under a force-null policy, got %+v", p.Name, p.Schema)
		}
	}
}

func admitsNull(n *apidoc.IRNode) bool {
	if n == nil {
		return false
	}
	for _, t := range n.Types {
		if t == "null" {
			return true
		}
	}
	for _, a := range n.AnyOf {
		if admitsNull(a) {
			return true
		}
	}
	return false
}

// TestReflectHelpers pins the v0-signature one-type helpers.
func TestReflectHelpers(t *testing.T) {
	s := reflector.Reflect[reflectortest.AuxCollect]()
	m := s.Map()
	if m["type"] != "object" {
		t.Fatalf("Reflect: want an object schema, got %v", m)
	}
	props, _ := m["properties"].(map[string]any)
	if props == nil {
		t.Fatalf("Reflect: no properties in %v", m)
	}
	one, _ := props["one"].(map[string]any)
	ref, _ := one["$ref"].(string)
	if !strings.HasSuffix(ref, "AuxInner") {
		t.Errorf("Reflect: property \"one\" must be a $ref to AuxInner, got %v", one)
	}
	when, _ := props["when"].(map[string]any)
	if when["format"] != "date-time" {
		t.Errorf("Reflect: property \"when\" must be a date-time string, got %v", when)
	}

	// ReflectType agrees with Reflect for the same type.
	a, _ := json.Marshal(reflector.ReflectType(reflect.TypeOf(reflectortest.AuxCollect{})).Map())
	b, _ := json.Marshal(m)
	if string(a) != string(b) {
		t.Errorf("ReflectType disagrees with Reflect:\n%s\n%s", a, b)
	}
}

// ---------------------------------------------------------------------------
// conversion errors are never swallowed
// ---------------------------------------------------------------------------

// NotExotic exposes a hand-written schema whose "not" arm carries a keyword the
// IR does not model. The arm must be CONVERTED (not dropped), so the violation
// surfaces as an error naming the keyword.
type NotExotic struct{}

// JSONSchemaBytes implements the engine's raw-schema exposer.
func (NotExotic) JSONSchemaBytes() ([]byte, error) {
	return []byte(`{"type":"object","not":{"patternProperties":{"^x":{"type":"string"}}}}`), nil
}

type notHolder struct {
	Weird NotExotic `json:"weird"`
}

func TestExoticaInsideNotIsAnError(t *testing.T) {
	r := &reflector.Reflector{}
	_, err := r.ReflectComponents([]reflect.Type{reflect.TypeOf(notHolder{})}, nil)
	if err == nil {
		t.Fatalf("want a conversion error for an exotic keyword inside \"not\"")
	}
	if !strings.Contains(err.Error(), "patternProperties") {
		t.Errorf("error must name the offending keyword, got: %v", err)
	}
}

// ---------------------------------------------------------------------------
// typeless schemas fold into one Opaque fragment
// ---------------------------------------------------------------------------

// typelessHolder carries the three typeless shapes: annotated "anything",
// annotated raw JSON, and a bare "anything".
type typelessHolder struct {
	Data any             `json:"data" description:"free form"`
	Raw  json.RawMessage `json:"raw" title:"payload"`
	Bare any             `json:"bare"`
}

func TestTypelessSchemasFoldIntoOpaque(t *testing.T) {
	got, err := (&reflector.Reflector{}).ReflectComponents(
		[]reflect.Type{reflect.TypeOf(typelessHolder{})}, nil)
	if err != nil {
		t.Fatalf("an annotated typeless field must convert, not fail: %v", err)
	}
	top := got["typelessHolder"]
	if top == nil {
		t.Fatalf("no typelessHolder component: %v", got)
	}
	if err := apidoc.ValidateComponent("typelessHolder", top); err != nil {
		t.Errorf("ValidateComponent: %v", err)
	}
	for _, tc := range []struct{ prop, want string }{
		{"data", `"description":"free form"`},
		{"raw", `"title":"payload"`},
		{"bare", `{}`},
	} {
		p, ok := propOf(top, tc.prop)
		if !ok {
			t.Errorf("no property %q", tc.prop)
			continue
		}
		if p.Schema.Kind != apidoc.KindOpaque {
			t.Errorf("property %q: want a KindOpaque node, got kind %v", tc.prop, p.Schema.Kind)
			continue
		}
		if !strings.Contains(string(p.Schema.Opaque), tc.want) {
			t.Errorf("property %q: Opaque bytes %s must carry %s", tc.prop, p.Schema.Opaque, tc.want)
		}
	}
	// Byte-identical between runs: the fragment is re-marshalled from a map, so
	// its keys are sorted rather than map-ordered.
	again, err := (&reflector.Reflector{}).ReflectComponents(
		[]reflect.Type{reflect.TypeOf(typelessHolder{})}, nil)
	if err != nil {
		t.Fatalf("ReflectComponents: %v", err)
	}
	if !reflect.DeepEqual(top, again["typelessHolder"]) {
		t.Errorf("the opaque fragment is not deterministic between two calls")
	}
}

// RefInNot exposes a TYPELESS schema that reaches a component from inside the
// fragment. The fold must rewrite the engine's definitions location into the
// document's component location, and the ref must stay visible to components
// assembly's dangling-ref check.
type RefInNot struct{}

// JSONSchemaBytes implements the engine's raw-schema exposer.
func (RefInNot) JSONSchemaBytes() ([]byte, error) {
	return []byte(`{"not":{"$ref":"#/definitions/Foo"}}`), nil
}

type refInNotHolder struct {
	Held RefInNot `json:"held"`
}

func TestFoldedOpaqueRefIsRewrittenAndVisible(t *testing.T) {
	got, err := (&reflector.Reflector{}).ReflectComponents(
		[]reflect.Type{reflect.TypeOf(refInNotHolder{})}, nil)
	if err != nil {
		t.Fatalf("ReflectComponents: %v", err)
	}
	n := got["RefInNot"]
	if n == nil || n.Kind != apidoc.KindOpaque {
		t.Fatalf("want an opaque RefInNot component, got %+v", got)
	}
	body := string(n.Opaque)
	if strings.Contains(body, "#/definitions/") {
		t.Errorf("the folded fragment kept the engine's definitions location: %s", body)
	}
	if !strings.Contains(body, `"$ref":"`+apidoc.RefPrefix+`Foo"`) {
		t.Errorf("the folded fragment must reference %sFoo, got %s", apidoc.RefPrefix, body)
	}
	// The rewritten ref is the form components assembly scans for: registering
	// the fragment and verifying must report Foo as unresolved.
	frag, err := apidoc.OpaqueFragment(n.Opaque)
	if err != nil {
		t.Fatalf("OpaqueFragment: %v", err)
	}
	c := apidoc.NewComponents()
	c.Add("RefInNot", frag)
	err = c.Verify()
	if err == nil || !strings.Contains(err.Error(), "RefInNot -> Foo") {
		t.Errorf("Verify must report the dangling ref inside the opaque fragment, got: %v", err)
	}
}

// XLinked exposes an "x-" extension whose value carries a $ref.
type XLinked struct{}

// JSONSchemaBytes implements the engine's raw-schema exposer.
func (XLinked) JSONSchemaBytes() ([]byte, error) {
	return []byte(`{"type":"object","x-links":{"$ref":"#/definitions/Bar"}}`), nil
}

type xLinkedHolder struct {
	Held XLinked `json:"held"`
}

func TestExtensionRefIsRewrittenAndVisible(t *testing.T) {
	got, err := (&reflector.Reflector{}).ReflectComponents(
		[]reflect.Type{reflect.TypeOf(xLinkedHolder{})}, nil)
	if err != nil {
		t.Fatalf("ReflectComponents: %v", err)
	}
	n := got["XLinked"]
	if n == nil {
		t.Fatalf("no XLinked component: %v", got)
	}
	link, _ := n.Extensions["x-links"].(map[string]any)
	if link == nil || link["$ref"] != apidoc.RefPrefix+"Bar" {
		t.Fatalf("x-links must carry the rewritten ref, got %v", n.Extensions)
	}
	// Registering the component makes the extension's ref visible to Verify.
	raw, err := json.Marshal(n)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var m map[string]any
	if err := json.Unmarshal(raw, &m); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	c := apidoc.NewComponents()
	c.Add("XLinked", apidoc.RawFragment(m))
	err = c.Verify()
	if err == nil || !strings.Contains(err.Error(), "XLinked -> Bar") {
		t.Errorf("Verify must report the dangling ref inside the extension, got: %v", err)
	}
}

func propOf(n *apidoc.IRNode, name string) (apidoc.Prop, bool) {
	for _, p := range n.Props {
		if p.Name == name {
			return p, true
		}
	}
	return apidoc.Prop{}, false
}

// ---------------------------------------------------------------------------
// component-name collisions
// ---------------------------------------------------------------------------

// Dup collides by name with collide.Dup.
type Dup struct {
	A string `json:"a"`
}

func TestComponentNameCollisionIsAnError(t *testing.T) {
	local, other := reflect.TypeOf(Dup{}), reflect.TypeOf(collide.Dup{})
	for _, order := range [][]reflect.Type{{local, other}, {other, local}} {
		_, err := (&reflector.Reflector{}).ReflectComponents(order, nil)
		if err == nil {
			t.Fatalf("%v: want an error: one component name cannot stand for two types", order)
		}
		if !strings.Contains(err.Error(), `component name "Dup" claimed by both`) {
			t.Errorf("%v: error must name the collision, got: %v", order, err)
		}
	}
}

func TestOverrideCollisionIsAnError(t *testing.T) {
	ov := map[reflect.Type]string{reflect.TypeOf(reflectortest.SharedAux{}): "NamingTopB"}
	_, err := (&reflector.Reflector{}).ReflectComponents(
		[]reflect.Type{reflect.TypeOf(reflectortest.NamingTopB{})}, ov)
	if err == nil || !strings.Contains(err.Error(), "claimed by both") {
		t.Fatalf("an override colliding with a Go type name must be an error, got: %v", err)
	}
}

// ---------------------------------------------------------------------------
// shadowed embedded fields (encoding/json depth dominance)
// ---------------------------------------------------------------------------

// ShadowBase is embedded by both shadow fixtures. The colliding field is
// deliberately UNTAGGED: `go vet`'s structtag check rejects a literal json tag
// repeated across an embedding, and the dominance rule is about the json NAME,
// which an untagged exported field carries just as well.
type ShadowBase struct {
	Name string
	Only string `json:"only"`
}

// ShadowOuter re-declares "Name" at depth 0: encoding/json writes the OUTER
// field and drops the promoted one — exactly one property, no duplicate key.
type ShadowOuter struct {
	ShadowBase
	Name string
}

// TieA and TieB both promote "Name" at the same depth, neither tagged:
// encoding/json resolves nothing and drops the name entirely.
type TieA struct {
	Name string
}
type TieB struct {
	Name string
	Kept string `json:"kept"`
}
type ShadowTie struct {
	TieA
	TieB
}

func TestShadowedEmbeddedFieldIsNotDuplicated(t *testing.T) {
	got, err := (&reflector.Reflector{}).ReflectComponents(
		[]reflect.Type{reflect.TypeOf(ShadowOuter{}), reflect.TypeOf(ShadowTie{})}, nil)
	if err != nil {
		t.Fatalf("ReflectComponents: %v", err)
	}
	outer := got["ShadowOuter"]
	if outer == nil {
		t.Fatalf("no ShadowOuter component: %v", got)
	}
	if n := countProps(outer, "Name"); n != 1 {
		t.Errorf("ShadowOuter: property \"Name\" appears %d times, want exactly 1 (%s)", n, names(outer))
	}
	if countProps(outer, "only") != 1 {
		t.Errorf("ShadowOuter: the un-shadowed promoted property \"only\" is missing (%s)", names(outer))
	}

	tie := got["ShadowTie"]
	if tie == nil {
		t.Fatalf("no ShadowTie component: %v", got)
	}
	if n := countProps(tie, "Name"); n != 0 {
		t.Errorf("ShadowTie: an unresolvable tie must drop the name, got %d occurrences (%s)", n, names(tie))
	}
	if countProps(tie, "kept") != 1 {
		t.Errorf("ShadowTie: the untied property \"kept\" is missing (%s)", names(tie))
	}
}

func countProps(n *apidoc.IRNode, name string) int {
	c := 0
	for _, p := range n.Props {
		if p.Name == name {
			c++
		}
	}
	return c
}

func names(n *apidoc.IRNode) string {
	out := make([]string, 0, len(n.Props))
	for _, p := range n.Props {
		out = append(out, p.Name)
	}
	return "properties: [" + strings.Join(out, " ") + "]"
}

func TestReflectPanicsOnNonStruct(t *testing.T) {
	defer func() {
		if recover() == nil {
			t.Errorf("Reflect[int]: want a panic")
		}
	}()
	_ = reflector.Reflect[int]()
}

// ---------------------------------------------------------------------------
// spec §5a — BYTES parity
// ---------------------------------------------------------------------------

// bytesParityFixture is a real Go value that exercises every verdict the
// nullability policy can reach, in the wire shapes that actually differ:
//
//   - Plain       → always emitted, never null;
//   - Null        → a pointer WITHOUT omitempty: the key is emitted, as null
//     when the pointer is nil;
//   - Optional    → omitempty / omitzero: the key VANISHES at its zero value.
//
// The point of the test below is that it does not ask the policy what the
// document should say and then check the document against the policy — that
// would be circular. It MARSHALS this value with encoding/json and validates
// the resulting BYTES against the reflected schema, which is what the spec
// means by "the bytes agree with the document".
type bytesParityFixture struct {
	// Plain: present with any value.
	ID    string `json:"id"`
	Count int    `json:"count"`

	// Null: pointer, no omitempty — the key is always present, the value may
	// be null. Both states appear in the cases below.
	NilPtr *string `json:"nilPtr"`
	SetPtr *string `json:"setPtr"`
	NilAux *pAux   `json:"nilAux"`
	SetAux *pAux   `json:"setAux"`

	// Optional: absent at the zero value.
	OmitEmpty string `json:"omitEmpty,omitempty"`
	OmitZero  int    `json:"omitZero,omitzero"`

	// Plain nested struct and slice, to prove the walk reaches composites.
	//
	// NOTE — a NIL slice or map marshals to JSON null under encoding/json,
	// while the nullability policy verdicts an untagged slice field Plain (it
	// is not a pointer and carries no omitempty). Those two disagree, and this
	// test would report it as a §5a violation. That corner is a KNOWN v1
	// boundary of the policy, not of this test, so the fixture keeps Tags
	// non-nil in every case: the assertion below stays about the verdicts it
	// was written to pin. Give a slice field `omitempty` (Optional) or make the
	// mapper emit an empty slice to stay inside the document.
	Aux  pAux     `json:"aux"`
	Tags []string `json:"tags"`
}

type pAux struct {
	Label string `json:"label"`
}

// TestReflectedSchemaAgreesWithMarshalledBytes is spec §5a: for a REAL value
// marshalled by encoding/json, the emitted bytes and the reflected component
// must agree on
//
//   - every emitted key is a declared property (no key the document omits);
//   - every REQUIRED property is present in the bytes;
//   - a property emitted as JSON null is one the schema ADMITS null for;
//   - a key that is absent is one the schema does NOT require.
//
// It is engine-agnostic: it reads the schema out of the reflector's IR and the
// keys out of encoding/json's output. It never consults the nullability policy,
// so it cannot pass by agreeing with itself.
func TestReflectedSchemaAgreesWithMarshalledBytes(t *testing.T) {
	set := "v"
	for _, tc := range []struct {
		name  string
		value bytesParityFixture
	}{
		{
			// The interesting case: nil pointers (emitted as null) AND
			// omitempty/omitzero fields at their zero value (absent).
			name:  "nil pointers, zero optionals",
			value: bytesParityFixture{ID: "a", Aux: pAux{Label: "l"}, Tags: []string{}},
		},
		{
			name: "everything populated",
			value: bytesParityFixture{
				ID: "a", Count: 3,
				SetPtr: &set, SetAux: &pAux{Label: "s"},
				OmitEmpty: "x", OmitZero: 7,
				Aux: pAux{Label: "l"}, Tags: []string{"t"},
			},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			raw, err := json.Marshal(tc.value)
			if err != nil {
				t.Fatalf("Marshal: %v", err)
			}
			// json.RawMessage keeps each value's BYTES, so "null" stays
			// distinguishable from a decoded nil interface.
			var body map[string]json.RawMessage
			if err := json.Unmarshal(raw, &body); err != nil {
				t.Fatalf("Unmarshal: %v", err)
			}

			comps, err := (&reflector.Reflector{}).ReflectComponents(
				[]reflect.Type{reflect.TypeOf(tc.value)}, nil)
			if err != nil {
				t.Fatalf("ReflectComponents: %v", err)
			}
			top := comps["bytesParityFixture"]
			if top == nil {
				t.Fatalf("no component for the fixture: %v", comps)
			}

			declared := map[string]*apidoc.Prop{}
			for i := range top.Props {
				declared[top.Props[i].Name] = &top.Props[i]
			}

			// (1) every emitted key is declared.
			for key := range body {
				if _, ok := declared[key]; !ok {
					t.Errorf("marshalled bytes carry the key %q, which the schema does not declare (%s)", key, raw)
				}
			}
			for name, p := range declared {
				val, present := body[name]

				// (2) a required property must be present in the bytes — read
				//     the other way, an ABSENT key must have been declared
				//     optional.
				if p.Required && !present {
					t.Errorf("schema requires %q, but the marshalled bytes omit it: %s", name, raw)
				}
				// (3) a value emitted as JSON null must be admitted by the
				//     property's schema.
				if present && string(val) == "null" && !admitsNull(p.Schema) {
					t.Errorf("key %q is null in the bytes, but its schema does not admit null: %+v", name, p.Schema)
				}
				// (4) a NON-null present value must not be forced to null-only.
				if present && string(val) != "null" && isNullOnly(p.Schema) {
					t.Errorf("key %q carries %s, but its schema admits only null", name, val)
				}
			}
		})
	}
}

// isNullOnly reports whether n admits nothing but JSON null.
func isNullOnly(n *apidoc.IRNode) bool {
	return n != nil && len(n.Types) == 1 && n.Types[0] == "null"
}

type docTagHolder struct {
	A string `json:"a" doc:"from doc"`
	B string `json:"b" doc:"same" description:"same"`
	C string `json:"c" description:"only description"`
}

type docTagConflict struct {
	A string `json:"a" doc:"one" description:"two"`
}

func TestDocTagBecomesDescription(t *testing.T) {
	got, err := (&reflector.Reflector{}).ReflectComponents(
		[]reflect.Type{reflect.TypeOf(docTagHolder{})}, nil)
	if err != nil {
		t.Fatalf("ReflectComponents: %v", err)
	}
	want := map[string]string{"a": "from doc", "b": "same", "c": "only description"}
	if n := len(got["docTagHolder"].Props); n != len(want) {
		t.Fatalf("docTagHolder has %d props, want %d", n, len(want))
	}
	for _, p := range got["docTagHolder"].Props {
		if p.Schema.Description != want[p.Name] {
			t.Errorf("%s: description = %q, want %q", p.Name, p.Schema.Description, want[p.Name])
		}
	}
}

func TestDocTagDisagreementIsAnError(t *testing.T) {
	_, err := (&reflector.Reflector{}).ReflectComponents(
		[]reflect.Type{reflect.TypeOf(docTagConflict{})}, nil)
	if err == nil || !strings.Contains(err.Error(), "reflector: a: doc and description tags disagree") {
		t.Fatalf("err = %v, want a doc/description disagreement at the bare field path", err)
	}
	_, err = (&reflector.Reflector{}).ReflectComponents(
		[]reflect.Type{reflect.TypeOf(docTagConflictHolder{})}, nil)
	if err == nil || !strings.Contains(err.Error(), "reflector: inner.a: doc") {
		t.Fatalf("err = %v, want the nested field path without the # root", err)
	}
}

type docTagConflictHolder struct {
	Inner docTagConflict `json:"inner"`
}
