package reflectortest

import (
	"reflect"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/qrotux/wireleaf/apidoc"
)

// This package's self-test drives the contract table against a deliberately
// NAIVE reflector: it walks json tags, emits flat objects, collects auxiliary
// components — and does nothing else. It handles no nullability, no embedding,
// no constraint tags and no omitempty. Running the suite against it must
// therefore PASS the areas it does implement and FAIL exactly the ones it does
// not, which is what proves the suite bites: a harness that only ever passes
// documents nothing.

// ------------------------------------------------------------------ the stub

var timeType = reflect.TypeOf(time.Time{})

// stub is the naive reflector described above.
type stub struct{}

var _ apidoc.Reflector = stub{}

func (s stub) ReflectComponents(ts []reflect.Type, overrides map[reflect.Type]string) (map[string]*apidoc.IRNode, error) {
	out := map[string]*apidoc.IRNode{}
	for _, t := range ts {
		s.emit(apidoc.DerefType(t), overrides, out)
	}
	return out, nil
}

// emit registers t's component and returns the name it was registered under.
func (s stub) emit(t reflect.Type, overrides map[reflect.Type]string, out map[string]*apidoc.IRNode) string {
	name, ok := overrides[t]
	if !ok {
		name = t.Name()
	}
	if _, done := out[name]; done {
		return name
	}
	out[name] = nil // reserve the name so a cycle terminates

	props := make([]apidoc.Prop, 0, t.NumField())
	for i := 0; i < t.NumField(); i++ {
		f := t.Field(i)
		if f.PkgPath != "" {
			continue // unexported
		}
		tag := f.Tag.Get("json")
		if tag == "-" {
			continue
		}
		key := f.Name // NOTE: no embedding rules — an anonymous field is a field
		if tag != "" {
			if n, _, _ := strings.Cut(tag, ","); n != "" {
				key = n
			}
		}
		// NOTE: no omitempty handling — everything is required.
		props = append(props, apidoc.Prop{Name: key, Schema: s.schemaOf(f.Type, overrides, out), Required: true})
	}
	out[name] = &apidoc.IRNode{Kind: apidoc.KindObject, Types: []string{"object"}, Props: props}
	return name
}

// schemaOf renders one field type. NOTE: no nullability — a pointer is simply
// dereferenced — and no constraint tags are read.
func (s stub) schemaOf(ft reflect.Type, overrides map[reflect.Type]string, out map[string]*apidoc.IRNode) *apidoc.IRNode {
	ft = apidoc.DerefType(ft)
	switch {
	case ft == timeType:
		return &apidoc.IRNode{Kind: apidoc.KindScalar, Types: []string{"string"}, Format: "date-time"}
	case ft.Kind() == reflect.Struct:
		return &apidoc.IRNode{Kind: apidoc.KindRef, Ref: s.emit(ft, overrides, out)}
	case ft.Kind() == reflect.Slice || ft.Kind() == reflect.Array:
		return &apidoc.IRNode{Kind: apidoc.KindArray, Types: []string{"array"}, Items: s.schemaOf(ft.Elem(), overrides, out)}
	case ft.Kind() == reflect.Map:
		return &apidoc.IRNode{
			Kind:                 apidoc.KindObject,
			Types:                []string{"object"},
			Props:                []apidoc.Prop{},
			AdditionalProperties: s.schemaOf(ft.Elem(), overrides, out),
		}
	default:
		return &apidoc.IRNode{Kind: apidoc.KindScalar, Types: []string{"string"}}
	}
}

// ------------------------------------------------------------------ self-test

// wantViolations is the expected verdict of every subtest against the stub. It
// is exhaustive on purpose: a new subtest that nobody classified fails the
// self-test rather than sneaking in unchecked.
var wantViolations = map[string]bool{
	"nullability":              true,  // no pointer handling at all
	"nullability_bytes_parity": true,  // stub requires omitempty fields the bytes omit
	"naming_and_overrides":     false, // overrides and aux dedup ARE implemented
	"embedded_structs":         true,  // anonymous fields are treated as fields
	"aux_collection":           false, // structs through slices/maps/time.Time ARE implemented
	"constraint_tags":          true,  // no tags are read
	"required_semantics":       true,  // everything is required
	"json_tag_rules":           false, // json tags ARE implemented
	"recursive_types":          false, // the name reservation makes recursion terminate
	"property_order":           false, // the stub walks fields in declaration order
	"ir_invariants":            false, // the stub emits well-formed nodes
	"canonical_ir":             false, // the stub emits canonical nodes
	"determinism":              false, // the stub is order-independent
}

func TestSuiteAgainstNaiveStub(t *testing.T) {
	names := map[string]bool{}
	for _, c := range checks() {
		if names[c.name] {
			t.Fatalf("duplicate subtest name %q in the contract table", c.name)
		}
		names[c.name] = true
		want, classified := wantViolations[c.name]
		if !classified {
			t.Fatalf("subtest %q is not classified in wantViolations: add it (does the naive stub satisfy it?)", c.name)
		}
		got := c.run(stub{})
		if want && len(got) == 0 {
			t.Errorf("subtest %q PASSED against the naive stub, but the stub does not implement it — the check does not bite", c.name)
		}
		if !want && len(got) > 0 {
			t.Errorf("subtest %q failed against the naive stub, which does implement it:\n  %s", c.name, strings.Join(got, "\n  "))
		}
		if !sort.StringsAreSorted(got) {
			t.Errorf("subtest %q returned unsorted violations: %v", c.name, got)
		}
	}
	for name := range wantViolations {
		if !names[name] {
			t.Errorf("wantViolations classifies %q, which is not in the contract table", name)
		}
	}
}

// TestExpectedViolationText pins WHAT the failing subtests say, not merely that
// they say something: the messages are the reflector implementer's only guide.
func TestExpectedViolationText(t *testing.T) {
	all := strings.Join(check(stub{}), "\n")
	for _, want := range []string{
		// nullability: pointer scalars, pointer structs, omitempty
		`nullability: property "null" is a non-omitempty pointer scalar, so it must be a scalar with Types ["string","null"]`,
		`nullability: property "nullStruct" is a non-omitempty pointer-to-struct, so it must be the canonical anyOf[$ref NullabilitySub, {"type":"null"}]`,
		`nullability: property "opt" is required=true, but apidoc.DefaultNullability says VerdictOptional (want required=false)`,
		`nullability: property "optStruct" is required=true, but apidoc.DefaultNullability says VerdictOptional (want required=false)`,
		`nullability: property "optZero" is required=true, but apidoc.DefaultNullability says VerdictOptional (want required=false)`,
		// embedding: the plain embed must flatten, the tagged one must nest
		`embedded: an embedded struct WITHOUT a json tag must be flattened, so component "Embedding" needs the promoted property "baseField"`,
		`embedded: component "Embedding" carries a property "EmbeddedBase"`,
		`embedded: component "Embedding" has properties [EmbeddedBase tagged own], want [baseField tagged own]`,
		// constraint tags land in the TYPED fields
		`constraints: property "count" must carry minimum=0 in the typed IRNode field, but it is nil`,
		`constraints: property "name" must carry minLength=1 in the typed IRNode field, but it is nil`,
		`constraints: property "name" must carry pattern "^[a-z]+$" in IRNode.Pattern, got ""`,
		`constraints: property "email" must carry format "email" in IRNode.Format, got ""`,
		`constraints: property "tags" must carry minItems=1 in the typed IRNode field, but it is nil`,
		`constraints: property "status" must be a KindEnum node with Enum ["active","inactive"]`,
		// required, derived from the policy — omitzero included
		`required: property "may" is tagged json:"may,omitempty", which apidoc.DefaultNullability calls VerdictOptional, but Prop.Required is true (want false)`,
		`required: property "mayZero" is tagged json:"mayZero,omitzero", which apidoc.DefaultNullability calls VerdictOptional, but Prop.Required is true (want false)`,
	} {
		if !strings.Contains(all, want) {
			t.Errorf("expected a violation containing:\n  %s\ngot:\n  %s", want, strings.ReplaceAll(all, "\n", "\n  "))
		}
	}
}

// alphabetizing is the naive stub with every component's properties SORTED. It
// is otherwise identical, so it passes membership assertions everywhere — which
// is exactly the reflector that would slip through a suite that only ever looks
// properties up by name.
type alphabetizing struct{ stub }

func (a alphabetizing) ReflectComponents(ts []reflect.Type, ov map[reflect.Type]string) (map[string]*apidoc.IRNode, error) {
	got, err := a.stub.ReflectComponents(ts, ov)
	if err != nil {
		return nil, err
	}
	for _, n := range got {
		sort.Slice(n.Props, func(i, j int) bool { return n.Props[i].Name < n.Props[j].Name })
	}
	return got, nil
}

// nilProps emits an object whose Props slice is nil — well-formed to every
// membership check in the suite, and a registration error inside
// Components.Add.
type nilProps struct{ stub }

func (p nilProps) ReflectComponents(ts []reflect.Type, ov map[reflect.Type]string) (map[string]*apidoc.IRNode, error) {
	got, err := p.stub.ReflectComponents(ts, ov)
	if err != nil {
		return nil, err
	}
	for _, n := range got {
		n.Props = nil
	}
	return got, nil
}

// TestOrderAndInvariantChecksBite pins the two checks that a suite asserting
// only on property MEMBERSHIP would silently lack.
func TestOrderAndInvariantChecksBite(t *testing.T) {
	for _, tc := range []struct {
		area string
		r    apidoc.Reflector
		want string
	}{
		{"property_order", alphabetizing{}, `has the right properties in the WRONG order`},
		{"ir_invariants", nilProps{}, `does not satisfy apidoc.ValidateComponent`},
	} {
		got := runArea(t, tc.area, tc.r)
		if len(got) == 0 {
			t.Errorf("subtest %q reported nothing for %T", tc.area, tc.r)
			continue
		}
		if !strings.Contains(strings.Join(got, "\n"), tc.want) {
			t.Errorf("subtest %q for %T: no violation containing %q; got:\n  %s", tc.area, tc.r, tc.want, strings.Join(got, "\n  "))
		}
	}
}

// runArea runs the single named subtest of the contract table.
func runArea(t *testing.T, name string, r apidoc.Reflector) []string {
	t.Helper()
	for _, c := range checks() {
		if c.name == name {
			return c.run(r)
		}
	}
	t.Fatalf("no subtest named %q in the contract table", name)
	return nil
}

// TestReflectorErrorIsReported: a reflector that fails must produce one clear
// violation per subtest, not a panic or a silent pass.
func TestReflectorErrorIsReported(t *testing.T) {
	for _, r := range []apidoc.Reflector{failing{}, panicking{}, nilMap{}} {
		for _, c := range checks() {
			got := c.run(r)
			if len(got) == 0 {
				t.Errorf("subtest %q reported nothing for a broken reflector %T", c.name, r)
			}
		}
	}
}

type failing struct{}

func (failing) ReflectComponents([]reflect.Type, map[reflect.Type]string) (map[string]*apidoc.IRNode, error) {
	return nil, errBoom
}

type panicking struct{}

func (panicking) ReflectComponents([]reflect.Type, map[reflect.Type]string) (map[string]*apidoc.IRNode, error) {
	panic("boom")
}

type nilMap struct{}

func (nilMap) ReflectComponents([]reflect.Type, map[reflect.Type]string) (map[string]*apidoc.IRNode, error) {
	return nil, nil
}

var errBoom = boomError{}

type boomError struct{}

func (boomError) Error() string { return "boom" }

// TestFixtureTypesAreStructs guards the fixture list itself: FixtureTypes is
// public API a reflector implementer reflects directly.
func TestFixtureTypesAreStructs(t *testing.T) {
	seen := map[string]bool{}
	for _, ty := range FixtureTypes() {
		if ty.Kind() != reflect.Struct {
			t.Errorf("FixtureTypes: %s is not a struct", ty)
		}
		if seen[ty.Name()] {
			t.Errorf("FixtureTypes: %s listed twice", ty)
		}
		seen[ty.Name()] = true
	}
	if len(seen) == 0 {
		t.Fatal("FixtureTypes returned nothing")
	}
}
