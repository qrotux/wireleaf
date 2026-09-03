package apidoc

import (
	"reflect"
	"testing"

	"github.com/qrotux/wireleaf/include"
)

// ---------------------------------------------------------------------------
// Test resources. InputParams reads Edges() (through IncludePaths) and the
// OPTIONAL include.InputSource seam, so the ipRes stub of includepaths_test.go
// covers the undeclared case as-is and one wrapper covers the declaring one.
// ---------------------------------------------------------------------------

// ipInputsRes is an ipRes that DECLARES inputs.
type ipInputsRes struct {
	*ipRes
	in include.Inputs
}

func (r ipInputsRes) Inputs() (include.Inputs, bool) { return r.in, true }

var (
	_ include.Resource    = ipInputsRes{}
	_ include.InputSource = ipInputsRes{}
)

// withInputs returns a leaf resource declaring in.
func withInputs(t *testing.T, in include.Inputs) include.Resource {
	t.Helper()
	return ipInputsRes{ipRes: &ipRes{name: "Book", edges: map[string]include.Edge{}}, in: in}
}

// plainResource returns a leaf resource that is not an include.InputSource.
func plainResource(t *testing.T) include.Resource {
	t.Helper()
	return &ipRes{name: "Book", edges: map[string]include.Edge{}}
}

func TestInputParamsOffsetJSON(t *testing.T) {
	res := withInputs(t, include.Inputs{
		Sort:   include.SortInputs{Enabled: true, Default: "-year", Keys: map[string]string{"title": "title", "year": "pub_year"}},
		Filter: include.FilterInputs{Enabled: true, Fields: map[string]include.Column{"year": {Col: "pub_year", Type: reflect.TypeOf(0), Filterable: true}}},
		Page:   include.PageInputs{Mode: include.PageModeOffset, DefaultLimit: 2, MaxLimit: 50},
	})
	ps := InputParams(res, include.DefaultLimits, FilterJSON)
	names := make([]string, 0, len(ps))
	for _, p := range ps {
		names = append(names, p.Name)
	}
	if !reflect.DeepEqual(names, []string{"include", "sort", "page", "limit", "where"}) {
		t.Fatalf("names = %v", names)
	}
	if e := ps[1].Schema["enum"]; !reflect.DeepEqual(e, []any{"title", "-title", "year", "-year"}) || ps[1].Schema["default"] != "-year" {
		t.Errorf("sort = %v", ps[1].Schema)
	}
	if ps[3].Schema["maximum"] != 50 || ps[3].Schema["default"] != 2 {
		t.Errorf("limit = %v", ps[3].Schema)
	}
	if ps[4].Schema["type"] != "string" || ps[4].Schema[XFilterSyntax] != "json" {
		t.Errorf("where = %v", ps[4].Schema)
	}
	if ff := ps[4].Schema[XFilterFields].(map[string]any); !reflect.DeepEqual(ff["year"], []any{"eq", "ne", "in", "nin", "lt", "lte", "gt", "gte"}) {
		t.Errorf("x-filter-fields = %v", ff)
	}
	for _, p := range ps {
		if p.Description == "" {
			t.Errorf("parameter %q has no description", p.Name)
		}
	}
}

func TestInputParamsCursorBracket(t *testing.T) {
	res := withInputs(t, include.Inputs{
		Filter: include.FilterInputs{Enabled: true, Fields: map[string]include.Column{"year": {Col: "pub_year", Type: reflect.TypeOf(0), Filterable: true}}},
		Page:   include.PageInputs{Mode: include.PageModeCursor, DefaultLimit: 20, MaxLimit: 100},
	})
	ps := InputParams(res, include.DefaultLimits, FilterBracket)
	if ps[1].Name != "cursor" || ps[1].Schema["type"] != "string" {
		t.Errorf("cursor param = %+v", ps[1])
	}
	w := ps[3]
	if w.Name != "where" || w.Style != "deepObject" || !w.Explode || w.Schema["type"] != "object" {
		t.Fatalf("where = %+v", w)
	}
	if w.Schema[XFilterSyntax] != "bracket" {
		t.Errorf("x-filter-syntax = %v", w.Schema[XFilterSyntax])
	}
	props := w.Schema["properties"].(map[string]any)["year"].(map[string]any)["properties"].(map[string]any)
	if props["gt"].(map[string]any)["type"] != "integer" || props["in"].(map[string]any)["type"] != "array" {
		t.Errorf("year ops = %v", props)
	}
	if items := props["in"].(map[string]any)["items"].(map[string]any); items["type"] != "integer" {
		t.Errorf("in items = %v", items)
	}
}

func TestInputParamsUndeclared(t *testing.T) {
	ps := InputParams(plainResource(t), include.DefaultLimits, FilterJSON)
	if len(ps) != 3 || ps[0].Name != "include" || ps[1].Name != "page" || ps[2].Name != "limit" {
		t.Fatalf("undeclared = %+v", ps)
	}
	if ps[2].Schema["maximum"] != include.DefaultMaxPageLimit || ps[2].Schema["default"] != include.DefaultPageLimit {
		t.Errorf("limit = %v", ps[2].Schema)
	}
}

func TestInputParamsBracketScalarTypes(t *testing.T) {
	res := withInputs(t, include.Inputs{
		Filter: include.FilterInputs{Enabled: true, Fields: map[string]include.Column{
			"active": {Col: "active", Type: reflect.TypeOf(true), Filterable: true},
			"score":  {Col: "score", Type: reflect.TypeOf(1.5), Filterable: true},
			"title":  {Col: "title", Type: reflect.TypeOf(""), Filterable: true},
		}},
		Page: include.PageInputs{Mode: include.PageModeOffset, DefaultLimit: 20, MaxLimit: 100},
	})
	ps := InputParams(res, include.DefaultLimits, FilterBracket)
	w := ps[len(ps)-1]
	props := w.Schema["properties"].(map[string]any)
	want := map[string]string{"active": "boolean", "score": "number", "title": "string"}
	for name, typ := range want {
		ops := props[name].(map[string]any)["properties"].(map[string]any)
		if got := ops["eq"].(map[string]any)["type"]; got != typ {
			t.Errorf("%s eq type = %v, want %v", name, got, typ)
		}
	}
	// A bool column admits equality operators only.
	ff := w.Schema[XFilterFields].(map[string]any)
	if !reflect.DeepEqual(ff["active"], []any{"eq", "ne", "in", "nin"}) {
		t.Errorf("active ops = %v", ff["active"])
	}
}

// TestInputParamsSortWithoutKeys: sort enabled over no sortable column is a
// compile finding, but a hand-written InputSource can still say it; the
// document then carries no sort parameter rather than an empty enum.
func TestInputParamsSortWithoutKeys(t *testing.T) {
	res := withInputs(t, include.Inputs{Sort: include.SortInputs{Enabled: true}})
	ps := InputParams(res, include.DefaultLimits, FilterJSON)
	for _, p := range ps {
		if p.Name == "sort" {
			t.Fatalf("sort documented with no keys: %+v", p)
		}
	}
}
