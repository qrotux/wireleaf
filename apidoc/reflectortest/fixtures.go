package reflectortest

import (
	"reflect"
	"time"
)

// The fixture types are EXPORTED on purpose: a reflector implementer debugging
// a failing subtest reflects the very same struct by hand, in their own test,
// without copying it. Nothing here has behaviour — the structs and their tags
// ARE the specification input; reflectortest.go holds the expectations.

// ---------------------------------------------------------------------------
// 1. nullability idioms
// ---------------------------------------------------------------------------

// NullabilitySub is the auxiliary component referenced by Nullability. It
// exists so the struct-valued nullability idioms have a component to point at.
type NullabilitySub struct {
	ID string `json:"id"`
}

// Nullability carries one field per apidoc.DefaultNullability verdict, in both
// the scalar and the struct-reference flavour:
//
//	Plain      -> VerdictPlain:    required, never null
//	Null       -> VerdictNull:     required, {"type":["string","null"]}
//	Opt        -> VerdictOptional: NOT required, NOT null
//	OptZero    -> VerdictOptional: omitzero is omitempty's equal in the policy
//	NullStruct -> VerdictNull:     required, anyOf[$ref, {"type":"null"}]
//	OptStruct  -> VerdictOptional: NOT required, a BARE $ref
//
// The expectations are not hard-coded in the suite: checkNullability calls
// apidoc.DefaultNullability on these very fields, so the harness and the policy
// cannot drift apart.
type Nullability struct {
	Plain      string          `json:"plain"`
	Null       *string         `json:"null"`
	Opt        *string         `json:"opt,omitempty"`
	OptZero    *string         `json:"optZero,omitzero"`
	NullStruct *NullabilitySub `json:"nullStruct"`
	OptStruct  *NullabilitySub `json:"optStruct,omitempty"`
}

// ---------------------------------------------------------------------------
// 2. naming, overrides, shared-aux dedup
// ---------------------------------------------------------------------------

// SharedAux is referenced by BOTH naming tops: it must be emitted once, under
// one name, no matter how many tops reach it.
type SharedAux struct {
	V string `json:"v"`
}

// NamingTopA is reflected with an override forcing its component name to
// NamingOverrideName; the override must NOT leak to SharedAux or NamingTopB.
type NamingTopA struct {
	Shared SharedAux `json:"shared"`
	A      string    `json:"a"`
}

// NamingTopB is reflected WITHOUT an override: it keeps its Go type name.
type NamingTopB struct {
	Shared SharedAux `json:"shared"`
	B      string    `json:"b"`
}

// NamingOverrideName is the component name forced onto NamingTopA.
const NamingOverrideName = "RenamedTop"

// ---------------------------------------------------------------------------
// 3. embedded structs
// ---------------------------------------------------------------------------

// EmbeddedBase is embedded WITHOUT a json tag: encoding/json promotes its
// fields into the parent, and so must the reflector (spec §5c).
type EmbeddedBase struct {
	BaseField string `json:"baseField"`
}

// TaggedBase is embedded WITH a json tag: encoding/json treats it as an
// ordinary named field, so it nests instead of flattening.
type TaggedBase struct {
	TaggedField string `json:"taggedField"`
}

// Embedding exercises both embedding rules at once.
type Embedding struct {
	EmbeddedBase
	TaggedBase `json:"tagged"`
	Own        string `json:"own"`
}

// ---------------------------------------------------------------------------
// 4. auxiliary component collection
// ---------------------------------------------------------------------------

// AuxInner is reached three ways from AuxCollect (directly, through a slice and
// through a map) and must still be exactly one component.
type AuxInner struct {
	N string `json:"n"`
}

// AuxCollect pins auxiliary collection: named structs become components,
// slice/map element structs are collected through the container, and time.Time
// is a formatted STRING rather than a component of its own.
type AuxCollect struct {
	One   AuxInner            `json:"one"`
	List  []AuxInner          `json:"list"`
	ByKey map[string]AuxInner `json:"byKey"`
	When  time.Time           `json:"when"`
}

// ---------------------------------------------------------------------------
// 5. constraint tags
// ---------------------------------------------------------------------------

// Constrained carries the jsonschema-go native constraint tags. Every one of
// them must land in the IR's TYPED field, never in Extensions.
type Constrained struct {
	Count  int      `json:"count" minimum:"0" maximum:"10"`
	Name   string   `json:"name" minLength:"1" maxLength:"10" pattern:"^[a-z]+$"`
	Email  string   `json:"email" format:"email"`
	Tags   []string `json:"tags" minItems:"1" maxItems:"5"`
	Status string   `json:"status" enum:"active,inactive"`
}

// ---------------------------------------------------------------------------
// 6. required semantics
// ---------------------------------------------------------------------------

// RequiredRules is the minimal required/optional set: absence of an omit option
// is the ONLY thing that makes a field required, and omitzero counts exactly as
// omitempty does.
type RequiredRules struct {
	Must    string `json:"must"`
	May     string `json:"may,omitempty"`
	MayZero string `json:"mayZero,omitzero"`
}

// ---------------------------------------------------------------------------
// 7. json tag rules
// ---------------------------------------------------------------------------

// JSONTags pins the three json-tag rules: rename, skip, and the untagged
// exported field that keeps its Go field name.
type JSONTags struct {
	Renamed  string `json:"renamed_name"`
	Skipped  string `json:"-"`
	Untagged string
}

// ---------------------------------------------------------------------------
// 8. recursive types
// ---------------------------------------------------------------------------

// Tree references ITSELF through a slice. It is the hardest shape a reflector
// has to survive: the type must be emitted exactly once, the self-reference has
// to be a $ref (a reflector that inlines recurses forever), and the resulting
// component must still pass apidoc.ValidateComponent.
type Tree struct {
	ID   string `json:"id"`
	Kids []Tree `json:"kids"`
}

// ---------------------------------------------------------------------------
// the fixture set
// ---------------------------------------------------------------------------

// FixtureTypes returns every TOP fixture type the suite reflects, in a stable
// order. Auxiliary types (NullabilitySub, SharedAux, AuxInner, TaggedBase) are
// deliberately absent: the reflector is expected to discover them.
func FixtureTypes() []reflect.Type {
	return []reflect.Type{
		reflect.TypeOf(Nullability{}),
		reflect.TypeOf(NamingTopA{}),
		reflect.TypeOf(NamingTopB{}),
		reflect.TypeOf(Embedding{}),
		reflect.TypeOf(AuxCollect{}),
		reflect.TypeOf(Constrained{}),
		reflect.TypeOf(RequiredRules{}),
		reflect.TypeOf(JSONTags{}),
		reflect.TypeOf(Tree{}),
	}
}

// propertyOrder is the exact, ordered property list every fixture component
// must carry. Property ORDER is the reason the IR exists at all — a document
// whose properties reshuffle between runs is not reviewable — so it is pinned
// per component rather than inferred. Components not listed here (EmbeddedBase,
// which a conforming reflector need not emit at all) are not order-checked;
// Embedding is checked by the embedding area, where the flattened order is part
// of the embedding rule itself.
var propertyOrder = map[string][]string{
	"Nullability":      {"plain", "null", "opt", "optZero", "nullStruct", "optStruct"},
	"NullabilitySub":   {"id"},
	NamingOverrideName: {"shared", "a"},
	"NamingTopB":       {"shared", "b"},
	"SharedAux":        {"v"},
	"TaggedBase":       {"taggedField"},
	"AuxCollect":       {"one", "list", "byKey", "when"},
	"AuxInner":         {"n"},
	"Constrained":      {"count", "name", "email", "tags", "status"},
	"RequiredRules":    {"must", "may", "mayZero"},
	"JSONTags":         {"renamed_name", "Untagged"},
	"Tree":             {"id", "kids"},
}

// embeddingOrder is Embedding's property order: the plain embed's field is
// promoted IN PLACE (where the embedded field sat), not appended.
var embeddingOrder = []string{"baseField", "tagged", "own"}
