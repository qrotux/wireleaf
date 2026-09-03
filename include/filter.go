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
//   - A to-many hop carries a QUANTIFIER (any / all / none) on its FilterStep;
//     the adapter turns each one into a correlated EXISTS / NOT EXISTS. A
//     to-one hop carries none. Limits.MaxFilterMany bounds the to-many hops
//     of one path (each is a nested subquery), MaxFilterDepth all hops, and
//     MaxFilterSubqueries the to-many hops of the WHOLE tree summed together —
//     the per-path bounds say nothing about a tree of many cheap conditions.
//   - Generating SQL (joins, EXISTS) is the adapter's job, outside this
//     module. ResolvedCond.Hops is what it walks.

package include

import (
	"reflect"
	"strconv"
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

// Quant is the quantifier a condition applies at a to-many hop: how one
// parent's children decide the parent's fate. On an EMPTY relation `all` and
// `none` hold and `any` does not — the mathematical reading, the same one
// Payload uses; an adapter must reproduce it (see docs/include.md → Filters).
type Quant string

const (
	QuantAny  Quant = "any"  // at least one child satisfies the condition
	QuantAll  Quant = "all"  // no child violates it
	QuantNone Quant = "none" // no child satisfies it
)

// FilterStep is one hop of a condition path: the edge key as declared on the
// node, and the quantifier — empty on a to-one hop, required on a to-many hop.
type FilterStep struct {
	Key   string
	Quant Quant
}

// FilterCond is one leaf condition in WIRE terms: the hops from the root
// (outermost first; nil for a root column), the wire json key of the column,
// the operator and the value as the parser produced it.
type FilterCond struct {
	Path  []FilterStep
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
	// Node is the node Column belongs to: the root when Hops is empty, else
	// Hops[len-1].To. It is redundant with Hops and carried anyway so a LEAF
	// is interpretable on its own — cached, serialized, or unit-tested as a
	// bare ResolvedFilter — without the caller having to carry the root in
	// from outside just to name the table the column sits on.
	Node Resource
}

// FilterHop is one traversed edge: its key as declared, the edge itself, the
// quantifier ("" on a to-one hop; any / all / none on a to-many hop, where
// the adapter emits EXISTS / NOT EXISTS), and the nodes on either side. Edge
// is a COPY of the traversed edge as Edges() returned it — there is no shared
// identity to compare against.
type FilterHop struct {
	Key   string
	Edge  Edge
	Quant Quant
	From  Resource
	To    Resource
}

func (ResolvedAnd) resolvedFilterNode()  {}
func (ResolvedOr) resolvedFilterNode()   {}
func (ResolvedCond) resolvedFilterNode() {}

// ------------------------------------------------------------------ resolve

// ResolveFilter checks f against root's graph and returns its SQL-side twin.
// Errors are *Error: INVALID_FILTER for anything the graph does not admit,
// FILTER_TOO_DEEP for a path or node count beyond opts.Limits,
// FILTER_TOO_EXPENSIVE for a tree whose total to-many hops exceed
// Limits.MaxFilterSubqueries (zero fields mean DefaultLimits). A nil f
// resolves to nil. Like ResolvePlan it runs before
// any fetch: a handler resolves the client's filter, then hands the result
// to its RootFetcher through QueryArgs.Where.
func ResolveFilter(root Resource, f Filter, opts Options) (ResolvedFilter, error) {
	if f == nil {
		return nil, nil
	}
	// No root means no graph to check against: refuse rather than panic on the
	// first Edges() call.
	if root == nil {
		return nil, NewError(INVALID_FILTER, "")
	}
	r := &filterResolver{
		root:          root,
		maxDepth:      opts.Limits.MaxFilterDepth,
		maxMany:       opts.Limits.MaxFilterMany,
		maxNodes:      opts.Limits.MaxFilterNodes,
		maxSubqueries: opts.Limits.MaxFilterSubqueries,
	}
	if r.maxDepth == 0 {
		r.maxDepth = DefaultLimits.MaxFilterDepth
	}
	if r.maxMany == 0 {
		r.maxMany = DefaultLimits.MaxFilterMany
	}
	if r.maxNodes == 0 {
		r.maxNodes = DefaultLimits.MaxFilterNodes
	}
	if r.maxSubqueries == 0 {
		r.maxSubqueries = DefaultLimits.MaxFilterSubqueries
	}
	return r.resolve(f, "")
}

type filterResolver struct {
	root                                       Resource
	maxDepth, maxMany, maxNodes, maxSubqueries int
	nodes                                      int // conditions + groups seen so far
	subqueries                                 int // to-many hops seen so far, tree-wide
}

// resolve walks one AST node. at is the node's POSITION in the tree — "" at
// the root, "or[0].and[1]" for a member of a nested group — and is the error
// path for every STRUCTURAL fault, which has no field path of its own: an
// empty group, a nil member, a nil pointer node, an unrecognized type. A leaf
// fault keeps its dotted field path instead; that is what the client edits.
func (r *filterResolver) resolve(f Filter, at string) (ResolvedFilter, error) {
	r.nodes++
	if r.nodes > r.maxNodes {
		// A size fault is about the tree, not a position in it.
		return nil, NewError(FILTER_TOO_DEEP, "")
	}
	switch n := f.(type) {
	case FilterAnd:
		out, err := r.group(n, at, "and")
		if err != nil {
			return nil, err
		}
		return ResolvedAnd(out), nil
	case FilterOr:
		out, err := r.group(n, at, "or")
		if err != nil {
			return nil, err
		}
		return ResolvedOr(out), nil
	case FilterCond:
		return r.cond(n)
	case *FilterCond:
		if n == nil {
			return nil, NewError(INVALID_FILTER, at)
		}
		return r.cond(*n)
	case *FilterAnd:
		if n == nil {
			return nil, NewError(INVALID_FILTER, at)
		}
		out, err := r.group(*n, at, "and")
		if err != nil {
			return nil, err
		}
		return ResolvedAnd(out), nil
	case *FilterOr:
		if n == nil {
			return nil, NewError(INVALID_FILTER, at)
		}
		out, err := r.group(*n, at, "or")
		if err != nil {
			return nil, err
		}
		return ResolvedOr(out), nil
	}
	return nil, NewError(INVALID_FILTER, at)
}

// group resolves the members of an And/Or. An empty group is rejected: its
// truth value (vacuous true for and, false for or) differs between SQL
// dialects and adapters, so the parser must not produce one. at is the group's
// own position, label its wire spelling; member i sits at "<at>.<label>[i]",
// which is what a client needs to find the offending element in a body where
// the members carry no names — a bare index into an unnamed array is
// unusable, so the group kind is spelled out with it.
func (r *filterResolver) group(members []Filter, at, label string) ([]ResolvedFilter, error) {
	if len(members) == 0 {
		return nil, NewError(INVALID_FILTER, at)
	}
	out := make([]ResolvedFilter, 0, len(members))
	for i, m := range members {
		pos := label + "[" + strconv.Itoa(i) + "]"
		if at != "" {
			pos = at + "." + pos
		}
		if m == nil {
			return nil, NewError(INVALID_FILTER, pos)
		}
		rm, err := r.resolve(m, pos)
		if err != nil {
			return nil, err
		}
		out = append(out, rm)
	}
	return out, nil
}

func (r *filterResolver) cond(c FilterCond) (ResolvedFilter, error) {
	keys := stepKeys(c.Path)
	if len(c.Path) > r.maxDepth {
		return nil, NewError(FILTER_TOO_DEEP, truncPath(keys, r.maxDepth+1))
	}
	cur := r.root
	hops := make([]FilterHop, 0, len(c.Path))
	many := 0
	for i, step := range c.Path {
		e, ok := cur.Edges()[step.Key]
		if !ok {
			// The keys BEFORE i were found in the graph and are bounded by it,
			// so they go back whole; this one is raw client text.
			bad := clientEcho(step.Key)
			if i > 0 {
				bad = strings.Join(keys[:i], ".") + "." + bad
			}
			return nil, NewError(INVALID_FILTER, bad)
		}
		if !e.Filterable || e.Target == nil {
			return nil, NewError(INVALID_FILTER, strings.Join(keys[:i+1], "."))
		}
		// The quantifier belongs to a to-many hop and to nothing else: on a
		// to-one hop there is one row and nothing to quantify over; on a
		// to-many hop without one the adapter could not choose between
		// EXISTS and NOT EXISTS. An unknown quantifier is refused here too —
		// Quant is a closed set the adapter switches over — and it is refused
		// FIRST, before the hop's arity is consulted: `author:bogus` and
		// `reviews:bogus` are the same client mistake (a word that is not a
		// quantifier), and reporting one of them as "quantifier on a to-one
		// hop" would send the client to fix the wrong half of the spelling.
		switch {
		case step.Quant != "" && !quantKnown(step.Quant):
			return nil, NewError(INVALID_FILTER, strings.Join(keys[:i+1], ".")+":"+clientEcho(string(step.Quant)))
		case !e.Many && step.Quant != "":
			return nil, NewError(INVALID_FILTER, strings.Join(keys[:i+1], ".")+":"+clientEcho(string(step.Quant)))
		case e.Many && step.Quant == "":
			return nil, NewError(INVALID_FILTER, strings.Join(keys[:i+1], "."))
		}
		if e.Many {
			many++
			if many > r.maxMany {
				return nil, NewError(FILTER_TOO_DEEP, truncPath(keys, r.maxDepth+1))
			}
			// The tree-wide total is counted here and JUDGED after the path is
			// walked, so a path that breaks the per-path bound reports
			// FILTER_TOO_DEEP — the narrower, more actionable fault — rather
			// than the tree being blamed for one over-long condition.
			r.subqueries++
		}
		next := e.Target()
		// A hand-built graph may return a nil target; Compile never produces one.
		if next == nil {
			return nil, NewError(INVALID_FILTER, strings.Join(keys[:i+1], "."))
		}
		hops = append(hops, FilterHop{Key: step.Key, Edge: e, Quant: step.Quant, From: cur, To: next})
		cur = next
	}
	if r.subqueries > r.maxSubqueries {
		// A cost fault is about the tree, not a position in it.
		return nil, NewError(FILTER_TOO_EXPENSIVE, "")
	}
	col, ok := ColumnsOf(cur)[c.Field]
	if !ok {
		// Unknown: client text, bounded. A column that EXISTS but is not
		// filterable is a graph name and goes back whole.
		return nil, NewError(INVALID_FILTER, condPath(keys, clientEcho(c.Field)))
	}
	if !col.Filterable {
		return nil, NewError(INVALID_FILTER, condPath(keys, c.Field))
	}
	if !opAllowed(c.Op, col.Type) {
		return nil, NewError(INVALID_FILTER, condPath(keys, c.Field)+":"+clientEcho(string(c.Op)))
	}
	return ResolvedCond{Hops: hops, Column: col, Op: c.Op, Value: c.Value, Node: cur}, nil
}

// FilterSubqueries reports the number of to-many hops in a resolved tree —
// every hop carrying a non-empty Quant, which the adapter emits as one
// correlated EXISTS / NOT EXISTS. It is the number ResolveFilter compared
// against Limits.MaxFilterSubqueries, exposed so an application can read the
// cost back and reserve it in its own budget next to plan.Cost — an
// examples/costlimit-style bucket charging a request for the subqueries its
// filter will run, not only the rows its includes will fetch. A nil filter
// costs 0.
func FilterSubqueries(f ResolvedFilter) int {
	switch n := f.(type) {
	case ResolvedAnd:
		total := 0
		for _, m := range n {
			total += FilterSubqueries(m)
		}
		return total
	case ResolvedOr:
		total := 0
		for _, m := range n {
			total += FilterSubqueries(m)
		}
		return total
	case ResolvedCond:
		total := 0
		for _, h := range n.Hops {
			if h.Quant != "" {
				total++
			}
		}
		return total
	}
	return 0
}

func quantKnown(q Quant) bool {
	return q == QuantAny || q == QuantAll || q == QuantNone
}

// clientEchoMax bounds any client string echoed in an INVALID_FILTER path.
const clientEchoMax = 16

// clientEcho renders client text for an error path, at most clientEchoMax
// bytes followed by "…". It applies to every string that reaches an error path
// BECAUSE it was not found in the graph — an unknown edge key, an unknown
// field, an unknown operator, an unknown quantifier: those are raw client text
// of unbounded length, and echoing them whole would let a client choose the
// size of the 400 body — the same reason truncPath exists. Names that WERE
// found are bounded by the graph and are echoed whole. The cut lands on a rune
// boundary; the known spellings are far below the bound and pass through
// unchanged.
func clientEcho(s string) string {
	if len(s) <= clientEchoMax {
		return s
	}
	end := 0
	for i := range s { // i is the byte offset of each rune
		if i > clientEchoMax {
			break
		}
		end = i
	}
	return s[:end] + "…"
}

// stepKeys projects a path onto its edge keys. Quantifiers never appear in an
// error path except where they ARE the fault ("author:any").
func stepKeys(path []FilterStep) []string {
	keys := make([]string, len(path))
	for i, s := range path {
		keys[i] = s.Key
	}
	return keys
}

// condPath renders a condition's wire path for an error: "a.b.field".
func condPath(keys []string, field string) string {
	parts := make([]string, 0, len(keys)+1)
	parts = append(parts, keys...)
	return strings.Join(append(parts, field), ".")
}

// truncPath renders at most n leading keys of a REJECTED path, each key
// bounded by clientEcho, and appends a trailing "…" ONLY when keys were
// actually dropped. A too-deep condition is refused BEFORE any hop is
// validated, so every key in it is raw client text: bounding the segment COUNT
// alone still lets one 4 KB key choose the size of the error body, so each key
// is bounded individually too. The MaxFilterMany site has the same property
// for the keys after the current hop — those were never looked up — and
// bounding the earlier, graph-known ones as well costs nothing: real column and
// edge names sit far below the bound and pass through unchanged. That site
// also never truncates: its path is within MaxFilterDepth by construction, so
// it carries every key and no trailing ellipsis — the mark means "there was
// more", and claiming it where nothing was cut misreads the fault.
func truncPath(keys []string, n int) string {
	dropped := len(keys) > n
	if n > len(keys) {
		n = len(keys)
	}
	parts := make([]string, n)
	for i, k := range keys[:n] {
		parts[i] = clientEcho(k)
	}
	out := strings.Join(parts, ".")
	if dropped {
		out += "…"
	}
	return out
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

// FilterableType reports whether t is a column type the filter engine can
// judge at all: bool, any signed or unsigned integer, any float, string, or
// time.Time. It is ONE predicate for two callers that have to agree —
// graph.Compile admits exactly this set for the `filter` tag option, and
// opAllowed reasons over it — because a type the compiler let through and the
// resolver then refused would be a filterable column no condition can ever
// name, a grant that silently grants nothing.
//
// A nil type is false. graph.Compile always fills Column.Type, so a nil there
// means a hand-built ColumnSource the engine cannot judge, and an adapter that
// binds a value it cannot type is the wrong failure mode.
func FilterableType(t reflect.Type) bool {
	if t == nil {
		return false
	}
	if t == filterTimeType {
		return true
	}
	return t.Kind() == reflect.Bool || orderedKinds[t.Kind()]
}

// opAllowed is the operator ↔ column-type matrix. A column whose type is
// outside FilterableType admits NOTHING, not even equality — Compile refuses
// to mark such a field filterable in the first place, and a hand-built source
// that marks one anyway is refused here rather than handed to an adapter.
func opAllowed(op FilterOp, t reflect.Type) bool {
	if !FilterableType(t) {
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
