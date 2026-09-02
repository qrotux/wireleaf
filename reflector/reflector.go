// Package reflector is wireleaf's canonical struct reflector: it turns Go wire
// structs into typed apidoc IR components.
//
// swaggest/jsonschema-go drives the struct walk and the constraint-tag parsing;
// it is a HIDDEN engine — no jsonschema type appears in any exported identifier
// here, and its draft-07 idioms are normalized to OAS 3.1 at the boundary:
//
//   - nullability is decided by the apidoc.NullabilityPolicy, never by the
//     engine: a nullable scalar widens to {"type":["T","null"]}, a nullable
//     component reference becomes anyOf[$ref,{"type":"null"}], and the keyword
//     "nullable" is never emitted;
//   - required is the policy's verdict too (absence of an omit option), not the
//     engine's `required` tag;
//   - property ORDER is the struct's declaration order (embedded structs
//     flattened in place), recovered by walking the Go type in parallel with the
//     engine's property map;
//   - a standard keyword the IR does not model is a conversion ERROR naming the
//     keyword, never a silent drop; "x-" keys land in Extensions. The one
//     exception is a TYPELESS schema (interface{}, json.RawMessage), which has
//     no IR kind at all: it folds into a single Opaque node whose bytes are the
//     whole fragment (plan decision #9's escape hatch, taken deliberately).
//
// The package passes apidoc/reflectortest in full.
package reflector

import (
	"encoding/json"
	"fmt"
	"maps"
	"reflect"
	"slices"
	"strings"

	"github.com/swaggest/jsonschema-go"

	"github.com/qrotux/wireleaf/apidoc"
)

// defPrefix is the engine-side definitions prefix. It never reaches the IR: a
// $ref carries the bare component name (apidoc.RefPrefix is applied by the
// serializer).
const defPrefix = "#/definitions/"

// Reflector renders Go wire structs into typed IR components.
//
// The zero value is ready to use and applies apidoc.DefaultNullability. It is
// stateless between calls: every ReflectComponents call builds its own engine.
type Reflector struct {
	// Nullability decides, per struct field, whether the field is required and
	// whether it admits null. nil means apidoc.DefaultNullability.
	Nullability apidoc.NullabilityPolicy
}

var _ apidoc.Reflector = (*Reflector)(nil)

// policy returns the effective nullability policy.
func (r *Reflector) policy() apidoc.NullabilityPolicy {
	if r != nil && r.Nullability != nil {
		return r.Nullability
	}
	return apidoc.DefaultNullability
}

// ReflectComponents renders every requested type into a named IR component,
// alongside the auxiliary components they reach (nested structs, through slices
// and maps too), deduped by name. An entry in overrides forces the component
// name of THAT type only; every other type is named after its Go type.
//
// An ANONYMOUS struct has no Go type name, so it yields NO definition: it is
// INLINED wherever it appears, and a root anonymous type is simply absent from
// the result map (an override cannot rescue it — the engine never defines it).
// Callers that require a named component must therefore name the type.
//
// The result is independent of the order of types.
func (r *Reflector) ReflectComponents(types []reflect.Type, overrides map[reflect.Type]string) (map[string]*apidoc.IRNode, error) {
	e := &engine{
		policy:    r.policy(),
		overrides: overrides,
		defTypes:  map[string]reflect.Type{},
		defs:      map[string]jsonschema.Schema{},
	}
	if err := e.collect(types); err != nil {
		return nil, err
	}
	out := make(map[string]*apidoc.IRNode, len(e.defs))
	for name, s := range e.defs {
		n, err := e.convert(&s, e.defTypes[name], name)
		if err != nil {
			return nil, err
		}
		if err := apidoc.ValidateComponent(name, n); err != nil {
			return nil, fmt.Errorf("reflector: %w", err)
		}
		out[name] = n
	}
	return out, nil
}

// ---------------------------------------------------------------------------
// engine: the jsonschema-go pass
// ---------------------------------------------------------------------------

type engine struct {
	policy    apidoc.NullabilityPolicy
	overrides map[reflect.Type]string

	// defTypes maps a component name back to the Go type it came from — the
	// engine's property map has no order, so conversion re-walks the struct.
	defTypes map[string]reflect.Type
	// defs holds the collected engine schemas, deduped by component name.
	defs map[string]jsonschema.Schema
	// nameErr parks the first component-name collision: the engine's def-name
	// hook cannot fail, so the error is raised by collect right after.
	nameErr error
}

// collect runs the engine once per top type, gathering every definition.
func (e *engine) collect(types []reflect.Type) error {
	jr := &jsonschema.Reflector{}
	opts := []func(*jsonschema.ReflectContext){
		jsonschema.RootRef,
		jsonschema.DefinitionsPrefix(defPrefix),
		// Untagged exported fields are serialized by encoding/json, so they are
		// part of the wire shape; the engine skips them without this.
		jsonschema.ProcessWithoutTags,
		jsonschema.CollectDefinitions(func(name string, schema jsonschema.Schema) {
			if _, seen := e.defs[name]; !seen {
				e.defs[name] = schema
			}
		}),
		jsonschema.InterceptDefName(func(t reflect.Type, defaultDefName string) string {
			name := e.defName(t, defaultDefName)
			e.bindName(name, t)
			return name
		}),
		// The POLICY owns nullability (spec §5a): strip whatever the engine
		// added, then re-derive it per field during conversion.
		jsonschema.InterceptNullability(func(params jsonschema.InterceptNullabilityParams) {
			if params.Schema != nil {
				params.Schema.RemoveType(jsonschema.Null)
			}
		}),
	}
	for _, t := range types {
		st := apidoc.DerefType(t)
		if st == nil || st.Kind() != reflect.Struct {
			return fmt.Errorf("reflector: %v: only struct types can be reflected into components", t)
		}
		if _, err := jr.Reflect(reflect.New(st).Elem().Interface(), opts...); err != nil {
			return fmt.Errorf("reflector: reflecting %v: %w", st, err)
		}
		if e.nameErr != nil {
			return e.nameErr
		}
	}
	return nil
}

// bindName binds a component name to the type that claimed it. Two DIFFERENT
// types claiming one name (the same type name in two packages, or an override
// colliding with a Go type name) would silently emit one component for two
// shapes — and which one survives would depend on input order — so it is a hard
// error instead.
func (e *engine) bindName(name string, t reflect.Type) {
	prev, ok := e.defTypes[name]
	if ok && prev != t {
		if e.nameErr == nil {
			e.nameErr = fmt.Errorf("reflector: component name %q claimed by both %v and %v; use ReflectComponents' overrides to give one of them a distinct component name", name, prev, t)
		}
		return
	}
	e.defTypes[name] = t
}

// defName is the component name for a type: an override when one applies, the
// Go type name otherwise. The engine's own default is package-prefixed
// (ReflectortestSharedAux), which is not the wireleaf naming rule.
func (e *engine) defName(t reflect.Type, defaultDefName string) string {
	if name, ok := e.overrides[t]; ok && name != "" {
		return name
	}
	if n := t.Name(); n != "" {
		return n
	}
	return defaultDefName
}

// ---------------------------------------------------------------------------
// conversion: engine schema -> typed IR
// ---------------------------------------------------------------------------

// convert turns one engine schema into a typed IR node. t is the Go type the
// schema was reflected from, used to recover property order; it may be nil.
func (e *engine) convert(s *jsonschema.Schema, t reflect.Type, path string) (*apidoc.IRNode, error) {
	if s == nil {
		return nil, fmt.Errorf("reflector: %s: nil schema", path)
	}
	if err := rejectExotica(s, path); err != nil {
		return nil, err
	}
	ext, err := extensions(s, path)
	if err != nil {
		return nil, err
	}

	types := simpleTypes(s.Type)
	var n *apidoc.IRNode

	switch {
	case s.Ref != nil:
		if len(s.Properties) > 0 || s.Items != nil || len(s.Enum) > 0 || hasArms(s) {
			return nil, fmt.Errorf("reflector: %s: $ref combined with a structural keyword is not representable", path)
		}
		n = &apidoc.IRNode{Kind: apidoc.KindRef, Ref: strings.TrimPrefix(*s.Ref, defPrefix)}

	case hasArms(s):
		n, err = e.combinator(s, path)
		if err != nil {
			return nil, err
		}

	case len(s.Enum) > 0:
		if len(types) == 0 {
			return nil, fmt.Errorf("reflector: %s: enum without a type is not canonical", path)
		}
		n = &apidoc.IRNode{Kind: apidoc.KindEnum, Types: types, Enum: append([]any(nil), s.Enum...)}

	case slices.Contains(types, "object") || s.Properties != nil || s.AdditionalProperties != nil:
		n, err = e.object(s, t, types, path)
		if err != nil {
			return nil, err
		}

	case slices.Contains(types, "array") || s.Items != nil:
		n, err = e.array(s, t, types, path)
		if err != nil {
			return nil, err
		}

	case len(types) > 0:
		n = &apidoc.IRNode{Kind: apidoc.KindScalar, Types: types}

	default:
		// A typeless schema (interface{}, json.RawMessage) models "anything",
		// and the IR has no kind for that — so this is the ONE place the
		// reflector deliberately reaches for the Opaque escape hatch (plan
		// decision #9). A bare one is the empty fragment; one carrying keywords
		// folds them INTO the bytes, because the IR invariants forbid
		// annotations ON an Opaque node, which makes the fragment the only
		// representable shape. Nothing is dropped and nothing is refused.
		raw, err := opaqueBytes(s)
		if err != nil {
			return nil, fmt.Errorf("reflector: %s: %w", path, err)
		}
		// Through apidoc's own constructor, never a struct literal: it scans the
		// bytes for "$ref" so a reference made from INSIDE the fragment still
		// takes part in components assembly's dangling-ref check.
		frag, err := apidoc.OpaqueFragment(raw)
		if err != nil {
			return nil, fmt.Errorf("reflector: %s: %w", path, err)
		}
		return frag.IR(), nil
	}

	if err := e.applyAnnotations(n, s, path); err != nil {
		return nil, err
	}
	// Extensions go in through apidoc's own gate, one key at a time: it refuses
	// a standard keyword riding in Extensions and re-collects the "$ref" names
	// hiding inside extension values, which a direct map assignment would leave
	// invisible to the dangling-ref check.
	for _, k := range slices.Sorted(maps.Keys(ext)) {
		if err := n.SetExtension(k, ext[k]); err != nil {
			return nil, fmt.Errorf("reflector: %s: %w", path, err)
		}
	}
	return n, nil
}

// object builds an object node. When t is a struct the properties are ordered
// by the struct's own field order (embedded structs flattened in place) and
// their required/nullable facts come from the policy.
func (e *engine) object(s *jsonschema.Schema, t reflect.Type, types []string, path string) (*apidoc.IRNode, error) {
	n := &apidoc.IRNode{Kind: apidoc.KindObject, Types: types, Props: []apidoc.Prop{}}
	if len(n.Types) == 0 {
		n.Types = []string{"object"}
	}
	st := apidoc.DerefType(t)
	if st != nil && st.Kind() == reflect.Struct {
		for _, f := range jsonFields(st) {
			sub, ok := s.Properties[f.name]
			if !ok || sub.TypeObject == nil {
				return nil, fmt.Errorf("reflector: %s: field %s (property %q) produced no schema", path, f.field.Name, f.name)
			}
			child, err := e.convert(sub.TypeObject, f.field.Type, path+"."+f.name)
			if err != nil {
				return nil, err
			}
			verdict := e.policy(f.field)
			child = stripNull(child)
			if verdict == apidoc.VerdictNull {
				child = apidoc.Nullable(child)
			}
			n.Props = append(n.Props, apidoc.Prop{
				Name:     f.name,
				Schema:   child,
				Required: verdict != apidoc.VerdictOptional,
			})
		}
	} else if len(s.Properties) > 0 {
		return nil, fmt.Errorf("reflector: %s: properties without a struct type to order them by", path)
	}
	if s.AdditionalProperties != nil {
		switch {
		case s.AdditionalProperties.TypeBoolean != nil:
			n.AdditionalProperties = *s.AdditionalProperties.TypeBoolean
		case s.AdditionalProperties.TypeObject != nil:
			var vt reflect.Type
			if st != nil && st.Kind() == reflect.Map {
				vt = st.Elem()
			}
			ap, err := e.convert(s.AdditionalProperties.TypeObject, vt, path+".additionalProperties")
			if err != nil {
				return nil, err
			}
			n.AdditionalProperties = ap
		}
	}
	return n, nil
}

// array builds an array node.
func (e *engine) array(s *jsonschema.Schema, t reflect.Type, types []string, path string) (*apidoc.IRNode, error) {
	n := &apidoc.IRNode{Kind: apidoc.KindArray, Types: types}
	if len(n.Types) == 0 {
		n.Types = []string{"array"}
	}
	var et reflect.Type
	if dt := apidoc.DerefType(t); dt != nil && (dt.Kind() == reflect.Slice || dt.Kind() == reflect.Array) {
		et = dt.Elem()
	}
	switch {
	case s.Items != nil && len(s.Items.SchemaArray) > 0:
		return nil, fmt.Errorf("reflector: %s: tuple-form %q is not representable", path, "items")
	case s.Items == nil || s.Items.SchemaOrBool == nil || s.Items.SchemaOrBool.TypeObject == nil:
		// No usable item schema (absent, or the boolean form): the items admit
		// anything, which the IR spells as the empty Opaque fragment.
		n.Items = &apidoc.IRNode{Kind: apidoc.KindOpaque, Opaque: json.RawMessage("{}")}
	default:
		items, err := e.convert(s.Items.SchemaOrBool.TypeObject, et, path+".items")
		if err != nil {
			return nil, err
		}
		n.Items = items
	}
	return n, nil
}

// combinator builds an anyOf/oneOf/allOf node.
func (e *engine) combinator(s *jsonschema.Schema, path string) (*apidoc.IRNode, error) {
	arms := func(label string, in []jsonschema.SchemaOrBool) ([]*apidoc.IRNode, error) {
		out := make([]*apidoc.IRNode, 0, len(in))
		for i, a := range in {
			if a.TypeObject == nil {
				return nil, fmt.Errorf("reflector: %s: boolean %s arm is not representable", path, label)
			}
			n, err := e.convert(a.TypeObject, nil, fmt.Sprintf("%s.%s[%d]", path, label, i))
			if err != nil {
				return nil, err
			}
			out = append(out, n)
		}
		return out, nil
	}
	n := &apidoc.IRNode{Kind: apidoc.KindCombinator}
	groups := []struct {
		label string
		in    []jsonschema.SchemaOrBool
		dst   *[]*apidoc.IRNode
	}{
		{"anyOf", s.AnyOf, &n.AnyOf},
		{"oneOf", s.OneOf, &n.OneOf},
		{"allOf", s.AllOf, &n.AllOf},
	}
	nonEmpty := 0
	for _, g := range groups {
		if len(g.in) > 0 {
			nonEmpty++
		}
	}
	if nonEmpty > 1 {
		return nil, fmt.Errorf("reflector: %s: more than one of anyOf/oneOf/allOf is not representable", path)
	}
	for _, g := range groups {
		if len(g.in) == 0 {
			continue
		}
		a, err := arms(g.label, g.in)
		if err != nil {
			return nil, err
		}
		*g.dst = a
	}
	return n, nil
}

// ---------------------------------------------------------------------------
// canonical nullability idioms
// ---------------------------------------------------------------------------

// stripNull removes the null TYPE the engine may have attached: the policy, not
// the engine, decides nullability. The widening direction — re-adding null in
// the canonical per-kind form — is apidoc.Nullable.
//
// PRECEDENCE: the nullability policy OVERRIDES an Exposer's own declared null at
// the PROPERTY level — a field's presence/absence and its null-admission are the
// wire facts encoding/json produces, and only the policy models those.
// Element-level nulls (array items, additionalProperties, combinator arms) are
// untouched: no field verdict applies there, so the Exposer's declaration stands.
func stripNull(n *apidoc.IRNode) *apidoc.IRNode {
	if n == nil || !slices.Contains(n.Types, "null") {
		return n
	}
	c := *n
	c.Types = slices.DeleteFunc(slices.Clone(n.Types), func(t string) bool { return t == "null" })
	return &c
}

// ---------------------------------------------------------------------------
// keyword translation
// ---------------------------------------------------------------------------

// rejectExotica rejects the engine-side keywords the IR does not model. Meeting
// one is a conversion ERROR naming the keyword (typed ingress: an unmodelled
// construct is a bug to surface, not a silent drop).
func rejectExotica(s *jsonschema.Schema, path string) error {
	for _, k := range []struct {
		name string
		set  bool
	}{
		{"$id", s.ID != nil},
		{"$schema", s.Schema != nil},
		{"$comment", s.Comment != nil},
		{"additionalItems", s.AdditionalItems != nil},
		{"contains", s.Contains != nil},
		{"patternProperties", len(s.PatternProperties) > 0},
		{"dependencies", len(s.Dependencies) > 0},
		{"propertyNames", s.PropertyNames != nil},
		{"if", s.If != nil},
		{"then", s.Then != nil},
		{"else", s.Else != nil},
		{"not", s.Not != nil && s.Not.TypeObject == nil},
		{"definitions", len(s.Definitions) > 0},
	} {
		if k.set {
			return fmt.Errorf("reflector: %s: keyword %q is outside the typed IR", path, k.name)
		}
	}
	return nil
}

// opaqueBytes renders a typeless schema as the JSON fragment an Opaque node
// carries. Two normalizations happen on the way:
//
//   - every "$ref" is rewritten from the engine's "#/definitions/" location to
//     apidoc.RefPrefix, exactly as the KindRef path does — the fragment is
//     emitted verbatim, so a ref left in engine coordinates would neither
//     resolve in the document nor be recognized by the dangling-ref check;
//   - the bytes are re-marshalled from a map, because the engine's own
//     marshaller merges ExtraProperties in map-iteration order. encoding/json
//     writes map keys SORTED, which is what makes the fragment — and therefore
//     the whole component — byte-identical between two runs.
func opaqueBytes(s *jsonschema.Schema) (json.RawMessage, error) {
	raw, err := json.Marshal(s)
	if err != nil {
		return nil, fmt.Errorf("typeless schema is not serializable: %w", err)
	}
	var m map[string]any
	if err := json.Unmarshal(raw, &m); err != nil {
		return nil, fmt.Errorf("typeless schema is not serializable: %w", err)
	}
	if len(m) == 0 {
		return json.RawMessage("{}"), nil
	}
	out, err := json.Marshal(rewriteRefs(m))
	if err != nil {
		return nil, fmt.Errorf("typeless schema is not serializable: %w", err)
	}
	return out, nil
}

// rewriteRefs walks a decoded JSON value and moves every "$ref" from the
// engine's definitions location to the document's component location. Values
// that are not refs are returned untouched.
func rewriteRefs(v any) any {
	switch t := v.(type) {
	case map[string]any:
		out := make(map[string]any, len(t))
		for k, sub := range t {
			if k == "$ref" {
				if s, ok := sub.(string); ok {
					out[k] = apidoc.RefPrefix + strings.TrimPrefix(s, defPrefix)
					continue
				}
			}
			out[k] = rewriteRefs(sub)
		}
		return out
	case []any:
		out := make([]any, len(t))
		for i, sub := range t {
			out[i] = rewriteRefs(sub)
		}
		return out
	default:
		return v
	}
}

// extensions splits the engine's unmatched keys: "x-" keys are document-only
// Extensions, anything else is a keyword the IR should have modelled.
func extensions(s *jsonschema.Schema, path string) (map[string]any, error) {
	if len(s.ExtraProperties) == 0 {
		return nil, nil
	}
	out := make(map[string]any, len(s.ExtraProperties))
	for k, v := range s.ExtraProperties {
		if !strings.HasPrefix(k, "x-") {
			return nil, fmt.Errorf("reflector: %s: keyword %q is outside the typed IR", path, k)
		}
		// An extension value is arbitrary JSON and may carry a "$ref"; it is
		// rewritten into document coordinates like every other ref.
		out[k] = rewriteRefs(v)
	}
	return out, nil
}

// applyAnnotations copies the typed validation/annotation keywords across. They
// never ride in Extensions: the IR has a field for each.
func (e *engine) applyAnnotations(n *apidoc.IRNode, s *jsonschema.Schema, path string) error {
	n.Minimum = s.Minimum
	n.Maximum = s.Maximum
	n.ExclusiveMinimum = s.ExclusiveMinimum
	n.ExclusiveMaximum = s.ExclusiveMaximum
	n.MultipleOf = s.MultipleOf
	n.MaxLength = intPtr(s.MaxLength)
	n.MinLength = intVal(s.MinLength)
	n.MaxItems = intPtr(s.MaxItems)
	n.MinItems = intVal(s.MinItems)
	n.MaxProperties = intPtr(s.MaxProperties)
	n.MinProperties = intVal(s.MinProperties)
	if s.UniqueItems != nil {
		n.UniqueItems = *s.UniqueItems
	}
	n.Pattern = str(s.Pattern)
	n.Format = str(s.Format)
	n.Description = str(s.Description)
	n.Title = str(s.Title)
	n.ContentEncoding = str(s.ContentEncoding)
	n.ContentMediaType = str(s.ContentMediaType)
	if s.Const != nil {
		n.Const = *s.Const
	}
	if s.Default != nil {
		n.Default = *s.Default
	}
	if len(s.Examples) > 0 {
		n.Examples = append([]any(nil), s.Examples...)
	}
	if s.ReadOnly != nil {
		n.ReadOnly = *s.ReadOnly
	}
	if s.WriteOnly != nil {
		n.WriteOnly = *s.WriteOnly
	}
	if s.Deprecated != nil {
		n.Deprecated = *s.Deprecated
	}
	if s.Not != nil && s.Not.TypeObject != nil {
		// A "not" arm is reachable only through an Exposer; it is modelled, but
		// it carries no Go type to order properties by. It converts through THIS
		// engine (same overrides, same policy) and its errors propagate: a
		// swallowed error here would silently drop the arm and widen the schema.
		sub, err := e.convert(s.Not.TypeObject, nil, path+".not")
		if err != nil {
			return err
		}
		n.Not = sub
	}
	return nil
}

// ---------------------------------------------------------------------------
// struct walk (encoding/json rules)
// ---------------------------------------------------------------------------

// visibleField is one serialized field of a struct, in declaration order.
type visibleField struct {
	name   string
	field  reflect.StructField
	depth  int
	tagged bool
}

// jsonFields returns the fields encoding/json serializes for t, in declaration
// order, with untagged embedded structs FLATTENED IN PLACE — the promoted field
// takes the embedded field's position, it is not appended at the end.
//
// A json name declared at several embedding depths is resolved by DEPTH
// DOMINANCE, the same rule graph.Compile's deriveShape (graph/compile.go)
// mirrors — keep the two in lockstep: the unique shallowest field wins (the
// standard Base-embed-override idiom); a tie at the shallowest depth is broken
// in favour of a SINGLE json-tagged candidate; a tie that survives both rules
// makes encoding/json drop the name entirely, so it is dropped here too — the
// document must not promise a key the encoder never writes.
func jsonFields(t reflect.Type) []visibleField {
	var cands []visibleField
	collectFields(t, 0, map[reflect.Type]bool{}, &cands)

	// per name: the shallowest depth, how many candidates share it, and how many
	// of THOSE carry an explicit json tag name.
	type dominance struct{ depth, tied, tagged int }
	best := make(map[string]dominance, len(cands))
	for _, c := range cands {
		d, ok := best[c.name]
		switch {
		case !ok || c.depth < d.depth:
			d = dominance{depth: c.depth, tied: 1}
			if c.tagged {
				d.tagged = 1
			}
		case c.depth == d.depth:
			d.tied++
			if c.tagged {
				d.tagged++
			}
		}
		best[c.name] = d
	}

	out := make([]visibleField, 0, len(cands))
	for _, c := range cands {
		d := best[c.name]
		if c.depth != d.depth {
			continue // dominated by a shallower declaration: legally shadowed
		}
		if d.tied > 1 {
			if d.tagged != 1 || !c.tagged {
				continue // the tagged sibling wins, or the tie kills the name
			}
		}
		out = append(out, c)
	}
	return out
}

// collectFields appends every serialized field of t (recursing through
// flattened embeds, in place) to out, tagging each with its embedding depth.
func collectFields(t reflect.Type, depth int, inProgress map[reflect.Type]bool, out *[]visibleField) {
	if inProgress[t] {
		return // a self-embedding struct cannot terminate; stop the recursion
	}
	inProgress[t] = true
	defer delete(inProgress, t)

	for i := 0; i < t.NumField(); i++ {
		f := t.Field(i)
		tag := f.Tag.Get("json")
		if tag == "-" {
			continue
		}
		name := tagName(tag)
		if f.Anonymous && name == "" {
			if et := apidoc.DerefType(f.Type); et != nil && et.Kind() == reflect.Struct {
				collectFields(et, depth+1, inProgress, out)
				continue
			}
		}
		if f.PkgPath != "" { // unexported
			continue
		}
		tagged := name != ""
		if name == "" {
			name = f.Name
		}
		*out = append(*out, visibleField{name: name, field: f, depth: depth, tagged: tagged})
	}
}

// tagName is the property name a json tag asks for ("" when it asks for none).
func tagName(tag string) string {
	if i := strings.Index(tag, ","); i >= 0 {
		return tag[:i]
	}
	return tag
}

// ---------------------------------------------------------------------------
// one-type helpers (v0 signatures)
// ---------------------------------------------------------------------------

// Reflect reflects a single wire struct T into its flat OAS-3.1 object schema
// through the SAME pipeline ReflectComponents uses, so the two agree on shape,
// naming and nullability.
//
// It is NOT byte-identical to what the emitter writes for the same type: the
// result is carried across through apidoc.RawFragment, which orders properties
// by sorted key (a Go map has no order) and decodes every number as float64.
// Reach for ReflectComponents when declaration order or exact numeric types
// matter.
//
// Only the top-level schema of T is returned; nested struct fields reflect to
// $refs. Panics on a non-struct T or a reflection error: Reflect runs at
// registration/test time, never per-request.
func Reflect[T any]() apidoc.Schema {
	return reflectOne(reflect.TypeFor[T]())
}

// ReflectType is the reflect.Type-keyed sibling of Reflect, for callers holding
// a runtime type. Same contract and panics as Reflect.
func ReflectType(t reflect.Type) apidoc.Schema { return reflectOne(t) }

func reflectOne(orig reflect.Type) apidoc.Schema {
	t := apidoc.DerefType(orig)
	if t == nil || t.Kind() != reflect.Struct {
		panic(fmt.Sprintf("reflector: Reflect[%v]: T must be a struct", orig))
	}
	r := &Reflector{}
	out, err := r.ReflectComponents([]reflect.Type{t}, nil)
	if err != nil {
		panic(fmt.Sprintf("reflector: Reflect[%s]: %v", t, err))
	}
	// With no overrides the component name is just the Go type name; an
	// anonymous struct's "" never matches a component and panics below.
	name := t.Name()
	top, ok := out[name]
	if !ok {
		panic(fmt.Sprintf("reflector: Reflect[%s]: the top component %q was not emitted", t, name))
	}
	raw, err := json.Marshal(top)
	if err != nil {
		panic(fmt.Sprintf("reflector: Reflect[%s]: %v", t, err))
	}
	var m map[string]any
	if err := json.Unmarshal(raw, &m); err != nil {
		panic(fmt.Sprintf("reflector: Reflect[%s]: %v", t, err))
	}
	return apidoc.RawFragment(m)
}

// ---------------------------------------------------------------------------
// small helpers
// ---------------------------------------------------------------------------

// simpleTypes flattens the engine's type union into the IR's string slice.
func simpleTypes(t *jsonschema.Type) []string {
	if t == nil {
		return nil
	}
	if t.SimpleTypes != nil {
		return []string{string(*t.SimpleTypes)}
	}
	out := make([]string, 0, len(t.SliceOfSimpleTypeValues))
	for _, st := range t.SliceOfSimpleTypeValues {
		out = append(out, string(st))
	}
	return out
}

func hasArms(s *jsonschema.Schema) bool {
	return len(s.AnyOf) > 0 || len(s.OneOf) > 0 || len(s.AllOf) > 0
}

func str(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}

// intPtr narrows an optional int64 keyword to the IR's *int.
func intPtr(v *int64) *int {
	if v == nil {
		return nil
	}
	n := int(*v)
	return &n
}

// intVal narrows a NON-pointer int64 keyword. The engine models minLength,
// minItems and minProperties as plain int64, so a zero is indistinguishable
// from an absent keyword and is treated as absent (it constrains nothing).
func intVal(v int64) *int {
	if v == 0 {
		return nil
	}
	n := int(v)
	return &n
}
