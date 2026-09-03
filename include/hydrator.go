// hydrator.go — Hydrator, the bound form of the hydrate facades: root,
// registry and options captured once, the include string parsed inside, the
// id fetcher taken from the registry. The unbound facades stay for callers
// who need exclude lists or the plan back.

package include

import (
	"encoding/json"
	"fmt"
)

// ListFetcher is the list-side fetcher a Hydrator.Query calls: it owns the
// SQL, the offset or cursor select, and the continuation tokens.
type ListFetcher func(ctx *Ctx, q QueryArgs) (ListPage, error)

// ListPage is what a ListFetcher returns.
type ListPage struct {
	Docs    []any
	Total   int // offset mode: total rows; cursor mode: 0 = unknown
	HasMore bool
	// NextCursor / PrevCursor are opaque tokens in cursor mode; "" = none.
	NextCursor string
	PrevCursor string
}

// Hydrator binds one root resource to a registry and options.
type Hydrator struct {
	root Resource
	reg  Registry
	opts Options
}

// Bind returns a Hydrator for root over reg. Zero opts means DefaultOptions.
// A nil root or registry is a wiring error and panics.
func Bind(root Resource, reg Registry, opts Options) *Hydrator {
	if root == nil || reg == nil {
		panic("include: Bind needs a root resource and a registry")
	}
	if opts == (Options{}) {
		opts = DefaultOptions
	}
	return &Hydrator{root: root, reg: reg, opts: opts}
}

// Root returns the bound resource.
func (h *Hydrator) Root() Resource { return h.root }

func (h *Hydrator) ctx(c *Ctx) *Ctx {
	if c == nil {
		c = &Ctx{}
	}
	if c.Registry == nil {
		c.Registry = h.reg
	}
	return c
}

func (h *Hydrator) plan(inc string) (*PlanNode, error) {
	tree, err := ParseInclude(inc)
	if err != nil {
		return nil, err
	}
	return ResolvePlan(h.root, tree, nil, h.opts)
}

// ByID loads one root document through the registry's FetchByIDs fetcher and
// materializes it with the client's include string. 400 before fetch; a
// missing document is NOT_FOUND (404).
func (h *Hydrator) ByID(c *Ctx, id string, inc string) (json.RawMessage, error) {
	c = h.ctx(c)
	plan, err := h.plan(inc)
	if err != nil {
		return nil, err
	}
	fetch, ok := h.reg.FetchByIDs(h.root)
	if !ok {
		return nil, fmt.Errorf("include: node %q has no FetchByIDs fetcher", h.root.Name())
	}
	rows, err := fetch(c, []string{id})
	if err != nil {
		return nil, err
	}
	switch len(rows) {
	case 0:
		return nil, notFound(id)
	case 1:
	default:
		return nil, fmt.Errorf("include: FetchByIDs for node %q returned %d rows for one id", h.root.Name(), len(rows))
	}
	items, err := Materialize(plan, rows[:1], c)
	if err != nil {
		return nil, err
	}
	return items[0], nil
}

// Query runs the list pipeline: plan (400 before fetch), budget pre-check,
// fetch, materialize.
func (h *Hydrator) Query(c *Ctx, q QueryArgs, inc string, fetch ListFetcher) (QueryResult, error) {
	c = h.ctx(c)
	plan, err := h.plan(inc)
	if err != nil {
		return QueryResult{}, err
	}
	if q.Limit > 0 && plan.MaxRows > 0 && plan.Cost > 0 && q.Limit > plan.MaxRows/plan.Cost {
		return QueryResult{}, NewError(INCLUDE_BUDGET_EXCEEDED,
			fmt.Sprintf("estimated %d rows per document × page %d exceeds budget %d", plan.Cost, q.Limit, plan.MaxRows))
	}
	page, err := fetch(c, q)
	if err != nil {
		return QueryResult{}, err
	}
	data, err := Materialize(plan, page.Docs, c)
	if err != nil {
		return QueryResult{}, err
	}
	return QueryResult{
		Data: data, Total: page.Total, HasMore: page.HasMore,
		Page: q.Page, Limit: q.Limit,
		NextCursor: page.NextCursor, PrevCursor: page.PrevCursor,
	}, nil
}

// Hydrate materializes an already-loaded root document (POST/PATCH replies).
func (h *Hydrator) Hydrate(c *Ctx, doc any, inc string) (json.RawMessage, error) {
	c = h.ctx(c)
	plan, err := h.plan(inc)
	if err != nil {
		return nil, err
	}
	items, err := Materialize(plan, []any{doc}, c)
	if err != nil {
		return nil, err
	}
	return items[0], nil
}
