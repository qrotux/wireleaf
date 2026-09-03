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
	// path, a to-many hop without a quantifier, a root with no columns, an
	// empty or nil-holding group, an unknown operator, or an operator the
	// column's type does not support.
	//
	// For a LEAF fault Path is the dotted field path ("author.name"), the path
	// up to the offending hop ("reviews"), "<path>:<quant>" for a quantifier
	// fault (an unknown word, or one on a to-one hop — the unknown word is
	// reported as such either way), or "<path>:<op>" for an operator fault.
	// Text that was not found in the graph (an unknown key, field, operator or
	// quantifier) is echoed bounded to 16 bytes plus "…"; a name the graph does
	// know goes back whole.
	//
	// A STRUCTURAL fault — an empty group, a nil member, a nil pointer node —
	// has no field path, so Path is the node's POSITION in the tree instead:
	// "and[1]", "or[0].and[2]", indexed and labelled with the group kind
	// because the members of a group carry no names. The root has no position:
	// an empty root-level group, and a nil root node, still report "".
	INVALID_FILTER Code = "INVALID_FILTER"

	// FILTER_TOO_DEEP covers both filter size limits, as INCLUDE_TOO_DEEP does
	// for includes: a condition path longer than Limits.MaxFilterDepth, a path
	// crossing more than Limits.MaxFilterMany to-many hops, and a tree with
	// more than Limits.MaxFilterNodes conditions and groups (Path is empty).
	// A too-deep path is refused before any hop is checked, so both its
	// segment COUNT and each segment's LENGTH are bounded: Path echoes only
	// the first MaxFilterDepth+1 segments, each one cut to 16 bytes plus "…"
	// like any other unvalidated client text. The client must not get to
	// choose the size of the error body, by either dimension. A trailing "…"
	// appears only when segments were actually DROPPED — the mark means
	// "there was more" — so a too-MANY-hops path, already within
	// MaxFilterDepth by the time it is counted, always comes back whole and
	// unmarked. It is the PER-PATH family of bounds plus the node count;
	// the tree-wide bound on to-many hops is FILTER_TOO_EXPENSIVE.
	FILTER_TOO_DEEP Code = "FILTER_TOO_DEEP"

	// FILTER_TOO_EXPENSIVE is returned by ResolveFilter when the filter tree's
	// to-many hops, summed over every condition, exceed
	// Limits.MaxFilterSubqueries — each one is a correlated subquery in the
	// adapter's SQL, so the total is what the tree costs. As with
	// INCLUDE_TOO_EXPENSIVE the fault is the tree, not a position in it, so
	// Path is empty; the client fixes it by dropping conditions that cross
	// to-many edges.
	FILTER_TOO_EXPENSIVE Code = "FILTER_TOO_EXPENSIVE"

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
