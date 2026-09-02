package apidoc

import (
	"reflect"
	"testing"

	"github.com/qrotux/wireleaf/include"
)

// ---------------------------------------------------------------------------
// Minimal toy Resource for IncludePaths tests. A local type is enough: the
// walk only reads Edges(), so no rows, serialization or fetchers are needed.
// ---------------------------------------------------------------------------

// ipRes is a configurable inline Resource used to assemble toy graphs.
type ipRes struct {
	name  string
	edges map[string]include.Edge
}

func (r *ipRes) Name() string                            { return r.name }
func (r *ipRes) Slug() string                            { return r.name }
func (r *ipRes) Fields() []string                        { return nil }
func (r *ipRes) Defaults() []string                      { return nil }
func (r *ipRes) Edges() map[string]include.Edge          { return r.edges }
func (r *ipRes) IDOf(doc any) string                     { return "" }
func (r *ipRes) Serialize(doc any, c *include.Ctx) any   { return nil }
func (r *ipRes) Enrich(docs []any, c *include.Ctx) error { return nil }

var _ include.Resource = (*ipRes)(nil)

func TestIncludePathsLinear(t *testing.T) {
	// A -next-> B -next-> C -next-> D -next-> E: a straight chain, one
	// includable edge per node, deliberately longer than MaxDepth so the
	// bound truncation is observable.
	e := &ipRes{name: "E", edges: map[string]include.Edge{}}
	d := &ipRes{name: "D"}
	d.edges = map[string]include.Edge{"next": {Target: func() include.Resource { return e }, Includable: true}}
	c := &ipRes{name: "C"}
	c.edges = map[string]include.Edge{"next": {Target: func() include.Resource { return d }, Includable: true}}
	b := &ipRes{name: "B"}
	b.edges = map[string]include.Edge{"next": {Target: func() include.Resource { return c }, Includable: true}}
	a := &ipRes{name: "A"}
	a.edges = map[string]include.Edge{"next": {Target: func() include.Resource { return b }, Includable: true}}

	got := IncludePaths(a, include.Limits{MaxDepth: 3, MaxNodes: 50})
	want := []string{"next", "next.next", "next.next.next"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("IncludePaths(linear, depth 3) = %v, want %v", got, want)
	}
}

func TestIncludePathsCyclic(t *testing.T) {
	// X -self-> X: a self-cycle. The walk must terminate on MaxDepth alone,
	// producing the repeated "self" segment chain up to the bound.
	x := &ipRes{name: "X"}
	x.edges = map[string]include.Edge{"self": {Target: func() include.Resource { return x }, Includable: true}}

	got := IncludePaths(x, include.Limits{MaxDepth: 3, MaxNodes: 50})
	want := []string{"self", "self.self", "self.self.self"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("IncludePaths(cyclic, depth 3) = %v, want %v", got, want)
	}
}

func TestIncludePathsNonIncludableExcluded(t *testing.T) {
	leaf := &ipRes{name: "Leaf", edges: map[string]include.Edge{}}
	root := &ipRes{name: "Root"}
	root.edges = map[string]include.Edge{
		"visible": {Target: func() include.Resource { return leaf }, Includable: true},
		"hidden":  {Target: func() include.Resource { return leaf }},
	}

	got := IncludePaths(root, include.DefaultLimits)
	want := []string{"visible"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("IncludePaths(non-includable) = %v, want %v", got, want)
	}
}

func TestIncludePathsSorted(t *testing.T) {
	leaf := &ipRes{name: "Leaf", edges: map[string]include.Edge{}}
	root := &ipRes{name: "Root"}
	root.edges = map[string]include.Edge{
		"zzz": {Target: func() include.Resource { return leaf }, Includable: true},
		"aaa": {Target: func() include.Resource { return leaf }, Includable: true},
		"mmm": {Target: func() include.Resource { return leaf }, Includable: true},
	}

	got := IncludePaths(root, include.DefaultLimits)
	want := []string{"aaa", "mmm", "zzz"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("IncludePaths(sorted) = %v, want %v", got, want)
	}
}

func TestIncludePathsMaxDepthZero(t *testing.T) {
	leaf := &ipRes{name: "Leaf", edges: map[string]include.Edge{}}
	root := &ipRes{name: "Root"}
	root.edges = map[string]include.Edge{
		"child": {Target: func() include.Resource { return leaf }, Includable: true},
	}

	got := IncludePaths(root, include.Limits{MaxDepth: 0, MaxNodes: 50})
	if got != nil {
		t.Fatalf("IncludePaths(MaxDepth=0) = %#v, want nil", got)
	}
}

func TestIncludeParamSchema(t *testing.T) {
	paths := []string{"aaa", "mmm", "zzz"}
	got := IncludeParamSchema(paths)

	if got["type"] != "string" {
		t.Fatalf("IncludeParamSchema type = %v, want %q", got["type"], "string")
	}
	vals, ok := got[XIncludePaths].([]any)
	if !ok {
		t.Fatalf("IncludeParamSchema[%s] type = %T, want []any", XIncludePaths, got[XIncludePaths])
	}
	want := []any{"aaa", "mmm", "zzz"}
	if !reflect.DeepEqual(vals, want) {
		t.Fatalf("IncludeParamSchema[%s] = %v, want %v", XIncludePaths, vals, want)
	}
}
