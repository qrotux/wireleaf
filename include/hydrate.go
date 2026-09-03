// hydrate.go — three thin facades wrapping resolve → (optional) root-fetch →
// materialize, plus HasInclude. These are the call-site entry points for
// endpoint handlers; the package stays transport-free and DB-free.
//
// Design:
//   - The "400-before-fetch" invariant: ResolvePlan always runs FIRST. A bad
//     include/exclude string returns an *Error before any fetch closure is invoked.
//   - Offset-vs-cursor is NOT the facade's concern. The RootFetcher closure owns
//     the SQL and the cursor-select invariant; HydrateByQuery is mode-agnostic.
//   - HydrateByID handles the 404 path: fetch returning (nil, nil) → *Error{404}.
//   - HydrateEntity is for POST/PATCH: no root-fetch, doc is already in memory.

package include

import (
	"encoding/json"
	"fmt"
)

// ------------------------------------------------------------------ Types

// QueryArgs carries the pagination and filter parameters for a list query.
// The endpoint-supplied RootFetcher receives these verbatim; the facade does
// not interpret them.
//
// Where is the graph-checked filter — the output of ResolveFilter, carrying
// SQL-side names only — passed to the RootFetcher verbatim; nil means no
// filter. (The v0 opaque string is gone: an application-owned filter travels
// in the RootFetcher closure, not in QueryArgs.)
type QueryArgs struct {
	Where ResolvedFilter
	Sort  string
	Page  int
	Limit int
	// Cursor is the OPAQUE continuation token in cursor mode ("" = first
	// page); the fetcher that produced it decodes it. Zero in offset mode.
	Cursor string
}

// RootFetcher is the endpoint's root-fetch closure. It owns the SQL (including
// the cursor-select invariant when operating in cursor mode). The facade calls
// it AFTER a successful ResolvePlan so any DB round-trip is skipped on a bad
// ?include= string.
//
// Returns:
//   - docs: the fetched root documents (may be empty, must not be nil on success).
//   - total: the total row count (meaningful for offset pagination; 0 is fine for cursor).
//   - hasMore: whether there is a next page.
//   - err: non-nil aborts the hydration.
type RootFetcher func(ctx *Ctx, q QueryArgs) (docs []any, total int, hasMore bool, err error)

// QueryResult is the materialized list result returned by HydrateByQuery.
type QueryResult struct {
	Data    []json.RawMessage
	Total   int
	HasMore bool
	Page    int
	Limit   int
	// NextCursor / PrevCursor are the tokens the ListFetcher returned in
	// cursor mode; "" = none. Untouched by the offset facades.
	NextCursor string
	PrevCursor string
}

// ListFetcher is the cursor-aware sibling of RootFetcher: it owns the SQL, the
// offset or cursor select, and the continuation tokens.
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

// ------------------------------------------------------------------ shared pipeline

// budgetCheck refuses a page whose static row estimate would exceed the plan's
// budget: the estimate is per document, so a known page size lets the facade
// refuse before the root SQL runs. Division form so a hostile Limit cannot
// overflow the product.
func budgetCheck(plan *PlanNode, limit int) error {
	if limit > 0 && plan.MaxRows > 0 && plan.Cost > 0 && limit > plan.MaxRows/plan.Cost {
		return NewError(INCLUDE_BUDGET_EXCEEDED,
			fmt.Sprintf("estimated %d rows per document × page %d exceeds budget %d", plan.Cost, limit, plan.MaxRows))
	}
	return nil
}

// runQuery is the list pipeline every facade shares: budget pre-check, fetch,
// materialize, assemble. The plan is already resolved by the caller, which is
// what keeps the 400 ahead of the fetch.
func runQuery(plan *PlanNode, q QueryArgs, fetch ListFetcher, ctx *Ctx) (QueryResult, error) {
	if err := budgetCheck(plan, q.Limit); err != nil {
		return QueryResult{}, err
	}
	page, err := fetch(ctx, q)
	if err != nil {
		return QueryResult{}, err
	}
	data, err := Materialize(plan, page.Docs, ctx)
	if err != nil {
		return QueryResult{}, err
	}
	return QueryResult{
		Data: data, Total: page.Total, HasMore: page.HasMore,
		Page: q.Page, Limit: q.Limit,
		NextCursor: page.NextCursor, PrevCursor: page.PrevCursor,
	}, nil
}

// materializeOne materializes a single already-loaded root document.
func materializeOne(plan *PlanNode, doc any, ctx *Ctx) (json.RawMessage, error) {
	items, err := Materialize(plan, []any{doc}, ctx)
	if err != nil {
		return nil, err
	}
	return items[0], nil
}

// listFetcherOf adapts a RootFetcher to the ListFetcher shape; it returns no
// continuation tokens.
func listFetcherOf(fetch RootFetcher) ListFetcher {
	return func(ctx *Ctx, q QueryArgs) (ListPage, error) {
		docs, total, hasMore, err := fetch(ctx, q)
		if err != nil {
			return ListPage{}, err
		}
		return ListPage{Docs: docs, Total: total, HasMore: hasMore}, nil
	}
}

// ------------------------------------------------------------------ HydrateByQuery

// HydrateByQuery is the list facade: ResolvePlan first, so a bad include
// string returns before fetch is ever called, then the shared list pipeline.
//
// Offset-vs-cursor is the caller's concern: the RootFetcher encapsulates the
// SQL and the cursor-select invariant. This facade is mode-agnostic.
func HydrateByQuery(
	root Resource,
	q QueryArgs,
	inc IncludeTree,
	exc [][]string,
	fetch RootFetcher,
	ctx *Ctx,
	opts Options,
) (QueryResult, *PlanNode, error) {
	plan, err := ResolvePlan(root, inc, exc, opts)
	if err != nil {
		return QueryResult{}, nil, err
	}
	res, err := runQuery(plan, q, listFetcherOf(fetch), ctx)
	if err != nil {
		return QueryResult{}, nil, err
	}
	return res, plan, nil
}

// ------------------------------------------------------------------ HydrateByID

// HydrateByID is the single-resource-by-id facade: ResolvePlan first
// (400-before-fetch), then the caller's fetch; a nil doc is the 404 path.
//
// This facade is for detail endpoints. The fetch closure owns the DB call; the
// facade never touches a database.
func HydrateByID(
	root Resource,
	id string,
	inc IncludeTree,
	exc [][]string,
	fetch func(ctx *Ctx, id string) (doc any, err error),
	ctx *Ctx,
	opts Options,
) (json.RawMessage, *PlanNode, error) {
	plan, err := ResolvePlan(root, inc, exc, opts)
	if err != nil {
		return nil, nil, err
	}
	doc, err := fetch(ctx, id)
	if err != nil {
		return nil, nil, err
	}
	if doc == nil {
		return nil, nil, notFound(id)
	}
	raw, err := materializeOne(plan, doc, ctx)
	if err != nil {
		return nil, nil, err
	}
	return raw, plan, nil
}

// ------------------------------------------------------------------ HydrateEntity

// HydrateEntity is the already-loaded-doc facade (POST/PATCH): no root fetch,
// the doc is in memory. Returns the materialized bytes and the resolved plan.
func HydrateEntity(
	root Resource,
	doc any,
	inc IncludeTree,
	exc [][]string,
	ctx *Ctx,
	opts Options,
) (json.RawMessage, *PlanNode, error) {
	plan, err := ResolvePlan(root, inc, exc, opts)
	if err != nil {
		return nil, nil, err
	}
	raw, err := materializeOne(plan, doc, ctx)
	if err != nil {
		return nil, nil, err
	}
	return raw, plan, nil
}

// ------------------------------------------------------------------ HasInclude

// HasInclude reports whether plan.Children contains a child with the given
// EdgeKey. Handlers branch on it to know whether a specific include resolved.
// (*PlanNode).Get returns that child node itself — use it when the handler
// needs the node's Args or its Computed flag, e.g. to decide whether to splice
// a computed edge's value into the engine bytes.
func HasInclude(plan *PlanNode, edgeKey string) bool {
	return plan.Get(edgeKey) != nil
}

// ------------------------------------------------------------------ helpers

// notFound returns a 404 *Error for the given resource path/id.
func notFound(path string) *Error {
	return &Error{Code: NOT_FOUND, Path: path, Status: 404}
}
