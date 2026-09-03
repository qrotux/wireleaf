# include — the graph-driven ?include= engine

## Overview

`include` turns a client's `?include=` / `?exclude=` strings into wire JSON
bytes: parse, resolve into a validated plan against a declared resource graph,
materialize with level-batched child loading. The engine is transport-free
and DB-free; `Ctx.Request` carries an HTTP-shaped snapshot (stdlib types
only) that an adapter fills and application hooks read — every round-trip
still goes through fetcher closures the application supplies. `graph` is the
typed builder producing the `Resource`/`Edge`/`Registry` values this package
consumes (`graph.Compile`'s `*graph.Graph` implements `include.Registry`);
`apidoc` reads the same declarations to emit OpenAPI components.

The pipeline is `ParseInclude`/`ParseExclude` (strings) → `ResolvePlan`
(`*PlanNode`) → `Materialize` (`[]json.RawMessage`); handlers normally call
only the `Hydrate*` facades wrapping it. The invariant threaded through
everything is **400-before-fetch**: `ResolvePlan` always runs before any fetch
closure, so a malformed or illegal include string costs no database round-trip.

## Core types

### IncludeTree

```go
type IncludeTree = map[string]any
```

An **alias**, not a defined type — any `map[string]any` of the right shape is
an `IncludeTree` and vice versa. Keys starting with `":"` are edge args
(`string` or `[]string`); every other key is a child edge name holding a
nested `IncludeTree`.

### Resource

```go
type Resource interface {
    Name() string                        // globally-unique component name
    Slug() string                        // DOC-layer key; NOT used by include resolution
    Fields() []string                    // scalar field whitelist
    Defaults() []string                  // default-included edge keys
    Edges() map[string]Edge              // all declared edges by include key
    IDOf(doc any) string                 // string primary key of a doc
    Serialize(doc any, ctx *Ctx) any     // this level only, no child edges
    Enrich(docs []any, ctx *Ctx) error   // batch pre-serialize hook
}
```

Normally implemented by `graph.Compile`'s output, not by hand. `Enrich` runs
once per level, before `Serialize`, and may be invoked with an **empty** docs
slice (a fully guarded-out level) — implementations must tolerate that.

`ColumnSource` is the optional seam next to it:

```go
type Column struct {
    Col        string       // SQL-side name, from the col tag; never client input
    Type       reflect.Type // wire field type, pointers dereferenced
    Sortable   bool         // `sort` tag option
    Filterable bool         // `filter` tag option
}
type ColumnSource interface{ Columns() map[string]Column } // wire json key → Column
func ColumnsOf(res Resource) map[string]Column           // nil when res is no ColumnSource
```

`graph.Compile`'s nodes implement it (live map — do not mutate); a hand-built
`Resource` that does not simply has no columns. The `Col` guarantee — a
compile-time constant from a struct tag, never client input, which is why an
adapter may treat it as SQL identifier text — is `graph.Compile`'s; a
hand-built `ColumnSource` owns it itself. And presence in the map is a
projection binding, not a permission: check `Sortable` / `Filterable` before
sorting or filtering on an entry.

### Edge

`Edge` is a plain struct: `Target func() Resource` (a thunk, so cyclic graphs
declare cleanly), `Many`, `Required`, `Includable` and `Filterable` (both
deny-by-default and independent of each other — an edge is invisible to
`?include=` until `Includable`, and to a filter condition until `Filterable`;
`graph.Compile` admits `Filterable` on to-one, reverse and to-many edges —
never on in-array or computed ones — and never with
`Guard`), the kind discriminants (`Backref`,
`ArrayPath`/`SubField`, `Computed`/`ComputedSchema`), loading knobs (`Limit`,
`Bare`, `EstimatedRows`, `Sort`, `SortCols`, `Args []EdgeArg`), closures (`Guard`,
`ForeignKey`, `ForeignKeys`) and three policy override pointers
(`ExcludeRequired`, `MissingRequired`, `MissingForeign` — `nil` inherits the
engine-wide fallback). `EdgeKind(e Edge) EdgeKindType` classifies in canonical
discriminant order: `Computed` → `KindComputed`, else `ArrayPath != ""` →
`KindInArray`, else `Backref != ""` → `KindReverse`, else `Many` →
`KindForwardHasMany`, else `KindToOne`.

| Kind | Fetch | Value shape |
| --- | --- | --- |
| `KindToOne` | `ForeignKey(parent)` → one batched `FetchByIDs` per level; `""` → `null` | object or `null` |
| `KindForwardHasMany` | `ForeignKeys(parent)`, trimmed to the limit, one `FetchByIDs` | `{items,hasMore}`, or flat array when `Bare` |
| `KindReverse` | parent ids (`IDOf`) → **one** `FetchByParents` per level with an `EdgeQuery` | `{items,hasMore(,nextCursor)}`, or flat array when `Bare` |
| `KindInArray` | `ForeignKeys(parent)` positional with `ArrayPath` elements, one `FetchByIDs` | no top-level key — targets are byte-spliced into each array element under `SubField` |
| `KindComputed` | none — the engine **skips** it | no key in engine bytes; the application splices its own value |

`Guard func(ctx *Ctx, parent any) bool` returning `false` hides the value in
the shape the kind already promises — `null` for to-one, an *empty* collection
for to-many (never `null`), `SubField: null` per element for in-array — and no
fetch happens for that parent; a guard changes content, never shape. `Bare`
means fetch-all (`EdgeQuery.Limit == 0`) *and* a flat array. Zero-value gotcha
on hand-built edges: a **non-bare** edge with `Limit == 0` is not fetch-all —
0 reads as "unset" and the engine substitutes the default of 20
(`graph.Compile` always writes an explicit limit).

### Registry and the fetchers

```go
type Registry interface {
    FetchByIDs(r Resource) (FetchByIDs, bool)
    FetchByParents(r Resource) (FetchByParents, bool)
    FetchByEdge(parent Resource, key string) (FetchByParents, bool) // reverse fetchers bound PER EDGE
}

type FetchByIDs      func(c *Ctx, ids []string) ([]any, error)
type FetchByParents  func(c *Ctx, parentIDs []string, q EdgeQuery) (map[string]ParentRows, error)
```

A reverse join belongs to the edge, not the target node — `author.books` and
`tag.tagged` both load `Book` rows by different keys — so for a reverse edge
the engine first asks `FetchByEdge(parent, key)` and falls back to
`FetchByParents(target)` when the edge has no fetcher of its own (a
`(nil, true)` answer counts as none). A registry with node-level fetchers
only answers `(nil, false)`. A registry that **wraps** another (tracing,
metrics) must forward `FetchByEdge` too — `FetchByEdge` is part of the
interface precisely so that a wrapper cannot compile without it: a graph
whose reverse edges are bound per edge has no node-level fetcher to fall
back to, and `graph.Compile` has already accepted it.

Both fetcher types **must be safe for concurrent use**: the v1 engine loads
sibling edges sequentially, but the contract is concurrency-safe. A missing
registration surfaces at materialize time as a plain `error` (not an
`*Error`), and only if an edge of that kind actually executes — a reverse edge
whose every parent is guarded out never looks the fetcher up.
`FetchByIDs`: return rows for the requested ids only; an id you cannot resolve
is simply absent from the result — what that absence *means* is the engine's
policy business, never the fetcher's.

`FetchByParents` — the **probe** contract. One call covers every parent id of
a level; return each parent's rows keyed by parent id. A parent with no
children is an absent key or empty rows, never an error; unrequested keys are
dropped by the engine. With `q.Limit == n > 0`: select `n+1` rows, return at
most `n`, set `ParentRows.HasMore` iff the extra row existed — **the limit+1
probe belongs to the fetcher**; the engine only truncates defensively if more
come back. `q.Limit == 0` is a bare edge: fetch everything, never report
`HasMore`. `ParentRows.NextCursor` is opaque: a non-empty value is copied into
the envelope verbatim (JSON-string-quoted, no HTML escaping); empty omits the
`"nextCursor"` key. `HasMore` and `NextCursor` are ignored on bare edges.

### Envelope (wrapped edge values)

`Edge.Envelope` (`include.Envelope{Key, Pagination}`, zero = plain) is applied
as the LAST step of every edge value, after guards and policies. With
`Key = "data"`, `Pagination = "pagination"`:

| Edge | Plain | Wrapped |
| --- | --- | --- |
| to-one, target present | `obj` | `{"data":obj}` |
| to-one, absent (empty FK, dangling under `MissingForeignNull`, guard false, `MissingRequiredNull`) | `null` | `{"data":null}` |
| reverse / to-many | `{"items":[e…],"hasMore":b(,"nextCursor")}` | `{"data":[e…],"pagination":{"hasNextPage":b(,"nextCursor")}}` |
| reverse / to-many, guard false or no rows | `{"items":[],"hasMore":false}` | `{"data":[],"pagination":{"hasNextPage":false}}` |
| `Bare`, or `Pagination == ""` | `[e…]` / n.a. | `{"data":[e…]}` (HasMore/NextCursor dropped) |
| in-array element | `subField: obj` / `null` | `subField: {"data":obj}` / `{"data":null}` |

Every list element `e` is wrapped too (`{"data":obj}`). Member names are
spliced verbatim; `graph.Compile` rejects names that would need escaping.
The inner keys `hasNextPage` / `nextCursor` are fixed.

`EdgeQuery` arrives fully resolved — apply it as given, never re-read client
input. `Limit` is `clamp(client :limit, 1, Edge.Limit)`; `Sort` has passed the
`SortCols` whitelist (`'-'` prefix = descending, `""` = the fetcher's own
default order) and is never a raw client string; `Args` holds the remaining
declared arguments verbatim, minus the built-ins `limit`/`sort` (already
folded into the other fields), `nil` when nothing remains — the values are
shared with the plan, do not mutate them. `Edge` (`EdgeRef{Parent, Key}`,
`String()` = `"Author.books"`) names the reverse edge being loaded — the owner
resource, whose ids are in `parentIDs`, and the include key. A fetcher bound
per edge already knows which edge it serves and may ignore it; a node-level
fetcher shared by several inbound reverse edges switches on it to pick the
join.

### Ctx

```go
type Ctx struct {
    Context  context.Context  // nil → context.Background()
    Registry Registry         // nil only valid for plans without child edges
    Request  *Request         // HTTP snapshot an adapter fills; nil outside HTTP
    Env      any              // application per-request state; opaque to the engine
    Marshal  MarshalFunc      // nil → MarshalNoEscape
    Policies Policies         // materialize-time fallbacks; zero value = defaults
}                             // + an unexported mutex-guarded loader cache

type Request struct {
    Method      string            // "GET"
    Path        string            // the URL path as requested: "/books/b1"
    Route       string            // the matched template: "/books/{id}"
    OperationID string            // the operation's id when the framework has one
    PathParams  map[string]string // {"id": "b1"}
    Query       url.Values
    Header      http.Header       // canonical keys
    RemoteAddr  string
}
```

One `Ctx` is shared by every fetcher of a request and is **read-only** for
them: the engine never mutates it after construction, and fetchers may run
concurrently — per-request writes belong behind the application's own
synchronization in `Env`. `Ctx` contains a mutex: always pass `*Ctx`, never
copy it by value. The zero value is usable.

- `Request` is RAW and UNTRUSTED client input: a header is whatever the
  client (or the edge proxy) sent. Interpreted, trusted per-request state
  (the authenticated viewer, the tenant) belongs in `Env`, not `Request`.
  Only stdlib types (`net/http`, `net/url`) — the engine stays free of any
  HTTP framework. It is `nil` outside HTTP (tests, workers, CLIs); read it
  through the nil-safe accessors below rather than the field directly.
  - `func (c *Ctx) Header(name string) string` — one request header via
    `http.Header.Get` (canonicalized), `""` when there is no `Request`.
  - `func (c *Ctx) PathParam(name string) string` — one matched path
    parameter, `""` when absent or there is no `Request`.
  - `func (c *Ctx) QueryValue(name string) string` — the first value of one
    query parameter, `""` when absent or there is no `Request`.
- `func (c *Ctx) Rows() int` — documents materialized so far in this request,
  root included; the actual-cost counterpart of `PlanNode.Cost` for cost-based
  rate limiting. The counter is the second engine-written field on `Ctx`
  (after the loader cache); it is single-threaded and needs no lock.
- `func (c *Ctx) StdContext() context.Context` — the request Go context,
  falling back to `context.Background()`; for fetchers/`MapFn`s/`EnrichFn`s,
  which receive only a `*Ctx` but need cancellation for DB calls.
- `Marshal`: the default (`nil`) is `MarshalNoEscape(v any) ([]byte, error)` —
  `JSON.stringify`-parity bytes with raw `&`, `<`, `>`. Install `MarshalStd`
  to opt back into `encoding/json`'s HTML escaping. (Residual divergence: Go
  always escapes U+2028/U+2029 where `JSON.stringify` emits them raw.)
  **Security:** the default bytes are for `application/json` bodies only. Do
  not embed them in HTML (SSR `<script>` bootstraps, mail templates) without
  escaping at the embedding site; a string containing `</script>` would break
  out of the script context. Use `MarshalStd` there.
- `Policies` (nested struct — the materialize-time policies live here, not on
  `Options`): the engine-wide fallback set; the zero value is the permissive
  default for every field, and a per-edge override on `Edge` wins where set.
  Set it at construction; the read-only contract applies.
- `LoaderState` is library-internal plumbing for `graph.Loader` — not for
  application use.

## Parsing

```go
func ParseInclude(raw string) (IncludeTree, error)
func ParseExclude(raw string) ([][]string, error)
```

Grammar: comma separates sibling paths (`a,b`), dot separates nesting (`a.b`),
`:name(value)` attaches an arg to the segment it follows, `|` inside parens
splits multi-values (`:tag(x|y)` → `[]string`; one value → `string`); `:name`
with no or empty parens yields the omitted marker `""`. Whitespace around
tokens is trimmed; empty tokens skipped; empty input → empty tree. Names must
match `[A-Za-z_][A-Za-z0-9_]*`; arg values may not contain `, . : ( ) |`,
control bytes, or invalid UTF-8; a
duplicate arg on one segment, or the same arg repeated across paths with a
*different* value, is rejected (the same value merges silently). Violations
return `NewError(INVALID_INCLUDE, …)`. `ParseExclude` is paths only —
`"a.b,c"` → `[["a","b"], ["c"]]` — no args; empty input returns `nil, nil`.

## Planning

```go
func ResolvePlan(root Resource, tree IncludeTree, exclude [][]string, opts Options) (*PlanNode, error)
```

Validates and expands with no data fetched:

- The children at each node are the union of `Defaults()` and the client keys.
  **Order is deterministic**: defaults first in declaration order, then
  client-only keys **sorted** — materialization emits edges in exactly this
  order, so response bytes are stable run-to-run.
- Client paths: an unknown or non-`Includable` edge → `INVALID_INCLUDE`; depth
  beyond `Limits.MaxDepth` or more than `Limits.MaxNodes` client edges →
  `INCLUDE_TOO_DEEP`. Only client-introduced edges count toward the caps.
- Cost: every plan node (defaults included) is estimated as the product of the
  per-edge multipliers on its path — root and to-one 1, enveloped to-many the
  resolved limit (client `:limit` clamped to `Edge.Limit`), Bare and in-array
  `Edge.EstimatedRows` (default 100), computed 0. The sum is `PlanNode.Cost`
  (each node carries its subtree's; the root carries the plan's). Root cost
  above `Limits.MaxCost` → `INCLUDE_TOO_EXPENSIVE` before any fetch. The root
  also carries `PlanNode.MaxRows`, the runtime budget (see Materialize).
- Defaults expansion is uncapped but cycle-guarded: a default edge re-entering
  a resource already on the default chain is silently skipped, as is a stale
  default with no matching edge. Client edges are exempt from the cycle guard
  (bounded by the limits), so diamonds via sibling defaults survive.
- A computed edge plans like any other (Includable gate, args, accounting) but
  is a **leaf**: a client child segment under it is `INVALID_INCLUDE`.
- Declared arg values reach the fetcher verbatim in `EdgeQuery.Args`; they are
  raw client input (only delimiters, control bytes and invalid UTF-8 are
  rejected at parse time) and must be parameterized, never concatenated into
  SQL.
- Args are validated per edge (see Policies). The built-ins are kind-gated —
  an accepted-and-ignored built-in is a contract error, never a silent no-op:
  `:limit` only on reverse and forward-hasMany edges and never on a bare one;
  `:sort` only on reverse edges. `:limit` is coerced to a positive `int` in
  `PlanNode.Args` at plan time (single string form only).
- `exclude` applies **last**, two-pass: validate every path against the
  resolved plan (unknown → `INVALID_INCLUDE`; naming a `Required` edge →
  `INVALID_INCLUDE` under `ExcludeRequiredStrict`), then remove tolerantly. A
  required edge is *never actually removed* under either policy; sub-paths
  beneath it still prune. Root-level args on a hand-built tree are rejected.

### PlanNode

```go
type PlanNode struct {
    Path     string        // "" (root), "author", "author.avatar"
    Resource Resource      // nil on a Computed node
    Many     bool
    Args     map[string]any // ":"-stripped; "limit" is an int, the rest string/[]string
    Children []*PlanNode
    EdgeKey  string        // "" for the root
    Edge     *Edge         // nil for the root
    Computed bool
}

func (p *PlanNode) Get(edgeKey string) *PlanNode  // direct child or nil; nil-safe receiver
func HasInclude(plan *PlanNode, edgeKey string) bool
```

`Get` is nil-safe, so lookups chain: `plan.Get("a").Get("b")`. `HasInclude`
answers presence only; use `Get` when you need the node's `Args` or `Computed`
flag — e.g. to decide whether to splice a computed edge's value in.

## Materialization

```go
func Materialize(plan *PlanNode, docs []any, ctx *Ctx) ([]json.RawMessage, error)
```

Breadth-first, level-batched, sequential. Per level: `Enrich` on the whole doc
slice, then `Serialize` + marshal each doc into its scalar bytes, then each
child edge is resolved **across the whole level** (one `FetchByIDs` per
forward edge, one `FetchByParents` per reverse edge — nested levels stay
batched too) and attached in plan order. The result is aligned 1:1 with
`docs`, input order preserved; ids are deduped level-wide in first-seen order,
and `""` is never fetched on forward edges (a legitimate no-target).

Byte-exactness contract: a document's bytes are the marshaled scalar with each
edge member appended before the closing `}` — key order is always *scalar
struct fields first, then edges in plan order*, and no scalar byte is
re-encoded. In-array stitching goes through `jsonsplice`: element interiors
and everything outside the array member survive verbatim (an element already
carrying `SubField` has the value replaced in place, keeping its position);
the one normalization is the array frame — inter-element whitespace is dropped
on rebuild. An absent or `null` array member skips the edge for that parent; a
non-object element, or an id list whose length differs from the wire array's,
fails the request (developer error) — except under a guard-false parent, whose
ids are not even length-checked.

## Hydrate facades

```go
func HydrateByID(root Resource, id string, inc IncludeTree, exc [][]string,
    fetch func(ctx *Ctx, id string) (doc any, err error),
    ctx *Ctx, opts Options) (json.RawMessage, *PlanNode, error)

func HydrateByQuery(root Resource, q QueryArgs, inc IncludeTree, exc [][]string,
    fetch RootFetcher, ctx *Ctx, opts Options) (QueryResult, *PlanNode, error)

func HydrateEntity(root Resource, doc any, inc IncludeTree, exc [][]string,
    ctx *Ctx, opts Options) (json.RawMessage, *PlanNode, error)
```

All three run `ResolvePlan` first (400-before-fetch), then delegate to
`Materialize`, and return the resolved plan alongside the bytes.

- `HydrateByID`: `fetch` returning `(nil, nil)` maps to
  `*Error{Code: NOT_FOUND, Status: 404}` with the id in `Path`; a non-nil
  error propagates verbatim.
- `HydrateByQuery` is the list facade. `QueryArgs{Where, Sort, Page, Limit}`
  is **passed to the `RootFetcher` verbatim — the facade does not interpret
  it**; offset-vs-cursor and the SQL are entirely the closure's business.
  `Where` is a `ResolvedFilter` from `ResolveFilter` (see Filters), nil when
  there is no filter; an application-owned filter of its own travels in the
  fetcher closure, not here. `RootFetcher` returns `(docs, total,
  hasMore, err)`; `QueryResult{Data, Total, HasMore, Page, Limit}` copies
  `total`/`hasMore` through untouched and echoes `q.Page`/`q.Limit` back. Root
  pagination and per-edge `EdgeQuery.Limit` are two independent contracts.
- `HydrateEntity` is for POST/PATCH: the doc is already in memory, no root
  fetch.

### Hydrator

`Hydrator` is the bound form of the three facades: root, registry and options
are captured once, the raw `?include=` string is parsed inside, and the
by-id fetcher comes from the registry instead of a caller-supplied closure.

```go
func Bind(root Resource, reg Registry, opts Options) *Hydrator

func (h *Hydrator) Root() Resource
func (h *Hydrator) ByID(ctx *Ctx, id string, inc string) (json.RawMessage, error)
func (h *Hydrator) Query(ctx *Ctx, q QueryArgs, inc string, fetch ListFetcher) (QueryResult, error)
func (h *Hydrator) Hydrate(ctx *Ctx, doc any, inc string) (json.RawMessage, error)
```

`Bind` panics on a nil root or registry (a wiring error, not a request error);
a zero `Options` means `DefaultOptions`. Every method accepts a nil `ctx` —
it becomes `&Ctx{Registry: reg}` — and fills in `ctx.Registry` from the bound
registry when the caller left it nil.

- `ByID` parses the include string, resolves the plan (still
  400-before-fetch), then takes the fetcher from `reg.FetchByIDs(root)` and
  calls it with `[]string{id}`. No fetcher registered for the node is a wiring
  error (`include: node %q has no FetchByIDs fetcher`); zero rows back is
  `*Error{Code: NOT_FOUND, Status: 404}`; more than one row for one id is a
  fetcher bug and errors out.
- `Query` is the list path over a `ListFetcher`. It applies the same budget
  pre-check as `HydrateByQuery` (a known `q.Limit` against `plan.Cost` and
  `plan.MaxRows`) before the fetcher runs, and copies `NextCursor` /
  `PrevCursor` from the page into the `QueryResult` alongside `Total` /
  `HasMore`, echoing `q.Page` / `q.Limit`.
- `Hydrate` is the POST/PATCH path: materializes an already-loaded document.

Both families run one pipeline (`budgetCheck` → fetch → `Materialize`); a
`RootFetcher` is adapted to a `ListFetcher` internally.

```go
type ListFetcher func(ctx *Ctx, q QueryArgs) (ListPage, error)

type ListPage struct {
    Docs       []any
    Total      int  // offset mode: total rows; cursor mode: 0 = unknown
    HasMore    bool
    NextCursor string // opaque token in cursor mode; "" = none
    PrevCursor string
}
```

`ListFetcher` is the cursor-aware sibling of `RootFetcher`: the same
mode-agnostic contract, with the continuation tokens returned in the page
rather than dropped. Offset-vs-cursor stays the closure's business.

Prefer the unbound facades when you need something `Hydrator` deliberately
does not expose: an exclude list, the resolved `*PlanNode` back (for
`HasInclude` and computed-edge splicing), an already-parsed `IncludeTree`, a
per-request `Options`, or a by-id fetch that is not the registry's
`FetchByIDs`.

## Inputs

A root resource's **inputs** are the list parameters it accepts and the keys it
accepts them under. `graph.Compile` derives the value from the node's `Inputs`
declaration and its `col` tags, `apidoc.InputParams` documents it, and
`ResolveInputs` enforces it — one value read by both, so the served document
and the accepted requests cannot drift.

```go
type PageMode string // PageModeOffset "offset", PageModeCursor "cursor"

type SortInputs struct {
    Enabled bool
    Default string            // WIRE form ("-name"); "" = the fetcher's own order
    Keys    map[string]string // wire key → SQL sort key
}
type FilterInputs struct {
    Enabled bool
    Fields  map[string]Column // DOCUMENTATION vocabulary, not the check set
}
type PageInputs struct {
    Mode         PageMode
    DefaultLimit int
    MaxLimit     int
}
type Inputs struct {
    Sort   SortInputs
    Filter FilterInputs
    Page   PageInputs
}

// The OPTIONAL Resource seam. The bool reports whether the node DECLARED inputs.
type InputSource interface{ Inputs() (Inputs, bool) }

func DefaultInputs() Inputs
func InputsOf(res Resource) (Inputs, bool)
```

`Filter.Enabled` is the only part of `FilterInputs` `ResolveInputs` reads:
`Fields` is the **documentation** vocabulary — the filterable root columns
`apidoc.InputParams` renders into the `?where=` description and
`x-filter-fields` — while the runtime check is `ResolveFilter(root, …)`, which
judges every name (root columns *and* the columns reached across a filterable
edge, which `Fields` does not list) against the resource graph itself.

`InputsOf` returns `DefaultInputs()` and `false` for a resource that is not an
`InputSource`. Callers treat the two cases identically: "declared nothing" is
one well-defined contract — offset pagination with `DefaultPageLimit` 20 and
`DefaultMaxPageLimit` 100, **no sort, no filter** — not a special case.

### ResolveInputs

```go
type RawInputs struct {
    Sort   string
    Page   int
    Cursor string
    Limit  int
    Where  Filter // already parsed (ParseFilterJSON / ParseFilterQuery); nil = none
}

func ResolveInputs(root Resource, raw RawInputs, opts Options) (QueryArgs, error)
```

`ResolveInputs` is the **only** runtime enforcement of these parameters. A
framework adapter validates just what it derives from its own input struct's
tags (a missing field, a non-numeric `?limit=`); every rule below lives here,
so the same request is judged the same way whichever transport bound it.
`Include` is deliberately absent from `RawInputs`: the include string is parsed
by the `Hydrator` method that consumes it.

Every rejection is a `*Error` with status 400. On success `Limit` is never
zero, for an `Inputs` built by `graph.Compile` or `DefaultInputs` (both of
which give `Page.DefaultLimit` a non-zero value).

| Parameter | Rule |
| --- | --- |
| `Sort` | `""` → `Inputs.Sort.Default`, resolved through `Keys` (wire → SQL, a leading `-` kept); a client key resolved the same way. An unknown key, or any non-empty key when sort is disabled → `INVALID_SORT`, `Path` = the clipped client key. Sort disabled *and* nothing to resolve → `Sort` stays `""`. |
| `Limit` | `0` → `Page.DefaultLimit`; `< 0` or `> Page.MaxLimit` → `INVALID_PAGINATION` (`Path` `limit=<n>`). |
| `Page`/`Cursor`, offset mode | `Page 0` → `1`; `Page < 0` → `INVALID_PAGINATION` (`page=<n>`); a non-empty `Cursor` → `INVALID_PAGINATION` (`cursor=<v>`, clipped). |
| `Page`/`Cursor`, cursor mode | `Cursor` copied through opaquely; `Page != 0` → `INVALID_PAGINATION` (`page=<n>`); the resulting `QueryArgs.Page` is `0`. |
| `Where` | nil → nil. Non-nil with filtering disabled → `INVALID_FILTER` (`Path` `"where"`). Otherwise `ResolveFilter(root, raw.Where, opts)`, whose errors (and codes) pass through unchanged. |

The checks run in a fixed order and the first fault wins: sort → limit →
page/cursor → where.

The two page modes are mutually exclusive by rejection, not by precedence: a
`?cursor=` sent to an offset list and a `?page=` sent to a cursor list are both
faults, so a client cannot silently get the other mode's semantics.

Unvalidated client text in `Path` is clipped by the same bound as the filter
codes use (16 bytes plus `…`) — the client does not get to choose the size of
the error body.

## Filters

`include` carries the filter **model**: a sealed AST a parser produces, and
`ResolveFilter`, which checks it against the graph. It also ships the two
parsers most applications would otherwise rewrite (see *Parsers* below) — but
never the SQL generation (joins, `EXISTS`), which is a dialect and stays in the
application's adapter. `ResolveFilter` remains the ONE place that judges names,
operators and limits, whichever parser built the AST.

```go
type FilterOp string // OpEq, OpNe, OpLt, OpLte, OpGt, OpGte, OpIn, OpNin

type Filter interface{ /* sealed */ }
type FilterAnd []Filter
type FilterOr  []Filter
type Quant string // QuantAny "any", QuantAll "all", QuantNone "none"
type FilterStep struct {
    Key   string // edge key as declared
    Quant Quant  // "" on a to-one hop; required on a to-many hop
}
type FilterCond struct {
    Path  []FilterStep // hops from the root, outermost first; nil = root column
    Field string       // wire json key of the column
    Op    FilterOp
    Value any          // verbatim from the parser; the engine never coerces it
}

func ResolveFilter(root Resource, f Filter, opts Options) (ResolvedFilter, error)

type ResolvedFilter interface{ /* sealed */ }
type ResolvedAnd  []ResolvedFilter
type ResolvedOr   []ResolvedFilter
type ResolvedCond struct {
    Hops   []FilterHop // edges traversed, root outwards
    Column Column      // SQL-side binding on the last hop's target (or the root)
    Op     FilterOp
    Value  any
    Node   Resource    // the node Column sits on — the root, or the last hop's To
}
type FilterHop struct{ Key string; Edge Edge; Quant Quant; From, To Resource }
```

`ResolveFilter` returns the three concrete value types only, never pointers,
although it accepts pointer forms as input; an adapter's type switch needs no
pointer cases and may treat `default` as a programming error.

Resolution rules:

- every hop must be a `Filterable` edge; a **to-many** hop must carry a
  quantifier (`any` / `all` / `none`) and a **to-one** hop must not; the
  path may cross at most `Limits.MaxFilterMany` to-many hops;
- the whole tree may contain at most `Limits.MaxFilterSubqueries` to-many hops
  summed over every condition — a tree-wide bound on correlated subqueries,
  reported as `FILTER_TOO_EXPENSIVE` with an empty `Path` (the per-path
  `MaxFilterMany` is judged first, so an over-long single path is still
  `FILTER_TOO_DEEP`);
- the leaf must be a `Filterable` column of the node the path reaches
  (`ColumnSource`); a root that is no `ColumnSource` has no filterable column;
- `eq`/`ne`/`in`/`nin` apply to every column, `lt`/`lte`/`gt`/`gte` to
  numbers, strings and `time.Time` — never to bool;
- an empty `And`/`Or`, or a nil member, is rejected (its truth value differs
  between dialects), and reported at the node's position in the tree —
  `"and[1]"`, `"or[0].and[2]"`, `""` for the root group itself;
- `len(Path) > Limits.MaxFilterDepth` and more than `Limits.MaxFilterNodes`
  conditions + groups are `FILTER_TOO_DEEP`; everything else the graph does
  not admit is `INVALID_FILTER` with `Path` = `"author.name"`, the path up to
  the offending hop (`"reviews"` for a to-many hop without a quantifier),
  `"author:any"` / `"reviews:some"` for a quantifier fault, or
  `"author.name:like"` for an operator fault. Text that was not found in the
  graph (an unknown key, field, operator or quantifier) is echoed bounded to
  16 bytes plus `…`; a name the graph does know goes back whole.

A handler resolves before it fetches (the same 400-before-fetch rule as
`ResolvePlan`) and hands the result to its `RootFetcher` through
`QueryArgs.Where`. The client's spelling never reaches SQL text: an adapter
walks `Hops` and `Column.Col`, both compile-time constants from struct tags.
`FilterSubqueries(resolved)` reads that cost back: it is the number of to-many
hops the tree will run as correlated subqueries, the number to reserve in an
application cost bucket next to `plan.Cost`.

`examples/filter/main.go` is the runnable reference for the application-owned
half: it unwraps a `{"where": …}` envelope into `ParseFilterJSON` and prints
the `ResolvedFilter` as SQL — joins, the three `EXISTS` templates, and bound
arguments — without a database.

### Parsers

A syntax is a product decision, so the AST above is the contract and an
application may always write its own parser against it. Two spellings are
common enough to ship:

```go
func ParseFilterJSON(root Resource, raw []byte) (Filter, error)
func ParseFilterQuery(root Resource, values url.Values) (Filter, error)
func FilterOpsFor(t reflect.Type) []FilterOp
```

`ParseFilterJSON` parses ONE filter node — the value of a request's `where`
key; the envelope around it stays the application's:

```
{"and": [<node>, ...]}          {"or": [<node>, ...]}
{"<dotted.path>": {"<op>": <value>}}
```

One key per object, always: a group key and a field key in one object have no
defined precedence. A path is edge keys followed by the field, with the
quantifier suffixes of *Quantifiers* below — none = `any`, `*` = `all`,
`~` = `none` — on the SEGMENT when the path crosses several to-many hops and
on the FIELD when it crosses exactly one. Resolving the field suffix needs the
graph, so the parser takes the root and walks `Edges()` to learn which segments
are `Many`; a to-many segment nobody quantified becomes `any`.

The parser judges SYNTAX only. An unknown edge key or field passes through
untouched, for `ResolveFilter` to reject with a bounded path — so there is one
place a name is judged, not two. Its own faults are
`*Error{Code: INVALID_FILTER, Status: 400}` with `Path` in the conventions
above and nothing else in it: the offending key, `"<key>:<op>"` for an operator
fault, `"<path>:<quant>"` for a quantifier fault, and `""` for a structural
fault with no key to name. Client text is echoed through the same 16-byte bound
the resolver uses, and a multi-key object reports its first key plus a count
rather than all of them — a client never sizes the 400 body. Values pass
through as `encoding/json` decoded them (a JSON number is a `float64` even for
an int column); the adapter coerces by `Column.Type`.

`ParseFilterQuery` is the same AST from a query string — the spelling a browser
or a `curl` can type — reading only the keys that start with `where[` and
ignoring every other parameter, so it composes with `page`, `include` and the
rest:

```
where[<dotted.path>][<op>]=<value>
where[or][<i>][<dotted.path>][<op>]=<value>       where[and][<i>]…
```

Paths and quantifier suffixes are exactly `ParseFilterJSON`'s
(`where[works.title~][eq]=x`), and groups nest — a group's members are its
distinct indices, ordered numerically, each member itself an AND of the keys
that share its index. Bracket keys carry no precedence, so **siblings at one
level are always AND-ed**: two conditions, two groups, or a group standing next
to a condition. The order inside that AND is fixed for determinism — the groups
first (`and`, then `or`, by index), then the conditions sorted by key — and a
lone member is returned bare rather than wrapped. No `where[` key at all yields
a nil `Filter`, not an empty one.

A URL value is a string, so this parser is the one place that coerces, by the
`Column.Type` of the leaf the path lands on:

| Column type | Value |
| --- | --- |
| `int`, `int8` … `int64` | `int64` |
| `uint`, `uint8` … `uint64` | `uint64` |
| `float32`, `float64` | `float64` |
| `bool` | `bool` (`strconv.ParseBool`: `true`, `1`, `t`, …) |
| `time.Time` | `time.Time`, parsed as RFC 3339 |
| anything else, or an unknown path or field | the string as given |

`in` and `nin` split the value on `,` into a `[]any` of coerced members
(`where[age][in]=1,2`) — which is also the limit of the syntax: a value that
itself contains a comma needs the JSON spelling. The widths are the AST's, not
the column's; narrowing to the column is the adapter's job, as it is for the
`float64` a JSON number decodes to.

Its faults are the same `*Error{INVALID_FILTER}` with the same clipped-client-
text `Path`, and in the same conventions: a condition fault names the path —
`"<path>:<op>"` for an unknown operator, `"<path>:<value>"` for a value the
column's type refuses — while a structural fault, which has read no path yet,
names the whole bracket key: a malformed key (`where[title]`, `where[]`,
`where[title][eq][x]`), a key given twice, a non-numeric group index. Unknown edges and fields still pass through to
`ResolveFilter`, where the value stays a string.

`FilterOpsFor` reads the operator ↔ column-type matrix forwards: the subset of
`eq ne in nin lt lte gt gte` a column of type `t` admits, in that fixed order,
and nil for a type outside `FilterableType`. The resolver asks "is this
operator allowed here"; a documentation or input-schema caller asks "which
operators may a client name here", and both answer from the one matrix.

### Quantifiers

A to-many hop asks how one parent's children decide the parent's fate. The
core carries the answer on `FilterHop.Quant`; the adapter must reproduce
these templates, `cond` being the leaf comparison and `child.fk = parent.id`
the correlation it binds for the hop:

| Quant | Template | Empty relation |
| --- | --- | --- |
| `any` | `EXISTS (SELECT 1 FROM child WHERE child.fk = parent.id AND cond)` | does not hold |
| `all` | `NOT EXISTS (SELECT 1 FROM child WHERE child.fk = parent.id AND NOT cond)` | holds |
| `none` | `NOT EXISTS (SELECT 1 FROM child WHERE child.fk = parent.id AND cond)` | holds |

Nested to-many hops nest the templates (`reviews(all).author.works(none).title`
is a `NOT EXISTS` whose body joins `author` and contains another `NOT
EXISTS`) — the inner template stands in for `cond` of the outer one, so under
`all` it is negated as a whole. A to-one hop inside a subquery is a plain
inner join, so a child whose foreign key is `NULL` drops out of the subquery
body: it is neither a match (under `any` / `none`) nor a violation (under
`all`), and `all` holds vacuously over such children — the reading the
empty-relation table assumes. `Bare()` and
`Limit(n)` on an edge govern include loading only — a filter's `EXISTS` sees
every child. SQL three-valued logic applies as-is: a child whose
column is `NULL` makes `cond` — and so `NOT cond` — `NULL`, so that child is
neither selected under `any` / `none` nor a violation under `all`; the core
neither touches values nor compensates for it.

The spelling `ParseFilterJSON` accepts, and the one to keep in a hand-written
parser: no suffix is `any` (`{"reviews.rating": {"gt": 4}}`), `*` is `all`
(`{"reviews.rating*": {"gt": 4}}`), `~` is `none` (`{"reviews.rating~":
{"gt": 4}}`); the suffix sits on the field when the path has exactly one
to-many segment and on the segment when it has several
(`reviews~.author.works*.title`).

Reserved keys: both shipped formats use `and` /
`or` as group keys, so `graph.Compile` refuses a filterable column whose json
key is one of them — and a `Filterable()` EDGE keyed `and` or `or` just the
same, since a hop and a group sit in the same key position.

What `Filterable` grants. A filter is SQL the adapter builds from the graph
binding; it never calls the target's fetcher. Row-level scoping that lives in
a `FetchIDs` / `FetchParents` closure — tenancy, soft-delete, an ACL — is
therefore NOT applied inside a filter's `EXISTS`, and neither is `Guard`
(which is why `Filterable` + `Guard` is a compile finding). Put such scoping
in the adapter's join binding, or do not make the edge filterable. A
`filter` column is likewise a READ grant on its value: `lt` / `gt` turn any
list endpoint into a binary-search oracle for it, even on a node a client
can never `?include=`. Grant `filter` as you would grant a field in the
response.

What the resolved filter does NOT carry, and the adapter therefore owns:
the table behind each `Resource` (bind it by `Name()`), the join behind each
hop (bind it by `(From.Name(), Key)` — `include.Edge` deliberately carries no
foreign-key column name, only the typed closures; `Edge.Backref` is the
wire-side classification name of the child's back-reference, never a SQL
column — do not join on it), the shape of `Value` for `in` / `nin` (the core
admits any value; the parser and adapter agree on the collection type between
themselves), and the coercion of `Value` to `Column.Type`. `graph.Compile` validates the graph, not those
bindings; an adapter should check at start-up that every filterable edge and
column of every root it serves has one.

## Limits: bounding nested loading

Nested loading is bounded at four levels, from the graph declaration down to
one request. The first two are the developer's; the third is the client's;
the fourth is what the application reads back.

### 1. Per edge, at declaration (the ceiling)

```go
book.Edge("reviews", graph.Reverse[ReviewWire]("bookId")).
    Limit(50).         // rows per parent; enveloped to-many default is 20
    Includable()

book.Edge("tags", graph.ToMany[TagWire]()).
    ForeignKeys(func(r bookRow) []string { return r.TagIDs }).
    Bare().            // fetch-all: no ceiling, flat array
    EstimatedRows(10). // what the cost model multiplies by (default 100)
    Includable()
```

- `Limit(n)` is legal on reverse and forward-hasMany edges only and is the
  value a client `:limit()` is clamped to. `Compile` rejects it on a `Bare()`
  edge, on to-one / in-array edges, and below 1.
- `Bare()` compiles to `Limit 0` — fetch everything, never report `hasMore`.
  Use it only for relations you know are small.
- `EstimatedRows(n)` is legal on unbounded edges only (`Bare()`, in-array).
  `Compile` REQUIRES it when the unbounded edge sits on a node that is itself
  the target of a to-many edge — the position where the default of 100 would
  multiply a second time.

### 2. Per request, `Options.Limits`

Passed to every `HydrateByQuery` / `HydrateByID` / `HydrateEntity` call (and
`ResolvePlan` directly):

```go
opts := include.Options{Limits: include.Limits{
    MaxDepth: 3,
    MaxNodes: 30,
    MaxCost:  2000,
    MaxRows:  20000,
}}
```

| Field | Default | Bounds | Failure code |
| --- | --- | --- | --- |
| `MaxDepth` | 4 | deepest CLIENT segment chain | `INCLUDE_TOO_DEEP` |
| `MaxNodes` | 50 | number of CLIENT edges in the tree | `INCLUDE_TOO_DEEP` |
| `MaxCost` | 5000 | static row estimate for ONE root document, defaults included | `INCLUDE_TOO_EXPENSIVE` |
| `MaxRows` | 50000 | rows actually materialized by one hydrate call, root included | `INCLUDE_BUDGET_EXCEEDED` |
| `MaxFilterDepth` | 4 | edge hops in one filter condition (`author.name` = 1) | `FILTER_TOO_DEEP` |
| `MaxFilterMany` | 2 | to-many hops in one filter condition (each is a nested `EXISTS`) | `FILTER_TOO_DEEP` |
| `MaxFilterNodes` | 32 | conditions + groups in one filter tree | `FILTER_TOO_DEEP` |
| `MaxFilterSubqueries` | 8 | to-many hops summed over the whole filter tree (correlated subqueries) | `FILTER_TOO_EXPENSIVE` |

`MaxFilterMany` is counted within `MaxFilterDepth`, and at the defaults (2
within 4) it refuses the **third** to-many hop of one path — two nested
`EXISTS` is already a query most planners handle badly — while
`MaxFilterDepth` goes on bounding the path as a whole. Raise it, up to
`MaxFilterDepth`, to allow more deeply nested `EXISTS`; lower it to
`MaxFilterMany: 1` to allow one `EXISTS` around a chain of joins.

`MaxFilterSubqueries` is the tree-wide twin of `MaxFilterMany`: the per-path
bound says nothing about a tree of many cheap conditions, and thirty one-hop
conditions are thirty correlated subqueries. It is summed over every condition
in the tree, and the per-path bound is judged first, so one over-long path
still reports `FILTER_TOO_DEEP`.

A zero `MaxCost`, `MaxRows`, `MaxFilterDepth`, `MaxFilterMany`,
`MaxFilterNodes` or `MaxFilterSubqueries` means the default, so a literal that
names only `MaxDepth` and `MaxNodes` keeps working.
`include.DefaultOptions` is `{Limits: DefaultLimits}`.

`MaxCost` is computed by `ResolvePlan` after `exclude` is applied: every plan
node is estimated as the product of the multipliers on its path — root and
to-one 1, enveloped to-many the resolved limit, unbounded `EstimatedRows`,
computed 0 — and summed. At the default edge limit of 20, `author` costs 2,
`reviews.author` costs 41, `kids.kids.kids` costs 8 421 and is refused.

`MaxRows` is enforced twice: up front in `HydrateByQuery` as
`Cost × QueryArgs.Limit` (before the root SQL runs, when the page size is
known), and per level inside `Materialize`, where rows are counted BEFORE the
level does any work. `Materialize` also returns the request context's error
between levels, so a cancelled request stops at the next level.

### 3. Per request, from the client

`?include=reviews:limit(5)` lowers the edge's limit for that request. The
value is clamped to the edge ceiling — `min(max(n, 1), Edge.Limit)` — so a
client can ask for less, never for more. `:limit` is accepted on reverse and
forward-hasMany edges only; on a `Bare()` edge or a to-one edge it is
`INVALID_INCLUDE`.

### 4. Reading the cost back

- `plan.Cost` (the root `PlanNode`) — the static estimate `MaxCost` was
  compared to; every node carries its own subtree's estimate.
- `ctx.Rows()` — the actual count after hydration.

The library never rate-limits by itself; these two numbers are the inputs for
a GitHub / Shopify-style cost bucket in the application. `examples/costlimit`
is a reference implementation: reserve `plan.Cost × page` before the root
fetch, refuse with 429 when the bucket cannot cover it, settle to `ctx.Rows()`
afterwards so an over-estimate is refunded.

### What the library does NOT bound

- The root page size. `QueryArgs.Limit` is the application's; the engine only
  catches it through the `Cost × page` pre-check against `MaxRows`.
- The rows a `Bare()` fetcher actually returns. `MaxRows` counts them once
  they are back from the database; only `EstimatedRows` protects the SQL
  side, and only if it is honest.
- Documentation depth is NOT read from the request: `adapters/huma.IncludeParam`
  enumerates include paths with `include.DefaultLimits`; pass the handler's
  own `Limits` to `IncludeParamWithLimits` (and to `apidoc.SchemaFor`) so the
  document matches what the planner accepts.

## Policies

Policies are split by phase. **Resolve-time — `Options`** (read only by
`ResolvePlan`):

```go
type Options struct {
    Limits                Limits       // Limits{MaxDepth, MaxNodes, MaxCost, MaxRows}
    SortPolicy            SortPolicy   // SortStrict (default) | SortFallback
    ArgPolicy             ArgPolicy    // ArgsStrict (default) | ArgsTolerant
    ExcludeRequiredPolicy ExcludeRequiredPolicy // ExcludeRequiredTolerant (default) | Strict
}

var DefaultLimits  = Limits{MaxDepth: 4, MaxNodes: 50, MaxCost: 5000, MaxRows: 50000} // zero MaxCost/MaxRows → these defaults
var DefaultOptions = Options{Limits: DefaultLimits} // strict sort, strict args, tolerant exclude
```

- `SortPolicy` judges an unknown client `:sort()` key **only** when the edge
  has a `SortCols` whitelist; with `SortCols == nil` the key is an undeclared
  argument judged by `ArgPolicy` instead. `SortFallback` falls back to
  `Edge.Sort` (also whitelist-checked); the client string never reaches SQL —
  it is only ever a map lookup key.
- `ArgPolicy`: `ArgsStrict` fails the plan on an undeclared `:name(...)`;
  `ArgsTolerant` passes it through to `PlanNode.Args`. A declared
  `EdgeArg.Validate` rejection is `INVALID_INCLUDE` at `path:arg` either way.
  Declaring `"sort"` as an `EdgeArg` is the escape hatch: its own `Validate`
  replaces the whitelist acceptance check (SQL-side resolution still goes
  through `SortCols`); declaring `"limit"` never shadows the built-in
  coercion. `Edge.ExcludeRequired` overrides `ExcludeRequiredPolicy` per edge.

**Materialize-time — `Ctx.Policies`** (never read by `ResolvePlan`):

```go
type Policies struct {
    MissingRequired MissingRequiredPolicy // Null (default) | Error
    MissingForeign  MissingForeignPolicy  // Null (default) | Error
    FetcherContract FetcherContractPolicy // Tolerant (default) | Strict
}
```

- `MissingRequired` — a `Required` to-one edge resolving to no target (empty
  FK, guarded-out parent, or a row the fetcher did not return): emit a silent
  `null` the published component contradicts (`MissingRequiredNull`), or fail
  the request (`MissingRequiredError`). Ignored on non-required edges.
  `Edge.MissingRequired` / `Edge.MissingForeign` override per edge where
  non-nil.
- `MissingForeign` — a **non-empty** FK that resolved to no row (a dangling
  reference), on the three parent-FK kinds only (to-one, forward-hasMany,
  in-array): keep the v0 shape (`MissingForeignNull` — to-one `null`, a
  silently dropped list item, an in-array `SubField: null`) or fail
  (`MissingForeignError`). An *empty* FK is not dangling; reverse and computed
  edges are out of scope.
- `FetcherContract` — what a fetcher does wrong that the type system cannot
  see: over-limit rows, an unrequested parent id or row id, `HasMore` on a
  bare edge. `Tolerant` corrects silently (truncate, drop); `Strict` fails the
  request naming the edge and violation — run it in tests and dev. No per-edge
  override (a contract violation is a fetcher bug, not edge semantics);
  `graph/loadertest` remains the normative harness.

(`EdgePolicies` / `EdgePolicy` / `EdgePoliciesOf` are plumbing for
`graph.Policies(...)` — set overrides through the builder, not these.)

## Errors

Planning and facade failures are `*include.Error{Code, Path, Status}`
(`NewError(code, path)` defaults `Status` to 400):

| Code | Status | When |
| --- | --- | --- |
| `INVALID_INCLUDE` | 400 | grammar violation, unknown/non-includable path, rejected or misplaced arg (`Path` is `"a.b"` or `"a.b:arg"`), illegal exclude |
| `INCLUDE_TOO_DEEP` | 400 | `MaxDepth` **or** `MaxNodes` exceeded — one code for both, the message distinguishes |
| `INCLUDE_TOO_EXPENSIVE` | 400 | the plan's static row estimate (`PlanNode.Cost`) exceeds `MaxCost`; the message carries both numbers |
| `INCLUDE_BUDGET_EXCEEDED` | 400 | rows materialized exceed `MaxRows` — mid-hydration, or up front in `HydrateByQuery` as `Cost × QueryArgs.Limit` |
| `INVALID_FILTER` | 400 | `ResolveFilter`: unknown/non-filterable edge or column, root without columns, empty group, a to-many hop without a quantifier, a quantifier on a to-one hop, an unknown quantifier, unknown operator or operator illegal for the column type. For a leaf fault `Path` is `"a.b.field"`, the path up to the bad edge, `"a.b.field:op"`, or `"a.b:quant"`; for a **structural** fault (empty group, nil member, nil node) it is the node's position in the tree — `"and[1]"`, `"or[0].and[2]"` — since group members carry no names. The root has no position: an empty root-level group reports `""` |
| `FILTER_TOO_DEEP` | 400 | `MaxFilterDepth`, `MaxFilterMany` **or** `MaxFilterNodes` exceeded; for a too-deep or too-many path `Path` is the client path's keys cut to `MaxFilterDepth+1` segments, each one itself bounded to 16 bytes plus `…`, with a trailing `…` only when segments were actually dropped — so a too-many-hops path (bounded by `MaxFilterDepth` already) always comes back whole and unmarked (a node-count fault carries an empty `Path`) |
| `FILTER_TOO_EXPENSIVE` | 400 | `ResolveFilter`: the tree's to-many hops, summed over every condition, exceed `MaxFilterSubqueries`. A tree-wide fault, so `Path` is empty; the client fixes it by dropping conditions that cross to-many edges |
| `INVALID_SORT` | 400 | `ResolveInputs`: a `?sort=` key the node does not accept — sort not enabled, or the key (after an optional leading `-`) is not one of `Inputs.Sort.Keys`. `Path` echoes the client's key, clipped |
| `INVALID_PAGINATION` | 400 | `ResolveInputs`: a pagination value outside the node's contract — a limit above `MaxLimit` or negative, a negative page, `?page=` in cursor mode, `?cursor=` in offset mode. `Path` names the parameter and the value (`"limit=500"`, `"page=-2"`, `"cursor=abc"`) |
| `NOT_FOUND` | 404 | `HydrateByID`'s fetch returned a nil doc |

Materialize-time failures — a fetcher error, a missing registration, a strict
policy firing, an in-array length mismatch, a serialize failure — are plain
wrapped `error`s, not `*Error`: server-side faults the HTTP layer should treat
as 500s, not client mistakes.
Two exceptions stay client-facing `*Error`s: `INCLUDE_BUDGET_EXCEEDED` when the
root `PlanNode.MaxRows` budget is spent (Materialize counts rows per level,
root documents included, before doing any work on that level), and the
request context's own error, returned wrapped between levels so a cancelled
or timed-out request stops at the next level instead of the next SQL call.
