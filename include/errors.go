package include

import "fmt"

// Code is a string error code for include-engine errors.
type Code string

const (
	// INVALID_INCLUDE is returned when a requested include path is unknown or
	// not marked Includable on the resource graph.
	INVALID_INCLUDE Code = "INVALID_INCLUDE"

	// INCLUDE_TOO_DEEP is the code for BOTH include-tree size limits: the
	// requested depth exceeding Limits.MaxDepth, and the requested tree's total
	// node count exceeding Limits.MaxNodes. One code covers both because both
	// mean the same thing to a client — "ask for less" — and the Error's
	// message distinguishes them.
	INCLUDE_TOO_DEEP Code = "INCLUDE_TOO_DEEP"

	// INCLUDE_TOO_EXPENSIVE is returned at PLAN time when the static cost
	// estimate of the requested tree — per-edge limits multiplied down every
	// path and summed over all nodes, for ONE root document — exceeds
	// Limits.MaxCost. The client fixes it by lowering :limit() values or
	// dropping a nested collection.
	INCLUDE_TOO_EXPENSIVE Code = "INCLUDE_TOO_EXPENSIVE"

	// INCLUDE_BUDGET_EXCEEDED is returned when the rows actually materialized
	// for a request exceed Limits.MaxRows — either up front in HydrateByQuery
	// (estimate × page size) or mid-hydration when a fetcher returned more
	// than the estimate assumed. Same client remedy as INCLUDE_TOO_EXPENSIVE,
	// or a smaller page.
	INCLUDE_BUDGET_EXCEEDED Code = "INCLUDE_BUDGET_EXCEEDED"

	// INVALID_FILTER is returned by ResolveFilter for a condition the graph
	// does not admit: an unknown or non-filterable edge or column on the
	// path, a root with no columns, an empty or nil-holding group, an unknown
	// operator, or an operator the column's type does not support. Path is
	// the dotted field path ("author.name"), the path up to the offending
	// edge, or "<path>:<op>" for an operator fault.
	INVALID_FILTER Code = "INVALID_FILTER"

	// FILTER_TOO_DEEP covers both filter size limits, as INCLUDE_TOO_DEEP does
	// for includes: a condition path longer than Limits.MaxFilterDepth and a
	// tree with more than Limits.MaxFilterNodes conditions and groups (Path is
	// empty). A too-deep path is refused before any hop is checked, so Path
	// echoes only its first MaxFilterDepth+1 segments followed by "…" — the
	// client must not get to choose the size of the error body.
	FILTER_TOO_DEEP Code = "FILTER_TOO_DEEP"

	// NOT_FOUND is returned when the requested root entity does not exist.
	NOT_FOUND Code = "NOT_FOUND"
)

// Error is a structured include-engine error. Status is a plain int (HTTP
// status code) that callers may map onto their own error type.
type Error struct {
	Code   Code
	Path   string
	Status int
}

// NewError constructs an Error with the given code and path, defaulting Status
// to 400 (Bad Request).
func NewError(code Code, path string) *Error {
	return &Error{Code: code, Path: path, Status: 400}
}

// Error implements the error interface.
func (e *Error) Error() string {
	if e.Path != "" {
		return fmt.Sprintf("%s: %s (status %d)", e.Code, e.Path, e.Status)
	}
	return fmt.Sprintf("%s (status %d)", e.Code, e.Status)
}
