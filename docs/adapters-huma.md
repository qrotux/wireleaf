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

The bridge has six functional layers:

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

convert.go is the one IR → `humav2.Schema` converter every served schema goes
through; build.go's `BuildInto` merges a component set into a huma document
built *without* the bridge.

## Setup

[`examples/huma/main.go`](../examples/huma/main.go) is the runnable version of
this section: graph → components → `NewConfig` → two operations (`GET
/books/{id}`, `GET /books` with a page envelope) served in-process, plus the
document's `x-include-paths`.

```go
c := apidoc.NewComponents()
// register graph-derived components and node wrappers into c:
wfhuma.RegisterNode[BookWire](c, "Book")

cfg := wfhuma.NewConfig("Books", "1.0.0", wfhuma.WithRegistry(c))
api := humav2.NewAPI(cfg, adapter)

humav2.Register(api, wfhuma.Op(humav2.Operation{
    OperationID: "get-book",
    Method:      http.MethodGet,
    Path:        "/books/{id}",
},
    wfhuma.IncludeParam(g.Resource("Book")),
    wfhuma.Errors(wfhuma.ErrorDef{Status: 404, Code: "BOOK_NOT_FOUND", Message: "no such book"}),
), handler)
```

Without `WithRegistry`, `NewConfig` bridges over a fresh
`apidoc.NewComponents()` — only useful for a document with no graph-derived
components.

## Config (config.go)

```go
func NewConfig(title, version string, opts ...ConfigOpt) humav2.Config
func WithRegistry(c *apidoc.Components) ConfigOpt
func BridgeOf(oapi *humav2.OpenAPI) *Registry   // panics if not NewConfig-built
```

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

## API (api.go, request.go)

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
- `Register` walks the output type `O` (through pointers, slices, arrays, maps
  and struct fields) and panics on a `Node[W]` wrapper no `Bind` registered.
  Without the guard the same wiring bug surfaces later, deep inside huma's
  reflection, as `Node[W].Schema` failing to find W's component.

`New` does the emission itself: `apidoc.EmitComponents(&reflector.Reflector{},
g.Roots())` collects the reachable set from the roots and its fragments are
added, in sorted order, to a fresh `apidoc.NewComponents()`; the config is
`NewConfig(title, version, WithRegistry(c), extra…)`. A zero `opts` means
`include.DefaultOptions`. Any emission failure is a wiring error and panics.

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
  descriptions instead of one overwriting the other; a duplicate def is not
  repeated. A pre-existing response at that status whose body is not the
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
