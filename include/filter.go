// filter.go — the filter model: a format-neutral condition tree over a
// Resource's filterable columns and edges, and its resolution against the
// graph.
//
// Design:
//   - The AST here is DATA a parser produces — a JSON `where` body, a
//     `?where[field][op]=` query string; neither parser lives in this
//     package — and ResolveFilter checks it against the compiled graph: every
//     hop must be a Filterable edge, the leaf a Filterable column, the
//     operator legal for the column's Go type, the tree within Limits. What
//     comes out carries SQL-side names only; the client's spelling never
//     reaches SQL text, the rule EdgeQuery.Sort already follows.
//   - Values are carried verbatim (any). Coercing "2024-01-01" into a
//     time.Time is the parser's job and binding it is the adapter's; the
//     engine judges the OPERATOR against the column's type, not the value.
//   - v1 traverses TO-ONE edges only: graph.Compile admits Filterable() on
//     nothing else, and resolution refuses a to-many hop on its own account
//     too (include is hand-implementable). Quantifiers (any / all / none) are
//     the extension point, not a special case.
//   - Generating SQL (joins, EXISTS) is the adapter's job, outside this
//     module. ResolvedCond.Hops is what it walks.

package include

import (
	"reflect"
	"strings"
	"time"
)

// ------------------------------------------------------------------ AST

// FilterOp is one operator of the closed set a condition may use.
type FilterOp string

const (
	OpEq  FilterOp = "eq"
	OpNe  FilterOp = "ne"
	OpLt  FilterOp = "lt"
	OpLte FilterOp = "lte"
	OpGt  FilterOp = "gt"
	OpGte FilterOp = "gte"
	OpIn  FilterOp = "in"
	OpNin FilterOp = "nin"
)

// Filter is the sealed filter AST a parser produces: FilterAnd, FilterOr or
// FilterCond (by value or pointer).
type Filter interface{ filterNode() }

// FilterAnd holds conditions that must all hold. Never empty.
type FilterAnd []Filter

// FilterOr holds conditions of which at least one must hold. Never empty.
type FilterOr []Filter

// FilterCond is one leaf condition in WIRE terms: edge keys from the root
// (outermost first; nil for a root column), the wire json key of the column,
// the operator and the value as the parser produced it.
type FilterCond struct {
	Path  []string
	Field string
	Op    FilterOp
	Value any
}

func (FilterAnd) filterNode()  {}
func (FilterOr) filterNode()   {}
func (FilterCond) filterNode() {}

// ------------------------------------------------------------------ resolved

// ResolvedFilter mirrors Filter with every name resolved to its SQL side:
// ResolvedAnd, ResolvedOr or ResolvedCond.
type ResolvedFilter interface{ resolvedFilterNode() }

// ResolvedAnd is a resolved FilterAnd.
type ResolvedAnd []ResolvedFilter

// ResolvedOr is a resolved FilterOr.
type ResolvedOr []ResolvedFilter

// ResolvedCond is one resolved leaf: the edges traversed from the root, the
// column on the last hop's target (or on the root when Hops is empty), the
// operator, and the value verbatim from FilterCond.Value.
type ResolvedCond struct {
	Hops   []FilterHop
	Column Column
	Op     FilterOp
	Value  any
}

// FilterHop is one traversed edge: its key as declared, the edge itself, and
// the nodes on either side. Edge is a COPY of the traversed edge as Edges()
// returned it — there is no shared identity to compare against.
type FilterHop struct {
	Key  string
	Edge Edge
	From Resource
	To   Resource
}

func (ResolvedAnd) resolvedFilterNode()  {}
func (ResolvedOr) resolvedFilterNode()   {}
func (ResolvedCond) resolvedFilterNode() {}

// ------------------------------------------------------------------ resolve

// ResolveFilter checks f against root's graph and returns its SQL-side twin.
// Errors are *Error: INVALID_FILTER for anything the graph does not admit,
// FILTER_TOO_DEEP for a tree beyond opts.Limits (zero fields mean
// DefaultLimits). A nil f resolves to nil. Like ResolvePlan it runs before
// any fetch: a handler resolves the client's filter, then hands the result
// to its RootFetcher through QueryArgs.Where.
func ResolveFilter(root Resource, f Filter, opts Options) (ResolvedFilter, error) {
	if f == nil {
		return nil, nil
	}
	r := &filterResolver{
		root:     root,
		maxDepth: opts.Limits.MaxFilterDepth,
		maxNodes: opts.Limits.MaxFilterNodes,
	}
	if r.maxDepth == 0 {
		r.maxDepth = DefaultLimits.MaxFilterDepth
	}
	if r.maxNodes == 0 {
		r.maxNodes = DefaultLimits.MaxFilterNodes
	}
	return r.resolve(f)
}

type filterResolver struct {
	root               Resource
	maxDepth, maxNodes int
	nodes              int // conditions + groups seen so far
}

func (r *filterResolver) resolve(f Filter) (ResolvedFilter, error) {
	r.nodes++
	if r.nodes > r.maxNodes {
		return nil, NewError(FILTER_TOO_DEEP, "")
	}
	switch n := f.(type) {
	case FilterAnd:
		out, err := r.group(n)
		if err != nil {
			return nil, err
		}
		return ResolvedAnd(out), nil
	case FilterOr:
		out, err := r.group(n)
		if err != nil {
			return nil, err
		}
		return ResolvedOr(out), nil
	case FilterCond:
		return r.cond(n)
	case *FilterCond:
		if n == nil {
			return nil, NewError(INVALID_FILTER, "")
		}
		return r.cond(*n)
	case *FilterAnd:
		if n == nil {
			return nil, NewError(INVALID_FILTER, "")
		}
		out, err := r.group(*n)
		if err != nil {
			return nil, err
		}
		return ResolvedAnd(out), nil
	case *FilterOr:
		if n == nil {
			return nil, NewError(INVALID_FILTER, "")
		}
		out, err := r.group(*n)
		if err != nil {
			return nil, err
		}
		return ResolvedOr(out), nil
	}
	return nil, NewError(INVALID_FILTER, "")
}

// group resolves the members of an And/Or. An empty group is rejected: its
// truth value (vacuous true for and, false for or) differs between SQL
// dialects and adapters, so the parser must not produce one.
func (r *filterResolver) group(members []Filter) ([]ResolvedFilter, error) {
	if len(members) == 0 {
		return nil, NewError(INVALID_FILTER, "")
	}
	out := make([]ResolvedFilter, 0, len(members))
	for _, m := range members {
		if m == nil {
			return nil, NewError(INVALID_FILTER, "")
		}
		rm, err := r.resolve(m)
		if err != nil {
			return nil, err
		}
		out = append(out, rm)
	}
	return out, nil
}

func (r *filterResolver) cond(c FilterCond) (ResolvedFilter, error) {
	if len(c.Path) > r.maxDepth {
		return nil, NewError(FILTER_TOO_DEEP, truncPath(c.Path, r.maxDepth+1))
	}
	cur := r.root
	hops := make([]FilterHop, 0, len(c.Path))
	for i, key := range c.Path {
		e, ok := cur.Edges()[key]
		// Many is re-checked here, not assumed: graph.Compile already refuses
		// Filterable() on a to-many edge, but include is hand-implementable,
		// and a to-many hop without a quantifier would make the adapter emit a
		// row-multiplying join.
		if !ok || !e.Filterable || e.Many || e.Target == nil {
			return nil, NewError(INVALID_FILTER, strings.Join(c.Path[:i+1], "."))
		}
		next := e.Target()
		hops = append(hops, FilterHop{Key: key, Edge: e, From: cur, To: next})
		cur = next
	}
	col, ok := ColumnsOf(cur)[c.Field]
	if !ok || !col.Filterable {
		return nil, NewError(INVALID_FILTER, condPath(c))
	}
	if !opAllowed(c.Op, col.Type) {
		return nil, NewError(INVALID_FILTER, condPath(c)+":"+string(c.Op))
	}
	return ResolvedCond{Hops: hops, Column: col, Op: c.Op, Value: c.Value}, nil
}

// condPath renders a condition's wire path for an error: "a.b.field".
func condPath(c FilterCond) string {
	parts := make([]string, 0, len(c.Path)+1)
	parts = append(parts, c.Path...)
	return strings.Join(append(parts, c.Field), ".")
}

// truncPath renders at most n leading segments of a REJECTED path, marked with
// an ellipsis. A too-deep condition is refused before any hop is checked, so
// its path is unvalidated client text of unbounded length; echoing all of it
// back would let a client choose the size of the error body.
func truncPath(path []string, n int) string {
	if n > len(path) {
		n = len(path)
	}
	return strings.Join(path[:n], ".") + "…"
}

// orderedKinds are the column kinds the ordering operators apply to; time.Time
// is admitted by type. Equality and membership apply to every column.
var orderedKinds = map[reflect.Kind]bool{
	reflect.String: true,
	reflect.Int:    true, reflect.Int8: true, reflect.Int16: true, reflect.Int32: true, reflect.Int64: true,
	reflect.Uint: true, reflect.Uint8: true, reflect.Uint16: true, reflect.Uint32: true, reflect.Uint64: true,
	reflect.Float32: true, reflect.Float64: true,
}

var filterTimeType = reflect.TypeFor[time.Time]()

// opAllowed is the operator ↔ column-type matrix. A typeless column admits
// NOTHING, not even equality: graph.Compile always fills Column.Type, so a nil
// there means a hand-built ColumnSource the engine cannot judge — and an
// adapter that binds a value it cannot type is the wrong failure mode.
func opAllowed(op FilterOp, t reflect.Type) bool {
	if t == nil {
		return false
	}
	switch op {
	case OpEq, OpNe, OpIn, OpNin:
		return true
	case OpLt, OpLte, OpGt, OpGte:
		return orderedKinds[t.Kind()] || t == filterTimeType
	}
	return false
}
