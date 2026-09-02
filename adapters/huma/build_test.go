package huma

import (
	"encoding/json"
	"reflect"
	"testing"

	humav2 "github.com/danielgtaylor/huma/v2"

	"github.com/qrotux/wireleaf/apidoc"
	"github.com/qrotux/wireleaf/reflector"
)

func newDoc() *humav2.OpenAPI {
	reg := humav2.NewMapRegistry(apidoc.RefPrefix, humav2.DefaultSchemaNamer)
	return &humav2.OpenAPI{Components: &humav2.Components{Schemas: reg}}
}

func TestBuildEmptyRegistryIsNoop(t *testing.T) {
	oapi := newDoc()
	c := apidoc.NewComponents()
	if err := BuildInto(oapi, c); err != nil {
		t.Fatal(err)
	}
	if n := len(oapi.Components.Schemas.Map()); n != 0 {
		t.Fatalf("no-op expected, %d components", n)
	}
}

func TestBuildMergesThroughExtensionsBridge(t *testing.T) {
	oapi := newDoc()
	c := apidoc.NewComponents()
	c.Add("Thing", apidoc.RawFragment(map[string]any{
		"type": "object", "properties": map[string]any{"id": map[string]any{"type": "string"}},
	}))
	if err := BuildInto(oapi, c); err != nil {
		t.Fatal(err)
	}
	got, ok := oapi.Components.Schemas.Map()["Thing"]
	if !ok {
		t.Fatalf("Thing not merged")
	}
	// Extensions bridge: a zero typed Schema with Extensions marshals to
	// exactly the fragment.
	b, err := json.Marshal(got)
	if err != nil {
		t.Fatal(err)
	}
	var m map[string]any
	if err := json.Unmarshal(b, &m); err != nil {
		t.Fatal(err)
	}
	if m["type"] != "object" {
		t.Fatalf("bridge lost the fragment: %s", b)
	}
}

func TestBuildClashGuard(t *testing.T) {
	oapi := newDoc()
	oapi.Components.Schemas.Map()["Taken"] = &humav2.Schema{Type: "object"}
	c := apidoc.NewComponents()
	c.Add("Taken", apidoc.RawFragment(map[string]any{"type": "object"}))
	c.Add("Free", apidoc.RawFragment(map[string]any{"type": "object"}))
	if err := BuildInto(oapi, c); err == nil {
		t.Fatalf("clash must error")
	}
	// The clash check runs to completion before any insertion — a rejected
	// merge leaves the doc untouched.
	if _, inserted := oapi.Components.Schemas.Map()["Free"]; inserted {
		t.Fatalf("partial insertion on clash")
	}
}

func TestBuildNilDocErrors(t *testing.T) {
	c := apidoc.NewComponents()
	if err := BuildInto(nil, c); err == nil {
		t.Fatalf("nil doc must error")
	}
	if err := BuildInto(&humav2.OpenAPI{}, c); err == nil {
		t.Fatalf("doc without Components.Schemas must error")
	}
	if err := BuildInto(newDoc(), nil); err == nil {
		t.Fatalf("nil component registry must error")
	}
}

// Verify runs FIRST and its failure propagates: a dangling $ref stops assembly
// before anything reaches the doc.
func TestBuildVerifyFailurePropagates(t *testing.T) {
	oapi := newDoc()
	c := apidoc.NewComponents()
	c.Add("Thing", apidoc.RawFragment(map[string]any{
		"type":       "object",
		"properties": map[string]any{"other": map[string]any{"$ref": apidoc.RefPrefix + "Missing"}},
	}))
	err := BuildInto(oapi, c)
	if err == nil {
		t.Fatalf("dangling $ref must fail Verify")
	}
	if len(oapi.Components.Schemas.Map()) != 0 {
		t.Fatalf("nothing may reach the doc when Verify fails")
	}
	// The whitelist makes the same set verify, and then the merge proceeds.
	c.ExternalRefs("Missing")
	if err := BuildInto(oapi, c); err != nil {
		t.Fatal(err)
	}
}

// When the doc's registry IS the bridge over the same Components, the merge is a
// no-op: the components already ARE the doc's schema map.
func TestBuildIntoBridgeBackedDocIsNoop(t *testing.T) {
	c := apidoc.NewComponents()
	b := NewRegistry(c, &reflector.Reflector{})
	oapi := &humav2.OpenAPI{Components: &humav2.Components{Schemas: b}}
	b.Schema(reflect.TypeFor[BridgeAux](), true, "aux")

	before := len(b.Map())
	if err := BuildInto(oapi, c); err != nil {
		t.Fatal(err)
	}
	if after := len(b.Map()); after != before {
		t.Fatalf("bridge-backed BuildInto changed the set: %d -> %d", before, after)
	}
	if _, ok := b.Map()["BridgeAux"]; !ok {
		t.Fatalf("component vanished")
	}
	// Verify still runs on the bridge path.
	c.Add("Dangling", apidoc.RawFragment(map[string]any{"$ref": apidoc.RefPrefix + "Nope"}))
	if err := BuildInto(oapi, c); err == nil {
		t.Fatalf("Verify must run on the bridge path too")
	}
}

// A bridge over a DIFFERENT Components is a wiring mistake: the bridge's Map is
// a snapshot of its own set, so an insertion there would vanish.
func TestBuildIntoForeignBridgeErrors(t *testing.T) {
	docRegistry := NewRegistry(apidoc.NewComponents(), &reflector.Reflector{})
	oapi := &humav2.OpenAPI{Components: &humav2.Components{Schemas: docRegistry}}

	other := apidoc.NewComponents()
	other.Add("Foreign", apidoc.RawFragment(map[string]any{"type": "object"}))
	if err := BuildInto(oapi, other); err == nil {
		t.Fatalf("two component sets in one doc must error")
	}
}
