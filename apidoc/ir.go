package apidoc

import (
	"encoding/json"
	"fmt"
	"maps"
	"reflect"
	"slices"
	"strings"
)

// Kind discriminates the shape of an IRNode. Each kind admits exactly one
// canonical encoding of a schema; the Kind<->field invariants are enforced by
// checkInvariants.
type Kind int

const (
	KindObject Kind = iota + 1
	KindRef
	KindArray
	KindCombinator
	KindScalar
	KindEnum
	KindOpaque
)

// String renders the kind name used in invariant error messages.
func (k Kind) String() string {
	switch k {
	case KindObject:
		return "object"
	case KindRef:
		return "ref"
	case KindArray:
		return "array"
	case KindCombinator:
		return "combinator"
	case KindScalar:
		return "scalar"
	case KindEnum:
		return "enum"
	case KindOpaque:
		return "opaque"
	default:
		return fmt.Sprintf("kind(%d)", int(k))
	}
}

// Prop is one ordered property of an object schema.
type Prop struct {
	Name     string
	Schema   *IRNode
	Required bool
}

// IRNode is the typed schema IR. It is the single internal representation of a
// JSON Schema (OAS 3.1 dialect) inside wireleaf; the wire form is produced by
// MarshalJSON, which preserves property and keyword order.
//
// It is named IRNode because the name Node is taken by the wire carrier
// Node[W] (node.go).
type IRNode struct {
	Kind                Kind
	Types               []string // e.g. ["string","null"]
	Ref                 string   // component name; RefPrefix applied at serialization
	Props               []Prop   // ORDERED
	Items               *IRNode
	AnyOf, OneOf, AllOf []*IRNode
	Not                 *IRNode

	AdditionalProperties any // nil | bool | *IRNode
	Enum                 []any

	// Typed validation/annotation set: everything huma v2.39.1 validates, plus
	// ContentMediaType, which jsonschema-go emits from struct tags
	// (document-only).
	Minimum, Maximum, ExclusiveMinimum, ExclusiveMaximum, MultipleOf       *float64
	MinLength, MaxLength, MinItems, MaxItems, MinProperties, MaxProperties *int
	UniqueItems                                                            bool
	Pattern, Format, Description, Title, ContentEncoding, ContentMediaType string
	Const                                                                  any
	Default                                                                any
	Examples                                                               []any
	DependentRequired                                                      map[string][]string
	ReadOnly, WriteOnly, Deprecated                                        bool

	Extensions map[string]any  // x-* keys ONLY -- document-only, never validated
	Opaque     json.RawMessage // escape hatch (RawFragment)

	// opaqueRefs holds the component names referenced from inside Opaque bytes,
	// collected once at construction time (newOpaque) and consumed by the
	// components assembly's dangling-ref check.
	opaqueRefs []string

	// extRefs is the same mechanism for EXTENSIONS: an x- value is arbitrary
	// JSON and may carry a "$ref" (an x-links block, say). The names are
	// collected whenever Extensions is written (assignKeyword — the ONE gate
	// both typed ingress paths, fragmentToIR and Schema.Set, go through) so
	// Verify can see a reference the typed fields never hold.
	extRefs []string
}

// SetExtension writes one document-only "x-" key into Extensions. It is the ONE
// gate every writer of Extensions goes through — the typed ingress paths
// (assignKeyword, reached from fragmentToIR and Schema.Set) and a Reflector
// building nodes by hand alike.
//
// A standard keyword is refused: it has a typed field, and the two would fight
// at serialization. So is a key that does not start with "x-": Extensions hold
// vendor keys only.
//
// extRefs is re-collected over the WHOLE map rather than just the new value, so
// overwriting an extension drops the names it used to contribute.
func (n *IRNode) SetExtension(key string, v any) error {
	if standardKeywords[key] {
		return fmt.Errorf("apidoc: standard keyword %q must not ride in Extensions: write its typed field instead", key)
	}
	if !strings.HasPrefix(key, "x-") {
		return fmt.Errorf("apidoc: extension key %q must start with \"x-\"", key)
	}
	if n.Extensions == nil {
		n.Extensions = map[string]any{}
	}
	n.Extensions[key] = v
	n.extRefs = collectRefs(n.Extensions)
	return nil
}

// ---------------------------------------------------------------------------
// invariants
// ---------------------------------------------------------------------------

// standardKeywords is every keyword the serializer emits from a typed field.
// Such a key may never appear in Extensions, which hold x-* and unknown vendor
// keys only.
var standardKeywords = map[string]bool{
	"$ref":                 true,
	"type":                 true,
	"enum":                 true,
	"const":                true,
	"minimum":              true,
	"maximum":              true,
	"exclusiveMinimum":     true,
	"exclusiveMaximum":     true,
	"multipleOf":           true,
	"minLength":            true,
	"maxLength":            true,
	"minItems":             true,
	"maxItems":             true,
	"minProperties":        true,
	"maxProperties":        true,
	"uniqueItems":          true,
	"pattern":              true,
	"format":               true,
	"contentEncoding":      true,
	"contentMediaType":     true,
	"dependentRequired":    true,
	"properties":           true,
	"required":             true,
	"items":                true,
	"anyOf":                true,
	"oneOf":                true,
	"allOf":                true,
	"not":                  true,
	"additionalProperties": true,
	"description":          true,
	"title":                true,
	"default":              true,
	"examples":             true,
	"readOnly":             true,
	"writeOnly":            true,
	"deprecated":           true,
}

func (n *IRNode) invErr(format string, args ...any) error {
	return fmt.Errorf("apidoc: invalid %s node: %s", n.Kind, fmt.Sprintf(format, args...))
}

func (n *IRNode) hasArms() bool {
	return len(n.AnyOf) > 0 || len(n.OneOf) > 0 || len(n.AllOf) > 0
}

// typesAre reports whether Types is empty, exactly [base] or exactly [base,"null"].
func (n *IRNode) typesAre(base string) bool {
	switch len(n.Types) {
	case 0:
		return true
	case 1:
		return n.Types[0] == base
	case 2:
		return n.Types[0] == base && n.Types[1] == "null"
	default:
		return false
	}
}

// checkInvariants enforces the Kind<->field table, SHALLOWLY (one node, not its
// children). The CONSTRUCTORS do not call it — they build well-formed nodes by
// construction. It is applied where a node could have become ill-formed:
//
//   - components assembly (build.go's validateComponent), which walks the WHOLE
//     tree and is the gate every node passes to reach the document;
//   - fragment ingress (schema.go), on each node parsed from a map;
//   - Schema.Set (dsl.go), after a keyword assignment mutates a node.
func (n *IRNode) checkInvariants() error {
	if n == nil {
		return fmt.Errorf("apidoc: nil IRNode")
	}
	// Opaque forbids everything else, so check it before the shared guards.
	// The comparison against a canonical minimal copy uses DeepEqual, which
	// sees unexported fields too: a future IRNode field automatically TIGHTENS
	// this check instead of silently weakening it. The copy carries the payload
	// and the refs newOpaque collected from it; every other field must be zero
	// (an opaque node is built only by newOpaque, which leaves them all nil, so
	// nil-vs-empty mismatches cannot arise here).
	if n.Kind == KindOpaque {
		if len(n.Opaque) == 0 {
			return n.invErr("Opaque is required")
		}
		want := &IRNode{Kind: KindOpaque, Opaque: n.Opaque, opaqueRefs: n.opaqueRefs}
		if !reflect.DeepEqual(n, want) {
			return n.invErr("Opaque nodes carry no other structural fields, annotations or validations")
		}
		return nil
	}

	if len(n.Opaque) > 0 {
		return n.invErr("Opaque is only allowed on opaque nodes")
	}
	// Extensions carry x-* and unknown vendor keys ONLY: a standard keyword
	// must go through its typed field, or the two would fight at serialization.
	for _, k := range slices.Sorted(maps.Keys(n.Extensions)) {
		if standardKeywords[k] {
			return n.invErr("standard keyword %q must not ride in Extensions", k)
		}
	}

	// The shared prohibitions: any structural field the table does not allow
	// for this kind must be absent.
	allow, ok := kindAllows[n.Kind]
	if !ok {
		return fmt.Errorf("apidoc: invalid IRNode: unknown kind %d", int(n.Kind))
	}
	for _, f := range []struct {
		present bool
		allowed bool
		name    string
	}{
		{len(n.Types) > 0, allow.types, "Types"},
		{n.Ref != "", allow.ref, "Ref"},
		{n.Props != nil, allow.props, "Props"},
		{n.Items != nil, allow.items, "Items"},
		{n.hasArms(), allow.arms, "AnyOf/OneOf/AllOf"},
		{len(n.Enum) > 0, allow.enum, "Enum"},
	} {
		if f.present && !f.allowed {
			return n.invErr("%s is not allowed", f.name)
		}
	}

	// The kind-specific requirements on the fields the table allows.
	switch n.Kind {
	case KindObject:
		if n.Props == nil {
			return n.invErr("Props is required (use an empty slice for an empty object)")
		}
		if !n.typesAre("object") {
			return n.invErr(`Types must be empty, ["object"] or ["object","null"], got %v`, n.Types)
		}
		for i, p := range n.Props {
			if p.Name == "" {
				return n.invErr("Props[%d] has an empty name", i)
			}
			if p.Schema == nil {
				return n.invErr("Props[%d] (%q) has a nil schema", i, p.Name)
			}
		}

	case KindRef:
		if n.Ref == "" {
			return n.invErr("Ref is required")
		}

	case KindArray:
		if n.Items == nil {
			return n.invErr("Items is required")
		}
		if !n.typesAre("array") {
			return n.invErr(`Types must be empty, ["array"] or ["array","null"], got %v`, n.Types)
		}

	case KindCombinator:
		nonEmpty := 0
		for _, arms := range [][]*IRNode{n.AnyOf, n.OneOf, n.AllOf} {
			if len(arms) > 0 {
				nonEmpty++
			}
			for i, a := range arms {
				if a == nil {
					return n.invErr("arm %d is nil", i)
				}
			}
		}
		if nonEmpty != 1 {
			return n.invErr("exactly one of AnyOf/OneOf/AllOf must be non-empty, got %d", nonEmpty)
		}

	case KindScalar:
		if len(n.Types) == 0 {
			return n.invErr("Types is required")
		}

	case KindEnum:
		if len(n.Enum) == 0 {
			return n.invErr("Enum is required")
		}
		if len(n.Types) == 0 {
			return n.invErr("Types is required")
		}
	}
	return nil
}

// kindAllows is the Kind<->field table the IRNode and Kind comments allude to:
// which structural fields each kind may carry at all. checkInvariants rejects
// any field present outside its row; whether an ALLOWED field is also required
// (or further constrained, e.g. Types on object/array) is checked per kind
// after the table pass. KindOpaque is absent by design — it forbids everything
// and is checked separately, via DeepEqual against a canonical minimal copy.
var kindAllows = map[Kind]struct{ types, ref, props, items, arms, enum bool }{
	KindObject:     {types: true, props: true},
	KindRef:        {ref: true},
	KindArray:      {types: true, items: true},
	KindCombinator: {arms: true},
	KindScalar:     {types: true},
	KindEnum:       {types: true, enum: true},
}

// ---------------------------------------------------------------------------
// constructors -- canonical form by construction
// ---------------------------------------------------------------------------

// newObject builds an object node with ordered properties. A nil props slice
// yields an empty (but non-nil) property list.
func newObject(props []Prop) *IRNode {
	if props == nil {
		props = []Prop{}
	}
	return &IRNode{Kind: KindObject, Types: []string{"object"}, Props: props}
}

// newRef builds a reference to a component by name (RefPrefix is applied at
// serialization time).
func newRef(name string) *IRNode {
	return &IRNode{Kind: KindRef, Ref: name}
}

// newArray builds an array node with the given item schema.
func newArray(items *IRNode) *IRNode {
	return &IRNode{Kind: KindArray, Types: []string{"array"}, Items: items}
}

// newScalar builds a scalar node with the given JSON types.
func newScalar(types ...string) *IRNode {
	return &IRNode{Kind: KindScalar, Types: append([]string(nil), types...)}
}

// newEnum builds an enum node over base, optionally admitting null. A nullable
// enum widens both Types and Enum: without a null member the enum -- the
// stricter of the two constraints -- would make the null type unreachable.
func newEnum(base string, isNullable bool, values ...any) *IRNode {
	types := []string{base}
	enum := append([]any(nil), values...)
	if isNullable {
		types = append(types, "null")
		enum = appendNullValue(enum)
	}
	return &IRNode{Kind: KindEnum, Types: types, Enum: enum}
}

// appendNullValue appends a null member to an enum unless one is already there.
func appendNullValue(values []any) []any {
	for _, v := range values {
		if v == nil {
			return values
		}
	}
	return append(values, nil)
}

func newAnyOf(arms ...*IRNode) *IRNode {
	return &IRNode{Kind: KindCombinator, AnyOf: append([]*IRNode(nil), arms...)}
}

func newOneOf(arms ...*IRNode) *IRNode {
	return &IRNode{Kind: KindCombinator, OneOf: append([]*IRNode(nil), arms...)}
}

func newAllOf(arms ...*IRNode) *IRNode {
	return &IRNode{Kind: KindCombinator, AllOf: append([]*IRNode(nil), arms...)}
}

// newOpaque wraps a raw JSON fragment verbatim. The bytes are validated as JSON
// and scanned once for $ref occurrences; the referenced component names are
// retained for the dangling-ref check performed by components assembly.
func newOpaque(raw json.RawMessage) (*IRNode, error) {
	if len(raw) == 0 {
		return nil, fmt.Errorf("apidoc: opaque schema fragment is empty")
	}
	if !json.Valid(raw) {
		return nil, fmt.Errorf("apidoc: opaque schema fragment is not valid JSON")
	}
	var decoded any
	if err := json.Unmarshal(raw, &decoded); err != nil {
		return nil, fmt.Errorf("apidoc: opaque schema fragment is not valid JSON: %w", err)
	}
	n := &IRNode{
		Kind:       KindOpaque,
		Opaque:     append(json.RawMessage(nil), raw...),
		opaqueRefs: collectRefs(decoded),
	}
	return n, nil
}

// collectRefs walks a generically decoded JSON value and returns the values of
// every "$ref" string, with RefPrefix stripped when present. Object keys are
// visited in sorted order (JSON object iteration is randomized), arrays in
// element order; duplicates are dropped.
func collectRefs(v any) []string {
	var out []string
	seen := map[string]bool{}
	var walk func(any)
	walk = func(cur any) {
		switch t := cur.(type) {
		case map[string]any:
			if rv, ok := t["$ref"]; ok {
				if s, ok := rv.(string); ok {
					name := strings.TrimPrefix(s, RefPrefix)
					if !seen[name] {
						seen[name] = true
						out = append(out, name)
					}
				}
			}
			for _, k := range slices.Sorted(maps.Keys(t)) {
				if k == "$ref" {
					continue
				}
				walk(t[k])
			}
		case []any:
			for _, e := range t {
				walk(e)
			}
		}
	}
	walk(v)
	return out
}

// ---------------------------------------------------------------------------
// canonical-idiom helpers
// ---------------------------------------------------------------------------

// irNullableRef is the canonical encoding of an optional-or-null component
// reference: anyOf[ $ref, {"type":"null"} ]. A Ref node never carries a null
// type of its own.
func irNullableRef(name string) *IRNode {
	return newAnyOf(newRef(name), newScalar("null"))
}

// irNullable returns the canonical nullable form of n. It does NOT mutate its
// argument: when a change is needed it returns a shallow clone with any
// appended-to slice copied first. It is idempotent:
//   - scalar/enum/array/object: "null" appended to Types once (an enum also
//     gains a null member, without which the null type stays unreachable);
//   - ref: wrapped in the canonical anyOf idiom;
//   - anyOf/oneOf: a null arm is appended unless one is already present;
//   - allOf: the whole node is wrapped in anyOf[allOf, null] -- appending a
//     null arm to an allOf would make the schema unsatisfiable;
//   - opaque: returned unchanged (the fragment is not transformed).
func irNullable(n *IRNode) *IRNode {
	if n == nil {
		return nil
	}
	switch n.Kind {
	case KindRef:
		return irNullableRef(n.Ref)
	case KindOpaque:
		return n
	case KindCombinator:
		if len(n.AllOf) > 0 {
			// allOf means "all arms must hold"; a null arm would contradict the
			// others. Wrap instead. The result is idempotent: it is an anyOf
			// that already carries a null arm.
			return newAnyOf(n, newScalar("null"))
		}
		arms := n.AnyOf
		if len(n.OneOf) > 0 {
			arms = n.OneOf
		}
		if hasNullArm(arms) {
			return n
		}
		widened := append(append([]*IRNode(nil), arms...), newScalar("null"))
		out := n.shallowClone()
		if len(n.OneOf) > 0 {
			out.OneOf = widened
		} else {
			out.AnyOf = widened
		}
		return out
	default:
		if n.isNullType() {
			if n.Kind != KindEnum {
				return n
			}
			for _, v := range n.Enum {
				if v == nil {
					return n
				}
			}
		}
		out := n.shallowClone()
		if !n.isNullType() {
			out.Types = append(append([]string(nil), n.Types...), "null")
		}
		if n.Kind == KindEnum {
			out.Enum = appendNullValue(append([]any(nil), n.Enum...))
		}
		return out
	}
}

// Nullable returns the canonical nullable form of n (see irNullable, above, for
// the per-kind widening rules). It exists for EXTERNAL reflection code: a
// Reflector implementation in another module builds IR nodes by hand and would
// otherwise have to keep its own copy of these rules.
func Nullable(n *IRNode) *IRNode {
	return irNullable(n)
}

// hasNullArm reports whether one of arms is (or admits) the bare null type.
func hasNullArm(arms []*IRNode) bool {
	for _, a := range arms {
		if a != nil && a.isNullType() {
			return true
		}
	}
	return false
}

// forEachChild calls fn on every DIRECT child IR node, in the one traversal
// order all tree walkers share: AdditionalProperties (only when it holds an
// *IRNode — the bool form carries no child), Props in declared order, Items,
// Not, then every arm of AnyOf, OneOf, AllOf. It stops at the first non-nil
// error and returns it; a read-only walker just returns nil from fn.
//
// A typed-nil *IRNode in AdditionalProperties IS yielded (the type assertion
// admits it): validateComponent must see it to report the nil sub-schema, and
// the read-only walkers nil-check anyway.
//
// This is the CANONICAL definition of the IR's child edge set. A new
// *IRNode-carrying field must be added here — and mirrored in substituteAux
// (emit.go), which REBUILDS the tree instead of walking it and therefore
// cannot share this helper.
func (n *IRNode) forEachChild(fn func(*IRNode) error) error {
	if ap, ok := n.AdditionalProperties.(*IRNode); ok {
		if err := fn(ap); err != nil {
			return err
		}
	}
	for _, p := range n.Props {
		if err := fn(p.Schema); err != nil {
			return err
		}
	}
	if n.Items != nil {
		if err := fn(n.Items); err != nil {
			return err
		}
	}
	if n.Not != nil {
		if err := fn(n.Not); err != nil {
			return err
		}
	}
	for _, arms := range [][]*IRNode{n.AnyOf, n.OneOf, n.AllOf} {
		for _, a := range arms {
			if err := fn(a); err != nil {
				return err
			}
		}
	}
	return nil
}

// shallowClone copies the node header. Slices and maps are shared with the
// original; callers that append must copy the slice they touch first.
func (n *IRNode) shallowClone() *IRNode {
	c := *n
	return &c
}

// isNullType reports whether the node is (or admits) the bare null type.
func (n *IRNode) isNullType() bool {
	for _, t := range n.Types {
		if t == "null" {
			return true
		}
	}
	return false
}
