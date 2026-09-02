package apidoc

import (
	"reflect"
	"testing"
)

func fixture() Schema {
	return RawFragment(map[string]any{
		"type": "object",
		"properties": map[string]any{
			"id":    map[string]any{"type": "string"},
			"count": map[string]any{"type": "integer", "format": "int64"},
			"note":  map[string]any{"type": "string"},
			"owner": map[string]any{"$ref": RefPrefix + "User"},
		},
		"required":             []any{"id", "count"},
		"additionalProperties": false,
	})
}

// TestDSLRequiredReplaces — Required REPLACES the required set.
//
// DRIFT from v0: v0 emitted the caller's argument order ([note id]); the IR
// derives `required` from the Prop.Required flags, so the list follows PROPERTY
// order. The SET is what the verb promises, and it is unchanged.
func TestDSLRequiredReplaces(t *testing.T) {
	m := fixture().Required("note", "id").Map()
	if got := m["required"]; !reflect.DeepEqual(got, []any{"id", "note"}) {
		t.Fatalf("required = %v, want [id note] (set {note,id} in property order)", got)
	}
}

// TestDSLRequireAlsoAppends — the additive counterpart of Required: the existing
// list survives (in order), new keys land at the end, and a key already required
// is not duplicated. The contrast with TestDSLRequiredReplaces above is the whole
// point: confusing the two collapses a merged required list to its last writer.
//
// DRIFT from v0: same as Required — the emitted list is in property order
// ([count id note]) rather than kept-then-appended ([id count note]). What the
// verb guarantees, that the pre-existing members survive, still holds.
func TestDSLRequireAlsoAppends(t *testing.T) {
	m := fixture().RequireAlso("note", "id").Map()
	if got := m["required"]; !reflect.DeepEqual(got, []any{"count", "id", "note"}) {
		t.Fatalf("required = %v, want [count id note] (kept {id,count} + note, no duplicate id)", got)
	}
}

// TestDSLRequireAlsoOnEmpty — a component with no required list yet.
func TestDSLRequireAlsoOnEmpty(t *testing.T) {
	s := RawFragment(map[string]any{
		"type":       "object",
		"properties": map[string]any{"id": map[string]any{"type": "string"}},
	})
	if got := s.RequireAlso("id").Map()["required"]; !reflect.DeepEqual(got, []any{"id"}) {
		t.Fatalf("required = %v, want [id]", got)
	}
}

// TestDSLRequireAlsoPanicsOnUnknownKey — same typo guard as Required.
func TestDSLRequireAlsoPanicsOnUnknownKey(t *testing.T) {
	defer func() {
		if recover() == nil {
			t.Fatal("RequireAlso on a missing property must panic")
		}
	}()
	fixture().RequireAlso("nope")
}

func TestDSLOptionalRemoves(t *testing.T) {
	m := fixture().Optional("count").Map()
	if got := m["required"]; !reflect.DeepEqual(got, []any{"id"}) {
		t.Fatalf("required = %v", got)
	}
}

func TestDSLNullableScalarAndRef(t *testing.T) {
	m := fixture().Nullable("note", "owner").Map()
	props := m["properties"].(map[string]any)
	if got := props["note"].(map[string]any)["type"]; !reflect.DeepEqual(got, []any{"string", "null"}) {
		t.Fatalf("scalar: %v", got)
	}
	if _, ok := props["owner"].(map[string]any)["anyOf"]; !ok {
		t.Fatalf("$ref must wrap into anyOf[…,null]: %v", props["owner"])
	}
}

func TestDSLNoFormat(t *testing.T) {
	m := fixture().NoFormat("count").Map()
	if _, ok := m["properties"].(map[string]any)["count"].(map[string]any)["format"]; ok {
		t.Fatalf("format survived")
	}
}

func TestDSLRefReplaces(t *testing.T) {
	m := fixture().Ref("note", "Note").Map()
	want := map[string]any{"$ref": RefPrefix + "Note"}
	if got := m["properties"].(map[string]any)["note"]; !reflect.DeepEqual(got, want) {
		t.Fatalf("got %v", got)
	}
}

func TestDSLOpenObject(t *testing.T) {
	m := fixture().OpenObject().Map()
	if _, ok := m["additionalProperties"]; ok {
		t.Fatalf("additionalProperties survived")
	}
}

func TestDSLPickOmitRequiredAll(t *testing.T) {
	m := fixture().Pick("id", "note").Map()
	props := m["properties"].(map[string]any)
	if len(props) != 2 {
		t.Fatalf("pick left %v", sortedKeys(props))
	}
	if got := m["required"]; !reflect.DeepEqual(got, []any{"id"}) {
		t.Fatalf("required after pick = %v", got)
	}
	m2 := fixture().Omit("count").Map()
	if got := m2["required"]; !reflect.DeepEqual(got, []any{"id"}) {
		t.Fatalf("required after omit = %v", got)
	}
	m3 := fixture().Pick("id", "note").RequiredAll().Map()
	if got := m3["required"]; !reflect.DeepEqual(got, []any{"id", "note"}) {
		t.Fatalf("requiredAll = %v", got)
	}
}

func TestDSLSetUpsertAndPanic(t *testing.T) {
	m := fixture().Set("properties.extra", map[string]any{"type": "boolean"}).Map()
	if _, ok := m["properties"].(map[string]any)["extra"]; !ok {
		t.Fatalf("final-key upsert failed")
	}
	assertPanics(t, func() { fixture().Set("nosuch.deep.key", 1) })
}

func TestDSLPanicsOnUnknownKey(t *testing.T) {
	for name, fn := range map[string]func(){
		"Required": func() { fixture().Required("nope") },
		"Optional": func() { fixture().Optional("nope") },
		"Nullable": func() { fixture().Nullable("nope") },
		"NoFormat": func() { fixture().NoFormat("nope") },
		"Ref":      func() { fixture().Ref("nope", "X") },
		"Pick":     func() { fixture().Pick("nope") },
		"Omit":     func() { fixture().Omit("nope") },
	} {
		t.Run(name, func(t *testing.T) { assertPanics(t, fn) })
	}
}

func TestAnyOfNullBuilderScalarEnum(t *testing.T) {
	inner := RawFragment(map[string]any{"type": "string", "enum": []any{"a", "b"}})
	got := AnyOfNull(inner).Map()
	want := map[string]any{"anyOf": []any{
		map[string]any{"type": "string", "enum": []any{"a", "b"}},
		map[string]any{"type": "null"},
	}}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("scalar-enum: got %v", got)
	}
}

func TestAnyOfNullBuilderBareRef(t *testing.T) {
	got := AnyOfNull(RefTo("User")).Map()
	want := map[string]any{"anyOf": []any{
		map[string]any{"$ref": RefPrefix + "User"},
		map[string]any{"type": "null"},
	}}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("bare-ref: got %v", got)
	}
}

func TestAnyOfNullDeltaInlineObject(t *testing.T) {
	s := RawFragment(map[string]any{
		"type": "object",
		"properties": map[string]any{
			"badge": map[string]any{
				"type":       "object",
				"properties": map[string]any{"level": map[string]any{"type": "integer"}},
			},
		},
	})
	m := s.AnyOfNull("badge").Map()
	got := m["properties"].(map[string]any)["badge"]
	want := map[string]any{"anyOf": []any{
		map[string]any{
			"type":       "object",
			"properties": map[string]any{"level": map[string]any{"type": "integer"}},
		},
		map[string]any{"type": "null"},
	}}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("inline-object delta: got %v", got)
	}
	// Must be anyOf-null, NOT the type:[object,null] idiom.
	if _, ok := got.(map[string]any)["type"]; ok {
		t.Fatalf("delta must not fold null into type: %v", got)
	}
}

func TestAnyOfNullIdempotent(t *testing.T) {
	inner := RawFragment(map[string]any{"type": "string", "enum": []any{"a"}})
	once := AnyOfNull(inner)
	twice := AnyOfNull(once)
	if !reflect.DeepEqual(once.Map(), twice.Map()) {
		t.Fatalf("builder not idempotent: once=%v twice=%v", once.Map(), twice.Map())
	}
	// Delta form: applying twice equals applying once.
	base := func() Schema {
		return RawFragment(map[string]any{
			"type":       "object",
			"properties": map[string]any{"owner": map[string]any{"$ref": RefPrefix + "User"}},
		})
	}
	d1 := base().AnyOfNull("owner").Map()
	d2 := base().AnyOfNull("owner").AnyOfNull("owner").Map()
	if !reflect.DeepEqual(d1, d2) {
		t.Fatalf("delta not idempotent: once=%v twice=%v", d1, d2)
	}
}

func TestCombinators(t *testing.T) {
	m := AllOf(RefTo("A"), RawFragment(map[string]any{"type": "object"})).Map()
	arr := m["allOf"].([]any)
	if len(arr) != 2 || !reflect.DeepEqual(arr[0], map[string]any{"$ref": RefPrefix + "A"}) {
		t.Fatalf("allOf = %v", arr)
	}
	if _, ok := OneOf(RefTo("A"), RefTo("B")).Map()["oneOf"]; !ok {
		t.Fatalf("oneOf missing")
	}
}

// ---------------------------------------------------------------------------
// v1: Set over the typed IR
// ---------------------------------------------------------------------------

// TestSetTypedPaths — Set walks TYPED nodes now: structural segments descend
// (properties.<name>, items, anyOf.<i>), and the final segment lands in its
// typed field with a value-type check. The point of the check is that a
// document bug becomes a declaration-time panic instead of a wrong document.
func TestSetTypedPaths(t *testing.T) {
	for name, tc := range map[string]struct {
		path   string
		value  any
		verify func(*testing.T, Schema)
	}{
		"numeric coercion": {"properties.count.minimum", 10, func(t *testing.T, s Schema) {
			n := s.mustProp("count").Schema
			if n.Minimum == nil || *n.Minimum != 10 {
				t.Fatalf("minimum = %v", n.Minimum)
			}
		}},
		"annotation": {"properties.note.description", "a note", func(t *testing.T, s Schema) {
			if got := s.mustProp("note").Schema.Description; got != "a note" {
				t.Fatalf("description = %q", got)
			}
		}},
		"extension": {"properties.id.x-vendor", "v", func(t *testing.T, s Schema) {
			if got := s.mustProp("id").Schema.Extensions["x-vendor"]; got != "v" {
				t.Fatalf("extensions = %v", s.mustProp("id").Schema.Extensions)
			}
		}},
		"root keyword": {"description", "the thing", func(t *testing.T, s Schema) {
			if got := s.IR().Description; got != "the thing" {
				t.Fatalf("description = %q", got)
			}
		}},
		"upsert new property": {"properties.extra", map[string]any{"type": "boolean"}, func(t *testing.T, s Schema) {
			if got := s.mustProp("extra").Schema.Types; !reflect.DeepEqual(got, []string{"boolean"}) {
				t.Fatalf("extra = %v", got)
			}
		}},
	} {
		t.Run(name, func(t *testing.T) {
			s := fixture().Set(tc.path, tc.value)
			tc.verify(t, s)
		})
	}
}

// TestSetWalksItemsAndArms — structural descent through an array element and a
// combinator arm index.
func TestSetWalksItemsAndArms(t *testing.T) {
	s := RawFragment(map[string]any{
		"type":  "array",
		"items": map[string]any{"type": "string"},
	}).Set("items.format", "uuid")
	if got := s.IR().Items.Format; got != "uuid" {
		t.Fatalf("items.format = %q", got)
	}

	c := AnyOf(RefTo("A"), RawFragment(map[string]any{"type": "string"}))
	c.Set("anyOf.1.maxLength", 3)
	if got := c.IR().AnyOf[1].MaxLength; got == nil || *got != 3 {
		t.Fatalf("anyOf.1.maxLength = %v", got)
	}
	assertPanicsWith(t, "out of range", func() { c.Set("anyOf.7.maxLength", 3) })
}

// TestSetPanics — the three failure classes, each with the message a caller
// needs to fix the call site.
func TestSetPanics(t *testing.T) {
	for name, tc := range map[string]struct {
		path  string
		value any
		want  string
	}{
		"mistyped number":     {"properties.count.minimum", "10", "mistyped value"},
		"mistyped annotation": {"properties.note.description", 7, "mistyped value"},
		"unknown property":    {"properties.nope.x", 1, `unknown property "nope"`},
		"untyped keyword":     {"patternProperties", map[string]any{}, "outside the typed set"},
		"unknown keyword":     {"properties.id.wat", 1, "outside the typed set"},
		"broken path":         {"nosuch.deep.key", 1, "does not address a schema"},
	} {
		t.Run(name, func(t *testing.T) {
			assertPanicsWith(t, tc.want, func() { fixture().Set(tc.path, tc.value) })
		})
	}
	// The unknown-property panic carries the sorted available props, so the typo
	// is diagnosable without opening the reflector's output.
	assertPanicsWith(t, "[count id note owner]", func() { fixture().Set("properties.nope.x", 1) })
}

// TestSetSplicesSchemaValue — passing a Schema splices its IR node directly.
// This is what retires RefPrefix string concatenation at composition sites.
func TestSetSplicesSchemaValue(t *testing.T) {
	s := fixture().Set("properties.owner", RefTo("Account"))
	if n := s.mustProp("owner").Schema; n.Kind != KindRef || n.Ref != "Account" {
		t.Fatalf("owner = %+v", n)
	}
	s2 := fixture().Set("properties.extra", AnyOfNull(RefTo("Badge")))
	if got := s2.Map()["properties"].(map[string]any)["extra"]; !reflect.DeepEqual(got, map[string]any{
		"anyOf": []any{
			map[string]any{"$ref": RefPrefix + "Badge"},
			map[string]any{"type": "null"},
		},
	}) {
		t.Fatalf("extra = %v", got)
	}
}

// ---------------------------------------------------------------------------
// v1: new verbs
// ---------------------------------------------------------------------------

func TestDSLEnum(t *testing.T) {
	got := fixture().Enum("note", "a", "b").Map()["properties"].(map[string]any)["note"]
	want := map[string]any{"type": "string", "enum": []any{"a", "b"}}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("enum = %v", got)
	}
}

// TestDSLNullableEnum — "null" must join BOTH the type list and the enum
// members: the enum is the stricter constraint, so without a null member the
// null type would be unreachable.
func TestDSLNullableEnum(t *testing.T) {
	got := fixture().NullableEnum("note", "a").Map()["properties"].(map[string]any)["note"]
	want := map[string]any{"type": []any{"string", "null"}, "enum": []any{"a", nil}}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("nullable enum = %v", got)
	}
}

func TestDSLDescribe(t *testing.T) {
	got := fixture().Describe("id", "the id").Map()["properties"].(map[string]any)["id"]
	want := map[string]any{"type": "string", "description": "the id"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("describe = %v", got)
	}
	assertPanics(t, func() { fixture().Describe("nope", "x") })
}

func TestDSLNewVerbsPanicOnUnknownKey(t *testing.T) {
	for name, fn := range map[string]func(){
		"Enum":         func() { fixture().Enum("nope", "a") },
		"NullableEnum": func() { fixture().NullableEnum("nope", "a") },
		"Describe":     func() { fixture().Describe("nope", "x") },
		"AnyOfNull":    func() { fixture().AnyOfNull("nope") },
	} {
		t.Run(name, func(t *testing.T) { assertPanicsWith(t, `unknown property "nope"`, fn) })
	}
}

// TestDSLNullableIdempotent — Nullable applied twice equals once, for both the
// scalar (type-array) and the $ref (anyOf-null) idioms.
func TestDSLNullableIdempotent(t *testing.T) {
	once := fixture().Nullable("note", "owner").Map()
	twice := fixture().Nullable("note", "owner").Nullable("note", "owner").Map()
	if !reflect.DeepEqual(once, twice) {
		t.Fatalf("not idempotent:\n once=%v\ntwice=%v", once, twice)
	}
}

// TestDSLNullableOpaqueProperty — Nullable must never be a silent no-op. An
// Opaque property cannot have "null" folded into a type it does not model, so
// it takes the anyOf wrap (what v0 did); the wrap makes the node a combinator,
// so a second Nullable is idempotent through the normal path.
func TestDSLNullableOpaqueProperty(t *testing.T) {
	base := func() Schema {
		return RawFragment(map[string]any{
			"type": "object",
			"properties": map[string]any{
				"cond": map[string]any{"if": map[string]any{"const": 1}},
			},
		})
	}
	if k := base().mustProp("cond").Schema.Kind; k != KindOpaque {
		t.Fatalf("fixture property is %s, want opaque", k)
	}
	once := base().Nullable("cond").Map()["properties"].(map[string]any)["cond"]
	want := map[string]any{"anyOf": []any{
		map[string]any{"if": map[string]any{"const": float64(1)}},
		map[string]any{"type": "null"},
	}}
	if !reflect.DeepEqual(once, want) {
		t.Fatalf("nullable opaque = %v, want %v", once, want)
	}
	twice := base().Nullable("cond").Nullable("cond").Map()["properties"].(map[string]any)["cond"]
	if !reflect.DeepEqual(once, twice) {
		t.Fatalf("not idempotent:\n once=%v\ntwice=%v", once, twice)
	}
}
