package huma

import (
	"context"
	"fmt"
	"maps"
	"reflect"
	"slices"
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
	for _, name := range slices.Sorted(maps.Keys(frags)) {
		// Checked here, not left to registerLibraryComponents: that installer
		// would refuse the name too, but as a schema conflict on ITS component,
		// which points away from the node that caused it.
		if slices.Contains(libraryComponents, name) {
			panic(fmt.Sprintf("adapters/huma: node %q collides with the library component of the same name", name))
		}
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

// Attach installs the request middleware on h, hooks the document so Inputs'
// pruning runs on every operation huma adds, and makes Register usable. Call
// it once.
func (a *API) Attach(h humav2.API) {
	if h == nil {
		panic("adapters/huma: Attach needs a huma API")
	}
	if a.huma != nil {
		panic("adapters/huma: Attach called twice")
	}
	h.UseMiddleware(requestMiddleware)
	// The pruning is a document hook, not a step after Register, because h may
	// be a *humav2.Group: a group rewrites op.Path through its prefix modifier
	// on the way to AddOperation, so the path Register holds is not the path
	// the operation is filed under and looking it up by that path would panic.
	// OnAddOperation runs with the operation huma actually filed — after
	// processInputType appended the ListQuery-derived parameters, and under
	// its final path. A hidden operation is never added, and has nothing to
	// prune.
	oapi := h.OpenAPI()
	oapi.OnAddOperation = append(oapi.OnAddOperation, func(_ *humav2.OpenAPI, op *humav2.Operation) {
		dropQueryParams(op)
	})
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
//
// ORDER MATTERS. Inputs also PRUNES: every query parameter huma would derive
// from ListQuery that this resource does not accept (the off-mode page or
// cursor, sort when the node disabled sorting, where when it disabled
// filtering) is removed from the registered operation, so the document never
// advertises an input the resolver will reject. The "already declared" snapshot
// that exempts a parameter from both the documenting and the pruning is taken
// when the decorator RUNS — so an application that wants to keep one of those
// names on the operation must declare it BEFORE Inputs() in the option list; a
// decorator listed after Inputs() is invisible to it and its parameter is
// pruned.
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
			if p.Style != "" {
				// A style carries its own explode default, so a styled
				// parameter states the flag either way; an unstyled one
				// keeps huma's form/true default.
				explode := p.Explode
				hp.Explode = &explode
			}
			op.Parameters = append(op.Parameters, hp)
		}
		errs(op)
		// ListQuery carries EVERY list field (the resolver must see a value
		// the resource does not accept in order to reject it), and huma
		// derives a query parameter from each tagged field; everything
		// InputParams left out is therefore pruned. Not here — huma appends
		// the struct-derived parameters after the decorators run — so this
		// only records the names and Attach's OnAddOperation hook removes them.
		documented := map[string]bool{}
		for _, p := range params {
			documented[p.Name] = true
		}
		var drop []string
		for _, name := range listQueryParams {
			if !documented[name] && !declared[name] {
				drop = append(drop, name)
			}
		}
		if len(drop) > 0 {
			m := maps.Clone(op.Metadata) // never write through a shared base's map
			if m == nil {
				m = map[string]any{}
			}
			prev, _ := m[metaDropQuery].([]string)
			m[metaDropQuery] = append(append([]string{}, prev...), drop...)
			op.Metadata = m
		}
	}
}

// listQueryParams are the query parameter names huma derives from ListQuery,
// read off its tags so a field added there is pruned without a second edit.
var listQueryParams = queryTagsOf(reflect.TypeFor[ListQuery]())

func queryTagsOf(t reflect.Type) []string {
	var names []string
	for i := 0; i < t.NumField(); i++ {
		if name := t.Field(i).Tag.Get("query"); name != "" {
			names = append(names, name)
		}
	}
	return names
}

// metaDropQuery is the operation-metadata key Inputs uses to tell the
// document hook which huma-derived query parameters the resource does not
// accept. huma's Operation.Metadata is yaml:"-", so it never reaches the
// document.
const metaDropQuery = "wireleaf:drop-query-params"

// dropQueryParams removes the parameters Inputs marked from op, in place. It
// runs from Attach's OnAddOperation hook, so op is the operation huma filed in
// the document: its parameter list is final and its path is whatever a group
// prefix made it.
func dropQueryParams(op *humav2.Operation) {
	names, _ := op.Metadata[metaDropQuery].([]string)
	if len(names) == 0 {
		return
	}
	drop := map[string]bool{}
	for _, n := range names {
		drop[n] = true
	}
	kept := op.Parameters[:0:0]
	for _, p := range op.Parameters {
		if p != nil && p.In == "query" && drop[p.Name] {
			continue
		}
		kept = append(kept, p)
	}
	op.Parameters = kept
}

// Register registers op with its decorators on the attached huma API.
func Register[I, O any](a *API, op humav2.Operation, handler func(context.Context, *I) (*O, error), opts ...OpOpt) {
	if a == nil {
		panic("adapters/huma: Register on a nil *API")
	}
	if a.huma == nil {
		panic("adapters/huma: Register before Attach")
	}
	a.checkBound(reflect.TypeFor[O]())
	humav2.Register(a.huma, Op(op, opts...), handler)
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
		case reflect.Pointer, reflect.Slice, reflect.Array:
			walk(t.Elem())
		case reflect.Map:
			walk(t.Key())
			walk(t.Elem())
		case reflect.Struct:
			// Both wrappers Bind registers: this package's Node[W] and the
			// core apidoc.Node[W] it embeds.
			if isNodeWrapper(t) {
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

func isNodeWrapper(t reflect.Type) bool {
	pkg := t.PkgPath()
	return (pkg == reflect.TypeFor[API]().PkgPath() || pkg == reflect.TypeFor[apidoc.Node[struct{}]]().PkgPath()) && strings.HasPrefix(t.Name(), "Node[")
}
