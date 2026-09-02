# graph — the typed builder that compiles a resource graph into the engine's registry

## Overview

`graph` is the declaration layer of wireleaf: an application describes its
nodes (Row → Wire mapping, primary key, edges) on a `Builder`, and `Compile`
validates the whole declaration at once and freezes it into an immutable
`*Graph`. The compiled graph is both what the `include` engine hydrates
through — every compiled node is an `include.Resource`, and `*Graph` is the
`include.Registry` that routes fetcher lookups — and what the `apidoc` layer
reads to emit OpenAPI components (`WireSample`, `FieldVerdicts`,
`IsDocExternal` are its duck-typed seams). The package also ships
`Loader[K, V]`, a request-scoped batch cache for side data, and the
`graph/loadertest` subpackage, a contract harness for fetchers.

## Core types

| Type | Role |
| --- | --- |
| `Builder` | Accumulates declarations. Single-goroutine, write-only; dead after `Compile`. |
| `NodeHandle[Row, Wire]` | Typed façade over one declared node: chained configurator and the value you keep for edges, roots and fetcher binds. |
| `EdgeKind` | Opaque discriminant of the five edge kinds; built only through `ToOne` / `ToMany` / `Reverse` / `InArray` / `Computed`. |
| `EdgeBuilder[Row]` | Chained configurator returned by `NodeHandle.Edge`, typed by the parent's Row. |
| `Graph` | Immutable result of `Compile`; implements `include.Registry`. |
| `Finding`, `CompileError` | The compile-time error taxonomy. |
| `Loader[K, V]` | Request-scoped batch loader for out-of-graph side data. |

Nothing validates at declaration time: every builder method only records, and
every check runs in `Compile`. After `Compile` the builder is dead — **any**
further builder, handle, or edge-builder call panics with
`"graph: builder is dead after Compile"`, including a second `Compile`.
Calling a chained method twice is last-wins (except `Defaults` and `Policies`,
which append/merge — see below).

## Builder lifecycle

```go
func NewBuilder() *Builder
func Node[Row any, Wire any](b *Builder, name string) *NodeHandle[Row, Wire]
func (b *Builder) Root(h handle) // handle: any NodeHandle instantiation
func (b *Builder) Envelope(e include.Envelope) *Builder
func (b *Builder) Compile() (*Graph, error)
```

`Node` declares a node named `name`; `Row` is the DB/query row type, `Wire`
the serialized wire struct. The wire type is also the node's *address*: one
wire type may belong to exactly one node per builder (a duplicate is a
finding), because edges target nodes by wire type. `Root` marks entry points;
roots are deduped and kept in declaration order.

### Node configuration (`NodeHandle[Row, Wire]` methods)

```go
func (h *NodeHandle[Row, W]) Slug(s string) *NodeHandle[Row, W]
func (h *NodeHandle[Row, W]) Wire(fn func(Row, *include.Ctx) W) *NodeHandle[Row, W]
func (h *NodeHandle[Row, W]) PrimaryKey(fn func(Row) string) *NodeHandle[Row, W]
func (h *NodeHandle[Row, W]) Enrich(fn func([]Row, *include.Ctx) error) *NodeHandle[Row, W]
func (h *NodeHandle[Row, W]) Defaults(keys ...string) *NodeHandle[Row, W]
func (h *NodeHandle[Row, W]) DocExternal() *NodeHandle[Row, W]
func (h *NodeHandle[Row, W]) Envelope(e include.Envelope) *NodeHandle[Row, W]
func (h *NodeHandle[Row, Wire]) Edge(key string, kind EdgeKind) *EdgeBuilder[Row]
```

- **`Slug`** — the node's short lowercase DOC-layer key (routes, operation
  ids, tags); include resolution never reads it (see `include.Resource`).
  Absent (or set to `""`), `Compile` derives `lowercase(name)`.
- **`Wire`** — the one Row → Wire mapper, run per document *after* `Enrich`;
  its result is a value (never JSON) that `Ctx.Marshal` serializes. Mandatory.
- **`PrimaryKey`** — extracts a row's own id: the identity the engine batches
  and indexes by, and what a `ForeignKey` elsewhere points at. Mandatory.
- **`Enrich`** — optional batch hook over a whole level's rows before `Wire`;
  the sanctioned place to side-load data (typically `Loader.Warm`), once per
  level rather than per row.
- **`Defaults`** — edge keys included when the client asks for none. Repeated
  calls **append**. Every `Required()` edge key is auto-appended by `Compile`
  (after the explicit keys, in edge-declaration order) if not already listed.
- **`DocExternal`** — the node's OpenAPI component is owned externally:
  emission stitches a `$ref` but never emits the fragment.

All closures are typed by the handle's own `Row`/`Wire` parameters, so a
closure written against another node's types is a **Go compile error**, not a
runtime finding. Declaring a closure with a literal `nil` is recorded as
"declared, but nil" and reported by `Compile` (e.g. `Wire(nil) on node Book`)
rather than silently ignored.

### Wire shape derivation

`Compile` reflects each Wire struct once, following `encoding/json`'s field
rules (tag name wins, `json:"-"` skipped, anonymous untagged structs
flattened, depth-dominance for shadowed names). From that walk it derives:

- `Fields()` — the serialized json names in output order;
- per-field nullability verdicts via the one policy `apidoc.DefaultNullability`;
- the **column bindings** (`include.Column`, exposed through the
  `include.ColumnSource` seam as `Columns()` on every compiled node): a
  `col:"sql_name[,sort][,filter]"` struct tag on a serialized, json-tagged
  field binds that json name to a SQL-side column; the `sort` option makes it
  a legal sort key (the `sortCol` whitelist below), the `filter` option a
  legal filter column. The legacy `sortCol:"sql_name"` tag is exactly
  `col:"sql_name,sort"`. `Type` is the field's Go type, pointers dereferenced.
  Findings: both tags on one field; an empty name; an unknown or repeated
  option; `filter` on a field that is not bool / number / string /
  `time.Time`; a filterable column whose json key is `and` or `or` (reserved
  for filter groups); a tag on an unexported or embedded field. On a
  `json:"-"` or untagged field the tag is silently dropped. Two legacy edge
  cases changed with the `col` tag: an empty `sortCol:""` is now a finding (it
  used to read as no tag), and a `sortCol` value is parsed with the same
  `name[,option…]` grammar, so a comma inside it is an option, not part of the
  SQL name.
- the **sortCol whitelist** (`Edge.SortCols` on reverse edges): the
  `Sortable` columns, json name → `Col`.

A json-name tie that `encoding/json` would drop entirely (several candidates
at the same depth, all tagged or all untagged) is a `duplicate json name`
finding, because the wire would silently lose the field.

## Edge declaration

`NodeHandle.Edge(key, kind)` declares one edge and returns its
`EdgeBuilder[Row]`. The target inside `kind` is addressed by the target's
**wire type**; `Compile` resolves it against the builder's wire-type → node
map. Declaration order and even declaration file therefore do not matter, and
cyclic graphs need no forward references — a typo in the type is a Go compile
error, an unknown wire type is a finding
(`edge target: no node of this builder declares wire type T`).

### Kinds

```go
func ToOne[W any]() EdgeKind                          // parent holds one scalar FK (.ForeignKey)
func ToMany[W any]() EdgeKind                         // parent holds an FK array (.ForeignKeys)
func Reverse[W any](backref string) EdgeKind          // FK lives on the child, in backref
func InArray[W any](arrayPath, subField string) EdgeKind // FKs inside parent's array, positional
func Computed(schema apidoc.Schema) EdgeKind          // no target; app produces the value
```

`W` is always the **target** node's wire type; `EdgeKind`'s fields are
unexported, so only these constructors build one. `Reverse` requires a
non-empty `backref`, `InArray` a non-empty `arrayPath` and `subField`
(findings otherwise); an `InArray` `ForeignKeys` result is positional — 1:1
with the parent's array elements, in array order.

### Edge options (`EdgeBuilder[Row]` methods)

```go
func (e *EdgeBuilder[Row]) ForeignKey(fn func(Row) string) *EdgeBuilder[Row]
func (e *EdgeBuilder[Row]) ForeignKeys(fn func(Row) []string) *EdgeBuilder[Row]
func (e *EdgeBuilder[Row]) Guard(fn func(*include.Ctx, Row) bool) *EdgeBuilder[Row]
func (e *EdgeBuilder[Row]) Inverse(key string) *EdgeBuilder[Row]
func (e *EdgeBuilder[Row]) Required() *EdgeBuilder[Row]
func (e *EdgeBuilder[Row]) Includable() *EdgeBuilder[Row]
func (e *EdgeBuilder[Row]) Filterable() *EdgeBuilder[Row]
func (e *EdgeBuilder[Row]) Limit(n int) *EdgeBuilder[Row]
func (e *EdgeBuilder[Row]) Bare() *EdgeBuilder[Row]
func (e *EdgeBuilder[Row]) Sort(key string) *EdgeBuilder[Row]
func (e *EdgeBuilder[Row]) Args(args ...EdgeArgOpt) *EdgeBuilder[Row]
func (e *EdgeBuilder[Row]) Policies(ps ...include.EdgePolicy) *EdgeBuilder[Row]
func (e *EdgeBuilder[Row]) Envelope(env include.Envelope) *EdgeBuilder[Row]
```

The kind ↔ option matrix, enforced by `Compile` (a declaration outside it is a
finding, never a silent no-op):

| Option | Legal on | Notes |
| --- | --- | --- |
| `ForeignKey` | ToOne (required there) | `""` → the edge value is null (an *absent* reference, judged by nobody; a non-empty FK that resolves to nothing answers to `MissingForeignPolicy`). |
| `ForeignKeys` | ToMany, InArray (required there) | Empty/nil slice = empty collection. |
| `Limit` | Reverse, ToMany | Must be ≥ 1; illegal with `Bare`. Unset on a non-bare (`{items, hasMore}`) edge → default 20. |
| `Bare` | Reverse, ToMany | Flat array, no envelope; compiles to `Limit 0` = fetch-all. |
| `Sort` | Reverse only | Wire-form key, `-` prefix = descending; must be in the target's sortCol whitelist. |
| `Args` | Reverse, Computed | `Arg("limit", …)` is rejected — `:limit` is engine-owned. |
| `Guard` | everything but Computed | Cheap, pure, no-DB check over the parent row; `false` hides the value in the kind's own shape (`null` for to-one, an empty collection for to-many), no fetch. Illegal with `Required()`. |
| `Filterable` | ToOne only | Lets a filter condition traverse the edge (`include.ResolveFilter`). Deny-by-default and **independent of `Includable`**. Illegal with `Guard()`; illegal when no filterable column is reachable from the target through filterable edges (nothing to filter on); an edge keyed `and` / `or` cannot be filterable (reserved for filter groups). To-many edges are rejected until the filter model carries quantifiers. |
| `Required` | ToOne only | Never-null doc fact; implies membership in `Defaults()` (constructed by `Compile`). Its two policies are only legal here. |
| `Inverse` | everything but Computed | Cross-checked: the named edge must exist on the target, point back, run the opposite direction, and (if it declares an Inverse itself) name this edge. |
| `Envelope` | Builder, Node, everything but Computed | Wrapper style of the edge value (`include.Envelope{Key, Pagination}`); resolved edge > target node > graph into `include.Edge.Envelope`. Findings: `Envelope` on a computed edge; `Pagination` without `Key`; `Key == Pagination`; a name needing JSON escaping (`"`, `\`, control chars). `Inverse` does not sync it. |

```go
func Arg(name string, validate func(raw any) error) EdgeArgOpt
```

declares a client-suppliable `:name(value)` argument; a nil `validate`
accepts anything, a validation error fails the plan with `INVALID_INCLUDE`.

**`Policies`** takes the closed-interface policy constants directly
(`include.MissingRequiredError`, `include.ExcludeRequiredStrict`,
`include.MissingForeignError`, …); each names the policy it overrides, an
unnamed policy inherits the request-level fallback
(`include.Options.ExcludeRequiredPolicy` at plan time, `include.Ctx.Policies`
at materialize time), and several `Policies` calls on one edge **merge**
(later calls override only what they name). Scope, enforced by `Compile`: `ExcludeRequired` / `MissingRequired`
only on `Required()` edges; `MissingForeign` only on kinds that read a
parent-side FK (ToOne, ToMany, InArray).

`Includable` is deny-by-default: an edge is invisible to `?include=` until
declared includable. A non-includable edge listed in `Defaults()` still
fetches at runtime, so its target's fetcher bind is still mandatory. The same
holds for `Filterable`, which is a separate permission: a filter through an
edge reveals facts about the target row without loading it.

## Fetcher binds

```go
func FetchIDs[Row, Wire any](b *Builder, h *NodeHandle[Row, Wire],
    fn func(c *include.Ctx, ids []string) ([]Row, error))
func FetchParents[Row, Wire any](b *Builder, h *NodeHandle[Row, Wire], fn FetchByParents[Row])

type FetchByParents[Row any] func(c *include.Ctx, parentIDs []string,
    q include.EdgeQuery) (map[string]ParentRows[Row], error)
type ParentRows[Row any] struct {
    Rows       []Row
    HasMore    bool
    NextCursor string
}

func PerParent[Row any](fn func(c *include.Ctx, parentID string,
    q include.EdgeQuery) ([]Row, bool, error)) FetchByParents[Row]
```

`FetchIDs` is the forward batch (ids → rows) used by ToOne/ToMany/InArray
targets; `FetchParents` the reverse batch (one call per level, rows keyed by
parent id) used by Reverse targets. `Row` is inferred from the handle, so a
fetcher of the wrong row type does not build. Re-binding the same node is
last-wins; binding `nil` clears the bind. `PerParent` adapts a per-parent
closure by looping the parent ids in the given order, short-circuiting on the
first error; it cannot emit `NextCursor`. The normative closure contract
(probe semantics, only-requested keys, absent-parent-is-not-an-error,
concurrency safety) lives with the fetcher types in
[include.md](include.md) (and, in short form, the README's "Key contracts")
and is machine-checked by `loadertest` below.

## Compile

```go
func (b *Builder) Compile() (*Graph, error)

type Finding struct{ Node, Edge, Msg string } // String(): "Node.edge: msg"
type CompileError struct{ Findings []Finding }
```

`Compile` is the **only** validation point. It kills the builder first, then
runs its passes over every declaration: node identity (empty/duplicate names,
duplicate wire types), per-node shape and closures, per-edge target
resolution and kind ↔ option coherence, inverse pairing, default-edge cycle
detection, and reachability-driven fetcher completeness (walking includable ∪
default edges from the roots: a node reached by a forward edge needs
`FetchIDs`, by a reverse edge `FetchParents`; roots themselves need neither —
the handler seeds them).

Error taxonomy:

- **Findings** (returned as one `*CompileError`) are declaration problems.
  There is no fail-fast: every finding of one run is collected and reported
  together, each with a stable, greppable message.
- **Panics** are usage-protocol violations: any builder call after `Compile`
  (including a second `Compile`), and the post-compile wiring bugs below
  (`Resource` on an unknown name, `Reachable` on a foreign resource).

On a clean build the returned `*Graph` is immutable and every compiled node
is a valid `include.Resource` with all mandatory closures non-nil — hydration
never hits a nil `Wire` or `PrimaryKey`.

## Graph

```go
func (g *Graph) Resource(name string) include.Resource   // PANICS on unknown name
func (g *Graph) Roots() []include.Resource               // declaration order, deduped
func (g *Graph) Reachable(res include.Resource) []include.Resource
```

`Resource` treats an unknown name as a wiring bug and panics. `Reachable`
returns `res` plus every node reachable from it over **includable** edges
only — a BFS, edges in declaration order, deduped by `Name()` so cycles
terminate; deterministic across runs. Its contract is narrow: `res` must be a
node of **this** graph (from `Resource` or `Roots`); handing it any other
`include.Resource` implementation panics
(`"graph: Reachable walked a resource not compiled by this graph"`). A nil
`res` returns nil. This is the walk that feeds `apidoc.EmitComponents`.

### include.Resource and the doc seams

Each compiled node implements `include.Resource` (`Name`, `Slug`, `Fields`,
`Defaults`, `Edges`, `IDOf`, `Serialize`, `Enrich`) plus three duck-typed
methods the doc layer reads:

```go
WireSample() any                            // zero value of the Wire type, for reflection
IsDocExternal() bool                        // component owned externally
FieldVerdicts() map[string]apidoc.Verdict   // per-field nullability, a COPY
```

**Accessor contract:** `Fields`, `Defaults` and `Edges` return the *live*
backing slice/map of the frozen graph, not copies — they are on the
per-request hot path. Callers must not mutate them; a write reaches every
request in the process. `FieldVerdicts` does copy (doc-time metadata) and is
exposed metadata only: emission derives nullability independently through the
canonical reflector, applying the same policy.

### include.Registry

```go
func (g *Graph) FetchByIDs(res include.Resource) (include.FetchByIDs, bool)
func (g *Graph) FetchByParents(res include.Resource) (include.FetchByParents, bool)
```

`*Graph` satisfies `include.Registry`: the engine looks fetchers up by
`res.Name()` at hydrate time. Set it as `include.Ctx{Registry: g}` — the
compiled graph *is* the registry; there is nothing else to register.

## Loader

```go
func NewLoader[K comparable, V any](
    fetch func(c *include.Ctx, keys []K) (map[K]V, error)) *Loader[K, V]
func (l *Loader[K, V]) Warm(c *include.Ctx, keys ...K) error
func (l *Loader[K, V]) Get(c *include.Ctx, key K) (V, bool)
```

`Loader` batches side data that is not part of the graph (permissions,
counters, flags) for one request. The loader itself is a package-level
application value holding no data; per-request state lives on the
`*include.Ctx` via `Ctx.LoaderState`, keyed by the loader pointer, and dies
with the request. Two phases:

- **`Warm`** fetches: it batches every still-unknown key into one fetch call,
  awaits keys another goroutine is already fetching (single-flight), and
  skips keys already settled — found and not-found alike are cached, so a key
  that settles is fetched at most once per request. A fetch **error** caches
  nothing: the keys revert to unknown, every waiter on that in-flight fetch
  receives the error, and a later `Warm` retries. Zero keys → nil
  immediately (before even the nil-fetch check). A fetch **panic** is
  detected by a deferred settler: the claimed entries are failed and removed
  so waiters unblock, then the panic continues to unwind.
- **`Get`** never fetches. An unwarmed key yields `(zero, false)`; a key
  whose `Warm` is in flight blocks until it settles (a failed fetch also
  yields `(zero, false)`).

Fetch contract (checked by `loadertest.RunLoaderFetch`): safe for concurrent
use; return only requested keys; never call this loader back for keys it was
handed (self-deadlock); honor `c.StdContext()` cancellation — that is the only
way waiters unblock early; omit an absent key (negative-cached, never
refetched) rather than erroring. A hand-assembled `Loader{}` has a nil fetch:
every non-empty `Warm` returns `"graph: Loader has nil fetch (use NewLoader)"`
and every `Get` misses — loudly, instead of negative-caching everything.

## loadertest

`graph/loadertest` turns the fetcher contracts into subtests you add to your
own suite. Run with `-race` — the concurrency subtests are only meaningful
there. Each Run\* takes a fixture describing real test data and runs a
subtest table under `name`:

```go
func RunFetchByParents(t *testing.T, name string, fn include.FetchByParents, fx ParentsFixture)
func RunFetchIDs(t *testing.T, name string, fn include.FetchByIDs, fx IDsFixture)
func RunLoaderFetch[K comparable, V any](t *testing.T, name string,
    fetch func(*include.Ctx, []K) (map[K]V, error), fx LoaderFixture[K])
```

- **`ParentsFixture`** `{Ctx, ParentIDs, EmptyParentID, ProbeLimit, Query}` —
  checks fetch-all (`Limit 0`: every row, never `HasMore`), the limit+1 probe
  (`min(n, T)` rows, `HasMore == (T > n)`, corroborated differentially at
  `n` and `n+1`), only-requested keys (via a strict-subset call), the
  absent-parent rule, and concurrency. The bare call runs first and its row
  counts are the oracle for every other check. At least one parent must have
  **more** than `ProbeLimit` children, or the harness reports a fixture
  violation; `EmptyParentID` empty skips the absent-parent subtest. `Query`
  is the `EdgeQuery` template (only `Limit` is overridden) for edges with
  required `Args` or `Sort`.
- **`IDsFixture`** `{Ctx, KnownIDs, UnknownID, IDOf}` — only-requested ids,
  no duplicates, non-empty result for known ids, unknown ids dropped
  silently (alone and mixed), concurrency. `IDOf` is required to match
  documents to ids; a panic inside it is caught and reported.
- **`LoaderFixture[K]`** `{Ctx, KnownKeys, UnknownKey}` — only-requested
  keys, values for known keys, the unknown key omitted (not erroring, not
  invented), concurrency.

A nil fixture `Ctx` is replaced by one usable zero `&include.Ctx{}`, and the
one `Ctx` is deliberately shared by every call of a check — concurrent ones
included — so state hung off it is actually contended. Fetcher errors and
panics are converted into violations, reported sorted, one `t.Errorf` each;
an invalid fixture fails the whole table before blaming the fetcher.
