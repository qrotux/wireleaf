// Package reflectortest is the executable specification of the wireleaf
// reflector contract.
//
// A reflector turns Go wire structs into typed IR components
// (apidoc.Reflector). Everything downstream — components assembly, $ref
// stitching, the OpenAPI bytes — is built on the assumption that the IR it gets
// is CANONICAL: nullability expressed as type-arrays and anyOf[$ref,null],
// constraints in the typed fields, no draft-07 residue. That assumption is not
// checkable at compile time, so it lives here instead, as subtests any
// implementation can run against itself:
//
//	func TestMyReflector(t *testing.T) {
//	    reflectortest.Run(t, myreflector.New())
//	}
//
// What is checked, in the reflector's own words:
//
//   - A pointer field without omitempty is PRESENT and may be NULL: a scalar
//     widens to {"type":["T","null"]}, a struct reference becomes
//     anyOf[$ref,{"type":"null"}] — never a $ref carrying a null type of its
//     own, never a "nullable" keyword.
//   - omitempty means ABSENT, not null: the field drops out of required and its
//     schema stays un-widened.
//   - Component names default to the Go type name; an entry in overrides forces
//     the name for THAT type only. An auxiliary reached from two tops is
//     emitted once.
//   - An embedded struct without a json tag is FLATTENED into the parent; with
//     a json tag it nests.
//   - Nested named structs become components, through slices and maps too;
//     time.Time is {"type":"string","format":"date-time"}, not a component.
//   - Constraint tags land in the IR's typed fields, never in Extensions.
//   - A self-referential type is emitted once and reaches itself through a $ref.
//   - Properties keep DECLARATION order — the reason the IR carries an ordered
//     Props slice rather than a map.
//   - Every emitted component passes apidoc.ValidateComponent, the very check
//     components assembly applies at registration.
//   - The output is deterministic: the same types in a different order produce
//     the same components.
//
// Every check is available twice: as the exported Run driving *testing.T, and
// as an unexported check* core returning violations as sorted strings (which is
// how this package tests itself against a deliberately naive stub). Both drive
// the same table, so the two cannot diverge.
package reflectortest

import (
	"encoding/json"
	"fmt"
	"maps"
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/qrotux/wireleaf/apidoc"
)

// ------------------------------------------------------------------ subtest table

// subcheck is one row of the contract table. Run turns it into a t.Run; the
// self-test calls run directly — one table, so the two cannot drift.
type subcheck struct {
	name string
	run  func(r apidoc.Reflector) []string
}

// checks is the full contract table, in fixture-area order.
func checks() []subcheck {
	return []subcheck{
		{"nullability", func(r apidoc.Reflector) []string {
			return reflectThen(r, types(Nullability{}), nil, checkNullability)
		}},
		{"nullability_bytes_parity", func(r apidoc.Reflector) []string {
			return reflectThen(r, types(Nullability{}), nil, checkBytesParity)
		}},
		{"naming_and_overrides", func(r apidoc.Reflector) []string {
			return reflectThen(r, types(NamingTopA{}, NamingTopB{}), namingOverrides(), checkNaming)
		}},
		{"embedded_structs", func(r apidoc.Reflector) []string {
			return reflectThen(r, types(Embedding{}), nil, checkEmbedded)
		}},
		{"aux_collection", func(r apidoc.Reflector) []string {
			return reflectThen(r, types(AuxCollect{}), nil, checkAux)
		}},
		{"constraint_tags", func(r apidoc.Reflector) []string {
			return reflectThen(r, types(Constrained{}), nil, checkConstraints)
		}},
		{"required_semantics", func(r apidoc.Reflector) []string {
			return reflectThen(r, types(RequiredRules{}), nil, checkRequired)
		}},
		{"json_tag_rules", func(r apidoc.Reflector) []string {
			return reflectThen(r, types(JSONTags{}), nil, checkJSONTags)
		}},
		{"recursive_types", func(r apidoc.Reflector) []string {
			return reflectThen(r, types(Tree{}), nil, checkRecursive)
		}},
		{"property_order", func(r apidoc.Reflector) []string {
			return reflectThen(r, FixtureTypes(), namingOverrides(), checkPropertyOrder)
		}},
		{"ir_invariants", func(r apidoc.Reflector) []string {
			return reflectThen(r, FixtureTypes(), namingOverrides(), checkIRInvariants)
		}},
		{"canonical_ir", func(r apidoc.Reflector) []string {
			return reflectThen(r, FixtureTypes(), namingOverrides(), checkCanonical)
		}},
		{"determinism", checkDeterminism},
	}
}

// Run executes the reflector contract as named subtests. Each subtest asserts
// on the RETURNED IR (map[string]*apidoc.IRNode), never on serialized JSON:
// the contract is about IR semantics, and two different IRs can serialize to
// the same bytes only by accident.
func Run(t *testing.T, r apidoc.Reflector) {
	t.Helper()
	if r == nil {
		t.Fatalf("reflectortest.Run: nil Reflector")
	}
	for _, c := range checks() {
		t.Run(c.name, func(t *testing.T) {
			report(t, c.run(r))
		})
	}
}

// check runs the whole table and returns every violation, sorted. It is the
// core Run reports through, and the entry point of this package's self-test.
func check(r apidoc.Reflector) []string {
	var out []string
	for _, c := range checks() {
		out = append(out, c.run(r)...)
	}
	return sorted(out)
}

// ------------------------------------------------------------------ plumbing

// types is a shorthand for a list of fixture types.
func types(vals ...any) []reflect.Type {
	out := make([]reflect.Type, 0, len(vals))
	for _, v := range vals {
		out = append(out, reflect.TypeOf(v))
	}
	return out
}

// namingOverrides forces NamingTopA's component name and nothing else.
func namingOverrides() map[reflect.Type]string {
	return map[reflect.Type]string{reflect.TypeOf(NamingTopA{}): NamingOverrideName}
}

// reflectThen calls the reflector and hands the components to a check core,
// converting an error or a panic into a violation.
func reflectThen(r apidoc.Reflector, ts []reflect.Type, ov map[reflect.Type]string, core func(map[string]*apidoc.IRNode) []string) (v []string) {
	got, v := reflectComponents(r, ts, ov)
	if len(v) > 0 {
		return v
	}
	return sorted(core(got))
}

// reflectComponents is the guarded ReflectComponents call.
func reflectComponents(r apidoc.Reflector, ts []reflect.Type, ov map[reflect.Type]string) (got map[string]*apidoc.IRNode, v []string) {
	defer func() {
		if rec := recover(); rec != nil {
			got, v = nil, []string{fmt.Sprintf("reflector: ReflectComponents(%s) panicked: %v", typeNames(ts), rec)}
		}
	}()
	got, err := r.ReflectComponents(ts, ov)
	if err != nil {
		return nil, []string{fmt.Sprintf("reflector: ReflectComponents(%s) returned error: %v", typeNames(ts), err)}
	}
	if got == nil {
		return nil, []string{fmt.Sprintf("reflector: ReflectComponents(%s) returned a nil component map", typeNames(ts))}
	}
	for name, n := range got {
		if n == nil {
			return nil, []string{fmt.Sprintf("reflector: component %q is a nil *IRNode", name)}
		}
	}
	return got, nil
}

func typeNames(ts []reflect.Type) string {
	out := make([]string, 0, len(ts))
	for _, t := range ts {
		out = append(out, t.String())
	}
	return strings.Join(out, ", ")
}

// ------------------------------------------------------------------ 1. nullability

// checkNullability pins the nullability idioms field-for-field against
// apidoc.DefaultNullability. The expected verdicts are DERIVED by calling the
// policy on the fixture's own struct fields, so harness and policy are in
// parity by construction: a policy change either shows up here as a real
// disagreement with the reflector, or not at all.
func checkNullability(got map[string]*apidoc.IRNode) []string {
	v := requireComponents(got, "Nullability", "NullabilitySub")
	top, ok := got["Nullability"]
	if !ok {
		return v
	}
	v = append(v, requireAuxContents(got, "NullabilitySub", "id")...)
	rt := reflect.TypeOf(Nullability{})
	for i := 0; i < rt.NumField(); i++ {
		f := rt.Field(i)
		name := jsonName(f)
		verdict := apidoc.DefaultNullability(f)
		p, found := propOf(top, name)
		if !found {
			v = append(v, fmt.Sprintf("nullability: component %q has no property %q (properties: %s)", "Nullability", name, propNames(top)))
			continue
		}
		wantRequired := verdict != apidoc.VerdictOptional
		if p.Required != wantRequired {
			v = append(v, fmt.Sprintf("nullability: property %q is required=%v, but apidoc.DefaultNullability says %s (want required=%v)", name, p.Required, verdictName(verdict), wantRequired))
		}
		isStruct := apidoc.DerefType(f.Type) != nil && apidoc.DerefType(f.Type).Kind() == reflect.Struct
		switch {
		case verdict == apidoc.VerdictNull && isStruct:
			if !isCanonicalNullableRef(p.Schema, "NullabilitySub") {
				v = append(v, fmt.Sprintf("nullability: property %q is a non-omitempty pointer-to-struct, so it must be the canonical anyOf[$ref NullabilitySub, {\"type\":\"null\"}]; got %s", name, describe(p.Schema)))
			}
		case verdict == apidoc.VerdictNull:
			if p.Schema.Kind != apidoc.KindScalar || !typesEqual(p.Schema.Types, "string", "null") {
				v = append(v, fmt.Sprintf("nullability: property %q is a non-omitempty pointer scalar, so it must be a scalar with Types [\"string\",\"null\"]; got %s", name, describe(p.Schema)))
			}
		default:
			if admitsNull(p.Schema) {
				v = append(v, fmt.Sprintf("nullability: property %q is %s, so it must NOT admit null; got %s", name, verdictName(verdict), describe(p.Schema)))
			}
			if !isStruct && (p.Schema.Kind != apidoc.KindScalar || !typesEqual(p.Schema.Types, "string")) {
				v = append(v, fmt.Sprintf("nullability: property %q is %s, so it must be a scalar with Types [\"string\"] exactly; got %s", name, verdictName(verdict), describe(p.Schema)))
			}
			if isStruct && p.Schema.Kind != apidoc.KindRef {
				v = append(v, fmt.Sprintf("nullability: property %q is an omitempty pointer-to-struct, so it must be a BARE $ref to NullabilitySub; got %s", name, describe(p.Schema)))
			}
			if isStruct && p.Schema.Kind == apidoc.KindRef && p.Schema.Ref != "NullabilitySub" {
				v = append(v, fmt.Sprintf("nullability: property %q refs %q, want %q", name, p.Schema.Ref, "NullabilitySub"))
			}
		}
	}
	return v
}

// checkBytesParity pins the "bytes agree with the document" claim on REAL
// encoding/json output, not on the policy's word alone: a zero-valued
// Nullability is marshaled, and every field's presence/null in those bytes
// must agree BOTH with apidoc.DefaultNullability's verdict AND with the
// reflected document's required set. If encoding/json ever changes the rule
// the policy encodes (or the policy drifts), this is the check that trips.
func checkBytesParity(got map[string]*apidoc.IRNode) []string {
	v := requireComponents(got, "Nullability")
	top, ok := got["Nullability"]
	if !ok {
		return v
	}
	b, err := json.Marshal(Nullability{})
	if err != nil {
		return append(v, fmt.Sprintf("bytes-parity: marshal Nullability{}: %v", err))
	}
	var m map[string]json.RawMessage
	if err := json.Unmarshal(b, &m); err != nil {
		return append(v, fmt.Sprintf("bytes-parity: unmarshal: %v", err))
	}
	rt := reflect.TypeOf(Nullability{})
	for i := 0; i < rt.NumField(); i++ {
		f := rt.Field(i)
		name := jsonName(f)
		verdict := apidoc.DefaultNullability(f)
		raw, present := m[name]

		// bytes ↔ policy
		switch verdict {
		case apidoc.VerdictOptional:
			if present {
				v = append(v, fmt.Sprintf("bytes-parity: field %q: policy says Optional (absent) but encoding/json emitted %s", name, raw))
			}
		case apidoc.VerdictNull:
			if !present {
				v = append(v, fmt.Sprintf("bytes-parity: field %q: policy says Null (present) but encoding/json omitted it", name))
			} else if string(raw) != "null" {
				v = append(v, fmt.Sprintf("bytes-parity: field %q: zero pointer must marshal to null, got %s", name, raw))
			}
		default: // VerdictPlain
			if !present {
				v = append(v, fmt.Sprintf("bytes-parity: field %q: policy says Plain (present) but encoding/json omitted it", name))
			}
		}

		// bytes ↔ document
		p, found := propOf(top, name)
		if !found {
			continue // checkNullability reports the missing property
		}
		if present && !p.Required {
			v = append(v, fmt.Sprintf("bytes-parity: field %q is in the zero-value bytes but the document does not require it", name))
		}
		if !present && p.Required {
			v = append(v, fmt.Sprintf("bytes-parity: field %q is absent from the zero-value bytes but the document requires it", name))
		}
	}
	return v
}

func verdictName(v apidoc.Verdict) string {
	switch v {
	case apidoc.VerdictPlain:
		return "VerdictPlain"
	case apidoc.VerdictOptional:
		return "VerdictOptional"
	case apidoc.VerdictNull:
		return "VerdictNull"
	default:
		return fmt.Sprintf("Verdict(%d)", int(v))
	}
}

// ------------------------------------------------------------------ 2. naming

// checkNaming pins default naming, the blast radius of an override, and
// shared-auxiliary dedup.
func checkNaming(got map[string]*apidoc.IRNode) []string {
	v := requireComponents(got, NamingOverrideName, "NamingTopB", "SharedAux")
	v = append(v, requireAuxContents(got, "SharedAux", "v")...)
	if _, ok := got["NamingTopA"]; ok {
		v = append(v, fmt.Sprintf("naming: overrides forced NamingTopA to %q, but the component is ALSO emitted under its Go type name %q (components: %s)", NamingOverrideName, "NamingTopA", componentNames(got)))
	}
	// The override must not leak: the shared auxiliary and the un-overridden
	// top keep their Go type names.
	for _, unexpected := range []string{"RenamedTopSharedAux", "RenamedTopB"} {
		if _, ok := got[unexpected]; ok {
			v = append(v, fmt.Sprintf("naming: override leaked into an unrelated component name %q (components: %s)", unexpected, componentNames(got)))
		}
	}
	// Both tops must reach the ONE shared auxiliary, by name.
	for _, top := range []string{NamingOverrideName, "NamingTopB"} {
		n, ok := got[top]
		if !ok {
			continue
		}
		p, found := propOf(n, "shared")
		if !found {
			v = append(v, fmt.Sprintf("naming: component %q has no property %q (properties: %s)", top, "shared", propNames(n)))
			continue
		}
		if p.Schema.Kind != apidoc.KindRef || p.Schema.Ref != "SharedAux" {
			v = append(v, fmt.Sprintf("naming: component %q property %q must be a $ref to the shared auxiliary %q (emitted ONCE for both tops); got %s", top, "shared", "SharedAux", describe(p.Schema)))
		}
	}
	// A second copy of the auxiliary under a decorated name is the classic
	// dedup failure.
	for name := range got {
		if name != "SharedAux" && strings.Contains(name, "SharedAux") {
			v = append(v, fmt.Sprintf("naming: auxiliary %q is emitted twice — %q is a duplicate of the shared component (components: %s)", "SharedAux", name, componentNames(got)))
		}
	}
	return v
}

// ------------------------------------------------------------------ 3. embedding

// checkEmbedded pins the two embedding rules: a plain embed flattens, a
// json-tagged embed nests.
func checkEmbedded(got map[string]*apidoc.IRNode) []string {
	v := requireComponents(got, "Embedding")
	top, ok := got["Embedding"]
	if !ok {
		return v
	}
	if _, found := propOf(top, "baseField"); !found {
		v = append(v, fmt.Sprintf("embedded: an embedded struct WITHOUT a json tag must be flattened, so component %q needs the promoted property %q (properties: %s)", "Embedding", "baseField", propNames(top)))
	}
	for _, leaked := range []string{"EmbeddedBase", "embeddedBase"} {
		if _, found := propOf(top, leaked); found {
			v = append(v, fmt.Sprintf("embedded: component %q carries a property %q — the plain embed must be flattened, not nested (properties: %s)", "Embedding", leaked, propNames(top)))
		}
	}
	if _, found := propOf(top, "own"); !found {
		v = append(v, fmt.Sprintf("embedded: component %q has no property %q (properties: %s)", "Embedding", "own", propNames(top)))
	}
	// The json-tagged embed nests: the property is there, and resolving it
	// (through a $ref, if that is how the reflector emits it) yields the
	// embedded struct's own field.
	p, found := propOf(top, "tagged")
	if !found {
		v = append(v, fmt.Sprintf("embedded: an embedded struct WITH a json tag must NEST, so component %q needs the property %q (properties: %s)", "Embedding", "tagged", propNames(top)))
		return v
	}
	if _, flat := propOf(top, "taggedField"); flat {
		v = append(v, fmt.Sprintf("embedded: component %q flattened the json-tagged embed: %q must live under %q, not at the top level (properties: %s)", "Embedding", "taggedField", "tagged", propNames(top)))
	}
	target := resolve(got, p.Schema)
	if target == nil || target.Kind != apidoc.KindObject {
		v = append(v, fmt.Sprintf("embedded: property %q must resolve to the nested object for the tagged embed; got %s", "tagged", describe(p.Schema)))
		return v
	}
	if _, ok := propOf(target, "taggedField"); !ok {
		v = append(v, fmt.Sprintf("embedded: the nested object behind property %q has no property %q (properties: %s)", "tagged", "taggedField", propNames(target)))
	}
	// Flattening is an ORDER rule as much as a membership rule: the promoted
	// field takes the embedded field's PLACE, it is not appended at the end.
	v = append(v, wantOrder("embedded", "Embedding", top, embeddingOrder)...)
	return v
}

// ------------------------------------------------------------------ 4. aux collection

// checkAux pins auxiliary collection through containers, dedup of a type
// reached three ways, and the time.Time carve-out.
func checkAux(got map[string]*apidoc.IRNode) []string {
	v := requireComponents(got, "AuxCollect", "AuxInner")
	v = append(v, requireAuxContents(got, "AuxInner", "n")...)
	top, ok := got["AuxCollect"]
	if !ok {
		return v
	}
	// A nested named struct is its own component, referenced by name.
	if p, found := propOf(top, "one"); !found {
		v = append(v, fmt.Sprintf("aux: component %q has no property %q (properties: %s)", "AuxCollect", "one", propNames(top)))
	} else if p.Schema.Kind != apidoc.KindRef || p.Schema.Ref != "AuxInner" {
		v = append(v, fmt.Sprintf("aux: property %q must be a $ref to the nested struct component %q; got %s", "one", "AuxInner", describe(p.Schema)))
	}
	// Slices of structs collect the element type.
	if p, found := propOf(top, "list"); !found {
		v = append(v, fmt.Sprintf("aux: component %q has no property %q (properties: %s)", "AuxCollect", "list", propNames(top)))
	} else if p.Schema.Kind != apidoc.KindArray || p.Schema.Items == nil ||
		p.Schema.Items.Kind != apidoc.KindRef || p.Schema.Items.Ref != "AuxInner" {
		v = append(v, fmt.Sprintf("aux: property %q must be an array whose items are a $ref to %q (a slice of structs collects its element component); got %s", "list", "AuxInner", describe(p.Schema)))
	}
	// Maps of structs collect the value type through additionalProperties.
	if p, found := propOf(top, "byKey"); !found {
		v = append(v, fmt.Sprintf("aux: component %q has no property %q (properties: %s)", "AuxCollect", "byKey", propNames(top)))
	} else {
		ap, _ := p.Schema.AdditionalProperties.(*apidoc.IRNode)
		if p.Schema.Kind != apidoc.KindObject || ap == nil || ap.Kind != apidoc.KindRef || ap.Ref != "AuxInner" {
			v = append(v, fmt.Sprintf("aux: property %q must be an object whose additionalProperties are a $ref to %q (a map of structs collects its value component); got %s", "byKey", "AuxInner", describe(p.Schema)))
		}
	}
	// time.Time is a formatted string, NOT a component.
	if p, found := propOf(top, "when"); !found {
		v = append(v, fmt.Sprintf("aux: component %q has no property %q (properties: %s)", "AuxCollect", "when", propNames(top)))
	} else if p.Schema.Kind != apidoc.KindScalar || !typesEqual(p.Schema.Types, "string") || p.Schema.Format != "date-time" {
		v = append(v, fmt.Sprintf("aux: property %q must be {\"type\":\"string\",\"format\":\"date-time\"} — time.Time is never a component; got %s", "when", describe(p.Schema)))
	}
	for _, forbidden := range []string{"Time", "time.Time"} {
		if _, ok := got[forbidden]; ok {
			v = append(v, fmt.Sprintf("aux: time.Time was emitted as the component %q — it must render inline as a formatted string (components: %s)", forbidden, componentNames(got)))
		}
	}
	// One type reached three ways is still one component.
	for name := range got {
		if name != "AuxInner" && strings.Contains(name, "AuxInner") {
			v = append(v, fmt.Sprintf("aux: %q is a duplicate of the component %q — a type reached through a field, a slice and a map is emitted ONCE (components: %s)", name, "AuxInner", componentNames(got)))
		}
	}
	return v
}

// ------------------------------------------------------------------ 5. constraints

// checkConstraints pins the constraint-tag flow: every jsonschema-go native tag
// lands in the IR's typed field, never in Extensions.
func checkConstraints(got map[string]*apidoc.IRNode) []string {
	v := requireComponents(got, "Constrained")
	top, ok := got["Constrained"]
	if !ok {
		return v
	}
	prop := func(name string) *apidoc.IRNode {
		p, found := propOf(top, name)
		if !found {
			v = append(v, fmt.Sprintf("constraints: component %q has no property %q (properties: %s)", "Constrained", name, propNames(top)))
			return nil
		}
		return p.Schema
	}
	if n := prop("count"); n != nil {
		v = append(v, wantFloat(n, "count", "minimum", n.Minimum, 0)...)
		v = append(v, wantFloat(n, "count", "maximum", n.Maximum, 10)...)
	}
	if n := prop("name"); n != nil {
		v = append(v, wantInt(n, "name", "minLength", n.MinLength, 1)...)
		v = append(v, wantInt(n, "name", "maxLength", n.MaxLength, 10)...)
		if n.Pattern != "^[a-z]+$" {
			v = append(v, fmt.Sprintf("constraints: property %q must carry pattern %q in IRNode.Pattern, got %q", "name", "^[a-z]+$", n.Pattern))
		}
	}
	if n := prop("email"); n != nil && n.Format != "email" {
		v = append(v, fmt.Sprintf("constraints: property %q must carry format %q in IRNode.Format, got %q", "email", "email", n.Format))
	}
	if n := prop("tags"); n != nil {
		v = append(v, wantInt(n, "tags", "minItems", n.MinItems, 1)...)
		v = append(v, wantInt(n, "tags", "maxItems", n.MaxItems, 5)...)
	}
	if n := prop("status"); n != nil {
		if n.Kind != apidoc.KindEnum || !enumEquals(n.Enum, "active", "inactive") {
			v = append(v, fmt.Sprintf("constraints: property %q must be a KindEnum node with Enum [\"active\",\"inactive\"] in IRNode.Enum; got %s", "status", describe(n)))
		}
	}
	// A tag that ended up in Extensions is the other half of this area: it would
	// serialize alongside — or instead of — the typed field. apidoc's own
	// validator already rejects a standard keyword riding in Extensions, so the
	// rule is asked of apidoc rather than restated here.
	if err := apidoc.ValidateComponent("Constrained", top); err != nil {
		v = append(v, fmt.Sprintf("constraints: component %q does not satisfy apidoc.ValidateComponent (a constraint tag riding in Extensions instead of its typed field is the usual cause): %v", "Constrained", err))
	}
	return v
}

func wantFloat(n *apidoc.IRNode, prop, keyword string, got *float64, want float64) []string {
	if got == nil {
		return []string{fmt.Sprintf("constraints: property %q must carry %s=%v in the typed IRNode field, but it is nil; got %s", prop, keyword, want, describe(n))}
	}
	if *got != want {
		return []string{fmt.Sprintf("constraints: property %q has %s=%v, want %v", prop, keyword, *got, want)}
	}
	return nil
}

func wantInt(n *apidoc.IRNode, prop, keyword string, got *int, want int) []string {
	if got == nil {
		return []string{fmt.Sprintf("constraints: property %q must carry %s=%d in the typed IRNode field, but it is nil; got %s", prop, keyword, want, describe(n))}
	}
	if *got != want {
		return []string{fmt.Sprintf("constraints: property %q has %s=%d, want %d", prop, keyword, *got, want)}
	}
	return nil
}

func enumEquals(enum []any, want ...string) bool {
	if len(enum) != len(want) {
		return false
	}
	for i, w := range want {
		s, ok := enum[i].(string)
		if !ok || s != w {
			return false
		}
	}
	return true
}

// ------------------------------------------------------------------ 6. required

// checkRequired pins the required rule in isolation. Like checkNullability it
// DERIVES its expectation from apidoc.DefaultNullability rather than restating
// it: required == the policy did not say VerdictOptional. Whatever the policy
// counts as an omit option (today omitempty and omitzero) is therefore covered
// the moment the fixture carries a field tagged with it.
func checkRequired(got map[string]*apidoc.IRNode) []string {
	v := requireComponents(got, "RequiredRules")
	top, ok := got["RequiredRules"]
	if !ok {
		return v
	}
	rt := reflect.TypeOf(RequiredRules{})
	for i := 0; i < rt.NumField(); i++ {
		f := rt.Field(i)
		name := jsonName(f)
		verdict := apidoc.DefaultNullability(f)
		want := verdict != apidoc.VerdictOptional
		p, found := propOf(top, name)
		if !found {
			v = append(v, fmt.Sprintf("required: component %q has no property %q (properties: %s)", "RequiredRules", name, propNames(top)))
			continue
		}
		if p.Required != want {
			v = append(v, fmt.Sprintf("required: property %q is tagged json:%q, which apidoc.DefaultNullability calls %s, but Prop.Required is %v (want %v)", name, f.Tag.Get("json"), verdictName(verdict), p.Required, want))
		}
	}
	return v
}

// ------------------------------------------------------------------ 8. recursion

// checkRecursive pins the self-referential shape: one component, reached from
// itself through a $ref. A reflector that inlines instead never terminates, and
// one that emits a decorated second copy breaks $ref identity downstream.
func checkRecursive(got map[string]*apidoc.IRNode) []string {
	v := requireComponents(got, "Tree")
	for name := range got {
		if name != "Tree" {
			v = append(v, fmt.Sprintf("recursion: reflecting Tree emitted the extra component %q — a self-referential type is ONE component (components: %s)", name, componentNames(got)))
		}
	}
	top, ok := got["Tree"]
	if !ok {
		return v
	}
	if _, found := propOf(top, "id"); !found {
		v = append(v, fmt.Sprintf("recursion: component %q has no property %q (properties: %s)", "Tree", "id", propNames(top)))
	}
	p, found := propOf(top, "kids")
	if !found {
		v = append(v, fmt.Sprintf("recursion: component %q has no property %q (properties: %s)", "Tree", "kids", propNames(top)))
		return v
	}
	if p.Schema.Kind != apidoc.KindArray || p.Schema.Items == nil ||
		p.Schema.Items.Kind != apidoc.KindRef || p.Schema.Items.Ref != "Tree" {
		v = append(v, fmt.Sprintf("recursion: property %q must be an array whose items are a $ref back to %q — the self-reference cannot be inlined; got %s", "kids", "Tree", describe(p.Schema)))
	}
	if err := apidoc.ValidateComponent("Tree", top); err != nil {
		v = append(v, fmt.Sprintf("recursion: component %q does not satisfy apidoc.ValidateComponent: %v", "Tree", err))
	}
	return v
}

// ------------------------------------------------------------------ order

// checkPropertyOrder pins DECLARATION order for every fixture component. This
// is the check a reflector that walks a map instead of the struct's fields
// fails: it produces a correct-looking document whose properties reshuffle,
// which makes every diff of the generated spec unreadable.
func checkPropertyOrder(got map[string]*apidoc.IRNode) []string {
	var v []string
	for _, name := range componentList(got) {
		want, pinned := propertyOrder[name]
		if !pinned {
			continue
		}
		v = append(v, wantOrder("property order", name, got[name], want)...)
	}
	return v
}

// wantOrder compares an object node's property names against the expected
// order, distinguishing "wrong members" from "right members, wrong order" —
// alphabetization is the failure mode this exists to name.
func wantOrder(area, name string, n *apidoc.IRNode, want []string) []string {
	if n == nil {
		return nil
	}
	gotNames := make([]string, 0, len(n.Props))
	for _, p := range n.Props {
		gotNames = append(gotNames, p.Name)
	}
	if typesEqual(gotNames, want...) {
		return nil
	}
	if sameSet(gotNames, want) {
		return []string{fmt.Sprintf("%s: component %q has the right properties in the WRONG order: got %s, want [%s] (declaration order — Props is an ordered slice on purpose)", area, name, propNames(n), strings.Join(want, " "))}
	}
	return []string{fmt.Sprintf("%s: component %q has properties %s, want [%s]", area, name, propNames(n), strings.Join(want, " "))}
}

func sameSet(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	x, y := append([]string(nil), a...), append([]string(nil), b...)
	slices.Sort(x)
	slices.Sort(y)
	return typesEqual(x, y...)
}

// ------------------------------------------------------------------ invariants

// checkIRInvariants runs apidoc's OWN component validator over every emitted
// component. It is the cheapest possible early warning: the same walk runs
// inside Components.Add, where a violation (an object with a nil Props slice, a
// ref carrying a type, additionalProperties of the wrong Go type) surfaces as a
// registration error or a mid-encode surprise, far from the reflector that
// caused it.
func checkIRInvariants(got map[string]*apidoc.IRNode) []string {
	var v []string
	for _, name := range componentList(got) {
		if err := apidoc.ValidateComponent(name, got[name]); err != nil {
			v = append(v, fmt.Sprintf("IR invariants: component %q does not satisfy apidoc.ValidateComponent: %v", name, err))
		}
	}
	return v
}

// ------------------------------------------------------------------ 7. json tags

// checkJSONTags pins the three json-tag rules.
func checkJSONTags(got map[string]*apidoc.IRNode) []string {
	v := requireComponents(got, "JSONTags")
	top, ok := got["JSONTags"]
	if !ok {
		return v
	}
	if _, found := propOf(top, "renamed_name"); !found {
		v = append(v, fmt.Sprintf("json tags: a json tag renames the property, so component %q needs %q (properties: %s)", "JSONTags", "renamed_name", propNames(top)))
	}
	if _, found := propOf(top, "Renamed"); found {
		v = append(v, fmt.Sprintf("json tags: component %q kept the Go field name %q for a field tagged json:%q (properties: %s)", "JSONTags", "Renamed", "renamed_name", propNames(top)))
	}
	for _, skipped := range []string{"Skipped", "skipped", "-"} {
		if _, found := propOf(top, skipped); found {
			v = append(v, fmt.Sprintf("json tags: a field tagged json:\"-\" is not serialized, so component %q must not carry the property %q (properties: %s)", "JSONTags", skipped, propNames(top)))
		}
	}
	if _, found := propOf(top, "Untagged"); !found {
		v = append(v, fmt.Sprintf("json tags: an untagged exported field keeps its Go field name, so component %q needs %q (properties: %s)", "JSONTags", "Untagged", propNames(top)))
	}
	return v
}

// ------------------------------------------------------------------ canonical IR

// legacyExtensionKeys are the keys that must NEVER appear in Extensions:
// "nullable" (and its vendored spelling) is draft-07/OAS-3.0 residue that OAS
// 3.1 replaced with the null TYPE, and a standard keyword riding in Extensions
// would fight the typed field at serialization.
var legacyExtensionKeys = map[string]bool{
	"nullable":   true,
	"x-nullable": true,
}

// checkCanonical walks every node of every component and enforces what is
// canonical ON TOP of apidoc's own invariants (which the ir_invariants area
// asks apidoc itself about, rather than mirroring its keyword list here): no
// draft-07 nullability residue, a scalar or enum node always carrying its type,
// and no dangling $ref.
func checkCanonical(got map[string]*apidoc.IRNode) []string {
	var v []string
	for _, name := range componentList(got) {
		walk(got[name], name, func(n *apidoc.IRNode, path string) {
			for _, k := range slices.Sorted(maps.Keys(n.Extensions)) {
				if legacyExtensionKeys[k] {
					v = append(v, fmt.Sprintf("canonical IR: %s carries the draft-07 keyword %q in Extensions — OAS 3.1 expresses nullability as the null TYPE ({\"type\":[\"T\",\"null\"]}) or anyOf[$ref,{\"type\":\"null\"}]", path, k))
				}
			}
			switch n.Kind {
			case apidoc.KindScalar, apidoc.KindEnum:
				if len(n.Types) == 0 {
					v = append(v, fmt.Sprintf("canonical IR: %s is a %s node with an empty Types — a scalar always carries its type", path, n.Kind))
				}
			case apidoc.KindRef:
				if _, ok := got[n.Ref]; !ok {
					v = append(v, fmt.Sprintf("canonical IR: %s references the component %q, which was not emitted (components: %s)", path, n.Ref, componentNames(got)))
				}
			}
		})
	}
	return v
}

// ------------------------------------------------------------------ determinism

// checkDeterminism reflects the same fixture set twice, the second time in
// reverse order, and requires the two component maps to be identical. Input
// order is an accident of the caller; the document must not depend on it.
func checkDeterminism(r apidoc.Reflector) []string {
	ts := FixtureTypes()
	first, v := reflectComponents(r, ts, namingOverrides())
	if len(v) > 0 {
		return v
	}
	rev := make([]reflect.Type, len(ts))
	for i, t := range ts {
		rev[len(ts)-1-i] = t
	}
	second, v := reflectComponents(r, rev, namingOverrides())
	if len(v) > 0 {
		return v
	}
	var out []string
	if a, b := componentNames(first), componentNames(second); a != b {
		out = append(out, fmt.Sprintf("determinism: reflecting the same types in reverse order produced a different component set: %s then %s", a, b))
		return sorted(out)
	}
	for _, name := range componentList(first) {
		if !reflect.DeepEqual(first[name], second[name]) {
			out = append(out, fmt.Sprintf("determinism: component %q differs between two calls that only reordered the input types", name))
		}
	}
	return sorted(out)
}

// ------------------------------------------------------------------ IR helpers

// requireComponents reports every named component that is missing.
func requireComponents(got map[string]*apidoc.IRNode, names ...string) []string {
	var v []string
	for _, name := range names {
		if _, ok := got[name]; !ok {
			v = append(v, fmt.Sprintf("components: expected a component named %q (components: %s)", name, componentNames(got)))
		}
	}
	return v
}

// requireAuxContents checks that an auxiliary component really carries its own
// field. Without it a reflector that emits an EMPTY placeholder component
// satisfies every $ref assertion in the suite while documenting nothing.
func requireAuxContents(got map[string]*apidoc.IRNode, comp, prop string) []string {
	n, ok := got[comp]
	if !ok {
		return nil // a missing component is already reported by requireComponents
	}
	if _, found := propOf(n, prop); !found {
		return []string{fmt.Sprintf("components: auxiliary component %q must carry its own property %q — an empty placeholder component documents nothing (properties: %s)", comp, prop, propNames(n))}
	}
	return nil
}

// propOf returns the named property of an object node.
func propOf(n *apidoc.IRNode, name string) (apidoc.Prop, bool) {
	if n == nil {
		return apidoc.Prop{}, false
	}
	for _, p := range n.Props {
		if p.Name == name {
			return p, true
		}
	}
	return apidoc.Prop{}, false
}

// resolve follows a $ref to the component it names; every other node is
// returned as-is.
func resolve(got map[string]*apidoc.IRNode, n *apidoc.IRNode) *apidoc.IRNode {
	if n != nil && n.Kind == apidoc.KindRef {
		return got[n.Ref]
	}
	return n
}

// isCanonicalNullableRef reports whether n is exactly anyOf[$ref name,
// {"type":"null"}] — the ONE encoding of a nullable component reference.
func isCanonicalNullableRef(n *apidoc.IRNode, name string) bool {
	if n == nil || n.Kind != apidoc.KindCombinator || len(n.AnyOf) != 2 {
		return false
	}
	ref, null := n.AnyOf[0], n.AnyOf[1]
	if ref == nil || null == nil {
		return false
	}
	return ref.Kind == apidoc.KindRef && ref.Ref == name &&
		null.Kind == apidoc.KindScalar && typesEqual(null.Types, "null")
}

// admitsNull reports whether n permits the null value, in any canonical shape.
func admitsNull(n *apidoc.IRNode) bool {
	if n == nil {
		return false
	}
	for _, t := range n.Types {
		if t == "null" {
			return true
		}
	}
	for _, arms := range [][]*apidoc.IRNode{n.AnyOf, n.OneOf} {
		for _, a := range arms {
			if admitsNull(a) {
				return true
			}
		}
	}
	if n.Kind == apidoc.KindEnum {
		for _, e := range n.Enum {
			if e == nil {
				return true
			}
		}
	}
	return false
}

func typesEqual(got []string, want ...string) bool {
	if len(got) != len(want) {
		return false
	}
	for i := range want {
		if got[i] != want[i] {
			return false
		}
	}
	return true
}

// walk visits n and every node reachable from it WITHOUT following $refs
// (components are walked from their own root instead), passing a readable path.
// A shared or self-referential NODE pointer is visited once: a reflector that
// builds a pointer cycle in-tree must be reported, not hang the harness.
func walk(n *apidoc.IRNode, path string, fn func(*apidoc.IRNode, string)) {
	walkSeen(n, path, fn, map[*apidoc.IRNode]bool{})
}

func walkSeen(n *apidoc.IRNode, path string, fn func(*apidoc.IRNode, string), seen map[*apidoc.IRNode]bool) {
	if n == nil || seen[n] {
		return
	}
	seen[n] = true
	fn(n, path)
	for _, p := range n.Props {
		walkSeen(p.Schema, path+"."+p.Name, fn, seen)
	}
	walkSeen(n.Items, path+".items", fn, seen)
	walkSeen(n.Not, path+".not", fn, seen)
	for i, a := range n.AnyOf {
		walkSeen(a, fmt.Sprintf("%s.anyOf[%d]", path, i), fn, seen)
	}
	for i, a := range n.OneOf {
		walkSeen(a, fmt.Sprintf("%s.oneOf[%d]", path, i), fn, seen)
	}
	for i, a := range n.AllOf {
		walkSeen(a, fmt.Sprintf("%s.allOf[%d]", path, i), fn, seen)
	}
	if ap, ok := n.AdditionalProperties.(*apidoc.IRNode); ok {
		walkSeen(ap, path+".additionalProperties", fn, seen)
	}
}

// describe renders a node compactly for a violation message.
func describe(n *apidoc.IRNode) string {
	if n == nil {
		return "<nil node>"
	}
	var b strings.Builder
	fmt.Fprintf(&b, "%s node", n.Kind)
	if len(n.Types) > 0 {
		fmt.Fprintf(&b, " type=%v", n.Types)
	}
	if n.Ref != "" {
		fmt.Fprintf(&b, " $ref=%q", n.Ref)
	}
	if n.Format != "" {
		fmt.Fprintf(&b, " format=%q", n.Format)
	}
	if len(n.Enum) > 0 {
		fmt.Fprintf(&b, " enum=%v", n.Enum)
	}
	if n.Items != nil {
		fmt.Fprintf(&b, " items=(%s)", describe(n.Items))
	}
	if ap, ok := n.AdditionalProperties.(*apidoc.IRNode); ok {
		fmt.Fprintf(&b, " additionalProperties=(%s)", describe(ap))
	}
	for _, arms := range []struct {
		label string
		nodes []*apidoc.IRNode
	}{{"anyOf", n.AnyOf}, {"oneOf", n.OneOf}, {"allOf", n.AllOf}} {
		if len(arms.nodes) == 0 {
			continue
		}
		parts := make([]string, 0, len(arms.nodes))
		for _, a := range arms.nodes {
			parts = append(parts, describe(a))
		}
		fmt.Fprintf(&b, " %s=[%s]", arms.label, strings.Join(parts, ", "))
	}
	if n.Kind == apidoc.KindObject {
		fmt.Fprintf(&b, " properties=%s", propNames(n))
	}
	return b.String()
}

// propNames lists an object node's property names in order.
func propNames(n *apidoc.IRNode) string {
	if n == nil {
		return "[]"
	}
	out := make([]string, 0, len(n.Props))
	for _, p := range n.Props {
		out = append(out, p.Name)
	}
	return "[" + strings.Join(out, " ") + "]"
}

// componentList returns the component names, sorted.
func componentList(got map[string]*apidoc.IRNode) []string {
	return slices.Sorted(maps.Keys(got))
}

func componentNames(got map[string]*apidoc.IRNode) string {
	return "[" + strings.Join(componentList(got), " ") + "]"
}

// jsonName is the property name encoding/json gives a field.
func jsonName(f reflect.StructField) string {
	tag := f.Tag.Get("json")
	if tag == "" {
		return f.Name
	}
	name := tag
	if i := strings.Index(tag, ","); i >= 0 {
		name = tag[:i]
	}
	if name == "" {
		return f.Name
	}
	return name
}

// ------------------------------------------------------------------ reporting

// report fails t with one Errorf per violation.
func report(t *testing.T, violations []string) {
	t.Helper()
	for _, v := range violations {
		t.Errorf("%s", v)
	}
}

// sorted sorts v in place and returns it, for deterministic violation output.
// Every caller owns the slice it passes, so in-place is safe.
func sorted(v []string) []string {
	slices.Sort(v)
	return v
}
