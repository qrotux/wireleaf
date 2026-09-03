package apidoc

import (
	"encoding/json"
	"maps"
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/qrotux/wireleaf/include"
)

func TestMergeEmptyRegistryIsNoop(t *testing.T) {
	dst := map[string]any{}
	var c Components
	if err := c.Merge(dst); err != nil {
		t.Fatal(err)
	}
	if n := len(dst); n != 0 {
		t.Fatalf("no-op expected, %d components", n)
	}
}

// Merge inserts PRE-ENCODED bytes, not a map: property order and Opaque
// byte-exactness would not survive a generic map.
func TestMergeInsertsEncodedFragment(t *testing.T) {
	dst := map[string]any{}
	var c Components
	c.Add("Thing", RawFragment(map[string]any{
		"type":       "object",
		"properties": map[string]any{"id": map[string]any{"type": "string"}},
		"required":   []any{"id"},
	}))
	if err := c.Merge(dst); err != nil {
		t.Fatal(err)
	}
	raw, ok := dst["Thing"].(json.RawMessage)
	if !ok {
		t.Fatalf("Thing merged as %T, want json.RawMessage", dst["Thing"])
	}
	want := `{"type":"object","properties":{"id":{"type":"string"}},"required":["id"]}`
	if string(raw) != want {
		t.Fatalf("merge altered the fragment:\n got %s\nwant %s", raw, want)
	}
}

// The bytes Merge inserts are HTML-UNESCAPED; a document emitter must therefore
// encode with SetEscapeHTML(false) or encoding/json will re-escape them.
func TestMergeKeepsOpaqueBytesVerbatim(t *testing.T) {
	dst := map[string]any{}
	var c Components
	c.Add("Raw", RawFragment(map[string]any{
		"type":        "string",
		"description": "a<b&c>d",
		"$comment":    "unmodelled keyword forces the Opaque path",
	}))
	if err := c.Merge(dst); err != nil {
		t.Fatal(err)
	}
	raw := string(dst["Raw"].(json.RawMessage))
	if !strings.Contains(raw, "a<b&c>d") {
		t.Fatalf("Merge escaped the fragment bytes: %s", raw)
	}
}

func TestMergeClashGuard(t *testing.T) {
	dst := map[string]any{"Taken": map[string]any{"type": "object"}}
	var c Components
	c.Add("Fresh", RawFragment(map[string]any{"type": "object", "properties": map[string]any{}}))
	c.Add("Taken", RawFragment(map[string]any{"type": "string"}))
	if err := c.Merge(dst); err == nil {
		t.Fatalf("clash must error")
	}
	// The clash check runs over every name BEFORE the first insert: a partial
	// merge would leave the destination half-written.
	if _, wrote := dst["Fresh"]; wrote {
		t.Fatalf("clash must abort before any insert")
	}
	if got := dst["Taken"].(map[string]any)["type"]; got != "object" {
		t.Fatalf("pre-existing schema clobbered: %v", got)
	}
}

func TestComponentsAddDupPanics(t *testing.T) {
	c := NewComponents()
	c.Add("X", RawFragment(map[string]any{"type": "object"}))
	assertPanics(t, func() { c.Add("X", RawFragment(map[string]any{"type": "object"})) })
}

// Registration is the gate for the RECURSIVE invariant walk: checkInvariants is
// shallow and the constructors do not call it, so a malformed nested node would
// otherwise only surface mid-serialization.
func TestAddRejectsMalformedNestedNode(t *testing.T) {
	bad := newObject([]Prop{{
		Name:   "inner",
		Schema: &IRNode{Kind: KindRef}, // Ref is required
	}})
	c := NewComponents()
	assertPanics(t, func() { c.Add("Bad", Schema{n: bad}) })
}

func TestAddRejectsBadAdditionalProperties(t *testing.T) {
	bad := newObject([]Prop{{Name: "a", Schema: newScalar("string")}})
	bad.AdditionalProperties = "yes" // nil | bool | *IRNode only
	c := NewComponents()
	assertPanics(t, func() { c.Add("Bad", Schema{n: bad}) })
}

// ---------------------------------------------------------------------------
// RegisterReflected
// ---------------------------------------------------------------------------

type rrWireA struct{ V string }
type rrWireB struct{ V string }

func TestRegisterReflectedIdempotentVsConflict(t *testing.T) {
	c := NewComponents()
	ta := reflect.TypeFor[rrWireA]()

	mk := func() *IRNode {
		return newObject([]Prop{{Name: "v", Schema: newScalar("string"), Required: true}})
	}
	if err := c.RegisterReflected("A", mk(), ta); err != nil {
		t.Fatalf("first registration: %v", err)
	}
	// An IDENTICAL re-request is not a duplicate: the bridge asks for the same
	// component once per referencing site.
	if err := c.RegisterReflected("A", mk(), ta); err != nil {
		t.Fatalf("identical re-registration must be a no-op, got %v", err)
	}
	if name, ok := c.TypeName(ta); !ok || name != "A" {
		t.Fatalf("TypeName = (%q, %v)", name, ok)
	}

	conflicting := newObject([]Prop{{Name: "v", Schema: newScalar("integer"), Required: true}})
	if err := c.RegisterReflected("A", conflicting, ta); err == nil {
		t.Fatal("a conflicting re-registration must error")
	}
	// The registry is unchanged by the rejected write.
	if got, _ := c.entries["A"].MarshalJSON(); !strings.Contains(string(got), `"string"`) {
		t.Fatalf("rejected registration mutated the entry: %s", got)
	}

	// Remapping a type to a second component name is also a conflict.
	if err := c.RegisterReflected("B", mk(), ta); err == nil {
		t.Fatal("remapping a wire type to a second component must error")
	}
	// A different type may legitimately reuse an identical schema under a new name.
	if err := c.RegisterReflected("B2", mk(), reflect.TypeFor[rrWireB]()); err != nil {
		t.Fatalf("distinct type + distinct name: %v", err)
	}
}

// ---------------------------------------------------------------------------
// type / wrapper indexes
// ---------------------------------------------------------------------------

type regWire struct{ V string }
type regWireOther struct{ V string }
type regAdapterNode struct{ Node[regWire] }

func TestRegisterNodeAndLookups(t *testing.T) {
	// No cleanup ritual: the registry is a VALUE, so each test owns its own.
	c := NewComponents()
	RegisterNode[regWire](c, "RegWireComponent")

	if name, ok := c.TypeName(reflect.TypeFor[regWire]()); !ok || name != "RegWireComponent" {
		t.Fatalf("TypeName: got (%q, %v)", name, ok)
	}
	if name, ok := c.NodeComponent(reflect.TypeFor[Node[regWire]]()); !ok || name != "RegWireComponent" {
		t.Fatalf("NodeComponent: got (%q, %v)", name, ok)
	}
	// The wrapper index is keyed by Node[W], NOT W.
	if name, ok := c.NodeComponent(reflect.TypeFor[regWire]()); ok {
		t.Fatalf("bare wire type must miss the wrapper index, got %q", name)
	}
	if name, ok := c.TypeName(reflect.TypeFor[regWireOther]()); ok {
		t.Fatalf("unregistered W must miss, got %q", name)
	}
	// Re-registering the SAME name is a no-op; a conflicting one panics.
	RegisterNode[regWire](c, "RegWireComponent")
	assertPanics(t, func() { RegisterNode[regWire](c, "Other") })
}

func TestRegisterWrapperTypeAndDupPanic(t *testing.T) {
	c := NewComponents()
	at := reflect.TypeFor[regAdapterNode]()
	c.RegisterWrapperType(at, "AdapterComponent")
	if name, ok := c.NodeComponent(at); !ok || name != "AdapterComponent" {
		t.Fatalf("got (%q, %v)", name, ok)
	}
	c.RegisterWrapperType(at, "AdapterComponent") // idempotent
	assertPanics(t, func() { c.RegisterWrapperType(at, "Other") })
}

// Two registries are independent — the whole point of retiring Default().
func TestRegistriesAreIndependent(t *testing.T) {
	a, b := NewComponents(), NewComponents()
	a.Add("Shared", RawFragment(map[string]any{"type": "object"}))
	b.Add("Shared", RawFragment(map[string]any{"type": "string"})) // no panic
	RegisterNode[regWireOther](a, "InA")
	if _, ok := b.TypeName(reflect.TypeFor[regWireOther]()); ok {
		t.Fatal("type index leaked between registries")
	}
}

// ---------------------------------------------------------------------------
// Verify
// ---------------------------------------------------------------------------

func TestVerifyPassesOnResolvedRefs(t *testing.T) {
	c := NewComponents()
	c.Add("Book", RawFragment(map[string]any{
		"type":       "object",
		"properties": map[string]any{"author": map[string]any{"$ref": RefPrefix + "Author"}},
	}))
	c.Add("Author", RawFragment(map[string]any{"type": "object", "properties": map[string]any{"id": map[string]any{"type": "string"}}}))
	if err := c.Verify(); err != nil {
		t.Fatalf("Verify: %v", err)
	}
}

// Every violation is reported at once: finding them one at a time turns
// assembly into a guessing loop.
func TestVerifyDanglingRefListsAll(t *testing.T) {
	c := NewComponents()
	c.Add("Book", RawFragment(map[string]any{
		"type": "object",
		"properties": map[string]any{
			"author": map[string]any{"$ref": RefPrefix + "Author"},
			"tags":   map[string]any{"type": "array", "items": map[string]any{"$ref": RefPrefix + "Tag"}},
		},
	}))
	err := c.Verify()
	if err == nil {
		t.Fatal("dangling refs must error")
	}
	for _, want := range []string{"Author", "Tag"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error must name %q: %v", want, err)
		}
	}
}

// A name on the ExternalRefs whitelist is owned by the platform, not this
// document: referencing it is legal.
func TestVerifyExternalRefsWhitelist(t *testing.T) {
	c := NewComponents()
	c.Add("Book", RawFragment(map[string]any{
		"type":       "object",
		"properties": map[string]any{"img": map[string]any{"$ref": RefPrefix + "Image"}},
	}))
	if err := c.Verify(); err == nil {
		t.Fatal("expected a dangling ref before whitelisting")
	}
	c.ExternalRefs("Image")
	if err := c.Verify(); err != nil {
		t.Fatalf("whitelisted ref must resolve: %v", err)
	}
}

// A ref buried in an Opaque fragment's raw bytes still counts — the names were
// collected at construction time exactly so this check can see them.
func TestVerifySeesOpaqueCarriedRef(t *testing.T) {
	c := NewComponents()
	c.Add("Exotic", RawFragment(map[string]any{
		"$comment":          "unmodelled keyword forces the Opaque path",
		"patternProperties": map[string]any{"^x": map[string]any{"$ref": RefPrefix + "Hidden"}},
	}))
	err := c.Verify()
	if err == nil || !strings.Contains(err.Error(), "Hidden") {
		t.Fatalf("Verify must see refs inside Opaque bytes, got %v", err)
	}
	c.ExternalRefs("Hidden")
	if err := c.Verify(); err != nil {
		t.Fatalf("whitelisted opaque-carried ref must resolve: %v", err)
	}
}

// An extension value is arbitrary JSON and may carry a "$ref" (an x-links
// block). Both TYPED INGRESS paths write Extensions through assignKeyword, so
// both must contribute their refs to the check.
func TestVerifySeesExtensionCarriedRef(t *testing.T) {
	cases := []struct {
		name string
		make func() Schema
	}{
		{"fragmentToIR", func() Schema {
			return RawFragment(map[string]any{
				"type": "object",
				"properties": map[string]any{
					"id": map[string]any{"type": "string"},
				},
				"x-links": map[string]any{
					"owner": map[string]any{"$ref": RefPrefix + "Hidden"},
				},
			})
		}},
		{"Set", func() Schema {
			s := RawFragment(map[string]any{"type": "object", "properties": map[string]any{"id": map[string]any{"type": "string"}}})
			return s.Set("x-links", map[string]any{"owner": map[string]any{"$ref": RefPrefix + "Hidden"}})
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			c := NewComponents()
			c.Add("Book", tc.make())
			err := c.Verify()
			if err == nil || !strings.Contains(err.Error(), "Hidden") {
				t.Fatalf("Verify must see a $ref inside Extensions, got %v", err)
			}
			c.ExternalRefs("Hidden")
			if err := c.Verify(); err != nil {
				t.Fatalf("whitelisted extension-carried ref must resolve: %v", err)
			}
		})
	}
}

// An extension-carried ref that names a PRESENT component resolves with no
// whitelist at all.
func TestVerifyExtensionRefToPresentComponent(t *testing.T) {
	c := NewComponents()
	c.Add("Book", RawFragment(map[string]any{
		"type":       "object",
		"properties": map[string]any{"id": map[string]any{"type": "string"}},
		"x-links":    []any{map[string]any{"$ref": RefPrefix + "Author"}},
	}))
	c.Add("Author", RawFragment(map[string]any{"type": "object", "properties": map[string]any{"id": map[string]any{"type": "string"}}}))
	if err := c.Verify(); err != nil {
		t.Fatalf("Verify: %v", err)
	}
}

// Overwriting an extension drops the names the old value contributed.
func TestExtensionRefsRecollectedOnOverwrite(t *testing.T) {
	s := RawFragment(map[string]any{"type": "object", "properties": map[string]any{"id": map[string]any{"type": "string"}}})
	s.Set("x-links", map[string]any{"$ref": RefPrefix + "Stale"})
	s.Set("x-links", map[string]any{"note": "no refs here"})
	c := NewComponents()
	c.Add("Book", s)
	if err := c.Verify(); err != nil {
		t.Fatalf("a replaced extension must not leave a stale ref behind: %v", err)
	}
}

// Verify covers every typed sub-schema position, with no special casing.
func TestVerifyWalksAllPositions(t *testing.T) {
	c := NewComponents()
	n := newObject([]Prop{{Name: "p", Schema: newRef("FromProp")}})
	n.AdditionalProperties = newRef("FromAdditional")
	n.Not = newRef("FromNot")
	c.Add("Wide", Schema{n: n})
	c.Add("Arms", AnyOf(RefTo("FromArm"), Schema{n: newArray(newRef("FromItems"))}))

	err := c.Verify()
	if err == nil {
		t.Fatal("expected unresolved refs")
	}
	for _, want := range []string{"FromProp", "FromAdditional", "FromNot", "FromArm", "FromItems"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("Verify missed the %s position: %v", want, err)
		}
	}
}

// The set EmitComponents produces verifies clean once the doc-external target
// is whitelisted — that is the whole contract between the two.
func TestVerifyOverEmittedComponents(t *testing.T) {
	g := buildToyGraph()
	out, err := EmitComponents(stubReflector{}, []include.Resource{g.Book})
	if err != nil {
		t.Fatal(err)
	}
	c := NewComponents()
	for _, name := range slices.Sorted(maps.Keys(out)) {
		c.Add(name, out[name])
	}
	if err := c.Verify(); err == nil {
		t.Fatal("the doc-external Image ref must dangle until whitelisted")
	}
	c.ExternalRefs("Image")
	if err := c.Verify(); err != nil {
		t.Fatalf("Verify: %v", err)
	}
}

// Get is the READ side of the registry: an assembler in another module (the
// huma adapter's bridge) serves a registered component from its typed IR
// instead of re-parsing the serialized fragment.
func TestComponentsGet(t *testing.T) {
	c := NewComponents()
	c.Add("Thing", RawFragment(map[string]any{
		"type": "object", "properties": map[string]any{"id": map[string]any{"type": "string"}},
	}))
	got, ok := c.Get("Thing")
	if !ok {
		t.Fatalf("registered component not readable")
	}
	if got.IR() == nil || got.IR().Kind != KindObject || len(got.IR().Props) != 1 {
		t.Fatalf("Get returned the wrong IR: %#v", got.IR())
	}
	// The node is SHARED with the registry, not a copy.
	if entry, _ := c.Get("Thing"); entry.IR() != got.IR() {
		t.Fatalf("Get must return the stored node")
	}
	if _, ok := c.Get("Missing"); ok {
		t.Fatalf("unknown name must report absent")
	}
	// It also sees what RegisterReflected wrote.
	if err := c.RegisterReflected("Reflected", newScalar("string"), nil); err != nil {
		t.Fatal(err)
	}
	if s, ok := c.Get("Reflected"); !ok || s.IR().Types[0] != "string" {
		t.Fatalf("reflected component not readable")
	}
}

type typeOfSecond struct{ X string }

// TypeOf is the reverse of TypeName. Both registration paths feed it, the node
// WRAPPER index does not, and the answer is stable when two types name one
// component.
func TestComponentsTypeOf(t *testing.T) {
	c := NewComponents()
	c.Add("Thing", RawFragment(map[string]any{"type": "object"}))
	c.RegisterType(reflect.TypeFor[rrWireA](), "Thing")
	if got, ok := c.TypeOf("Thing"); !ok || got != reflect.TypeFor[rrWireA]() {
		t.Fatalf("TypeOf = %v, %v", got, ok)
	}
	// RegisterReflected binds it too.
	if err := c.RegisterReflected("Reflected", newScalar("string"), reflect.TypeFor[rrWireB]()); err != nil {
		t.Fatal(err)
	}
	if got, ok := c.TypeOf("Reflected"); !ok || got != reflect.TypeFor[rrWireB]() {
		t.Fatalf("TypeOf(reflected) = %v, %v", got, ok)
	}
	// A component with no type binding has no answer.
	c.Add("Handmade", RawFragment(map[string]any{"type": "object"}))
	if _, ok := c.TypeOf("Handmade"); ok {
		t.Fatalf("a hand-assembled component has no wire type")
	}
	if _, ok := c.TypeOf("Nope"); ok {
		t.Fatalf("unknown name must report absent")
	}
	// A WRAPPER is not the component's type.
	c.RegisterWrapperType(reflect.TypeFor[Node[rrWireA]](), "Handmade")
	if _, ok := c.TypeOf("Handmade"); ok {
		t.Fatalf("the wrapper index must not answer TypeOf")
	}
	// Two types naming one component: the FIRST binding wins, every time.
	c.RegisterType(reflect.TypeFor[typeOfSecond](), "Thing")
	for range 20 {
		if got, _ := c.TypeOf("Thing"); got != reflect.TypeFor[rrWireA]() {
			t.Fatalf("TypeOf is not deterministic: %v", got)
		}
	}
}
