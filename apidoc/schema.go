// Package apidoc is the code-first OpenAPI doc layer: struct reflection +
// enumerated representation deltas + component assembly. Wire structs own the
// field inventory; Schema deltas own the representation facts struct tags
// cannot express. The Go code is the single source of truth; the document is a
// build artifact.
//
// The package never imports a reflection library: struct rendering is reached
// through the Reflector interface, so an adapter package owns that dependency.
package apidoc

import (
	"encoding/json"
	"fmt"
	"strings"
)

// Schema is a fluent wrapper over one typed IR node (*IRNode).
// Delta methods MUTATE the underlying node and return the receiver: a Schema
// must not be shared between components (combinators like AllOf compose by
// reference; the convention is single-owner per node).
type Schema struct{ n *IRNode }

// IR exposes the underlying typed node. Doc-layer internals (component
// assembly, emit) consume this; it is the replacement for the v0 habit of
// reaching into the raw map.
func (s Schema) IR() *IRNode { return s.n }

// Map renders the schema as a generic OAS-3.1 fragment.
//
// DRIFT from v0: the returned map is a DETACHED COPY produced by a JSON round
// trip (v0 returned the live backing map, so callers could mutate through it).
// Two consequences: writes to the result are lost, and every number comes back
// as float64. Reach for IR() when you need to mutate or need exact types.
func (s Schema) Map() map[string]any { return s.n.mapAny() }

// RawFragment wraps a hand-built OAS-3.1 fragment, converting it to typed IR.
// Compose-escape of last resort: composition of ready parts only, never a new
// field inventory.
//
// Conversion rules (see fragmentToIR): a fragment made only of keywords the IR
// models is parsed into a typed node. It becomes ONE Opaque node holding the
// whole fragment verbatim when it carries either
//   - ANY standard-but-untyped keyword ("if", "patternProperties", "$defs", …)
//     or unrecognized non-"x-" key, or
//   - a COMBINATION the IR cannot keep whole: the kind dispatch picks one shape,
//     so {"$ref":…,"properties":…} or {"anyOf":…,"type":…} would lose a keyword.
//     An unmodelled combination is as opaque as an unmodelled keyword.
//
// A "$ref" alongside pure annotations (description, title, validations, "x-")
// still parses as a Ref: nothing is lost there.
//
// Reaching for RawFragment IS the explicit escape-hatch choice, so an exotic
// fragment is legal here; the typed ingress paths (Set, reflector conversion)
// go through fragmentToIRMode in STRICT mode and reject exotica instead.
//
// Property ORDER: a Go map has none, so properties are ordered by SORTED key.
//
// Panics on an unconvertible fragment — v0's contract is panic-at-declaration,
// never a silent half-built document.
func RawFragment(m map[string]any) Schema {
	n, err := fragmentToIR(m)
	if err != nil {
		panic(err.Error())
	}
	return Schema{n: n}
}

// OpaqueFragment wraps a raw OAS-3.1 fragment VERBATIM as one opaque node: the
// bytes are validated as JSON and scanned once for "$ref" occurrences, so a
// reference made from inside the fragment still takes part in components
// assembly's dangling-ref check.
//
// It is the escape-hatch constructor of spec §5a, for a producer that already
// holds bytes and whose shape the typed IR does not model — a reflector folding
// a TYPELESS schema, say. Prefer RawFragment when a map is at hand;
// reach for this only when the bytes themselves are the fact to preserve.
//
// Unlike RawFragment it returns an error instead of panicking: its callers are
// conversion pipelines, where a bad fragment is a value to report, not a
// declaration bug to stop the process on.
func OpaqueFragment(raw json.RawMessage) (Schema, error) {
	n, err := newOpaque(raw)
	if err != nil {
		return Schema{}, err
	}
	return Schema{n: n}, nil
}

// ---------------------------------------------------------------------------
// fragment -> IR
// ---------------------------------------------------------------------------

// structuralKeywords are consumed by the kind dispatch in fragmentToIR; every
// other standard keyword is applied by applyFragmentKeywords.
var structuralKeywords = map[string]bool{
	"$ref":                 true,
	"type":                 true,
	"enum":                 true,
	"properties":           true,
	"required":             true,
	"items":                true,
	"anyOf":                true,
	"oneOf":                true,
	"allOf":                true,
	"not":                  true,
	"additionalProperties": true,
}

// kindKeywords are the structural keywords the KIND DISPATCH claims: exactly
// one branch is chosen, and every one of these keys the chosen branch does not
// claim would be silently dropped. "not" and "additionalProperties" are absent
// because they are applied to whatever node the dispatch produced, so they are
// always consumed.
var kindKeywords = map[string]bool{
	"$ref":       true,
	"type":       true,
	"enum":       true,
	"properties": true,
	"required":   true,
	"items":      true,
	"anyOf":      true,
	"oneOf":      true,
	"allOf":      true,
}

// fragmentToIR converts a generic OAS-3.1 fragment into a typed IR node,
// FORGIVING mode: anything the IR does not model becomes one Opaque node
// holding the whole fragment. This is the RawFragment path — reaching for
// RawFragment IS the explicit escape-hatch choice.
func fragmentToIR(m map[string]any) (*IRNode, error) {
	return fragmentToIRMode(m, false)
}

// fragmentToIRMode is the shared parser behind both modes. It returns an error
// rather than panicking; the facade (RawFragment, Set) supplies the panic.
func fragmentToIRMode(m map[string]any, strict bool) (*IRNode, error) {
	// bail is the "the IR does not model this" exit: opaque when forgiving, an
	// error naming the cause when strict.
	bail := func(format string, args ...any) (*IRNode, error) {
		if strict {
			return nil, fmt.Errorf("apidoc: "+format, args...)
		}
		return fragmentOpaque(m)
	}

	if len(m) == 0 {
		if strict {
			return nil, fmt.Errorf("apidoc: empty fragment has no typed encoding")
		}
		return newOpaque(json.RawMessage("{}"))
	}
	// One unmodelled key makes the WHOLE fragment opaque: a partial parse would
	// silently drop the keyword the caller reached for RawFragment to keep.
	for _, k := range sortedKeys(m) {
		if strings.HasPrefix(k, "x-") || standardKeywords[k] {
			continue
		}
		return bail("keyword %q is outside the typed set", k)
	}

	var types []string
	if tv, ok := m["type"]; ok {
		t, err := fragTypes(tv)
		if err != nil {
			return nil, err
		}
		types = t
	}
	base := ""
	for _, t := range types {
		if t != "null" {
			base = t
			break
		}
	}

	_, hasRef := m["$ref"]
	_, hasProps := m["properties"]
	_, hasItems := m["items"]
	_, hasEnum := m["enum"]
	arms := 0
	for _, k := range []string{"anyOf", "oneOf", "allOf"} {
		if _, ok := m[k]; ok {
			arms++
		}
	}

	// consumed records which kind keywords the chosen branch actually claims.
	// Whatever the branch leaves behind would be SILENTLY DROPPED, so an
	// unconsumed key makes the combination as unmodelled as an unknown keyword.
	consumed := map[string]bool{}
	claim := func(keys ...string) {
		for _, k := range keys {
			consumed[k] = true
		}
	}

	var n *IRNode
	switch {
	case arms > 1:
		return bail("fragment mixes anyOf/oneOf/allOf; the IR carries exactly one")
	case arms == 1:
		var err error
		if n, err = fragCombinator(m, strict); err != nil {
			return nil, err
		}
		claim("anyOf", "oneOf", "allOf")
	case hasRef:
		ref, ok := m["$ref"].(string)
		if !ok {
			return nil, fmt.Errorf(`apidoc: fragment "$ref" must be a string, got %T`, m["$ref"])
		}
		n = newRef(strings.TrimPrefix(ref, RefPrefix))
		claim("$ref")
	case hasProps || base == "object":
		props, err := fragProps(m, strict)
		if err != nil {
			return nil, err
		}
		n = newObject(props)
		n.Types = types
		if len(types) == 0 {
			n.Types = []string{"object"}
		}
		if err := fragApplyRequired(n, m["required"]); err != nil {
			return nil, err
		}
		claim("type", "properties", "required")
	case hasItems || base == "array":
		items, err := fragChild(m["items"], strict)
		if err != nil {
			return nil, err
		}
		if items == nil {
			// "type":"array" with no items: nothing typed to hang the array on.
			return bail(`"type":"array" without "items" has no typed encoding`)
		}
		n = newArray(items)
		if len(types) > 0 {
			n.Types = types
		}
		claim("type", "items")
	case hasEnum:
		vals, ok := m["enum"].([]any)
		if !ok {
			return nil, fmt.Errorf(`apidoc: fragment "enum" must be a []any, got %T`, m["enum"])
		}
		if len(types) == 0 {
			// An untyped enum has no base type for KindEnum to stand on.
			return bail(`"enum" without "type" has no typed encoding`)
		}
		n = &IRNode{Kind: KindEnum, Types: types, Enum: append([]any(nil), vals...)}
		claim("type", "enum")
	case len(types) > 0:
		n = newScalar(types...)
		claim("type")
	default:
		// Annotations only ("description", …): no kind fits.
		return bail("fragment carries no type, $ref, properties, items, enum or combinator")
	}

	// Unconsumed structural keys: the dispatch picked one shape, and every key
	// it did not claim would vanish. {"$ref":…,"properties":…} is not a ref, and
	// {"anyOf":…,"type":…} is not a plain anyOf.
	var leftover []string
	for _, k := range sortedKeys(m) {
		if kindKeywords[k] && !consumed[k] {
			leftover = append(leftover, k)
		}
	}
	if len(leftover) > 0 {
		return bail("fragment combines %s with %v; the IR has no encoding that keeps both",
			n.Kind, leftover)
	}

	if ap, ok := m["additionalProperties"]; ok {
		v, err := fragAdditionalProperties(ap, strict)
		if err != nil {
			return nil, err
		}
		n.AdditionalProperties = v
	}
	if not, ok := m["not"]; ok {
		child, err := fragChild(not, strict)
		if err != nil {
			return nil, err
		}
		n.Not = child
	}
	if err := applyFragmentKeywords(n, m); err != nil {
		return nil, err
	}
	if err := n.checkInvariants(); err != nil {
		return nil, err
	}
	return n, nil
}

// fragmentOpaque wraps the whole fragment verbatim. json.Marshal writes map
// keys sorted, so the bytes are deterministic.
func fragmentOpaque(m map[string]any) (*IRNode, error) {
	b, err := marshalValue(m)
	if err != nil {
		return nil, fmt.Errorf("apidoc: fragment is not JSON-encodable: %w", err)
	}
	return newOpaque(json.RawMessage(b))
}

func fragTypes(v any) ([]string, error) {
	switch t := v.(type) {
	case string:
		return []string{t}, nil
	case []string:
		return append([]string(nil), t...), nil
	case []any:
		out := make([]string, 0, len(t))
		for _, e := range t {
			s, ok := e.(string)
			if !ok {
				return nil, fmt.Errorf(`apidoc: fragment "type" list holds a %T, want string`, e)
			}
			out = append(out, s)
		}
		return out, nil
	default:
		return nil, fmt.Errorf(`apidoc: fragment "type" must be a string or a list of strings, got %T`, v)
	}
}

// fragChild parses a nested schema value: a fragment map, a Schema (spliced by
// reference) or an *IRNode.
func fragChild(v any, strict bool) (*IRNode, error) {
	switch t := v.(type) {
	case nil:
		return nil, nil
	case map[string]any:
		return fragmentToIRMode(t, strict)
	case Schema:
		if t.n == nil {
			return nil, fmt.Errorf("apidoc: nested Schema is empty")
		}
		return t.n, nil
	case *IRNode:
		return t, nil
	default:
		return nil, fmt.Errorf("apidoc: nested schema must be a fragment map or a Schema, got %T", v)
	}
}

// fragProps parses "properties" into ordered Props. A Go map has no order, so
// the ordering is by SORTED key -- deterministic, and the order the emitted
// document will carry.
func fragProps(m map[string]any, strict bool) ([]Prop, error) {
	raw, ok := m["properties"]
	if !ok {
		return []Prop{}, nil
	}
	pm, ok := raw.(map[string]any)
	if !ok {
		return nil, fmt.Errorf(`apidoc: fragment "properties" must be a map[string]any, got %T`, raw)
	}
	props := make([]Prop, 0, len(pm))
	for _, k := range sortedKeys(pm) {
		child, err := fragChild(pm[k], strict)
		if err != nil {
			return nil, fmt.Errorf("apidoc: property %q: %w", k, err)
		}
		if child == nil {
			return nil, fmt.Errorf("apidoc: property %q has a null schema", k)
		}
		props = append(props, Prop{Name: k, Schema: child})
	}
	return props, nil
}

// fragApplyRequired flips the Required flag on the named props. The IR has no
// standalone required list: it is derived from Props at serialization, in Props
// order.
func fragApplyRequired(n *IRNode, raw any) error {
	names, err := fragStrings(raw)
	if err != nil {
		return err
	}
	want := make(map[string]bool, len(names))
	for _, name := range names {
		want[name] = true
	}
	for i := range n.Props {
		if want[n.Props[i].Name] {
			n.Props[i].Required = true
			delete(want, n.Props[i].Name)
		}
	}
	if len(want) > 0 {
		return fmt.Errorf("apidoc: required names unknown property %q", sortedKeys(want)[0])
	}
	return nil
}

// fragStrings accepts nil, []string or []any-of-strings.
func fragStrings(raw any) ([]string, error) {
	switch t := raw.(type) {
	case nil:
		return nil, nil
	case []string:
		return t, nil
	case []any:
		out := make([]string, 0, len(t))
		for _, e := range t {
			s, ok := e.(string)
			if !ok {
				return nil, fmt.Errorf("apidoc: expected a string list, found a %T element", e)
			}
			out = append(out, s)
		}
		return out, nil
	default:
		return nil, fmt.Errorf("apidoc: expected a string list, got %T", raw)
	}
}

func fragCombinator(m map[string]any, strict bool) (*IRNode, error) {
	for _, k := range []string{"anyOf", "oneOf", "allOf"} {
		raw, ok := m[k]
		if !ok {
			continue
		}
		list, ok := raw.([]any)
		if !ok {
			return nil, fmt.Errorf("apidoc: fragment %q must be a []any, got %T", k, raw)
		}
		out := make([]*IRNode, 0, len(list))
		for i, e := range list {
			child, err := fragChild(e, strict)
			if err != nil {
				return nil, fmt.Errorf("apidoc: %s[%d]: %w", k, i, err)
			}
			if child == nil {
				return nil, fmt.Errorf("apidoc: %s[%d] is null", k, i)
			}
			out = append(out, child)
		}
		switch k {
		case "anyOf":
			return newAnyOf(out...), nil
		case "oneOf":
			return newOneOf(out...), nil
		default:
			return newAllOf(out...), nil
		}
	}
	return nil, fmt.Errorf("apidoc: fragment carries no combinator")
}

func fragAdditionalProperties(v any, strict bool) (any, error) {
	switch t := v.(type) {
	case bool:
		return t, nil
	default:
		child, err := fragChild(v, strict)
		if err != nil {
			return nil, fmt.Errorf(`apidoc: "additionalProperties" must be a bool or a schema: %w`, err)
		}
		return child, nil
	}
}

// applyFragmentKeywords copies every non-structural keyword into its typed
// field, and "x-" keys into Extensions.
func applyFragmentKeywords(n *IRNode, m map[string]any) error {
	for _, k := range sortedKeys(m) {
		if structuralKeywords[k] {
			continue
		}
		if err := assignKeyword(n, k, m[k]); err != nil {
			return err
		}
	}
	return nil
}
