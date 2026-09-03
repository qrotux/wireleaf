# adapters/huma — the huma v2 bridge: wireleaf components as huma's schema registry

`adapters/huma` (import alias recommended, e.g. `wfhuma`) is a separate Go
module and the only huma-facing package in wireleaf. Its inversion is the whole
point: instead of huma keeping its own schema map and wireleaf patching the
document afterwards, the shared `*apidoc.Components` set **is** huma's schema
registry. Every schema huma asks for — input structs, response bodies, nested
types — is answered from that set, either as a component the graph layer already
registered or as one reflected on demand through the canonical
`reflector.Reflector` and registered into the same set. One document, one
component set, one reflector.

## Overview

The bridge has seven functional layers:

- **`Registry`** (registry.go) — implements `humav2.Registry` over a shared
  `*apidoc.Components` plus an `apidoc.Reflector`.
- **`NewConfig`** (config.go) — the `humav2.Config` wireleaf ships, built
  directly rather than from `huma.DefaultConfig`.
- **Operation layer** (op.go) — `Op` and its decorators (`Errors`,
  `IncludeParam`) run *before* `huma.Register`; `ApplyRequestBodyDoc` and
  `ApplyResponseHeaders` run *after* it, because they replace things huma emits.
- **Envelope derivation + library components** (envelope.go) — a body struct
  matching the `{data[,pagination,…]}` convention documents as an envelope, not
  as its Go field shape.
- **`Node[W]`** (node.go) — the typed wire wrapper for engine-hydrated bytes;
  documents as `$ref` to W's component.
- **`API`** (api.go, request.go) — the wiring object over graph + options +
  components + the huma API, with the `Include`/`Inputs` operation decorators,
  a guarded `Register`, and the middleware that fills `include.Request`.
- **`Bound[W]`** (bound.go) — the per-resource facade `Bind` returns:
  `Get`/`Hydrate`/`List` over the resource's `include.Hydrator`, with
  `ListQuery` in and `Node[W]`/`Page[W]` out, engine errors mapped to huma.

convert.go is the one IR → `humav2.Schema` converter every served schema goes
through; build.go's `BuildInto` merges a component set into a huma document
built *without* the bridge.

## Setup

[`examples/huma/main.go`](../examples/huma/main.go) is the runnable version of
this section: two services on one `http.ServeMux` — Books (offset pagination,
JSON `?where=`) and Authors (cursor pagination, bracket `?where[f][op]=`) —
with `Get`/`List`/`Hydrate` handlers, a POST, and the document printed back.

The order is **`New` → `Attach` → every `Bind` → `Register`**: `New` emits the
graph's components and builds the huma config over them, `Attach` installs the
middleware that fills `include.Request`, `Bind` ties one graph node to its wire
type (registering the `Node[W]` wrapper component), and `Register` refuses an
output carrying a wrapper no `Bind` wired.

```go
a := wfhuma.New(g, include.DefaultOptions, "Books", "1.0.0")
api := humago.New(mux, a.Config())   // any huma adapter
a.Attach(api)
book := wfhuma.Bind[BookWire](a, "Book")

wfhuma.Register(a, humav2.Operation{
    OperationID: "get-book", Method: http.MethodGet, Path: "/books/{id}",
}, func(ctx context.Context, in *getBookInput) (*getBookOutput, error) {
    b, err := book.Get(ctx, in.ID, in.Include)
    if err != nil {
        return nil, err
    }
    return &getBookOutput{Body: BookEnvelope{Data: b}}, nil
}, book.Include(), a.Errors(errBookNotFound))

wfhuma.Register(a, humav2.Operation{
    OperationID: "list-books", Method: http.MethodGet, Path: "/books",
}, func(ctx context.Context, in *listBooksInput) (*listBooksOutput, error) {
    page, err := book.List(ctx, in.ListQuery, listBooks(in.Q)) // include.ListFetcher
    if err != nil {
        return nil, err
    }
    return &listBooksOutput{Body: BookPageEnvelope{Data: page.Data, Pagination: page.Offset}}, nil
}, book.Inputs())
```

`listBooksInput` embeds `wfhuma.ListQuery` (the `include`/`sort`/`page`/
`cursor`/`limit`/`where` block) and may add parameters of its own;
`book.Inputs()` documents the list parameters from the node's `graph.Inputs`
declaration, and `book.List` enforces the same declaration through
`include.ResolveInputs`. `wfhuma.WithFilterSyntax(apidoc.FilterBracket)` on
`New` switches `?where` to the bracket spelling.

Two services can share one router: `Config()` returns the config by value, so
moving `cfg.OpenAPIPath` / `cfg.DocsPath` aside before handing it to the router
adapter keeps the two documents from colliding.

## Config (config.go)

```go
func NewConfig(title, version string, opts ...ConfigOpt) humav2.Config
func WithRegistry(c *apidoc.Components) ConfigOpt
func BridgeOf(oapi *humav2.OpenAPI) *Registry   // panics if not NewConfig-built
```

`New` calls `NewConfig` for you, with `WithRegistry` pointed at the components
it emitted from the graph — `a.Config()` is that result. Calling `NewConfig`
by hand is the no-graph path: without `WithRegistry` it bridges over a fresh
`apidoc.NewComponents()`, only useful for a document with no graph-derived
components.

What `NewConfig` fixes relative to `huma.DefaultConfig`:

- **No `SchemaLinkTransformer`, no `/schemas` route** (`SchemasPath` empty). The
  transformer injects a `$schema` field into every response body — drift against
  the generated client — and its `TypeFromRef` dereference is a latent panic on
  hand-assembled components.
- **`OpenAPIPath: "/openapi"`, `DocsPath: "/docs"`** — DefaultConfig's values,
  kept.
- **JSON format is wireleaf's own** (registered as `"application/json"` and
  `"json"`, `DefaultFormat: "application/json"`): no trailing newline, no HTML
  escaping — a body must be byte-identical to the jsonsplice fragments it was
  spliced from. CBOR is deliberately absent.
- **`humav2.ValidateStrictCasing = true`**, set once per process via
  `sync.Once`. Gotcha: this is a huma package global, so it changes validation
  for *every* huma API in the process; an application wanting case-insensitive
  matching must reset it after building the config.
- **Library components installed** on the component set: `CursorPagination`,
  `CursorPaginationTotal`, `PagePagination`, `Error` (exported name constants
  `*Component`). Idempotent for identical re-registration; a conflicting
  application component squatting a name panics.

There are deliberately **no knobs** for `AllowAdditionalPropertiesByDefault` /
`FieldsOptionalByDefault`: huma applies them only to its own map registry, so
they would be silently inert against the bridge. Required-ness and
additional-properties are the canonical reflector's verdict. Note the migration
gotcha: the reflector does *not* emit `additionalProperties: false`, so bodies
accept unknown properties where stock huma rejects them.

## The Registry bridge (registry.go)

```go
func NewRegistry(c *apidoc.Components, r apidoc.Reflector) humav2.Registry // panics on nil args
func (b *Registry) Components() *apidoc.Components
```

`Schema(t, allowRef, hint)` resolution order: (1) `RegisterTypeAlias` aliases;
(2) node wrapper types → `$ref` to W's component; (3) types already bound to a
component name; (4) the success-envelope derivation (before any reflection);
(5) non-component-worthy types (scalars, slices, maps, `time.Time`, `url.URL`,
`SchemaProvider`/`TextUnmarshaler` implementors) → huma's own `SchemaFromType`,
recursing back through the bridge; (6) everything else → canonical reflector,
with the top component *and* every auxiliary registered into the shared set.

Contracts and gotchas:

- **Anonymous structs cannot become components** — the reflector inlines them,
  and the bridge panics with a message telling you to name the body type.
- **Node wrappers must never reach the reflector.** A transitive walk guards the
  reflection path: a wrapper (or an envelope struct nested inside a plain
  struct) found on the way panics at wiring time, because the reflector would
  emit `{}` for it. Nodes appear only as an envelope `data` field, an open-page
  extra, or a body type of their own.
- **Serving rule**: a component whose Go wire type is known and is a struct is
  served typed (documented *and* validated); anything else — no wire type, or a
  non-struct binding — is served as one opaque `Extensions` fragment. The served
  document is byte-identical either way; only huma-side validation is lost.
- **Concurrency**: registration (`Schema`, `RegisterTypeAlias`) is
  wiring-time-only and not self-concurrent; `SchemaFromRef` is on the
  request-validation hot path and caches conversions under a mutex.
- Any reflection/registration failure **panics** — a half-built document is
  worse than a loud stop at startup.

## API (api.go, request.go, bound.go)

Everything above is a free function over something the application assembles by
hand. `API` is the one object that owns the decisions which must agree with each
other: the compiled graph, the `include.Options` the planner enforces, the
component set the document is written into, and the huma API the operations are
registered on.

```go
func New(g *graph.Graph, opts include.Options, title, version string, o ...Option) *API
func WithFilterSyntax(s apidoc.FilterSyntax) Option   // default apidoc.FilterJSON
func WithConfig(c ...ConfigOpt) Option                // forwarded to NewConfig

func (a *API) Config() humav2.Config                  // build the router adapter with this
func (a *API) Components() *apidoc.Components
func (a *API) Graph() *graph.Graph
func (a *API) Options() include.Options
func (a *API) Attach(h humav2.API)

func (a *API) Include(res include.Resource) OpOpt
func (a *API) Inputs(res include.Resource) OpOpt
func (a *API) Errors(defs ...ErrorDef) OpOpt

func Register[I, O any](a *API, op humav2.Operation,
    handler func(context.Context, *I) (*O, error), opts ...OpOpt)
```

**Order: `New` → `Attach` → every `Bind` → `Register`.** Both steps are guarded,
loudly:

- `Register` before `Attach` panics with `adapters/huma: Register before
  Attach` — there is no huma API to register on yet.
- A second `Attach` panics with `adapters/huma: Attach called twice` — it would
  install the middleware and the document hook twice over.
- `Register` walks the output type `O` (through pointers, slices, arrays, map
  keys and values, and struct fields) and panics on a `Node[W]` wrapper —
  this package's or the core `apidoc.Node[W]` — no `Bind` registered.
  Without the guard the same wiring bug surfaces later, deep inside huma's
  reflection, as `Node[W].Schema` failing to find W's component.

`New` does the emission itself: `apidoc.EmitComponents(&reflector.Reflector{},
g.Roots())` collects the reachable set from the roots and its fragments are
added, in sorted order, to a fresh `apidoc.NewComponents()`; the config is
`NewConfig(title, version, WithRegistry(c), extra…)`. A zero `opts` means
`include.DefaultOptions`. Any emission failure is a wiring error and panics, as
does a graph node named like a library component (`Error`, `PagePagination`,
`CursorPagination`, `CursorPaginationTotal`): `adapters/huma: node "Error"
collides with the library component of the same name`.

### Include and Inputs

`a.Include(res)` is `IncludeParamWithLimits(res, a.Options().Limits)` plus the
`400 INVALID_INCLUDE` response — the API's own limits, so the enumerated
`x-include-paths` cannot describe a deeper tree than the planner accepts.

`a.Inputs(res)` appends one query parameter per `apidoc.InputParams(res,
limits, syntax)` entry — `include`, `sort` (when enabled), `page` or `cursor`,
`limit`, `where` (when enabled) — copying each fragment's schema plus `Style` /
`Explode` (the bracket `where` is a `deepObject`). It is **idempotent**: a
parameter of the same name already declared in the `query` location is left
alone, since two same-named parameters in one location is an invalid OpenAPI
document. It also declares the 400 codes the resolver can produce:
`INVALID_INCLUDE` and `INVALID_PAGINATION` always, `INVALID_SORT` when sort is
enabled, `INVALID_FILTER` when filter is enabled — merged into the single `400`
response by `Errors`.

> **`Inputs()` parameters are DOCUMENTATION ONLY.** huma validates a query
> parameter against the tags of the *input struct's own field* and nothing
> else, so a parameter that exists only in `Operation.Parameters` is never
> checked by huma. The enforcement is `Bound.List` → `include.ResolveInputs`,
> which reads the same `include.Inputs` this documents — so the two cannot
> drift, but they are two different mechanisms, and the 400s come from the
> resolver, not from huma's validator.

`Inputs()` also **prunes**: every query parameter huma derives from `ListQuery`
that `apidoc.InputParams` did not document (the off-mode `page` or `cursor`,
`sort` with sorting disabled, `where` with filtering disabled) is removed, so
the document never advertises an input the resolver will reject. The removal
cannot happen in the decorator — huma appends the struct-derived parameters
*after* the decorators run — so `Inputs()` records the names in the operation's
metadata under `wireleaf:drop-query-params` (`Metadata` is `yaml:"-"`, so it
never reaches the document) and the `OnAddOperation` hook `Attach` installed
prunes them off the operation huma files. A hook, not a step after `Register`,
because `h` may be a `humav2.Group`: a group rewrites `op.Path` through its
prefix modifier on the way into the document, so the path `Register` holds is
not the path the operation is filed under. The parameters a decorator declared
**before** `Inputs()` in the option list are exempt from both the documenting
and the pruning; one listed after it is invisible to `Inputs()`.

### The request middleware (request.go)

`Attach` installs one `UseMiddleware` middleware. huma runs middlewares *after*
routing, so `ctx.Operation()` and `ctx.Param()` are already known; it snapshots
the request once into an `*include.Request` and stores it on the Go context
under a package-private key:

| field | source |
| --- | --- |
| `Method` | `ctx.Method()` |
| `Path` | `ctx.URL().Path` — as requested |
| `Route` | `ctx.Operation().Path` — the matched template |
| `OperationID` | `ctx.Operation().OperationID` |
| `PathParams` | `ctx.Param(name)` for every `{name}` of the template |
| `Query` | `ctx.URL().Query()` |
| `Header` | every header, via `ctx.EachHeader` (canonical keys) |
| `RemoteAddr` | `ctx.RemoteAddr()` |

The binding layer reads it back and hangs it off `include.Ctx.Request`, where
`Ctx.Header` / `Ctx.PathParam` / `Ctx.QueryValue` reach it from Wire functions,
guards, enrich hooks and fetchers. It is **raw, untrusted client input** — a
header is whatever the client or the edge proxy sent; interpreted per-request
state (the authenticated viewer, the tenant) belongs in `Ctx.Env`. The context
key is private, so nothing outside this package can substitute a forged
snapshot; outside HTTP (tests, workers) there is simply no snapshot and the
`Ctx` accessors tolerate `nil`.

### Bound (bound.go)

`Bind` ties **one** graph node to **one** wire type `W` and returns the value a
handler closes over: the resource for the document decorators, the
`include.Hydrator` for the engine calls, and the mapping of engine errors onto
huma status errors.

```go
func Bind[W any](a *API, node string) *Bound[W]

func (b *Bound[W]) Resource() include.Resource
func (b *Bound[W]) Include() OpOpt   // a.Include(b.Resource())
func (b *Bound[W]) Inputs() OpOpt    // a.Inputs(b.Resource())

func (b *Bound[W]) Get(ctx context.Context, id, inc string) (Node[W], error)
func (b *Bound[W]) Hydrate(ctx context.Context, doc any, inc string) (Node[W], error)
func (b *Bound[W]) List(ctx context.Context, q ListQuery, fetch include.ListFetcher) (Page[W], error)
```

`Bind` is **wiring**, so every failure panics:

- an unknown node panics inside `a.Graph().Resource(node)` (`graph: unknown
  resource <name>`);
- a node whose declared wire type is not `W` panics with `wire type mismatch
  for node …: graph declares X, Bind asked for Y` — the check is
  `apidoc.DerefType(reflect.TypeOf(node.WireSample())) == reflect.TypeFor[W]()`.

On success it calls `RegisterNode[W](a.Components(), node)` **once** per the
pair `(node, Node[W])` — the node name is also the component name — and records
`Node[W]` in the API's bound set, which is what `Register`'s output-type guard
reads. Hence the order `New → Attach → every Bind → Register`.

The key is the **pair**, not `W` alone: a second `Bind` of `W` under a different
node name still reaches `RegisterNode`, where apidoc refuses the remap
(`node type … already registered as "X", cannot remap to "Y"`). Keying on `W`
would skip registration and silently document that node's payloads as a `$ref`
to the first component.

`Get`, `Hydrate` and `List` each build a fresh
`&include.Ctx{Context: ctx, Registry: a.Graph(), Request: requestOf(ctx)}`, so a
Wire function, guard or fetcher sees the request snapshot the middleware stored
(and tolerates its absence off the HTTP path).

**Error mapping.** An `*include.Error` (found with `errors.As`) becomes
`humav2.NewError(e.Status, e.Error())`: a bad `?include` / `?sort` / `?page` /
`?where` is the resolver's 400, a missing document is `NOT_FOUND` 404. Any other
error is returned **unchanged**, so an infrastructure fault stays a 500 and is
not dressed up as a client mistake.

**List parameters.** `ListQuery` is embedded anonymously in the operation's
input struct:

```go
type ListQuery struct {
    Include string `query:"include"`
    Sort    string `query:"sort"`
    Page    int    `query:"page"`
    Cursor  string `query:"cursor"`
    Limit   int    `query:"limit"`
    Where   string `query:"where"`
}
```

The tags carry **only the names**: the document comes from `Inputs()`, the
validation from `include.ResolveInputs` inside `List` (see the note above — they
share `include.Inputs`, but they are two mechanisms).

**Where comes from the API's filter syntax**, not from a per-call choice:

| `WithFilterSyntax` | source | parser |
| --- | --- | --- |
| `apidoc.FilterJSON` (default) | `q.Where` (empty → no filter) | `filters.ParseJSON` |
| `apidoc.FilterBracket` | `where[…]` keys of the request snapshot | `filters.ParseQuery` |

In bracket mode the filter comes from the `where[…]` keys, and a call with no
snapshot on the context fails with `adapters/huma: bracket filter needs Attach
…` — the keys can only be read off a real request. A **non-empty** `q.Where` in
that mode is the JSON spelling of a document that describes `where` as a
`deepObject`, so `?where={"title":{"eq":"x"}}` is a `400 INVALID_FILTER`, not a
silently unfiltered page. It cannot misfire: huma binds `ListQuery.Where` from a
literal `where=` key only, never from a `where[…]` one.

`List` then runs `ResolveInputs` and `Hydrator.Query(ctx, args, q.Include,
fetch)` and assembles the page:

```go
type Page[W any] struct {
    Data   []Node[W]             `json:"data"`
    Mode   include.PageMode      `json:"-"`
    Offset PagePagination        `json:"-"`
    Cursor CursorPaginationTotal `json:"-"`
}
```

Only the block of the resource's own mode is filled; the other stays zero.
Cursor mode fills `Cursor` (nullable `nextCursor` / `prevCursor` from the
fetcher's tokens, `hasNextPage` from `HasMore`, `hasPrevPage` from a non-empty
previous token, `limit` from the resolved limit, and `totalDocs` only when the
fetcher reported a total). Offset mode fills `Offset` with
`totalPages = ceil(total/limit)` and `hasPrevPage = page > 1`.

The pagination fields are `json:"-"` because the **application** owns its
envelope: a handler picks the block of its mode and returns its own
`{Data, Pagination}` body, which is what the envelope convention below derives
a document for.

> `Page[W]` is **not** usable as a huma response body — the envelope derivation
> refuses a `{data}` struct whose second field is not `json:"pagination"`, and
> the reflector then refuses the `Node[W]` it carries. Copy `Data` and the
> pagination block of the resource's mode into the application's own envelope
> struct.

## Operation layer (op.go)

```go
func Op(base humav2.Operation, opts ...OpOpt) humav2.Operation
func Errors(defs ...ErrorDef) OpOpt
func IncludeParam(res include.Resource) OpOpt
func IncludeParamWithLimits(res include.Resource, lim include.Limits) OpOpt
type ErrorDef struct{ Status int; Code, Message string }
func (d ErrorDef) Err(detail string) error
```

`Op` takes `base` **by value** and detaches the two reference fields before any
decorator runs: `Responses` is cloned, `Parameters` is capacity-clamped so an
append allocates. A shared base template therefore cannot leak one route's
errors or parameters into the next.

- **`Errors`** merges by status (sorted): codes sharing a status join into one
  description as `"CODE (HTTP n): message"` separated by `"; "`, and the body
  is a `$ref` to the `Error` component. Two `Errors` calls (or a base template
  plus an `Errors` call) contributing to the same status merge their
  **declarations** — kept in `Operation.Metadata`, never re-parsed from the
  rendered description, so a message containing `"; "` survives — instead of
  one overwriting the other; a duplicate def is not repeated. A pre-existing
  response at that status whose body is not the
  `Error` `$ref` is treated as the application's own and is replaced, not
  merged into. No last-writer-wins on distinct codes.
- **`ErrorDef.Err(detail)`** returns the runtime error the def documents: a
  huma status error with status `d.Status` and message `"CODE: message"`, plus
  `" (detail)"` when `detail` is non-empty. Declaring the def once and using it
  both in `Errors(...)` and in the handler keeps the document and the response
  from drifting apart.
- **`IncludeParam`** appends an optional `include` query parameter of type
  string whose valid paths are enumerated from the graph itself (depth-first
  walk over includable edges bounded by `include.DefaultLimits`; use
  `IncludeParamWithLimits` with the handler's own `Limits` when they differ,
  a zero value meaning the defaults) and carried
  under the `x-include-paths` extension — documented for generators, not an
  enum, since the value is a comma-separated path list with per-edge arguments.
  Idempotent: an operation already declaring an `include` query parameter is
  left alone. Panics if the parameter schema cannot be built.

Post-registration fixes (both **panic** on an unregistered method/path — a miss
is a caller typo, not a runtime condition):

```go
func ApplyRequestBodyDoc(oapi *humav2.OpenAPI, method, path string, bodyT reflect.Type)
func ApplyResponseHeaders(oapi *humav2.OpenAPI, method, path string, headers map[int][]ResponseHeader)
type ResponseHeader struct{ Name, Description string; Schema map[string]any; Required bool }
```

- `ApplyRequestBodyDoc` replaces the `application/octet-stream` request body
  huma derives from a `RawBody []byte` input with bodyT's JSON schema — from
  bodyT's `apidoc.RequestBodyProvider` when it has one, else via the bridge with
  a per-operation naming hint (`<OperationID>RequestBody`, or method+path).
  `Required` is always false: an absent body is the handler's concern.
- `ApplyResponseHeaders` attaches headers to already-emitted responses
  (copy-on-write — a shared `*Response` from a base template is shallow-copied,
  never written through). A status with no response yet gets a bare
  header-only one.

## Envelope convention and wire shapes (envelope.go, node.go)

A body struct qualifies as a success envelope when field 0 is `json:"data"`
typed as a registered node wrapper, a slice of one, or an
`apidoc.EnvelopeSchemaProvider`; then either it is the only field (`{data}`), or
field 1 is `json:"pagination"` typed `CursorPagination`/`CursorPaginationTotal`
(exactly two fields) or `PagePagination` (an *open* envelope: any further fields
are each documented and required, and nothing writes `additionalProperties`). An
`apidoc.BodySchemaProvider` short-circuits the whole convention. Anything else
reflects canonically.

The pagination doc schemas are hand-built and `number`-typed (not
`integer`/`int64`) to match the TypeScript client; `totalDocs` on
`CursorPaginationTotal` is optional. The Go structs exist only for
serialization.

`Node[W]` (`wfhuma.RegisterNode[W](c, "Component")`, `NodeOf[W](raw)`) carries
engine-hydrated bytes verbatim and implements `humav2.SchemaProvider` as a
`$ref` to W's component. It panics when reflected into a document whose registry
is not the wireleaf bridge, or when W was never registered — a missing
`RegisterNode` is a wiring bug, not something to auto-register silently.

## IR conversion (convert.go)

`toHuma` is a field-by-field copy with three real seams: a type array wider than
`["T","null"]` is an **error** (huma models one type plus a nullable flag);
`contentMediaType` rides through `Extensions`; `KindOpaque` fragments become a
zero schema with `Extensions` — served byte-identically but not validated by
huma. A bare `{"type":"null"}` gets `"enum":[null]` appended so huma's
validator, which has no null case, actually enforces it (semantically identical
in 2020-12). Property order is lost (huma sorts a map on marshal).

## BuildInto (build.go)

`BuildInto(oapi *humav2.OpenAPI, c *apidoc.Components) error` verifies `c`
first (unresolved `$ref` stops assembly), then: no-op when the doc's registry is
already the bridge over the *same* `Components`; an **error** when it bridges a
different set (two component sets in one document is a wiring mistake); against
a plain huma registry, components merge in sorted order via the `Extensions`
bridge, with the name-clash check run over every name before the first insert —
a rejected merge leaves the doc untouched. Gotcha: this fallback path routes
fragments through a Go map, losing keyword/property order; the bridge wiring
does not, which is one more reason it is the shipped path.

## Build-time panics vs request-time behavior

Panics (all at wiring/startup time): nil `Components`/`Reflector`, reflection or
registration failure, anonymous body structs, node wrappers reaching the
reflector, unregistered `Node[W]`, `BridgeOf`/`operationAt` on a wrong document,
library-component name conflicts. Errors (returned): `BuildInto`'s verify,
clash, and wiring checks; `toHuma` on unrepresentable type arrays (surfaced as a
panic by its wiring-time callers). At request time nothing new panics: schema
conversion is cached, and validation follows the served document (strict
casing, open objects).

## Versions

go.mod pins `github.com/danielgtaylor/huma/v2 v2.39.1` and Go 1.26, plus
`github.com/qrotux/wireleaf` and `…/reflector` pseudo-versions (replaced by the
sibling checkouts inside the repo). `apidoc/crosscheck` is test-only.
