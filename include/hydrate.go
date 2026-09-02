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
type QueryArgs struct {
	Where string
	Sort  string
	Page  int
	Limit int
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
}

// ------------------------------------------------------------------ HydrateByQuery

// HydrateByQuery is the list facade:
//  1. ResolvePlan(root, inc, exc, opts) — 400-before-fetch: a resolve error
//     returns before fetch is ever called.
//  2. docs, total, hasMore, err = fetch(ctx, q)
//  3. data, err = Materialize(plan, docs, ctx)
//  4. return QueryResult{Data:data, Total:total, HasMore:hasMore, Page:q.Page,
//     Limit:q.Limit} + plan.
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
	// Step 1: 400-before-fetch.
	plan, err := ResolvePlan(root, inc, exc, opts)
	if err != nil {
		return QueryResult{}, nil, err
	}

	// Step 1b: budget pre-check. The estimate is per document; a known page
	// size lets us refuse before the root SQL runs. Division form so a hostile
	// Limit cannot overflow the product.
	if q.Limit > 0 && plan.MaxRows > 0 && plan.Cost > 0 && q.Limit > plan.MaxRows/plan.Cost {
		return QueryResult{}, nil, NewError(INCLUDE_BUDGET_EXCEEDED,
			fmt.Sprintf("estimated %d rows per document × page %d exceeds budget %d", plan.Cost, q.Limit, plan.MaxRows))
	}

	// Step 2: root fetch.
	docs, total, hasMore, err := fetch(ctx, q)
	if err != nil {
		return QueryResult{}, nil, err
	}

	// Step 3: materialize.
	data, err := Materialize(plan, docs, ctx)
	if err != nil {
		return QueryResult{}, nil, err
	}

	// Step 4: assemble result.
	return QueryResult{
		Data:    data,
		Total:   total,
		HasMore: hasMore,
		Page:    q.Page,
		Limit:   q.Limit,
	}, plan, nil
}

// ------------------------------------------------------------------ HydrateByID

// HydrateByID is the single-resource-by-id facade:
//  1. ResolvePlan first (400-before-fetch).
//  2. doc, err = fetch(ctx, id); propagate any non-nil error.
//  3. doc == nil → return a *Error with Status 404 and Code NOT_FOUND.
//  4. Materialize the single doc and return its one element + plan.
//
// This facade is for detail endpoints. The RootFetcher closure owns the DB
// call; the facade never touches a database.
func HydrateByID(
	root Resource,
	id string,
	inc IncludeTree,
	exc [][]string,
	fetch func(ctx *Ctx, id string) (doc any, err error),
	ctx *Ctx,
	opts Options,
) (json.RawMessage, *PlanNode, error) {
	// 400-before-fetch.
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

	items, err := Materialize(plan, []any{doc}, ctx)
	if err != nil {
		return nil, nil, err
	}
	return items[0], plan, nil
}

// ------------------------------------------------------------------ HydrateEntity

// HydrateEntity is the already-loaded-doc facade (POST/PATCH):
//  1. ResolvePlan first.
//  2. Materialize the single supplied doc (no root-fetch).
//
// Returns the materialized json.RawMessage + the resolved plan.
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
	items, err := Materialize(plan, []any{doc}, ctx)
	if err != nil {
		return nil, nil, err
	}
	return items[0], plan, nil
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
