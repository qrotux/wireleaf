package huma

import (
	"context"
	"errors"
	"fmt"
	"reflect"

	humav2 "github.com/danielgtaylor/huma/v2"

	"github.com/qrotux/wireleaf/apidoc"
	"github.com/qrotux/wireleaf/include"
	"github.com/qrotux/wireleaf/include/filters"
)

// Bound is the per-resource facade: the resource, its Wire type W, the
// Hydrator, and the operation decorators, in one value a handler closes over.
type Bound[W any] struct {
	api *API
	res include.Resource
	hyd *include.Hydrator
}

// Bind binds node (the graph node name, which is also its component name) to
// W. It registers Node[W] as the component, so every Bind must precede the
// Register of an operation whose output carries Node[W]. Wiring errors panic.
func Bind[W any](a *API, node string) *Bound[W] {
	res := a.g.Resource(node) // panics "graph: unknown resource <name>" on a typo
	ws, ok := res.(apidoc.WireProvider)
	if !ok {
		panic(fmt.Sprintf("adapters/huma: Bind: node %q has no wire type", node))
	}
	sample := ws.WireSample()
	if sample == nil {
		panic(fmt.Sprintf("adapters/huma: Bind: node %q returns a nil WireSample", node))
	}
	if got, want := apidoc.DerefType(reflect.TypeOf(sample)), reflect.TypeFor[W](); got != want {
		panic(fmt.Sprintf("adapters/huma: Bind: wire type mismatch for node %q: graph declares %v, Bind asked for %v", node, got, want))
	}
	// Idempotent per (node, W), not per W — see API.bindings.
	wrapper := reflect.TypeFor[Node[W]]()
	if key := (bindKey{node: node, wrapper: wrapper}); !a.bindings[key] {
		RegisterNode[W](a.c, node)
		a.bindings[key] = true
	}
	// Keyed by wrapper type alone: this is checkBound's lookup, and an output
	// type carries the wrapper without naming a node. The core wrapper counts
	// too: RegisterNode registered it, and an envelope may carry it directly.
	a.bound[wrapper] = true
	a.bound[reflect.TypeFor[apidoc.Node[W]]()] = true
	return &Bound[W]{api: a, res: res, hyd: include.Bind(res, a.g, a.opts)}
}

// Resource returns the bound resource.
func (b *Bound[W]) Resource() include.Resource { return b.res }

// Include documents ?include for this resource (API.Include).
func (b *Bound[W]) Include() OpOpt { return b.api.Include(b.res) }

// Inputs documents every list parameter of this resource (API.Inputs).
func (b *Bound[W]) Inputs() OpOpt { return b.api.Inputs(b.res) }

func (b *Bound[W]) ctx(ctx context.Context) *include.Ctx {
	return &include.Ctx{Context: ctx, Registry: b.api.g, Request: requestOf(ctx)}
}

// asHumaError maps an engine *include.Error onto huma's status error; any
// other error is returned unchanged (a 500 for huma).
func asHumaError(err error) error {
	var ie *include.Error
	if errors.As(err, &ie) {
		return humav2.NewError(ie.Status, ie.Error())
	}
	return err
}

// Get loads and hydrates one document by id with the client's include string.
func (b *Bound[W]) Get(ctx context.Context, id, inc string) (Node[W], error) {
	raw, err := b.hyd.ByID(b.ctx(ctx), id, inc)
	if err != nil {
		return Node[W]{}, asHumaError(err)
	}
	return NodeOf[W](raw), nil
}

// Hydrate materializes an already-loaded row (a POST/PATCH reply).
func (b *Bound[W]) Hydrate(ctx context.Context, doc any, inc string) (Node[W], error) {
	raw, err := b.hyd.Hydrate(b.ctx(ctx), doc, inc)
	if err != nil {
		return Node[W]{}, asHumaError(err)
	}
	return NodeOf[W](raw), nil
}

// ListQuery is the list parameter block to embed anonymously in an input
// struct. Tags carry only the names: the document comes from Inputs(), the
// validation from include.ResolveInputs inside List. In bracket filter mode
// the filter is read from the request's where[...] keys instead, and a
// non-empty Where — the JSON spelling, which that mode does not accept — is a
// 400 INVALID_FILTER rather than a silently unfiltered page.
type ListQuery struct {
	Include string `query:"include"`
	Sort    string `query:"sort"`
	Page    int    `query:"page"`
	Cursor  string `query:"cursor"`
	Limit   int    `query:"limit"`
	Where   string `query:"where"`
}

// Page is a hydrated list page with the pagination block of the resource's
// mode filled (the other stays zero).
//
// It is NOT usable as a huma response body — the envelope derivation refuses a
// {data} struct whose second field is not json:"pagination", and the reflector
// then refuses the Node[W] it carries: copy Data and the pagination block of
// the resource's mode into the application's own {Data, Pagination} envelope
// struct instead.
type Page[W any] struct {
	Data   []Node[W]             `json:"data"`
	Mode   include.PageMode      `json:"-"`
	Offset PagePagination        `json:"-"`
	Cursor CursorPaginationTotal `json:"-"`
}

// List resolves q against the resource's Inputs, runs the fetcher through
// the Hydrator and assembles the page.
func (b *Bound[W]) List(ctx context.Context, q ListQuery, fetch include.ListFetcher) (Page[W], error) {
	in, _ := include.InputsOf(b.res)
	where, err := b.parseWhere(ctx, q.Where)
	if err != nil {
		return Page[W]{}, asHumaError(err)
	}
	args, err := include.ResolveInputs(b.res, include.RawInputs{Sort: q.Sort, Page: q.Page, Cursor: q.Cursor, Limit: q.Limit, Where: where}, b.api.opts)
	if err != nil {
		return Page[W]{}, asHumaError(err)
	}
	res, err := b.hyd.Query(b.ctx(ctx), args, q.Include, fetch)
	if err != nil {
		return Page[W]{}, asHumaError(err)
	}
	page := Page[W]{Data: make([]Node[W], len(res.Data)), Mode: in.Page.Mode}
	for i, raw := range res.Data {
		page.Data[i] = NodeOf[W](raw)
	}
	if in.Page.Mode == include.PageModeCursor {
		page.Cursor = CursorPaginationTotal{
			NextCursor:  optString(res.NextCursor),
			PrevCursor:  optString(res.PrevCursor),
			HasNextPage: res.HasMore,
			HasPrevPage: res.PrevCursor != "",
			Limit:       res.Limit,
		}
		if res.Total > 0 {
			total := res.Total
			page.Cursor.TotalDocs = &total
		}
	} else {
		page.Offset = PagePagination{
			Page:        res.Page,
			TotalPages:  (res.Total + res.Limit - 1) / res.Limit,
			TotalDocs:   res.Total,
			HasNextPage: res.HasMore,
			HasPrevPage: res.Page > 1,
		}
	}
	return page, nil
}

func (b *Bound[W]) parseWhere(ctx context.Context, raw string) (include.Filter, error) {
	if b.api.syntax == apidoc.FilterBracket {
		// In bracket mode the document describes `where` as a deepObject, so a
		// bare `?where=<json>` is the WRONG spelling, not an empty one — and
		// huma binds ListQuery.Where only from a literal `where=` key, never
		// from a `where[...]` one, so a non-empty raw here can only be that
		// mistake. Rejecting it beats answering it with an unfiltered 200.
		if raw != "" {
			return nil, include.NewError(include.INVALID_FILTER, "where")
		}
		r := requestOf(ctx)
		if r == nil {
			return nil, errors.New("adapters/huma: bracket filter needs Attach (no request snapshot on the context)")
		}
		return filters.ParseQuery(b.res, r.Query)
	}
	if raw == "" {
		return nil, nil
	}
	return filters.ParseJSON(b.res, []byte(raw))
}

func optString(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}
