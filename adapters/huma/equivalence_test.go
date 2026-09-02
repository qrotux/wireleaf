package huma

// equivalence_test.go — spec §7: the validation-equivalence suite.
//
// THE ONE HARD INVARIANT. For every typed validation field of the IR, an
// instance is accepted or rejected IDENTICALLY by
//
//	(a) the DOCUMENT side — the component as it is SERIALIZED into the emitted
//	    OpenAPI document, compiled by a real draft-2020-12 validator
//	    (apidoc/crosscheck, with format and content assertions ON), and
//	(b) the RUNTIME side — the same component run through the IR→huma converter
//	    (toHuma, Task 18) and validated by huma's own request validator.
//
// A divergence is a BUG in the converter or the serializer, not a test to
// weaken: it means the document promises something huma does not enforce (or
// huma rejects what the document allows). The documented exemptions live at the
// bottom of this file, each pinned by its own assertion so it cannot widen
// unnoticed:
//
//	Extensions             — document-only on BOTH sides (nothing diverges)
//	Opaque                 — spec §5a: doc enforces, huma accepts
//	unbound component      — the Task-18 serving rule: same shape as Opaque
//	lax formats            — huma's uuid/base64 checks are laxer (cited)
//	contentMediaType       — doc enforces, huma has no such field
//	undeclared dependent   — dependentRequired keyed on an undeclared property
//	readOnly / writeOnly   — huma is mode-aware, 2020-12 is annotation-only
//
// Every one of them is one-sided in the SAFE direction: the document promises at
// least as much as huma enforces, never less. The one place that was NOT true —
// huma's case-INSENSITIVE property matching, which rejects instances the
// document accepts — was fixed rather than exempted: NewConfig turns
// huma.ValidateStrictCasing on (config.go), and the Casing/* rows below pin the
// agreement.
//
// Fixtures are built through apidoc.RawFragment — the TYPED ingress path, so the
// row exercises real typed IR fields and not the Opaque escape hatch. The
// harness asserts that (runRow rejects a fixture that silently fell back to
// KindOpaque), because an opaque fixture would make the huma side a no-op and
// the row a false green.

import (
	"encoding/json"
	"reflect"
	"testing"

	humav2 "github.com/danielgtaylor/huma/v2"

	"github.com/qrotux/wireleaf/apidoc"
	"github.com/qrotux/wireleaf/apidoc/crosscheck"
	"github.com/qrotux/wireleaf/reflector"
)

// fixtureName is the component every row's fixture is registered under.
const fixtureName = "Fixture"

// eqRow is one line of the equivalence table.
type eqRow struct {
	// keyword names the IR field under test; it is what a divergence report
	// leads with.
	keyword string
	// fragment is the fixture, in document form (RawFragment converts it to
	// typed IR — the same IR the reflector would have produced).
	fragment map[string]any
	// aux are auxiliary components the fixture references, by name.
	aux map[string]map[string]any
	// auxTypes binds an auxiliary to a Go STRUCT type. Without a struct binding
	// the bridge serves a component opaque (registry.go's schemaFor SERVING
	// RULE), which would silently disable huma-side validation of a $ref arm.
	auxTypes map[string]reflect.Type
	// accept / reject are JSON instances. Both sides must agree with the label
	// AND with each other.
	accept []string
	reject []string
}

// refTarget is the Go struct an auxiliary component is bound to. Its fields are
// irrelevant: the bridge only asks whether the bound type is a struct.
type refTarget struct{}

func TestValidationEquivalence(t *testing.T) {
	// The suite validates the way an wireleaf-backed API validates: NewConfig's
	// process-global strict-casing switch is part of the runtime side's
	// definition, not an incidental setting (see the Casing/* rows).
	enableStrictCasing()
	for _, row := range equivalenceRows() {
		t.Run(row.keyword, func(t *testing.T) { runRow(t, row) })
	}
}

// runRow validates every instance of one row on BOTH sides and compares the
// verdicts against each other and against the row's own label.
func runRow(t *testing.T, row eqRow) {
	t.Helper()

	fixture := apidoc.RawFragment(row.fragment)
	if fixture.IR().Kind == apidoc.KindOpaque {
		t.Fatalf("%s: fixture fell back to Opaque IR — the row would not exercise a typed field; "+
			"fragment = %v", row.keyword, row.fragment)
	}

	// (a) document side: the serialized components, compiled for real.
	docComponents := map[string]apidoc.Schema{fixtureName: fixture}
	// (b) runtime side: the same IR through the converter, refs resolved by the
	// real bridge over the same component set.
	shared := apidoc.NewComponents()
	shared.Add(fixtureName, fixture)
	for name, frag := range row.aux {
		aux := apidoc.RawFragment(frag)
		docComponents[name] = aux
		shared.Add(name, aux)
	}
	for name, gt := range row.auxTypes {
		shared.RegisterType(gt, name)
	}

	validator, err := crosscheck.Compile(docComponents, fixtureName)
	if err != nil {
		t.Fatalf("%s: crosscheck.Compile: %v", row.keyword, err)
	}
	bridge := NewRegistry(shared, &reflector.Reflector{})
	humaSchema, err := toHuma(fixture.IR())
	if err != nil {
		t.Fatalf("%s: toHuma: %v", row.keyword, err)
	}

	check := func(instance string, want bool) {
		t.Helper()
		docOK := validator.Validate([]byte(instance)) == nil
		runtimeOK := humaAccepts(t, bridge, humaSchema, humav2.ModeWriteToServer, instance)

		if docOK != runtimeOK {
			t.Errorf("DIVERGENCE keyword=%s instance=%s: document side %s, huma side %s",
				row.keyword, instance, verdict(docOK), verdict(runtimeOK))
			return
		}
		if docOK != want {
			t.Errorf("keyword=%s instance=%s: both sides %s, want %s",
				row.keyword, instance, verdict(docOK), verdict(want))
		}
	}

	if len(row.accept) == 0 || len(row.reject) == 0 {
		t.Fatalf("%s: a row needs at least one accepting AND one rejecting instance", row.keyword)
	}
	for _, instance := range row.accept {
		check(instance, true)
	}
	for _, instance := range row.reject {
		check(instance, false)
	}
}

// humaAccepts runs huma's validator over one decoded instance.
func humaAccepts(t *testing.T, r humav2.Registry, s *humav2.Schema, mode humav2.ValidateMode, instance string) bool {
	t.Helper()
	var v any
	if err := json.Unmarshal([]byte(instance), &v); err != nil {
		t.Fatalf("instance %s is not valid JSON: %v", instance, err)
	}
	res := &humav2.ValidateResult{}
	humav2.Validate(r, s, humav2.NewPathBuffer(nil, 0), mode, v, res)
	return len(res.Errors) == 0
}

func verdict(ok bool) string {
	if ok {
		return "ACCEPTED"
	}
	return "REJECTED"
}

// frag is a fragment-building shorthand: it exists only so the table below
// reads as one row per line.
func frag(m map[string]any) map[string]any { return m }

// ---------------------------------------------------------------------------
// the table — one row per typed IR validation field, plus the structural rows
// ---------------------------------------------------------------------------

func equivalenceRows() []eqRow {
	return []eqRow{
		// --- numeric ---------------------------------------------------------
		{
			keyword:  "Minimum",
			fragment: frag(map[string]any{"type": "integer", "minimum": 5}),
			accept:   []string{`5`, `6`},
			reject:   []string{`4`},
		},
		{
			keyword:  "Maximum",
			fragment: frag(map[string]any{"type": "integer", "maximum": 10}),
			accept:   []string{`10`, `-3`},
			reject:   []string{`11`},
		},
		{
			keyword:  "ExclusiveMinimum",
			fragment: frag(map[string]any{"type": "number", "exclusiveMinimum": 0}),
			accept:   []string{`0.5`},
			reject:   []string{`0`, `-1`},
		},
		{
			keyword:  "ExclusiveMaximum",
			fragment: frag(map[string]any{"type": "number", "exclusiveMaximum": 1}),
			accept:   []string{`0.5`},
			reject:   []string{`1`, `2`},
		},
		{
			keyword:  "MultipleOf",
			fragment: frag(map[string]any{"type": "integer", "multipleOf": 3}),
			accept:   []string{`9`, `0`},
			reject:   []string{`10`},
		},

		// --- string ----------------------------------------------------------
		{
			keyword:  "MinLength",
			fragment: frag(map[string]any{"type": "string", "minLength": 3}),
			accept:   []string{`"abc"`},
			reject:   []string{`"ab"`, `""`},
		},
		{
			keyword:  "MaxLength",
			fragment: frag(map[string]any{"type": "string", "maxLength": 3}),
			accept:   []string{`"abc"`, `""`},
			reject:   []string{`"abcd"`},
		},
		{
			keyword:  "Pattern",
			fragment: frag(map[string]any{"type": "string", "pattern": "^[a-z]+$"}),
			accept:   []string{`"abc"`},
			reject:   []string{`"AB1"`, `"abc "`},
		},

		// --- array -----------------------------------------------------------
		{
			keyword: "MinItems",
			fragment: frag(map[string]any{
				"type": "array", "items": map[string]any{"type": "string"}, "minItems": 2,
			}),
			accept: []string{`["a","b"]`, `["a","b","c"]`},
			reject: []string{`["a"]`, `[]`},
		},
		{
			keyword: "MaxItems",
			fragment: frag(map[string]any{
				"type": "array", "items": map[string]any{"type": "string"}, "maxItems": 2,
			}),
			accept: []string{`["a","b"]`, `[]`},
			reject: []string{`["a","b","c"]`},
		},
		{
			keyword: "UniqueItems",
			fragment: frag(map[string]any{
				"type": "array", "items": map[string]any{"type": "string"}, "uniqueItems": true,
			}),
			accept: []string{`["a","b"]`, `[]`},
			reject: []string{`["a","a"]`},
		},

		// --- object counts ---------------------------------------------------
		{
			keyword: "MinProperties",
			fragment: frag(map[string]any{
				"type": "object",
				"properties": map[string]any{
					"a": map[string]any{"type": "string"},
					"b": map[string]any{"type": "string"},
				},
				"minProperties": 2,
			}),
			accept: []string{`{"a":"x","b":"y"}`},
			reject: []string{`{"a":"x"}`, `{}`},
		},
		{
			keyword: "MaxProperties",
			fragment: frag(map[string]any{
				"type": "object",
				"properties": map[string]any{
					"a": map[string]any{"type": "string"},
					"b": map[string]any{"type": "string"},
				},
				"maxProperties": 1,
			}),
			accept: []string{`{"a":"x"}`, `{}`},
			reject: []string{`{"a":"x","b":"y"}`},
		},

		// --- format ----------------------------------------------------------
		// Only formats BOTH sides assert are rowed. crosscheck runs with
		// AssertFormat on (draft 2020-12 would otherwise treat "format" as an
		// annotation); huma asserts the subset in its own switch
		// (validate.go:205-320 validateFormat). "date-time", "uuid" and "email"
		// are in the intersection. huma additionally checks formats the
		// 2020-12 vocabulary does NOT define — "date-time-http" (RFC1123,
		// validate.go:218) has no JSON Schema counterpart at all — so those are
		// deliberately absent from this table: they cannot be equivalent.
		{
			keyword:  "Format/date-time",
			fragment: frag(map[string]any{"type": "string", "format": "date-time"}),
			accept:   []string{`"2020-01-02T03:04:05Z"`, `"2020-01-02T03:04:05.123Z"`},
			reject:   []string{`"not-a-date"`, `"2020-01-02"`},
		},
		{
			keyword:  "Format/uuid",
			fragment: frag(map[string]any{"type": "string", "format": "uuid"}),
			accept:   []string{`"123e4567-e89b-12d3-a456-426614174000"`},
			// Only the canonical form and outright garbage are rowed: huma
			// accepts three NON-canonical spellings the 2020-12 assertion
			// rejects — see TestEquivalenceExemptionLaxFormats.
			reject: []string{`"not-a-uuid"`, `"123e4567-e89b-12d3-a456-42661417400"`},
		},
		{
			keyword:  "Format/email",
			fragment: frag(map[string]any{"type": "string", "format": "email"}),
			accept:   []string{`"user@example.com"`},
			reject:   []string{`"not-an-email"`, `"user@"`},
		},

		// --- content ---------------------------------------------------------
		// crosscheck runs with AssertContent on; huma checks base64 with
		// rxBase64 (validate.go:588-592).
		{
			keyword:  "ContentEncoding/base64",
			fragment: frag(map[string]any{"type": "string", "contentEncoding": "base64"}),
			accept:   []string{`"aGVsbG8="`},
			// "a===" is out-of-alphabet-free but undecodable; huma's shape
			// regex accepts it — see TestEquivalenceExemptionLaxFormats.
			reject: []string{`"!!!!"`, `"a b c"`},
		},

		// --- value constraints -----------------------------------------------
		{
			keyword:  "Enum",
			fragment: frag(map[string]any{"type": "string", "enum": []any{"a", "b"}}),
			accept:   []string{`"a"`, `"b"`},
			reject:   []string{`"c"`},
		},
		{
			keyword:  "Const",
			fragment: frag(map[string]any{"type": "string", "const": "fixed"}),
			accept:   []string{`"fixed"`},
			reject:   []string{`"other"`},
		},

		// --- object shape ----------------------------------------------------
		{
			keyword: "Required",
			fragment: frag(map[string]any{
				"type": "object",
				"properties": map[string]any{
					"a": map[string]any{"type": "string"},
					"b": map[string]any{"type": "string"},
				},
				"required": []any{"a"},
			}),
			accept: []string{`{"a":"x"}`, `{"a":"x","b":"y"}`},
			reject: []string{`{}`, `{"b":"y"}`},
		},
		{
			keyword: "DependentRequired",
			fragment: frag(map[string]any{
				"type": "object",
				"properties": map[string]any{
					"a": map[string]any{"type": "string"},
					"b": map[string]any{"type": "string"},
				},
				"dependentRequired": map[string][]string{"a": {"b"}},
			}),
			accept: []string{`{}`, `{"b":"y"}`, `{"a":"x","b":"y"}`},
			reject: []string{`{"a":"x"}`},
		},
		{
			keyword: "AdditionalProperties/false",
			fragment: frag(map[string]any{
				"type":                 "object",
				"properties":           map[string]any{"a": map[string]any{"type": "string"}},
				"additionalProperties": false,
			}),
			accept: []string{`{"a":"x"}`, `{}`},
			reject: []string{`{"a":"x","b":"y"}`},
		},
		{
			keyword: "AdditionalProperties/schema",
			fragment: frag(map[string]any{
				"type":                 "object",
				"properties":           map[string]any{"a": map[string]any{"type": "string"}},
				"additionalProperties": map[string]any{"type": "integer"},
			}),
			accept: []string{`{"a":"x","n":1}`, `{"n":2}`},
			reject: []string{`{"a":"x","n":"no"}`},
		},

		// --- property-name casing --------------------------------------------
		// JSON Schema matches property names byte-for-byte. huma would case-fold
		// them at its default setting, which diverges in BOTH directions; these
		// rows pin the agreement that NewConfig's ValidateStrictCasing = true
		// buys (config.go, huma validate.go:40-45).
		{
			keyword: "Casing/undeclared property is extra",
			fragment: frag(map[string]any{
				"type":       "object",
				"properties": map[string]any{"a": map[string]any{"type": "integer"}},
			}),
			// "A" is a DIFFERENT, undeclared property: with additionalProperties
			// open it is simply allowed, and its value is NOT checked against
			// "a"'s integer subschema.
			accept: []string{`{"A":"x"}`, `{"a":1,"A":"x"}`},
			reject: []string{`{"a":"x"}`},
		},
		{
			keyword: "Casing/undeclared property is refused when closed",
			fragment: frag(map[string]any{
				"type":                 "object",
				"properties":           map[string]any{"a": map[string]any{"type": "integer"}},
				"additionalProperties": false,
			}),
			accept: []string{`{"a":1}`, `{}`},
			reject: []string{`{"A":1}`, `{"a":1,"A":1}`},
		},
		{
			keyword: "Casing/required is case-sensitive",
			fragment: frag(map[string]any{
				"type":       "object",
				"properties": map[string]any{"a": map[string]any{"type": "string"}},
				"required":   []any{"a"},
			}),
			accept: []string{`{"a":"x"}`},
			reject: []string{`{"A":"x"}`},
		},

		// --- subschema recursion ---------------------------------------------
		{
			keyword: "Items/subschema",
			fragment: frag(map[string]any{
				"type":  "array",
				"items": map[string]any{"type": "string", "minLength": 3},
			}),
			accept: []string{`["abc","abcd"]`, `[]`},
			reject: []string{`["ab"]`, `["abc","ab"]`, `[1]`},
		},
		{
			keyword: "Properties/subschema",
			fragment: frag(map[string]any{
				"type": "object",
				"properties": map[string]any{
					"n":     map[string]any{"type": "integer", "minimum": 5},
					"inner": map[string]any{"type": "array", "items": map[string]any{"type": "string", "maxLength": 2}},
				},
			}),
			accept: []string{`{"n":5,"inner":["ab"]}`, `{}`},
			reject: []string{`{"n":1}`, `{"inner":["abc"]}`, `{"n":"x"}`},
		},

		// --- structural ------------------------------------------------------
		{
			keyword:  "Type/mismatch",
			fragment: frag(map[string]any{"type": "string"}),
			accept:   []string{`"x"`},
			reject:   []string{`1`, `true`, `null`, `["x"]`, `{"a":1}`},
		},
		{
			keyword: "AnyOf",
			fragment: frag(map[string]any{"anyOf": []any{
				map[string]any{"type": "string"},
				map[string]any{"type": "integer"},
			}}),
			accept: []string{`"x"`, `3`},
			reject: []string{`true`, `null`},
		},
		{
			keyword: "OneOf",
			fragment: frag(map[string]any{"oneOf": []any{
				map[string]any{"type": "string", "maxLength": 2},
				map[string]any{"type": "string", "pattern": "^a"},
			}}),
			// "bb" matches only the first arm, "abcd" only the second.
			accept: []string{`"bb"`, `"abcd"`},
			// "ab" matches BOTH (oneOf demands exactly one); "zzzz" matches neither.
			reject: []string{`"ab"`, `"zzzz"`},
		},
		{
			keyword: "AllOf",
			fragment: frag(map[string]any{"allOf": []any{
				map[string]any{"type": "string"},
				map[string]any{"type": "string", "minLength": 3},
			}}),
			accept: []string{`"abcd"`},
			reject: []string{`"ab"`, `4`},
		},
		{
			keyword: "Not",
			fragment: frag(map[string]any{
				"type": "string",
				"not":  map[string]any{"type": "string", "pattern": "^bad"},
			}),
			accept: []string{`"good"`},
			reject: []string{`"badthing"`},
		},
		{
			keyword:  "Nullable/type-array",
			fragment: frag(map[string]any{"type": []any{"string", "null"}, "minLength": 2}),
			accept:   []string{`"xy"`, `null`},
			reject:   []string{`"x"`, `1`},
		},
		{
			// THE CROWN JEWEL. anyOf[$ref, {"type":"null"}] is the canonical
			// nullable-reference idiom the reflector emits. huma's validator has
			// no "null" type case, so before Task 18's null-enum rule the null
			// arm accepted EVERY value and the whole reference went unvalidated
			// at runtime while the document still promised it. The converter now
			// emits "enum":[null] on such an arm (convert.go), which is
			// semantically identical in 2020-12 and enforced by huma's
			// membership check — so the two sides agree here.
			keyword: "Nullable/ref",
			fragment: frag(map[string]any{"anyOf": []any{
				map[string]any{"$ref": apidoc.RefPrefix + "Target"},
				map[string]any{"type": "null"},
			}}),
			aux: map[string]map[string]any{
				"Target": {
					"type":       "object",
					"properties": map[string]any{"id": map[string]any{"type": "string"}},
					"required":   []any{"id"},
				},
			},
			auxTypes: map[string]reflect.Type{"Target": reflect.TypeFor[refTarget]()},
			accept:   []string{`{"id":"x"}`, `null`},
			reject:   []string{`{}`, `{"id":1}`, `3`},
		},
	}
}

// ---------------------------------------------------------------------------
// exemptions — the places the two sides are KNOWN to disagree
// ---------------------------------------------------------------------------
//
// Each is pinned by an assertion so the deviation cannot widen unnoticed.

// TestEquivalenceExemptionExtensions pins that an "x-" annotation is
// DOCUMENT-ONLY on both sides: neither the 2020-12 validator (an unknown
// keyword is ignored) nor huma (Extensions ride through the converter as an
// inline map, never as a constraint) enforces it. Nothing diverges here — the
// row exists so "Extensions are not validation" stays a tested fact.
func TestEquivalenceExemptionExtensions(t *testing.T) {
	fixture := apidoc.RawFragment(map[string]any{
		"type":         "string",
		"x-min-words":  2,
		"x-note":       "vendor annotation, not a constraint",
		"x-forbidden":  []any{"nope"},
		"x-max-length": 1,
	})
	instance := `"nope"` // violates every x- annotation above

	validator, err := crosscheck.Compile(map[string]apidoc.Schema{fixtureName: fixture}, fixtureName)
	if err != nil {
		t.Fatalf("crosscheck.Compile: %v", err)
	}
	if err := validator.Validate([]byte(instance)); err != nil {
		t.Errorf("document side rejected %s: %v; x- annotations must not constrain", instance, err)
	}

	shared := apidoc.NewComponents()
	shared.Add(fixtureName, fixture)
	bridge := NewRegistry(shared, &reflector.Reflector{})
	humaSchema, err := toHuma(fixture.IR())
	if err != nil {
		t.Fatalf("toHuma: %v", err)
	}
	if !humaAccepts(t, bridge, humaSchema, humav2.ModeWriteToServer, instance) {
		t.Errorf("huma side rejected %s; x- annotations must not constrain", instance)
	}
}

// TestEquivalenceExemptionOpaque pins spec §5a's marked trade-off: an Opaque
// component is DOCUMENT-ONLY. The bytes reach the emitted document verbatim and
// the 2020-12 validator enforces every constraint in them, but the converter
// turns an opaque node into a zero typed Schema whose Extensions hold the whole
// fragment (convert.go's KindOpaque branch) — Type is "", so huma's validator
// falls through its type switch and accepts anything.
//
// This is the ONE place the invariant is knowingly one-sided, and it is
// one-sided in the SAFE direction: the document says more than huma enforces,
// never less.
func TestEquivalenceExemptionOpaque(t *testing.T) {
	// A fragment the typed IR cannot model ("patternProperties") carrying a real
	// constraint alongside it.
	fixture, err := apidoc.OpaqueFragment(json.RawMessage(
		`{"type":"object","patternProperties":{"^x-":{"type":"string"}},"required":["id"],"minProperties":2}`))
	if err != nil {
		t.Fatalf("OpaqueFragment: %v", err)
	}
	if fixture.IR().Kind != apidoc.KindOpaque {
		t.Fatalf("fixture is %s, want opaque", fixture.IR().Kind)
	}
	instance := `{}` // violates both "required" and "minProperties"

	validator, err := crosscheck.Compile(map[string]apidoc.Schema{fixtureName: fixture}, fixtureName)
	if err != nil {
		t.Fatalf("crosscheck.Compile: %v", err)
	}
	if err := validator.Validate([]byte(instance)); err == nil {
		t.Errorf("document side ACCEPTED %s; the opaque bytes carry real constraints", instance)
	}

	shared := apidoc.NewComponents()
	shared.Add(fixtureName, fixture)
	bridge := NewRegistry(shared, &reflector.Reflector{})
	humaSchema, err := toHuma(fixture.IR())
	if err != nil {
		t.Fatalf("toHuma: %v", err)
	}
	if !humaAccepts(t, bridge, humaSchema, humav2.ModeWriteToServer, instance) {
		t.Errorf("huma side REJECTED %s; the documented trade-off is that it accepts", instance)
	}
}

// TestEquivalenceExemptionUnboundComponent pins the SECOND, structural half of
// the same trade-off — the known Task-18 class. registry.go's schemaFor SERVING
// RULE serves a component through the Extensions bridge (opaque) whenever its Go
// wire type is unknown or is not a struct, because huma's SchemaLinkTransformer
// would otherwise panic building a wrapper struct from a nil type. A component
// registered by hand with no RegisterType is therefore UNVALIDATED at runtime
// however typed its IR is, exactly like an Opaque one — while the document still
// promises the constraint.
//
// Anything reached through a $ref to such a component inherits the exemption.
func TestEquivalenceExemptionUnboundComponent(t *testing.T) {
	loose := apidoc.RawFragment(map[string]any{"type": "integer", "minimum": 10})
	if loose.IR().Kind == apidoc.KindOpaque {
		t.Fatalf("fixture is opaque; the point of this row is a TYPED component")
	}
	instance := `1`

	validator, err := crosscheck.Compile(map[string]apidoc.Schema{"Loose": loose}, "Loose")
	if err != nil {
		t.Fatalf("crosscheck.Compile: %v", err)
	}
	if err := validator.Validate([]byte(instance)); err == nil {
		t.Errorf("document side ACCEPTED %s, want rejected by minimum", instance)
	}

	shared := apidoc.NewComponents()
	shared.Add("Loose", loose) // no RegisterType: nothing binds a Go type
	bridge := NewRegistry(shared, &reflector.Reflector{})
	served := bridge.SchemaFromRef(apidoc.RefPrefix + "Loose")
	if served == nil {
		t.Fatalf("bridge did not serve %q", "Loose")
	}
	if served.Type != "" {
		t.Fatalf("component served TYPED (type=%q) — the serving rule changed and this "+
			"exemption is stale; make it an equivalence row instead", served.Type)
	}
	if !humaAccepts(t, bridge, served, humav2.ModeWriteToServer, instance) {
		t.Errorf("huma side REJECTED %s; an unbound component is served opaque and accepts", instance)
	}
}

// TestEquivalenceExemptionLaxFormats pins two DOCUMENTED HUMA DEVIATIONS, both
// in the same safe direction as the Opaque trade-off: huma accepts a string the
// 2020-12 "uuid"/"contentEncoding" assertions reject, so the runtime is laxer
// than the document, never stricter. Neither is an wireleaf bug — the converter
// copies the keyword faithfully; the two validators simply implement the same
// keyword differently.
//
//   - "uuid": huma's validateUUID (validate.go:1102-1132) accepts the
//     unhyphenated 32-character form, the "{...}" bracketed form and the
//     "urn:uuid:" prefixed form alongside the canonical one. RFC 4122's
//     canonical spelling is what the 2020-12 format assertion checks, so only
//     that form is equivalent — and it is what wireleaf's reflector emits.
//   - "contentEncoding":"base64": huma checks the SHAPE with a regex
//     (rxBase64, validate.go:51 — `^[a-zA-Z0-9+/_-]*=*$`) rather than
//     decoding, so a string with impossible padding passes.
//
// Every instance below is one both sides WOULD have to agree on if the
// implementations matched; the assertions record that they do not.
func TestEquivalenceExemptionLaxFormats(t *testing.T) {
	cases := []struct {
		what     string
		fragment map[string]any
		instance string
	}{
		{"uuid without hyphens", map[string]any{"type": "string", "format": "uuid"},
			`"123e4567e89b12d3a456426614174000"`},
		{"uuid in braces", map[string]any{"type": "string", "format": "uuid"},
			`"{123e4567-e89b-12d3-a456-426614174000}"`},
		{"uuid with urn prefix", map[string]any{"type": "string", "format": "uuid"},
			`"urn:uuid:123e4567-e89b-12d3-a456-426614174000"`},
		{"base64 with impossible padding", map[string]any{"type": "string", "contentEncoding": "base64"},
			`"a==="`},
	}
	for _, c := range cases {
		t.Run(c.what, func(t *testing.T) {
			fixture := apidoc.RawFragment(c.fragment)
			validator, err := crosscheck.Compile(map[string]apidoc.Schema{fixtureName: fixture}, fixtureName)
			if err != nil {
				t.Fatalf("crosscheck.Compile: %v", err)
			}
			if err := validator.Validate([]byte(c.instance)); err == nil {
				t.Errorf("document side ACCEPTED %s; the 2020-12 assertion is expected to reject it "+
					"— if this now passes, promote the case to an equivalence row", c.instance)
			}
			shared := apidoc.NewComponents()
			shared.Add(fixtureName, fixture)
			bridge := NewRegistry(shared, &reflector.Reflector{})
			humaSchema, err := toHuma(fixture.IR())
			if err != nil {
				t.Fatalf("toHuma: %v", err)
			}
			if !humaAccepts(t, bridge, humaSchema, humav2.ModeWriteToServer, c.instance) {
				t.Errorf("huma side REJECTED %s; huma is documented to be laxer here "+
					"— if huma tightened, promote the case to an equivalence row", c.instance)
			}
		})
	}
}

// TestEquivalenceExemptionContentMediaType pins that "contentMediaType" is
// DOCUMENT-ONLY. huma's Schema has no such field, so the converter routes it
// into the inline Extensions map (convert.go, plan decision #11) where huma's
// validator never looks; crosscheck runs with AssertContent on and does decode
// the string as the declared media type. Safe direction — the document says more
// than huma enforces.
//
// This is a TRIPWIRE as much as a record: if huma ever grows the field, the
// assertion fails and the case is promoted to an equivalence row.
func TestEquivalenceExemptionContentMediaType(t *testing.T) {
	fixture := apidoc.RawFragment(map[string]any{
		"type":             "string",
		"contentMediaType": "application/json",
	})
	instance := `"this is not json"`

	validator, err := crosscheck.Compile(map[string]apidoc.Schema{fixtureName: fixture}, fixtureName)
	if err != nil {
		t.Fatalf("crosscheck.Compile: %v", err)
	}
	if err := validator.Validate([]byte(instance)); err == nil {
		t.Errorf("document side ACCEPTED %s; AssertContent is expected to parse the media type", instance)
	}

	shared := apidoc.NewComponents()
	shared.Add(fixtureName, fixture)
	bridge := NewRegistry(shared, &reflector.Reflector{})
	humaSchema, err := toHuma(fixture.IR())
	if err != nil {
		t.Fatalf("toHuma: %v", err)
	}
	if humaSchema.Extensions["contentMediaType"] != "application/json" {
		t.Errorf("contentMediaType did not ride through Extensions: %v", humaSchema.Extensions)
	}
	if !humaAccepts(t, bridge, humaSchema, humav2.ModeWriteToServer, instance) {
		t.Errorf("huma side REJECTED %s; huma has no contentMediaType field "+
			"— if it grew one, promote this to an equivalence row", instance)
	}
}

// TestEquivalenceExemptionUndeclaredDependentRequired pins the one case where
// "dependentRequired" is document-only. JSON Schema applies it to any property
// PRESENT in the instance; huma checks it inside its loop over the schema's
// DECLARED properties (validate.go:826-832 — the check hangs off
// s.propertyNames), so a dependency keyed on a property the schema does not
// declare is never reached. Safe direction again, and a tripwire: an equivalence
// row (DependentRequired) already covers the declared case, which is what the
// reflector emits.
func TestEquivalenceExemptionUndeclaredDependentRequired(t *testing.T) {
	fixture := apidoc.RawFragment(map[string]any{
		"type": "object",
		// "trigger" is deliberately NOT declared under "properties".
		"properties":        map[string]any{"b": map[string]any{"type": "string"}},
		"dependentRequired": map[string][]string{"trigger": {"b"}},
	})
	instance := `{"trigger":"x"}` // present trigger, missing dependency "b"

	validator, err := crosscheck.Compile(map[string]apidoc.Schema{fixtureName: fixture}, fixtureName)
	if err != nil {
		t.Fatalf("crosscheck.Compile: %v", err)
	}
	if err := validator.Validate([]byte(instance)); err == nil {
		t.Errorf("document side ACCEPTED %s; dependentRequired applies to any present property", instance)
	}

	shared := apidoc.NewComponents()
	shared.Add(fixtureName, fixture)
	bridge := NewRegistry(shared, &reflector.Reflector{})
	humaSchema, err := toHuma(fixture.IR())
	if err != nil {
		t.Fatalf("toHuma: %v", err)
	}
	if !humaAccepts(t, bridge, humaSchema, humav2.ModeWriteToServer, instance) {
		t.Errorf("huma side REJECTED %s; huma only checks dependencies of DECLARED properties "+
			"— if that changed, promote this to an equivalence row", instance)
	}
}

// TestEquivalenceReadWriteOnly is a HUMA-SIDE-ONLY row, and the last exemption.
// In draft 2020-12 "readOnly"/"writeOnly" are pure ANNOTATIONS: the document
// side is mode-blind, so "required" always binds and the two flags never do.
// huma instead makes them MODE-AWARE (validate.go:786-816) — a readOnly
// property is not required when writing to the server, and a writeOnly property
// must be absent or zero when reading from the server. There is therefore no
// equivalence to assert on these two fields; the table below pins BOTH verdicts
// per case and marks the ones that differ.
func TestEquivalenceReadWriteOnly(t *testing.T) {
	fixture := apidoc.RawFragment(map[string]any{
		"type": "object",
		"properties": map[string]any{
			"id":     map[string]any{"type": "string", "readOnly": true},
			"secret": map[string]any{"type": "string", "writeOnly": true},
		},
		"required": []any{"id"},
	})
	shared := apidoc.NewComponents()
	shared.Add(fixtureName, fixture)
	bridge := NewRegistry(shared, &reflector.Reflector{})
	humaSchema, err := toHuma(fixture.IR())
	if err != nil {
		t.Fatalf("toHuma: %v", err)
	}
	validator, err := crosscheck.Compile(map[string]apidoc.Schema{fixtureName: fixture}, fixtureName)
	if err != nil {
		t.Fatalf("crosscheck.Compile: %v", err)
	}

	cases := []struct {
		what     string
		instance string
		mode     humav2.ValidateMode
		wantHuma bool
		// wantDoc is the MODE-BLIND 2020-12 verdict: "required" always binds and
		// readOnly/writeOnly bind never. Where it differs from wantHuma the
		// difference IS the exemption, and the case says so.
		wantDoc bool
	}{
		// EXEMPT: huma waives a readOnly property's required-ness on the way in.
		{"readOnly required prop absent, writing to server",
			`{"secret":"s"}`, humav2.ModeWriteToServer, true, false},
		// Agree: reading from the server, readOnly is required on both sides.
		{"readOnly required prop absent, reading from server",
			`{"secret":""}`, humav2.ModeReadFromServer, false, false},
		{"readOnly present, reading from server",
			`{"id":"x"}`, humav2.ModeReadFromServer, true, true},
		// EXEMPT: huma refuses a non-zero writeOnly value on the way out.
		{"writeOnly non-zero, reading from server",
			`{"id":"x","secret":"s"}`, humav2.ModeReadFromServer, false, true},
		// Agree: the same instance is fine on the way in.
		{"writeOnly non-zero, writing to server",
			`{"id":"x","secret":"s"}`, humav2.ModeWriteToServer, true, true},
	}
	for _, c := range cases {
		t.Run(c.what, func(t *testing.T) {
			if got := humaAccepts(t, bridge, humaSchema, c.mode, c.instance); got != c.wantHuma {
				t.Errorf("huma side %s for %s in %v, want %s",
					verdict(got), c.instance, c.mode, verdict(c.wantHuma))
			}
			if got := validator.Validate([]byte(c.instance)) == nil; got != c.wantDoc {
				t.Errorf("document side %s for %s, want %s (readOnly/writeOnly are annotations in 2020-12)",
					verdict(got), c.instance, verdict(c.wantDoc))
			}
		})
	}
}
