package huma

// api.go — the wiring object.
//
// Every earlier piece of this adapter is a free function over something the
// application assembles by hand: a Components set, a huma Config, an
// Operation. API is the one place those stop being independent: the graph the
// planner walks, the include.Options it walks it with, the components the
// document is written into and the huma API the operations are registered on
// are ONE set of decisions, and the document is only trustworthy while they
// agree. New makes them together; Include/Inputs document a resource with the
// SAME options the planner will enforce; Register refuses an operation whose
// output carries a Node[W] no Bind ever wired.

import (
	"context"
	"fmt"
	"maps"
	"reflect"
	"sort"
	"strings"

	humav2 "github.com/danielgtaylor/huma/v2"

	"github.com/qrotux/wireleaf/apidoc"
	"github.com/qrotux/wireleaf/graph"
	"github.com/qrotux/wireleaf/include"
	"github.com/qrotux/wireleaf/reflector"
)

// API is the wiring object of a wireleaf-backed huma service: the compiled
// graph, the include options the planner AND the document use, the shared
// component set, and the huma API once attached. Order: New → Attach → every
// Bind → Register.
type API struct {
	g      *graph.Graph
	opts   include.Options
	c      *apidoc.Components
	cfg    humav2.Config
	syntax apidoc.FilterSyntax
	huma   humav2.API

	// bound records every Node[W] wrapper type a Bind registered, so Register
	// can refuse an output type carrying an unbound wrapper. Keyed by the
	// wrapper type alone, because that is all checkBound sees on an output.
	bound map[reflect.Type]bool

	// bindings is Bind's idempotency key: the PAIR (node, Node[W]). Keying it
	// on the wrapper alone would let a second node declaring the same wire type
	// skip RegisterNode and silently document as the FIRST node's component;
	// with the pair, the second Bind reaches RegisterNode and apidoc refuses
	// the remap, loudly.
	bindings map[bindKey]bool
}

// bindKey identifies one Bind: a graph node name plus the Node[W] wrapper type.
type bindKey struct {
	node    string
	wrapper reflect.Type
}

// Option customizes New.
type Option func(*apiOpts)

type apiOpts struct {
	syntax apidoc.FilterSyntax
	cfg    []ConfigOpt
}

// WithFilterSyntax selects how ?where is spelled (default apidoc.FilterJSON).
func WithFilterSyntax(s apidoc.FilterSyntax) Option { return func(o *apiOpts) { o.syntax = s } }

// WithConfig forwards options to NewConfig.
func WithConfig(c ...ConfigOpt) Option { return func(o *apiOpts) { o.cfg = append(o.cfg, c...) } }

// New emits the graph's components into a fresh set and builds the huma
// config over it. Zero opts means include.DefaultOptions. Any emission
// failure is a wiring error and panics.
func New(g *graph.Graph, opts include.Options, title, version string, o ...Option) *API {
	if g == nil {
		panic("adapters/huma: New needs a compiled graph")
	}
	if opts == (include.Options{}) {
		opts = include.DefaultOptions
	}
	ao := apiOpts{syntax: apidoc.FilterJSON}
	for _, f := range o {
		if f != nil {
			f(&ao)
		}
	}
	c := apidoc.NewComponents()
	frags, err := apidoc.EmitComponents(&reflector.Reflector{}, g.Roots())
	if err != nil {
		panic(fmt.Sprintf("adapters/huma: emitting graph components: %v", err))
	}
	for _, name := range sortedKeys(frags) {
		c.Add(name, frags[name])
	}
	cfgOpts := append([]ConfigOpt{WithRegistry(c)}, ao.cfg...)
	return &API{
		g: g, opts: opts, c: c, syntax: ao.syntax,
		cfg:      NewConfig(title, version, cfgOpts...),
		bound:    map[reflect.Type]bool{},
		bindings: map[bindKey]bool{},
	}
}

// Config returns the huma configuration to build the router adapter with.
func (a *API) Config() humav2.Config { return a.cfg }

// Components returns the shared component set.
func (a *API) Components() *apidoc.Components { return a.c }

// Graph returns the compiled graph.
func (a *API) Graph() *graph.Graph { return a.g }

// Options returns the include options the planner and the document share.
func (a *API) Options() include.Options { return a.opts }

// Attach installs the request middleware on h and makes Register usable.
func (a *API) Attach(h humav2.API) {
	if h == nil {
		panic("adapters/huma: Attach needs a huma API")
	}
	h.UseMiddleware(requestMiddleware)
	a.huma = h
}

// Errors is the package-level Errors, kept on the API for symmetry.
func (a *API) Errors(defs ...ErrorDef) OpOpt { return Errors(defs...) }

var (
	errInvalidInclude    = ErrorDef{Status: 400, Code: string(include.INVALID_INCLUDE), Message: "unknown or malformed include path"}
	errInvalidSort       = ErrorDef{Status: 400, Code: string(include.INVALID_SORT), Message: "sort key not accepted by this resource"}
	errInvalidPagination = ErrorDef{Status: 400, Code: string(include.INVALID_PAGINATION), Message: "page, cursor or limit outside the resource's contract"}
	errInvalidFilter     = ErrorDef{Status: 400, Code: string(include.INVALID_FILTER), Message: "filter names an unknown field, edge or operator"}
)

// Include documents the ?include parameter of res with the API's limits and
// the 400 it can produce.
func (a *API) Include(res include.Resource) OpOpt {
	inc := IncludeParamWithLimits(res, a.opts.Limits)
	errs := Errors(errInvalidInclude)
	return func(op *humav2.Operation) { inc(op); errs(op) }
}

// Inputs documents every list parameter of res (apidoc.InputParams) and the
// 400 codes ResolveInputs can produce. Parameters already declared on the
// operation by name are left alone.
//
// DOCUMENTATION ONLY. huma validates a query parameter against the tags of
// the input struct's own field and nothing else, so a parameter that exists
// only here is never checked by huma — the enforcement is Bound.List →
// include.ResolveInputs, which reads the same include.Inputs this documents.
// The two cannot drift because they share the source; they are nonetheless
// two different mechanisms, and the 400s below come from the resolver.
func (a *API) Inputs(res include.Resource) OpOpt {
	params := apidoc.InputParams(res, a.opts.Limits, a.syntax)
	in, _ := include.InputsOf(res)
	defs := []ErrorDef{errInvalidInclude, errInvalidPagination}
	if in.Sort.Enabled {
		defs = append(defs, errInvalidSort)
	}
	if in.Filter.Enabled {
		defs = append(defs, errInvalidFilter)
	}
	errs := Errors(defs...)
	return func(op *humav2.Operation) {
		declared := map[string]bool{}
		for _, p := range op.Parameters {
			if p != nil && p.In == "query" {
				declared[p.Name] = true
			}
		}
		for _, p := range params {
			if declared[p.Name] {
				continue
			}
			schema, err := toHuma(apidoc.RawFragment(p.Schema).IR())
			if err != nil {
				panic(fmt.Sprintf("adapters/huma: building the %s parameter schema: %v", p.Name, err))
			}
			hp := &humav2.Param{Name: p.Name, In: "query", Description: p.Description, Schema: schema, Style: p.Style}
			if p.Explode {
				explode := true
				hp.Explode = &explode
			}
			op.Parameters = append(op.Parameters, hp)
		}
		errs(op)
		// ListQuery carries BOTH page and cursor, because the resolver has to
		// see the off-mode one to reject it (INVALID_PAGINATION). huma derives
		// a query parameter from every tagged field, so without this the
		// document of a cursor resource would advertise ?page and an offset
		// one ?cursor — the very drift Inputs exists to prevent. The removal
		// cannot happen here: huma appends the struct-derived parameters after
		// the decorators run, so Register does it on the registered operation.
		drop := "cursor"
		if in.Page.Mode == include.PageModeCursor {
			drop = "page"
		}
		if !declared[drop] {
			m := maps.Clone(op.Metadata) // never write through a shared base's map
			if m == nil {
				m = map[string]any{}
			}
			prev, _ := m[metaDropQuery].([]string)
			m[metaDropQuery] = append(append([]string{}, prev...), drop)
			op.Metadata = m
		}
	}
}

// metaDropQuery is the operation-metadata key Inputs uses to tell Register
// which huma-derived query parameters the resource does not accept. huma's
// Operation.Metadata is yaml:"-", so it never reaches the document.
const metaDropQuery = "wireleaf:drop-query-params"

// dropQueryParams removes the parameters Inputs marked from the operation huma
// registered. A hidden operation is not in the document and has nothing to
// prune.
func dropQueryParams(oapi *humav2.OpenAPI, op humav2.Operation) {
	names, _ := op.Metadata[metaDropQuery].([]string)
	if len(names) == 0 || op.Hidden {
		return
	}
	drop := map[string]bool{}
	for _, n := range names {
		drop[n] = true
	}
	reg := operationAt(oapi, op.Method, op.Path)
	kept := reg.Parameters[:0:0]
	for _, p := range reg.Parameters {
		if p != nil && p.In == "query" && drop[p.Name] {
			continue
		}
		kept = append(kept, p)
	}
	reg.Parameters = kept
}

// Register registers op with its decorators on the attached huma API.
func Register[I, O any](a *API, op humav2.Operation, handler func(context.Context, *I) (*O, error), opts ...OpOpt) {
	if a.huma == nil {
		panic("adapters/huma: Register before Attach")
	}
	a.checkBound(reflect.TypeFor[O]())
	final := Op(op, opts...)
	humav2.Register(a.huma, final, handler)
	dropQueryParams(a.huma.OpenAPI(), final)
}

// checkBound walks t and panics on a Node[W] wrapper no Bind registered:
// Node[W].Schema would otherwise fail deep inside huma's reflection.
func (a *API) checkBound(t reflect.Type) {
	seen := map[reflect.Type]bool{}
	var walk func(reflect.Type)
	walk = func(t reflect.Type) {
		if t == nil || seen[t] {
			return
		}
		seen[t] = true
		switch t.Kind() {
		case reflect.Pointer, reflect.Slice, reflect.Array, reflect.Map:
			walk(t.Elem())
		case reflect.Struct:
			if t.PkgPath() == reflect.TypeFor[API]().PkgPath() && strings.HasPrefix(t.Name(), "Node[") {
				if !a.bound[t] {
					panic(fmt.Sprintf("adapters/huma: output carries %s but no Bind registered it (call Bind before Register)", t))
				}
				return
			}
			for i := 0; i < t.NumField(); i++ {
				walk(t.Field(i).Type)
			}
		}
	}
	walk(t)
}

// sortedKeys returns m's keys in sorted order — a deterministic component
// emission order for a deterministic document.
func sortedKeys[V any](m map[string]V) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}
