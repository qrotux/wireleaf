// Package include implements a graph-driven ?include= engine: it parses the
// client's include string, resolves it into a plan against a declared resource
// graph, and hydrates the plan into wire JSON bytes.
package include

// ------------------------------------------------------------------ Resource

// Resource is the interface every node in the include graph must implement.
type Resource interface {
	// Name returns the globally-unique OpenAPI component name for this node.
	Name() string
	// Slug returns the short lowercase key naming this node in the DOC layer
	// (routes, operation ids, tags). Include RESOLUTION does not use it — an
	// ?include= path is matched against Edges() keys, and the root resource is
	// picked by the caller — so a Slug collision is a documentation problem,
	// not a routing one.
	Slug() string
	// Fields returns the scalar field whitelist (what Serialize reads).
	Fields() []string
	// Defaults returns the default-included edge keys.
	Defaults() []string
	// Edges returns all declared edges keyed by their include key.
	Edges() map[string]Edge
	// IDOf extracts the string primary-key from a doc (used by the BFS engine).
	IDOf(doc any) string
	// Serialize serialises this node's level only (no child edges).
	Serialize(doc any, ctx *Ctx) any
	// Enrich is an optional batch hook called on a whole level's docs
	// immediately before Serialize. Implementations may issue DB calls and
	// mutate docs in place. A nil/no-op implementation is fine for nodes that
	// don't need it. It may be invoked with an EMPTY docs slice (e.g. a fully
	// guarded-out level); implementations must tolerate it.
	Enrich(docs []any, ctx *Ctx) error
}

// ------------------------------------------------------------------ Edge

// EdgeKindType classifies how an edge is fetched and represented in the response.
type EdgeKindType string

const (
	// KindInArray: edge lives inside an array field on the parent doc (ArrayPath set).
	KindInArray EdgeKindType = "in-array"
	// KindReverse: FK lives on the child pointing back to the parent (Backref set).
	KindReverse EdgeKindType = "reverse"
	// KindForwardHasMany: parent holds an FK array, Many=true (no Backref/ArrayPath).
	KindForwardHasMany EdgeKindType = "forward-hasMany"
	// KindToOne: parent holds a single scalar FK, Many=false.
	KindToOne EdgeKindType = "to-one"
	// KindComputed: the edge has no target Resource; its value is produced by
	// application code and described by ComputedSchema.
	KindComputed EdgeKindType = "computed"
)

// Edge describes a graph edge from one Resource to another.
// Target is a thunk so cyclic graphs can be declared without import cycles.
type Edge struct {
	// Target returns the destination Resource. Passed as a thunk so cyclic
	// graphs compile cleanly (resolved on demand, not at declaration time).
	Target func() Resource

	// Many indicates whether the edge produces a collection.
	Many bool

	// Required marks a TO-ONE edge as non-null: the target is always present.
	// It is a DOC-layer claim — the OpenAPI emitter renders such an edge as a
	// bare $ref and lists it in the schema's `required` set, under EVERY
	// RequiredMissing policy. Whether the engine HONOURS the claim when the
	// target turns out to be absent is RequiredMissing's business (null, or a
	// failed request); the published shape does not soften to match.
	Required bool

	// ExcludeRequired OVERRIDES Options.ExcludeRequiredPolicy for this edge; nil inherits
	// the request's. It decides only how an ?exclude= naming a REQUIRED edge is
	// answered — silently kept (ExcludeRequiredTolerant) or refused (ExcludeRequiredStrict);
	// the edge is never actually removed either way. Meaningless (and rejected
	// by graph.Compile) on a non-required edge.
	ExcludeRequired *ExcludeRequiredPolicy

	// MissingRequired OVERRIDES Ctx.Policies.MissingRequired for this edge;
	// nil inherits the engine-wide fallback. It decides what the engine does when a
	// REQUIRED to-one edge resolves to no target — an empty FK, a row the
	// fetcher did not return, or a guard-false parent: emit null
	// (MissingRequiredNull) or fail the request (MissingRequiredError).
	// Meaningless (and rejected by graph.Compile) on a non-required edge.
	MissingRequired *MissingRequiredPolicy

	// MissingForeign OVERRIDES Ctx.Policies.MissingForeign for this edge; nil
	// inherits the engine-wide fallback. It decides what happens when a NON-EMPTY FK read
	// off the parent resolves to no row: keep the v0 shape
	// (MissingForeignNull — null / a silently dropped list item) or fail the
	// request (MissingForeignError). Meaningless (and rejected by
	// graph.Compile) on reverse and computed edges, which read no parent FK.
	MissingForeign *MissingForeignPolicy

	// Includable: deny-by-default; an edge is invisible to clients until true.
	Includable bool

	// Backref is the FK field on the child pointing back to the parent.
	// Classification/OpenAPI metadata; NOT passed to the fetcher (the fetcher
	// encapsulates its own join shape via its closure).
	Backref string

	// ArrayPath is the array field on the parent that holds the FK elements
	// (in-array edges). Classification/OpenAPI metadata only.
	ArrayPath string

	// SubField is the sub-field inside each ArrayPath element.
	SubField string

	// Limit is the default per-edge top-N (to-many edges).
	Limit int

	// Bare: emit a flat array (no {items,hasMore} envelope) and fetch all
	// records (limit ≤ 0 → fetch-all). Safe only for bounded relations.
	Bare bool

	// EstimatedRows is the per-parent row estimate the cost model uses for an
	// UNBOUNDED edge (Bare, or in-array) that has no Limit to multiply by.
	// 0 means the engine default of 100. Ignored on enveloped edges, where
	// Limit is the multiplier.
	EstimatedRows int

	// Envelope is the RESOLVED wrapper style of this edge's value (see
	// Envelope). Zero = plain. graph.Compile fills it from the graph / target
	// node / edge declarations; ignored on computed edges.
	Envelope Envelope

	// Sort is the reverse edge's DEFAULT sort key in WIRE form (json key with
	// an optional '-' desc prefix, e.g. "startDate" / "-createdAt"). A client
	// `:sort(...)` arg overrides it. Resolved through SortCols before reaching
	// EdgeQuery.Sort; empty (or not whitelisted) → the fetcher's own default order.
	Sort string

	// SortCols whitelists the sortable wire keys of a reverse edge:
	// wire json key → SQL-side sort key (what EdgeQuery.Sort carries, with the
	// '-' prefix re-applied). graph.Compile builds it from `sortCol` wire-struct
	// tags. A client :sort() key absent from the map fails the plan under
	// SortStrict and falls back to Sort under SortFallback. nil → a client
	// `:sort()` is an UNDECLARED argument decided by ArgPolicy (ArgsStrict
	// fails the plan) and never reaches key lookup.
	SortCols map[string]string

	// Args declares the client-suppliable ":name(value)" arguments this edge
	// accepts beyond the built-ins ("limit" always; "sort" when SortCols is
	// set). Under ArgsStrict an undeclared argument fails the plan.
	//
	// DECLARING "sort" HERE is the documented escape hatch: the declared arg's
	// own Validate replaces the SortCols/SortStrict acceptance check entirely
	// (see sort.go's header). Declaring "limit" does not shadow the built-in —
	// graph.Compile rejects it, and the engine coerces it anyway.
	Args []EdgeArg

	// Guard is a cheap, pure, no-DB visibility check. Returning false hides the
	// edge's value, in the shape the edge's KIND already promises: a TO-ONE
	// edge's value becomes null; a TO-MANY edge's becomes an EMPTY collection
	// (an empty array when bare, {items:[],hasMore:false} when enveloped). Under
	// a non-plain Envelope the same shapes are wrapped: `{"<Key>":null}` /
	// `{"<Key>":[]}`. The key itself is still present — a guard must not change
	// the document's shape, only its content.
	Guard func(ctx *Ctx, parent any) bool

	// ForeignKey reads the TARGET's id out of a parent row (held as any) for a
	// TO-ONE edge. Returning "" means the parent holds no reference and the
	// edge value is null. This is how the engine reads a parent's forward
	// foreign-key VALUE without reflecting on the row struct (reflection is
	// fragile; a typed closure is exact). Set via graph.ForeignKey.
	//
	// A REVERSE edge has none — it fetches children by the PARENT's own id
	// (plan.Resource.IDOf(parentDoc)) — which is also why no absence there is
	// a dangling reference (see MissingForeignPolicy).
	ForeignKey func(parent any) string

	// ForeignKeys reads the TARGETS' id LIST out of a parent row (held as any)
	// for a FORWARD-hasMany or IN-ARRAY edge. An empty/nil slice → an empty
	// collection. On an in-array edge the list is POSITIONAL: it must line up
	// 1:1 with the parent's ArrayPath elements, in ARRAY ORDER. Set via
	// graph.ForeignKeys. (Reverse edges do not use it — they fetch by the
	// parent id.)
	ForeignKeys func(parent any) []string

	// Computed marks an edge with no target Resource: the value is produced by
	// application code and documented via ComputedSchema.
	Computed bool

	// ComputedSchema holds an apidoc.Schema describing a computed edge's value.
	// Typed as `any` to keep include free of an apidoc import (which would be
	// an import cycle); the OpenAPI emitter asserts it back.
	ComputedSchema any
}

// ------------------------------------------------------------------ Envelope

// Envelope is the wire-shape STYLE of one edge's value: how the target node
// (or the array of targets) is wrapped under the edge key. The zero value is
// the plain shape — the target object itself, `{items,hasMore}` for lists.
//
// graph.Compile resolves it from three layers (edge > target node > graph)
// into Edge.Envelope; the engine and apidoc read only that resolved field and
// never know where it came from. Both members are inserted into the bytes
// verbatim, so Compile rejects names that would need JSON escaping.
type Envelope struct {
	// Key is the member the node (or the array of nodes) lives under:
	// `{"<Key>": obj}`, `{"<Key>": [ {"<Key>": obj}, … ]}`. "" = plain shape.
	Key string
	// Pagination is the member carrying `{hasNextPage, nextCursor?}` on an
	// enveloped to-many value. "" = the block is not emitted (HasMore and
	// NextCursor are then dropped). Meaningful only with a non-empty Key.
	Pagination string
}

// Plain reports whether e is the unwrapped shape (no Key).
func (e Envelope) Plain() bool { return e.Key == "" }

// EdgeArg declares one client-suppliable edge argument.
type EdgeArg struct {
	// Name is the argument name as written by the client (":name(...)").
	Name string
	// Validate rejects a raw value ("" for a bare arg, string, or []string)
	// at plan time; a non-nil error fails the plan with INVALID_INCLUDE.
	// nil → any value is accepted.
	Validate func(raw any) error
}

// EdgeKind classifies e using the canonical discriminant order:
//
//	computed        → KindComputed
//	arrayPath set   → KindInArray
//	backref set     → KindReverse
//	many            → KindForwardHasMany
//	else            → KindToOne
func EdgeKind(e Edge) EdgeKindType {
	if e.Computed {
		return KindComputed
	}
	if e.ArrayPath != "" {
		return KindInArray
	}
	if e.Backref != "" {
		return KindReverse
	}
	if e.Many {
		return KindForwardHasMany
	}
	return KindToOne
}

// ------------------------------------------------------------------ Fetcher func types

// FetchByIDs is the forward-batch fetcher. The closure encapsulates the full
// join shape; Edge.Backref / Edge.ArrayPath are classification and
// OpenAPI metadata only and are not passed here. Per-request state (viewer,
// locale, …) is read from c.Env.
//
// Implementations MUST be safe for concurrent use: the engine may call the
// same fetcher from several goroutines for sibling edges (the v1 engine still
// loads sibling edges sequentially, but the contract is concurrency-safe).
type FetchByIDs func(c *Ctx, ids []string) ([]any, error)
