package graph

import "github.com/qrotux/wireleaf/include"

// ParentRows is one parent's slice of a batched reverse fetch result, in the
// application's own Row type. It is boxed to include.ParentRows at the bind
// seam so the engine only ever sees []any.
type ParentRows[Row any] struct {
	Rows       []Row
	HasMore    bool
	NextCursor string
}

// FetchByParents is the typed batched reverse fetcher: one call covers every
// parent id of a level, returning each parent's rows keyed by parent id.
type FetchByParents[Row any] func(c *include.Ctx, parentIDs []string, q include.EdgeQuery) (map[string]ParentRows[Row], error)

// FetchIDs binds a node's forward-batch fetcher. Row is inferred from the
// handle, so a fetcher of the wrong row type is a COMPILE error here.
// Re-binding the same node overwrites the previous fetcher (last wins);
// Compile does not reject it.
func FetchIDs[Row any, Wire any](b *Builder, h *NodeHandle[Row, Wire], fn func(c *include.Ctx, ids []string) ([]Row, error)) {
	b.mustLive()
	if fn == nil {
		h.spec.fetchIDs = nil
		return
	}
	h.spec.fetchIDs = func(c *include.Ctx, ids []string) ([]any, error) {
		rows, err := fn(c, ids)
		if err != nil {
			return nil, err
		}
		return boxSlice(rows), nil
	}
}

// FetchParents binds a node's batched reverse fetcher. Use PerParent to adapt
// a per-parent function. Re-binding the same node overwrites the previous
// fetcher (last wins); Compile does not reject it.
func FetchParents[Row any, Wire any](b *Builder, h *NodeHandle[Row, Wire], fn FetchByParents[Row]) {
	b.mustLive()
	if fn == nil {
		h.spec.fetchParents = nil
		return
	}
	h.spec.fetchParents = func(c *include.Ctx, parentIDs []string, q include.EdgeQuery) (map[string]include.ParentRows, error) {
		typed, err := fn(c, parentIDs, q)
		if err != nil {
			return nil, err
		}
		if typed == nil {
			return nil, nil
		}
		out := make(map[string]include.ParentRows, len(typed))
		for id, pr := range typed {
			out[id] = include.ParentRows{
				Rows:       boxSlice(pr.Rows),
				HasMore:    pr.HasMore,
				NextCursor: pr.NextCursor,
			}
		}
		return out, nil
	}
}

// PerParent adapts a per-parent fetcher into a batched one by looping the
// parent ids IN THE GIVEN ORDER. The first error short-circuits.
//
// A per-parent fetcher cannot emit NextCursor; use FetchParents directly if you
// need cursors.
func PerParent[Row any](fn func(c *include.Ctx, parentID string, q include.EdgeQuery) ([]Row, bool, error)) FetchByParents[Row] {
	return func(c *include.Ctx, parentIDs []string, q include.EdgeQuery) (map[string]ParentRows[Row], error) {
		out := make(map[string]ParentRows[Row], len(parentIDs))
		for _, id := range parentIDs {
			rows, hasMore, err := fn(c, id, q)
			if err != nil {
				return nil, err
			}
			out[id] = ParentRows[Row]{Rows: rows, HasMore: hasMore}
		}
		return out, nil
	}
}

// boxSlice converts a typed []Row into the engine's []any. A nil input yields
// a nil slice (not an empty non-nil one) so downstream "no rows" checks behave.
func boxSlice[Row any](rows []Row) []any {
	if rows == nil {
		return nil
	}
	out := make([]any, len(rows))
	for i, r := range rows {
		out[i] = r
	}
	return out
}
