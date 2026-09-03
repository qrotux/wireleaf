// hydrator.go — Hydrator, the bound form of the hydrate facades: root,
// registry and options captured once, the include string parsed inside, the
// id fetcher taken from the registry. The unbound facades stay for callers
// who need exclude lists or the plan back.

package include

import (
	"encoding/json"
	"fmt"
)

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
	return materializeOne(plan, rows[0], c)
}

// Query runs the list pipeline. The include plan resolves before fetch, so a
// malformed include is a 400 and never reaches the fetcher.
func (h *Hydrator) Query(c *Ctx, q QueryArgs, inc string, fetch ListFetcher) (QueryResult, error) {
	c = h.ctx(c)
	plan, err := h.plan(inc)
	if err != nil {
		return QueryResult{}, err
	}
	return runQuery(plan, q, fetch, c)
}

// Hydrate materializes an already-loaded root document (POST/PATCH replies).
func (h *Hydrator) Hydrate(c *Ctx, doc any, inc string) (json.RawMessage, error) {
	c = h.ctx(c)
	plan, err := h.plan(inc)
	if err != nil {
		return nil, err
	}
	return materializeOne(plan, doc, c)
}
