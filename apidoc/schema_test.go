package apidoc

import (
	"reflect"
	"strings"
	"testing"
)

// assertPanicsWith requires a panic whose message contains want. The DSL's
// contract is panic-at-declaration, and the MESSAGE is half the contract: it is
// what turns a typo into an instantly diagnosable unit-test failure.
func assertPanicsWith(t *testing.T, want string, fn func()) {
	t.Helper()
	defer func() {
		r := recover()
		if r == nil {
			t.Fatalf("expected panic containing %q", want)
		}
		got, _ := r.(string)
		if !strings.Contains(got, want) {
			t.Fatalf("panic = %q, want it to contain %q", got, want)
		}
	}()
	fn()
}

// ---------------------------------------------------------------------------
// RawFragment -> IR
// ---------------------------------------------------------------------------

func TestRawFragmentKinds(t *testing.T) {
	for name, tc := range map[string]struct {
		frag map[string]any
		want Kind
	}{
		"bare ref":    {map[string]any{"$ref": RefPrefix + "User"}, KindRef},
		"scalar":      {map[string]any{"type": "string", "format": "uuid"}, KindScalar},
		"enum":        {map[string]any{"type": "string", "enum": []any{"a", "b"}}, KindEnum},
		"object":      {map[string]any{"type": "object", "properties": map[string]any{}}, KindObject},
		"props only":  {map[string]any{"properties": map[string]any{"id": map[string]any{"type": "string"}}}, KindObject},
		"array":       {map[string]any{"type": "array", "items": map[string]any{"type": "string"}}, KindArray},
		"items only":  {map[string]any{"items": map[string]any{"type": "string"}}, KindArray},
		"combinator":  {map[string]any{"anyOf": []any{map[string]any{"type": "string"}}}, KindCombinator},
		"exotic":      {map[string]any{"type": "object", "patternProperties": map[string]any{}}, KindOpaque},
		"unknown key": {map[string]any{"type": "string", "weird": 1}, KindOpaque},
		"empty":       {map[string]any{}, KindOpaque},
		"annotations": {map[string]any{"description": "just a note"}, KindOpaque},
	} {
		t.Run(name, func(t *testing.T) {
			if got := RawFragment(tc.frag).IR().Kind; got != tc.want {
				t.Fatalf("kind = %s, want %s", got, tc.want)
			}
		})
	}
}

// TestRawFragmentRefStripsPrefix — the IR stores the bare component NAME;
// RefPrefix is re-applied at serialization, so a fragment that carries the
// prefix must not end up with it doubled.
func TestRawFragmentRefStripsPrefix(t *testing.T) {
	n := RawFragment(map[string]any{"$ref": RefPrefix + "User"}).IR()
	if n.Ref != "User" {
		t.Fatalf("Ref = %q, want %q", n.Ref, "User")
	}
	if got := RawFragment(map[string]any{"$ref": RefPrefix + "User"}).Map()["$ref"]; got != RefPrefix+"User" {
		t.Fatalf("round trip = %v", got)
	}
}

// TestRawFragmentPropsSortedAndRequiredFlags — a Go map has no order, so
// properties are ordered by SORTED key, and `required` becomes per-Prop flags.
func TestRawFragmentPropsSortedAndRequiredFlags(t *testing.T) {
	n := fixture().IR()
	var names []string
	for _, p := range n.Props {
		names = append(names, p.Name)
	}
	if want := []string{"count", "id", "note", "owner"}; !reflect.DeepEqual(names, want) {
		t.Fatalf("prop order = %v, want %v (sorted)", names, want)
	}
	req := map[string]bool{}
	for _, p := range n.Props {
		if p.Required {
			req[p.Name] = true
		}
	}
	if !reflect.DeepEqual(req, map[string]bool{"id": true, "count": true}) {
		t.Fatalf("required flags = %v", req)
	}
	if n.AdditionalProperties != false {
		t.Fatalf("additionalProperties = %v, want false", n.AdditionalProperties)
	}
}

// TestRawFragmentTypedKeywordsLand — scalar keywords reach their typed fields
// rather than a generic bag, and x- keys reach Extensions.
func TestRawFragmentTypedKeywordsLand(t *testing.T) {
	n := RawFragment(map[string]any{
		"type": "integer", "minimum": 1, "maxLength": 8,
		"description": "n", "deprecated": true, "x-units": "kg",
	}).IR()
	if n.Minimum == nil || *n.Minimum != 1 {
		t.Fatalf("minimum = %v", n.Minimum)
	}
	if n.MaxLength == nil || *n.MaxLength != 8 {
		t.Fatalf("maxLength = %v", n.MaxLength)
	}
	if n.Description != "n" || !n.Deprecated {
		t.Fatalf("annotations lost: %+v", n)
	}
	if got := n.Extensions["x-units"]; got != "kg" {
		t.Fatalf("extensions = %v", n.Extensions)
	}
}

// TestRawFragmentOpaqueIsWholeFragment — decision #9: one unmodelled keyword
// makes the WHOLE fragment opaque. A partial parse would silently drop the very
// keyword the caller reached for RawFragment to keep.
func TestRawFragmentOpaqueIsWholeFragment(t *testing.T) {
	m := map[string]any{
		"type":       "object",
		"properties": map[string]any{"id": map[string]any{"type": "string"}},
		"if":         map[string]any{"required": []any{"id"}},
	}
	s := RawFragment(m)
	if s.IR().Kind != KindOpaque {
		t.Fatalf("kind = %s, want opaque", s.IR().Kind)
	}
	if !reflect.DeepEqual(s.Map(), m) {
		t.Fatalf("opaque fragment altered: %v", s.Map())
	}
}

func TestRawFragmentPanicsOnBadValue(t *testing.T) {
	assertPanics(t, func() { RawFragment(map[string]any{"type": 42}) })
	assertPanics(t, func() { RawFragment(map[string]any{"type": "object", "required": []any{"nope"}}) })
}

// TestMapIsDetachedCopy — recorded DRIFT: v0 returned the live backing map.
func TestMapIsDetachedCopy(t *testing.T) {
	s := fixture()
	m := s.Map()
	m["properties"].(map[string]any)["id"] = map[string]any{"type": "boolean"}
	again := s.Map()
	if got := again["properties"].(map[string]any)["id"]; !reflect.DeepEqual(got, map[string]any{"type": "string"}) {
		t.Fatalf("Map() is not detached: %v", got)
	}
}

// TestSchemaAliasesSharedNode — v0 aliasing semantics: value-receiver deltas
// mutate the SHARED node, so two Schema values over one node see each other's
// edits (the single-owner convention exists because of this).
func TestSchemaAliasesSharedNode(t *testing.T) {
	a := fixture()
	b := Schema{n: a.IR()}
	a.Optional("count")
	if got := b.Map()["required"]; !reflect.DeepEqual(got, []any{"id"}) {
		t.Fatalf("alias did not observe the delta: %v", got)
	}
}

// ---------------------------------------------------------------------------
// unconsumed structural keywords (review fix #1)
// ---------------------------------------------------------------------------

// TestRawFragmentUnconsumedCombinationIsOpaque — the kind dispatch picks ONE
// shape, so any structural keyword the chosen branch does not claim would be
// silently dropped. An unmodelled COMBINATION is therefore as opaque as an
// unmodelled keyword: the whole fragment survives byte-for-byte instead of
// half of it vanishing.
func TestRawFragmentUnconsumedCombinationIsOpaque(t *testing.T) {
	for name, frag := range map[string]map[string]any{
		"ref + properties": {
			"$ref":       RefPrefix + "User",
			"properties": map[string]any{"id": map[string]any{"type": "string"}},
			"required":   []any{"id"},
		},
		"combinator + everything": {
			"anyOf":      []any{map[string]any{"type": "string"}},
			"type":       "object",
			"properties": map[string]any{"id": map[string]any{"type": "string"}},
			"$ref":       RefPrefix + "User",
		},
		"object + items": {
			"type":       "object",
			"properties": map[string]any{"id": map[string]any{"type": "string"}},
			"items":      map[string]any{"type": "string"},
		},
		"array + enum": {
			"type":  "array",
			"items": map[string]any{"type": "string"},
			"enum":  []any{"a"},
		},
	} {
		t.Run(name, func(t *testing.T) {
			s := RawFragment(frag)
			if got := s.IR().Kind; got != KindOpaque {
				t.Fatalf("kind = %s, want opaque (a keyword would have been dropped)", got)
			}
			if !reflect.DeepEqual(s.Map(), frag) {
				t.Fatalf("round trip lost data:\n got %v\nwant %v", s.Map(), frag)
			}
		})
	}
}

// TestRawFragmentRefKeepsAnnotationSiblings — the counterpart: annotations
// alongside a $ref lose nothing, so this stays a typed Ref.
func TestRawFragmentRefKeepsAnnotationSiblings(t *testing.T) {
	s := RawFragment(map[string]any{
		"$ref":        RefPrefix + "User",
		"description": "the owner",
		"x-tag":       "v",
	})
	n := s.IR()
	if n.Kind != KindRef {
		t.Fatalf("kind = %s, want ref", n.Kind)
	}
	if n.Ref != "User" || n.Description != "the owner" || n.Extensions["x-tag"] != "v" {
		t.Fatalf("ref siblings lost: %+v", n)
	}
}

// ---------------------------------------------------------------------------
// strict mode (review fix #3)
// ---------------------------------------------------------------------------

// TestFragmentToIRStrictNamesTheOffender — on a TYPED ingress path (reflector
// output, Set values) an unmodelled construct is a bug to surface, not an
// escape hatch the caller chose, so strict mode errors and says WHAT it choked
// on. Forgiving mode takes the same input to Opaque.
func TestFragmentToIRStrictNamesTheOffender(t *testing.T) {
	for name, tc := range map[string]struct {
		frag map[string]any
		want string
	}{
		"untyped keyword": {
			map[string]any{"type": "object", "patternProperties": map[string]any{}},
			`"patternProperties"`,
		},
		"unconsumed combination": {
			map[string]any{"$ref": RefPrefix + "User", "properties": map[string]any{}},
			"properties",
		},
		"untyped enum": {
			map[string]any{"enum": []any{"a"}},
			`"enum" without "type"`,
		},
		"array without items": {
			map[string]any{"type": "array"},
			`without "items"`,
		},
		"annotations only": {
			map[string]any{"description": "x"},
			"carries no type",
		},
		"empty": {map[string]any{}, "empty fragment"},
	} {
		t.Run(name, func(t *testing.T) {
			_, err := fragmentToIRMode(tc.frag, true)
			if err == nil {
				t.Fatalf("strict mode accepted %v", tc.frag)
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("error = %q, want it to name %q", err, tc.want)
			}
			// Forgiving mode is the escape hatch and must still take it.
			n, ferr := fragmentToIR(tc.frag)
			if ferr != nil || n.Kind != KindOpaque {
				t.Fatalf("forgiving mode: kind=%v err=%v, want opaque", n, ferr)
			}
		})
	}
}

// TestFragmentToIRStrictAcceptsOrdinaryFragments — strict rejects exotica, not
// the everyday reflector output union.go feeds it.
func TestFragmentToIRStrictAcceptsOrdinaryFragments(t *testing.T) {
	n, err := fragmentToIRMode(map[string]any{
		"type": "object",
		"properties": map[string]any{
			"id":    map[string]any{"type": "string"},
			"tags":  map[string]any{"type": "array", "items": map[string]any{"type": "string"}},
			"owner": map[string]any{"$ref": RefPrefix + "User"},
		},
		"required": []any{"id"},
	}, true)
	if err != nil {
		t.Fatalf("strict rejected a plain struct fragment: %v", err)
	}
	if n.Kind != KindObject || len(n.Props) != 3 {
		t.Fatalf("node = %+v", n)
	}
}

// TestSetIsStrictIngress — Set is a typed ingress path, so an exotic map value
// panics rather than sliding in as an Opaque property.
func TestSetIsStrictIngress(t *testing.T) {
	assertPanicsWith(t, "mistyped value", func() {
		fixture().Set("properties.extra", map[string]any{"patternProperties": map[string]any{}})
	})
}

// TestOpaqueFragmentScansRefs pins the escape-hatch constructor: it validates
// the bytes and collects the references made from INSIDE them, which is what
// keeps an opaque component honest at Verify time. A reflector folding a
// typeless schema reaches for it instead of building the node by hand — a
// struct literal would leave the scan undone and Verify blind.
func TestOpaqueFragmentScansRefs(t *testing.T) {
	if _, err := OpaqueFragment([]byte(`{"not":`)); err == nil {
		t.Errorf("invalid JSON must be an error, not an opaque node")
	}
	if _, err := OpaqueFragment(nil); err == nil {
		t.Errorf("an empty fragment must be an error")
	}
	s, err := OpaqueFragment([]byte(`{"not":{"$ref":"` + RefPrefix + `Missing"}}`))
	if err != nil {
		t.Fatalf("OpaqueFragment: %v", err)
	}
	if s.IR().Kind != KindOpaque {
		t.Fatalf("want an opaque node, got %s", s.IR().Kind)
	}
	c := NewComponents()
	c.Add("Holder", s)
	err = c.Verify()
	if err == nil || !strings.Contains(err.Error(), "Holder -> Missing") {
		t.Errorf("a $ref inside the fragment must reach Verify, got: %v", err)
	}
}

// TestSetExtensionIsTheOneGate pins the exported Extensions writer: it refuses a
// standard keyword and a non-"x-" key, and re-collects the refs hiding inside
// extension values over the WHOLE map.
func TestSetExtensionIsTheOneGate(t *testing.T) {
	n := newObject(nil)
	if err := n.SetExtension("description", "nope"); err == nil {
		t.Errorf("a standard keyword must not be settable as an extension")
	}
	if err := n.SetExtension("links", map[string]any{}); err == nil {
		t.Errorf("a key without the \"x-\" prefix must be refused")
	}
	if err := n.SetExtension("x-links", map[string]any{"$ref": RefPrefix + "Gone"}); err != nil {
		t.Fatalf("SetExtension: %v", err)
	}
	c := NewComponents()
	c.Add("Holder", Schema{n: n})
	err := c.Verify()
	if err == nil || !strings.Contains(err.Error(), "Holder -> Gone") {
		t.Errorf("a $ref inside an extension value must reach Verify, got: %v", err)
	}
	// Overwriting an extension drops the names it used to contribute.
	if err := n.SetExtension("x-links", map[string]any{}); err != nil {
		t.Fatalf("SetExtension: %v", err)
	}
	c2 := NewComponents()
	c2.Add("Holder", Schema{n: n})
	if err := c2.Verify(); err != nil {
		t.Errorf("the stale extension ref survived the overwrite: %v", err)
	}
}
