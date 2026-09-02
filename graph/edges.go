package graph

import (
	"reflect"

	"github.com/qrotux/wireleaf/apidoc"
	"github.com/qrotux/wireleaf/include"
)

// EdgeKind discriminates the five edge kinds at declaration; Compile maps it
// onto include.Edge fields. Its fields are deliberately unexported:
// applications only ever build one through the constructors below.
//
// The target is addressed by its WIRE TYPE, not by a node handle: Compile
// already enforces that one wire type belongs to exactly one node, so the type
// is a unique node key that the Go compiler spell-checks — and edge
// declarations need no sibling handles in scope, which is what lets a graph be
// laid out one file per node.
type EdgeKind struct {
	kind        include.EdgeKindType // always set (KindComputed for Computed)
	targetWireT reflect.Type         // nil for computed
	backref     string               // Reverse
	arrayPath   string               // InArray
	subField    string               // InArray
	schema      apidoc.Schema        // Computed only
}

// ToOne the parent holds a single scalar FK (declare it with .ForeignKey).
// W is the TARGET node's wire type.
func ToOne[W any]() EdgeKind {
	return EdgeKind{kind: include.KindToOne, targetWireT: reflect.TypeFor[W]()}
}

// ToMany the parent holds an FK array (forward-hasMany; declare it with
// .ForeignKeys). W is the TARGET node's wire type.
func ToMany[W any]() EdgeKind {
	return EdgeKind{kind: include.KindForwardHasMany, targetWireT: reflect.TypeFor[W]()}
}

// Reverse the FK lives on the child, in the field named by backref. W is the
// TARGET node's wire type.
func Reverse[W any](backref string) EdgeKind {
	return EdgeKind{kind: include.KindReverse, targetWireT: reflect.TypeFor[W](), backref: backref}
}

// InArray the FK elements live inside arrayPath on the parent, under
// subField (harvest them with .ForeignKeys). W is the TARGET node's wire type.
func InArray[W any](arrayPath, subField string) EdgeKind {
	return EdgeKind{kind: include.KindInArray, targetWireT: reflect.TypeFor[W](), arrayPath: arrayPath, subField: subField}
}

// Computed an edge with no target node whose value is produced by application
// code and documented by schema.
func Computed(schema apidoc.Schema) EdgeKind {
	return EdgeKind{kind: include.KindComputed, schema: schema}
}

// ------------------------------------------------------------------ edge settings

// edgeSettings is what the EdgeBuilder chain writes and Compile reads. The
// *Set flags distinguish "never declared" from "declared with a nil closure"
// (the latter is a finding, never a silent no-op).
type edgeSettings struct {
	foreignKey    func(any) string
	foreignKeySet bool

	foreignKeys    func(any) []string
	foreignKeysSet bool

	guard    func(*include.Ctx, any) bool
	guardSet bool

	inverse    string
	required   bool
	includable bool
	filterable bool
	bare       bool
	limit      int
	limitSet   bool

	estimatedRows    int
	estimatedRowsSet bool

	envelope    include.Envelope
	envelopeSet bool
	sort        string
	args        []include.EdgeArg

	// Per-edge policy overrides; a nil field inherits the request's Options.
	policies include.EdgePolicies
}

// ------------------------------------------------------------------ EdgeBuilder

// EdgeBuilder is the chained configurator NodeHandle.Edge returns. It is
// parameterized by the PARENT node's Row type, so the closures declared here
// (ForeignKey, ForeignKeys, Guard) are checked against the right Row by the Go
// COMPILER — a closure typed on another node's Row does not build, where the
// option-based API only caught it as a Compile finding.
//
// Methods record; nothing validates until Builder.Compile (which still owns
// the kind ↔ setting matrix, the nil-closure checks and everything else the
// type system cannot see). Every method panics after Compile, like every other
// builder call. Calling a method twice is last-wins.
type EdgeBuilder[Row any] struct {
	b   *Builder
	set *edgeSettings
}

// ForeignKey reads the TARGET's id out of the parent Row — the foreign key the
// parent carries for a TO-ONE edge. Returning "" means the parent holds no
// reference at all, and the edge value is null (that is an absent reference,
// never a dangling one — see include.MissingForeignPolicy).
//
// A nil fn is recorded as "declared, but nil" so Compile reports
// "ForeignKey(nil)" instead of the edge panicking at hydrate time. Use
// ForeignKeys on the kinds that carry a LIST of foreign keys (forward-hasMany,
// in-array); Compile rejects either one on the wrong kind.
func (e *EdgeBuilder[Row]) ForeignKey(fn func(Row) string) *EdgeBuilder[Row] {
	e.b.mustLive()
	e.set.foreignKeySet = true
	e.set.foreignKey = nil
	if fn != nil {
		e.set.foreignKey = func(parent any) string { return fn(parent.(Row)) }
	}
	return e
}

// ForeignKeys reads the TARGETS' id list out of the parent Row — the foreign
// keys the parent carries for a FORWARD-hasMany or IN-ARRAY edge. An empty or
// nil slice is an empty collection.
//
// On an IN-ARRAY edge the list is POSITIONAL: it must line up 1:1 with the
// parent's array elements, in array order.
//
// A nil fn is recorded as "declared, but nil" so Compile reports
// "ForeignKeys(nil)".
func (e *EdgeBuilder[Row]) ForeignKeys(fn func(Row) []string) *EdgeBuilder[Row] {
	e.b.mustLive()
	e.set.foreignKeysSet = true
	e.set.foreignKeys = nil
	if fn != nil {
		e.set.foreignKeys = func(parent any) []string { return fn(parent.(Row)) }
	}
	return e
}

// Guard attaches a cheap, pure, no-DB visibility check over the PARENT row.
// Returning false makes the edge value null. A nil fn is recorded as
// "declared, but nil" so Compile reports "Guard(nil)".
func (e *EdgeBuilder[Row]) Guard(fn func(*include.Ctx, Row) bool) *EdgeBuilder[Row] {
	e.b.mustLive()
	e.set.guardSet = true
	e.set.guard = nil
	if fn != nil {
		e.set.guard = func(c *include.Ctx, parent any) bool { return fn(c, parent.(Row)) }
	}
	return e
}

// Inverse names the edge on the target node that mirrors this one.
func (e *EdgeBuilder[Row]) Inverse(key string) *EdgeBuilder[Row] {
	e.b.mustLive()
	e.set.inverse = key
	return e
}

// Required marks a to-one edge as never-null (nullability + doc fact).
func (e *EdgeBuilder[Row]) Required() *EdgeBuilder[Row] {
	e.b.mustLive()
	e.set.required = true
	return e
}

// Includable makes the edge reachable from ?include= (deny-by-default).
func (e *EdgeBuilder[Row]) Includable() *EdgeBuilder[Row] {
	e.b.mustLive()
	e.set.includable = true
	return e
}

// Filterable lets a filter condition traverse this edge — `author.name` on a
// Book root reaches Author's filterable columns through a filterable `author`
// edge (deny-by-default). It is INDEPENDENT of Includable: an edge a client
// may filter through need not be one it may include, and vice versa — a
// filter reveals facts about the target row without loading it, so it is its
// own permission. Compile admits it on to-one edges only for now (a to-many
// filter needs a quantifier — any/all/none — that the filter model does not
// carry yet), never next to Guard (a Go closure over the parent row that no
// SQL-side filter can honour), and only when the target has something to
// filter on (a filterable column or a filterable edge).
func (e *EdgeBuilder[Row]) Filterable() *EdgeBuilder[Row] {
	e.b.mustLive()
	e.set.filterable = true
	return e
}

// Limit sets the default per-edge top-N for an ENVELOPED to-many edge —
// Reverse or ForwardHasMany ONLY. Compile REJECTS it anywhere else rather than
// letting it read as an inert declaration: on a to-one or in-array edge (no
// EdgeQuery to carry it), on a Bare() edge (which fetches all rows by
// definition), and for any value below 1. Left unset, an enveloped non-bare
// to-many edge gets the default of 20.
func (e *EdgeBuilder[Row]) Limit(n int) *EdgeBuilder[Row] {
	e.b.mustLive()
	e.set.limit, e.set.limitSet = n, true
	return e
}

// Bare emits a flat array (no {items,hasMore} envelope) and fetches all rows.
func (e *EdgeBuilder[Row]) Bare() *EdgeBuilder[Row] {
	e.b.mustLive()
	e.set.bare = true
	return e
}

// EstimatedRows declares the per-parent row estimate the cost model
// (include.Limits.MaxCost) multiplies by for an UNBOUNDED edge — Bare() or
// in-array — which has no Limit. Compile REJECTS it on enveloped edges (Limit
// is the multiplier there) and for any value below 1. Left unset, an
// unbounded edge estimates 100 rows per parent, and Compile reports an
// unbounded edge without an estimate whose owner node is itself the target of
// a to-many edge — the position where the estimate actually multiplies.
func (e *EdgeBuilder[Row]) EstimatedRows(n int) *EdgeBuilder[Row] {
	e.b.mustLive()
	e.set.estimatedRows, e.set.estimatedRowsSet = n, true
	return e
}

// Envelope overrides the wrapper style for THIS edge alone (over the target
// node's and the graph's). Legal on every kind but Computed, which has no
// engine-produced value to wrap.
func (e *EdgeBuilder[Row]) Envelope(env include.Envelope) *EdgeBuilder[Row] {
	e.b.mustLive()
	e.set.envelope, e.set.envelopeSet = env, true
	return e
}

// Sort sets the edge's default sort key in WIRE form ('-' prefix = descending).
func (e *EdgeBuilder[Row]) Sort(key string) *EdgeBuilder[Row] {
	e.b.mustLive()
	e.set.sort = key
	return e
}

// Args declares the client-suppliable ":name(value)" arguments of this edge.
func (e *EdgeBuilder[Row]) Args(args ...EdgeArgOpt) *EdgeBuilder[Row] {
	e.b.mustLive()
	for _, a := range args {
		e.set.args = append(e.set.args, a.edgeArg())
	}
	return e
}

// Policies declares this edge's per-edge policy OVERRIDES — the one place an
// edge departs from the request-level defaults on include.Options. Pass the
// policy constants themselves; each one names the policy it belongs to, so
// there is nothing to key by:
//
//	.Policies(
//	    include.MissingRequiredError,   // fail instead of nulling a required target
//	    include.ExcludeRequiredStrict,  // refuse an ?exclude= of this key
//	    include.MissingForeignError,    // fail on a dangling foreign key
//	)
//
// A policy left unnamed inherits the request's Options; naming one twice is
// last-wins, and several Policies(...) calls on one edge MERGE (a later call
// overrides only the policies it names). Because include.EdgePolicy is a closed
// interface, only those constants type-check here.
//
// Scope, enforced by Compile: ExcludeRequired and MissingRequired are legal on
// Required() edges only; MissingForeign is legal on every edge that reads a
// parent-side FK (to-one, forward-hasMany, in-array) and rejected on reverse
// and computed ones.
func (e *EdgeBuilder[Row]) Policies(ps ...include.EdgePolicy) *EdgeBuilder[Row] {
	e.b.mustLive()
	set := include.EdgePoliciesOf(ps...)
	if set.ExcludeRequired != nil {
		e.set.policies.ExcludeRequired = set.ExcludeRequired
	}
	if set.MissingRequired != nil {
		e.set.policies.MissingRequired = set.MissingRequired
	}
	if set.MissingForeign != nil {
		e.set.policies.MissingForeign = set.MissingForeign
	}
	return e
}

// ------------------------------------------------------------------ edge args

// EdgeArgOpt is one declared edge argument (built by Arg).
type EdgeArgOpt interface{ edgeArg() include.EdgeArg }

type argOpt struct{ arg include.EdgeArg }

func (o argOpt) edgeArg() include.EdgeArg { return o.arg }

// Arg declares a client-suppliable edge argument. A nil validate accepts any
// value; a non-nil error at plan time fails the request with INVALID_INCLUDE.
//
// The value the fetcher receives in EdgeQuery.Args is RAW CLIENT INPUT: the
// parser rejects only grammar delimiters, control bytes and invalid UTF-8.
// Quotes, semicolons, comment markers and everything else pass through
// verbatim. Treat it like any other query parameter — bind it as a SQL
// parameter, never concatenate it into SQL, shell or log formats — and use
// validate to whitelist the shape you expect.
func Arg(name string, validate func(raw any) error) EdgeArgOpt {
	return argOpt{arg: include.EdgeArg{Name: name, Validate: validate}}
}
