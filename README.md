# wireleaf

Declare your resource graph once — nodes, their wire shapes, and the edges
between them — on a builder, `Compile()` it, and get back, from that single
declaration:

- **include/exclude responses** with batched child loading (`?include=author,books`),
- **OpenAPI 3.1 components** for every node, with edges stitched into the schemas,
- **include-aware response schemas** (`SchemaFor`), where the edges a request
  actually asked for become `required`,
- **`x-include-paths`** — the enumerated set of include paths an operation accepts,
- a **huma bridge** that makes the same component set huma's own schema registry.

No ORM, no query builder: you supply the fetch closures, the engine supplies
the plan, the batching, and the bytes.

> **Status: v0. The API is unstable** and may change without a deprecation
> path, as v0 semver allows. Pin a version.

## Install

```
go get github.com/qrotux/wireleaf
```

The core module has **zero dependencies**. The reflector and the huma adapter
are separate nested modules, so neither `jsonschema-go` nor huma reaches
consumers who do not import them:

```
go get github.com/qrotux/wireleaf/reflector
go get github.com/qrotux/wireleaf/adapters/huma
```

## Quick start

[`examples/basic/main.go`](examples/basic/main.go) is the runnable version of
everything below: declaration → request time → build time.

### 1. Declare the graph

Rows are what the database hands you; wires are what the client sees.

```go
b := graph.NewBuilder()

book := graph.Node[bookRow, BookWire](b, "Book").
	Slug("book").
	Wire(func(r bookRow, _ *include.Ctx) BookWire {
		return BookWire{ID: r.ID, Title: r.Title}
	}).
	PrimaryKey(func(r bookRow) string { return r.ID })
author := graph.Node[authorRow, AuthorWire](b, "Author").
	Slug("author").
	Wire(func(r authorRow, _ *include.Ctx) AuthorWire {
		return AuthorWire{ID: r.ID, Name: r.Name}
	}).
	PrimaryKey(func(r authorRow) string { return r.ID })

// An edge addresses its target by the target's WIRE TYPE, resolved in Compile:
// declaration order (and even declaration file) does not matter, cyclic graphs
// need no late binding, and a typo in the type is a Go compile error. The
// ForeignKey/Guard closures are typed by the node's own Row — a wrong Row does
// not build. Includable is deny-by-default: an edge is invisible to ?include=
// until it is declared includable.
book.Edge("author", graph.ToOne[AuthorWire]()).
	ForeignKey(func(r bookRow) string { return r.AuthorID }).
	Inverse("books").
	Includable()
author.Edge("books", graph.Reverse[BookWire]("authorId")).
	Inverse("author").
	Limit(10).
	Includable()

// The fetchers the engine calls when an include pulls a node in. FetchIDs is
// the forward batch (ids → rows); FetchParents is the reverse batch (one call
// per level, rows keyed by parent id).
graph.FetchIDs(b, author, func(_ *include.Ctx, ids []string) ([]authorRow, error) { … })
graph.FetchParents(b, book, func(_ *include.Ctx, parentIDs []string, q include.EdgeQuery) (map[string]graph.ParentRows[bookRow], error) { … })

b.Root(book)

g, err := b.Compile() // *graph.CompileError lists every finding at once
```

`Compile` validates every declaration together and returns the immutable
`*graph.Graph`, which is also the engine's registry. Wiring bugs fail at
start-up, not at request time.

### 2. Request time: hydrate

```go
inc, err := include.ParseInclude("author")
if err != nil {
	return err // a malformed include string
}
ctx := &include.Ctx{Registry: g} // the compiled graph IS the registry
raw, _, err := include.HydrateByID(g.Resource("Book"), "b1", inc, nil,
	func(_ *include.Ctx, id string) (any, error) {
		bk, ok := books[id]
		if !ok {
			return nil, nil // nil doc → NOT_FOUND (404)
		}
		return bk, nil
	},
	ctx, include.DefaultOptions)
if err != nil {
	return err // *include.Error: Code / Path / Status for the HTTP layer
}
```

```json
{"id":"b1","title":"The Hobbit","author":{"id":"a1","name":"J. R. R. Tolkien"}}
```

The facades are `HydrateByID`, `HydrateByQuery` (a root list) and
`HydrateEntity` (a document you already have). All three resolve the plan
**before** calling any fetch closure, so a bad include string costs no database
round-trip.

### 3. Build time: components from the same declaration

```go
c := apidoc.NewComponents()      // the one shared component set
refl := &reflector.Reflector{}   // the canonical reflector (its own module)

// Reachable walks the includable edges out of Book, so one root reaches Author.
frags, err := apidoc.EmitComponents(refl, g.Reachable(g.Resource("Book")))
if err != nil {
	return err
}
for name, s := range frags {
	c.Add(name, s) // duplicate names PANIC: two writers for one fact is a bug
}
if err := c.Verify(); err != nil { // every $ref must resolve
	return err
}

bookComponent, _ := c.Get("Book")
pretty, err := bookComponent.IR().MarshalJSON() // preserves property/keyword order
if err != nil {
	return err
}
fmt.Println(string(pretty))
```

```json
{"type":"object","properties":{"id":{"type":"string"},"title":{"type":"string"},"author":{"anyOf":[{"$ref":"#/components/schemas/Author"},{"type":"null"}]}},"required":["id","title"]}
```

`apidoc.IncludePaths(res, include.DefaultLimits)` enumerates every include path
a client may legally ask for; `apidoc.SchemaFor(refl, res, tree, limits)` returns the
response schema for one *specific* include tree, where the requested edges are
`required`.

## Edge kinds

Five kinds, each with the options `Compile` accepts on it. Anything outside the
matrix (a `Limit` on a to-one, `ForeignKeys` on a reverse edge, `Bare` + `Limit`
together) is a compile finding, not a runtime surprise.

| Kind | How it is fetched | Response shape |
| --- | --- | --- |
| `graph.ToOne[TargetWire]()` | the parent's scalar FK (`.ForeignKey`), batched through the target's `FetchIDs` | the object, or `null` — a bare, non-null `$ref` under `Required()` |
| `graph.ToMany[TargetWire]()` | the parent's FK **array** (`.ForeignKeys`), batched through `FetchIDs` | `{items, hasMore}` (flat array under `Bare()`) |
| `graph.Reverse[TargetWire]("backref")` | the child's back-FK, one `FetchParents` call per level | `{items, hasMore}` (flat array under `Bare()`) |
| `graph.InArray[TargetWire](arrayPath, subField)` | `.ForeignKeys` harvests one id **per element** of the parent's `arrayPath`, in array order; loaded through `FetchIDs` | each element of `arrayPath` gains `subField` |
| `graph.Computed(schema)` | not fetched — application code produces the value | whatever `schema` documents |

```go
// Columns come from the TARGET wire struct's `col:"sql_name[,sort][,filter]"`
// tags — `sort` makes the field a legal sort key (so graph.Sort below has
// something to name), `filter` a legal filter column; the legacy
// `sortCol:"x"` is `col:"x,sort"`:
//
//	type BookWire struct {
//		CreatedAt time.Time `json:"createdAt" col:"created_at,sort,filter"`
//	}

book.Edge("author", graph.ToOne[AuthorWire]()).
	ForeignKey(func(r bookRow) string { return r.AuthorID }). // "" → null
	Required().                                               // DOC-only: bare $ref, in `required`
	Includable()
book.Edge("tags", graph.ToMany[TagWire]()).
	ForeignKeys(func(r bookRow) []string { return r.TagIDs }).
	Bare(). // flat array, no envelope, fetch-all (q.Limit == 0)
	Includable()
author.Edge("books", graph.Reverse[BookWire]("authorId")).
	Limit(10).
	Sort("-createdAt").
	Args(graph.Arg("page", validatePage)).
	Guard(func(c *include.Ctx, r authorRow) bool { return r.Public }).
	Includable()
book.Edge("lineItems", graph.InArray[ProductWire]("items", "product")).
	ForeignKeys(func(r bookRow) []string { return r.ItemProductIDs }). // positional, 1:1 with "items"
	Includable()
book.Edge("participants", graph.Computed(schema)).Includable()
```

- **`graph.Bare()`** (reverse / to-many only) drops the `{items, hasMore}`
  envelope for a flat array *and* fetches everything: it compiles to `Limit 0`,
  which is exactly the "limit 0 → fetch all, never report `HasMore`" branch of
  the fetcher contract below. Safe only for bounded relations; `Bare` and
  `Limit` together is a compile error.
- **`.Required()`** is legal on a to-one edge only: the emitter renders the
  edge as a bare `$ref` (no `null` arm) and lists the key in the component's
  `required` set. What the ENGINE does when the target is missing anyway (an
  empty or orphaned FK) is `include.MissingRequiredPolicy` — silent `null` by
  default, a failed request under `MissingRequiredError`; a dangling FK on ANY
  forward edge answers to `include.MissingForeignPolicy` the same way. Both
  have engine-wide defaults on `include.Ctx.Policies` and per-edge overrides
  via `.Policies(...)`.
  **`Required()` implies default-included:** because the engine only
  materializes edges the include tree asked for, a required key must be in the
  node's `Defaults()` — and since that is the only legal configuration,
  `Compile` constructs it, appending required keys after the explicit defaults
  in edge-declaration order. `Required()` + `Guard()` is rejected too (a guard-false parent
  would null a required key), and an `?exclude=` of a required key is never
  honoured (silently under `ExcludeRequiredTolerant`, as `INVALID_INCLUDE`
  under `ExcludeRequiredStrict`).
- **`.Guard(fn)`** is a cheap, pure, no-DB visibility check on the parent
  row; `false` → the edge value is `null` and no fetch happens. Legal on every
  kind except `Computed` (and not on `Required()` edges, see above).
- **`.Sort(key)` is reverse-only** — not legal on `ToOne`, `ToMany` or
  `InArray`. **`.Args(...)`** is legal on `Reverse` and `Computed` edges
  only.
- **`Envelope`** wraps edge values. Declare it once on the builder
  (`graph.NewBuilder().Envelope(graph.EnvelopeData)`), override it per target
  node (`graph.Node[...](b, "Tag").Envelope(graph.EnvelopePlain)`) or per edge
  (`.Envelope(include.Envelope{Key: "values", Pagination: "meta"})`); the
  closest declaration wins, `Compile` resolves it into `include.Edge.Envelope`.
  `graph.EnvelopeData` is `{Key: "data", Pagination: "pagination"}`. Under it a
  to-one is `{"data": obj}` (`{"data": null}` when absent), a list is
  `{"data": [{"data": obj}, …], "pagination": {"hasNextPage": bool, "nextCursor"?: string}}`
  (`Bare()` or an empty `Pagination` drops the block), and an in-array
  `subField` holds `{"data": obj}`. List elements are wrapped too, so a node
  has one shape wherever it appears. The root envelope stays the handler's
  job. Illegal on `Computed` edges; `Pagination` without `Key`, `Key ==
  Pagination`, and names needing JSON escaping are compile findings.

```json
{
  "data": {
    "id": "b1", "title": "The Hobbit",
    "author": {"data": {"id": "a1", "name": "J. R. R. Tolkien",
      "books": {"data": [{"data": {"id": "b1", "title": "The Hobbit"}}],
                "pagination": {"hasNextPage": true, "nextCursor": "opaque"}}}},
    "tags": [{"id": "t1", "name": "fantasy"}]
  },
  "pagination": {"page": 1, "totalPages": 3, "totalDocs": 41, "hasNextPage": true, "hasPrevPage": false}
}
```
(outer `data`/`pagination` written by the handler; `Tag` declared `EnvelopePlain` + `Bare()`)
- **In-array edges** carry no `{items, hasMore}` frame and take no `EdgeQuery`: `Limit`, `Bare`
  and `Sort` are not legal on them.

## Declarative nodes and relations

The chained builder has a struct-literal twin, so a node can be declared next
to its wire type — one file (or one domain package) per node — and the links
between nodes can be declared once, in both directions, wherever the handles
meet. Both are sugar over the chain: `Compile` sees the same declarations.

```go
// domain/author/author.go — knows nothing about books
var Node = graph.Spec[Row, Wire]{
	Name:       "Author",
	Wire:       func(r Row, _ *include.Ctx) Wire { return Wire{ID: r.ID, Name: r.Name} },
	PrimaryKey: func(r Row) string { return r.ID },
	FetchIDs:   fetchByIDs,
}

// api/graph.go — the one package that imports every domain
b := graph.NewBuilder()
bk := graph.Add(b, book.Node)   // the same *NodeHandle graph.Node returns
au := graph.Add(b, author.Node)

graph.OneToMany(au, bk, "books", "author").                   // targets inferred from the handles
	ForeignKey("authorId", func(r book.Row) string { return r.AuthorID }). // name + reader, typed by book.Row
	Includable().                                              // both directions
	Limit(10).                                                 // the many side
	FetchParents(book.FetchByAuthor)                           // bound next to the link it serves

b.Root(bk)
g, err := b.Compile()
```

`Spec.Edges` (a `[]graph.EdgeSpec[Row]`) lets a node declare its own edges in
the same literal when the whole graph lives in one package; `graph.ManyToMany`
is the id-list counterpart of `OneToMany`. Domain packages never import each
other, so cyclic graphs split across packages need no tricks. See
[`examples/declarative`](examples/declarative) (`node_spec.go` for `Spec`,
`graph_to_many.go` for the relations), [`examples/modular`](examples/modular)
for the per-package split, and [docs/graph.md](docs/graph.md).

## huma integration

`adapters/huma` is a separate module and the only huma-facing package. It is
imported under an alias, since it shares the upstream package name.

```go
import (
	"net/http"

	humav2 "github.com/danielgtaylor/huma/v2"
	"github.com/danielgtaylor/huma/v2/adapters/humago"

	wfhuma "github.com/qrotux/wireleaf/adapters/huma"
	"github.com/qrotux/wireleaf/include"
)

// New emits the graph's components and builds huma's config over them — the
// document's schema registry IS the bridge, so huma reflects request and
// response bodies through wireleaf's reflector into the same set.
a := wfhuma.New(g, include.DefaultOptions, "Books", "1.0.0")
api := humago.New(mux, a.Config())
a.Attach(api)                              // fills include.Request (Ctx.Header)
book := wfhuma.Bind[BookWire](a, "Book")   // node → wire type → Node[W] component

wfhuma.Register(a, humav2.Operation{
	OperationID: "get-book", Method: http.MethodGet, Path: "/books/{id}",
}, func(ctx context.Context, in *getBookInput) (*getBookOutput, error) {
	b, err := book.Get(ctx, in.ID, in.Include) // hydrated Node[BookWire]
	if err != nil {
		return nil, err
	}
	return &getBookOutput{Body: BookEnvelope{Data: b}}, nil
}, book.Include(), a.Errors(errBookNotFound)) // ?include= + x-include-paths + 404

wfhuma.Register(a, humav2.Operation{
	OperationID: "list-books", Method: http.MethodGet, Path: "/books",
}, func(ctx context.Context, in *listBooksInput) (*listBooksOutput, error) {
	page, err := book.List(ctx, in.ListQuery, listBooks(in.Q))
	if err != nil {
		return nil, err
	}
	return &listBooksOutput{Body: BookPageEnvelope{Data: page.Data, Pagination: page.Offset}}, nil
}, book.Inputs()) // ?sort/?page/?limit/?where from the node's graph.Inputs
```

`listBooksInput` embeds `wfhuma.ListQuery`; `book.Inputs()` documents the list
parameters from the node's declaration and `book.List` enforces the same one,
so the document and the 400s cannot drift. Field documentation uses huma's
`doc:"…"` tag on wire types, input structs and bodies alike — the reflector
reads it as the equivalent of `description:"…"`, so one dialect annotates the
whole surface.

`NewConfig` builds huma's config directly rather than from `DefaultConfig`: the
`SchemaLinkTransformer` (which injects a `$schema` field into every body) and
the `/schemas` route it points at are deliberately absent, because the emitted
document is the contract a client is generated from. It also installs the
library components (`Error`, the pagination blocks) that `Op`'s decorators
`$ref`.

`Op` takes its base operation **by value** and detaches `Responses` and
`Parameters`, so a shared base template cannot leak one route's errors into the
next. Other pieces: `wfhuma.RegisterNode[W]` for a `Node[W]` body wrapper,
`BuildInto` to merge a component set into a live `humav2.OpenAPI`,
`ApplyRequestBodyDoc` / `ApplyResponseHeaders` for post-registration fixes.

## Module layout

| Module | Path | Depends on | What it holds |
| --- | --- | --- | --- |
| core | `.` | **nothing** | `include` (the engine) and `include/filters` (the two shipped filter syntaxes), `graph` (the typed builder, `Compile`), `loader` (the request-scoped batch loader), `apidoc` (the doc core and IR), `jsonsplice`, plus the `graph/loadertest` and `apidoc/reflectortest` harnesses. |
| reflector | `reflector` | `jsonschema-go` | `*reflector.Reflector` — the canonical `apidoc.Reflector`: Go struct → wireleaf IR, with the nullability policy, naming, and constraint mapping the whole stack agrees on. |
| crosscheck | `apidoc/crosscheck` | `santhosh-tekuri/jsonschema/v6` | Test-only: compiles an emitted component set with a real draft-2020-12 validator and validates instances against it. |
| huma adapter | `adapters/huma` | huma, reflector | The `API` facade (`New`, `Attach`, `Bind` → `Bound[W]` with `Get`/`Hydrate`/`List`/`ListQuery`/`Page[W]`, `Register`), `NewConfig` / `WithRegistry`, the `Registry` bridge, `Op` and its decorators, the envelope and `Node[W]` types, `BuildInto`. |
| examples | `examples` | core, reflector, huma adapter | `basic`, `policies`, `costlimit`, `filter`, `huma`, `declarative`, `modular` — runnable programs, `basic` is the quick-start. |

An application on another documentation stack implements `apidoc.Reflector`
itself and imports neither `reflector` nor `adapters/huma`.

Per-package reference documentation — contracts, signatures, and gotchas beyond
this overview — lives in [`docs/`](docs/README.md).

## Key contracts

**Fetchers.** Two names, one fetcher: `graph.FetchParents` (and `FetchIDs`) is
the *binder* you call on the builder, while `graph.FetchByParents[Row]` — erased
to `include.FetchByParents` inside the engine — is the *contract type* of the
closure you hand it. A reverse fetcher can also be bound to one edge instead
of the whole target node (`graph.FetchEdge`, or a relation's `FetchParents`);
the engine prefers the edge's fetcher and falls back to the node's. The rules
below are the closure's.

`FetchByParents` is the *probe* contract: given `q.Limit == n`,
select `n+1` rows, return at most `n`, and set `HasMore` iff the extra row
existed — so a parent with `T` rows returns exactly `min(n, T)` rows and
`HasMore == (T > n)`. `q.Limit == 0` is a **bare** edge: fetch everything and
never report `HasMore`. Return only the parent keys you were asked for; a
parent with no children is an absent key or empty rows, never an error. Be safe
for concurrent use. `graph.PerParent` adapts a one-parent-at-a-time closure to
the batch signature.

**`loader.Loader[K, V]`** is the request-scoped batch loader for side data that
is not part of the graph (permissions, counters, flags). Two phases: `Warm`
fetches — batching every still-unknown key into one call and single-flighting
concurrent warms — and `Get` reads, never fetching. Cached values live on the
`*include.Ctx` of one request and die with it.

Build one with **`loader.New`** — a hand-assembled `Loader{}` has a nil
fetch and every `Warm` fails loudly rather than negative-caching every key. The
loader is a package-level value; it holds no data of its own.

```go
var perms = loader.New(func(c *include.Ctx, ids []string) (map[string]Perm, error) { … })

// in a MapFn / EnrichFn:
if err := perms.Warm(c, ids...); err != nil { … }
p, ok := perms.Get(c, id)
```

**Computed edges** (`graph.Computed(schema)`) have no target node: the value is
produced by application code and documented by the schema you hand the edge.
`jsonsplice` is the sanctioned tool for writing that value into the
materialized document — an order-preserving, string-aware byte splice over a
JSON object's top-level members, so key spellings, escapes and number
formatting survive untouched.

**List inputs** are enforced by `include.ResolveInputs`, never by the
framework's parameter schema. A node's `graph.Inputs` declaration compiles into
one `include.Inputs` value; `apidoc.InputParams` renders it as documentation
and `ResolveInputs` judges the request against the same value, so an
`?sort=`/`?limit=`/`?page=`/`?where=` the document does not describe is a
`400`, whatever the adapter's own query binding accepted.

**Harnesses.** `graph/loadertest` turns the fetcher contract above into
subtests you add to your own suite (`RunFetchByParents`, `RunFetchIDs`; run
them with `-race`). `apidoc/reflectortest` does the same for a custom
`apidoc.Reflector` (`reflectortest.Run(t, r)`). `apidoc/crosscheck` compiles
your components with an independent validator so the document is checked
against a real implementation, not against wireleaf's own reading of the spec.

## Policies

Everything policy-shaped is an explicit option, and the zero value is the
strict, safe choice.

**`Ctx.Marshal`** — how a wire value becomes bytes. `nil` means
`include.MarshalNoEscape`: `JSON.stringify`-parity bytes with raw `&`, `<`,
`>`. Set `Ctx.Marshal = include.MarshalStd` to opt back into `encoding/json`'s
HTML escaping.

**`Options.SortPolicy`** — an unknown client `:sort()` key on an edge whose
target wire struct declares sortable columns (`Compile` derives the whitelist
from the `sort` option of `col` struct tags, or the legacy `sortCol` tag (an
empty `sortCol:""` is now a compile finding, and a comma inside its value is a
finding too — the legacy tag grants `sort` and nothing else; see
[`docs/graph.md`](docs/graph.md)) — tag presence *is*
membership, deny-by-default):
`SortStrict` (default) fails the plan with `INVALID_INCLUDE`; `SortFallback`
falls back to the edge's own `graph.Sort(key)` default. The client string never
reaches SQL: it is only ever a lookup key for the tag-declared SQL-side value.
An edge whose target declares **no** sortable columns (`SortCols == nil`)
declares no sort at all, so there `:sort()` is an *undeclared argument* judged
by `ArgPolicy`, not by `SortPolicy`.

**`Options.ArgPolicy`** — a client `:name(...)` argument the edge never declared
(via `graph.Args`/`graph.Arg`, beyond the built-in `limit`): `ArgsStrict`
(default) fails the plan; `ArgsTolerant` passes it through to `PlanNode.Args`.
An `EdgeArg` may carry `Validate func(raw any) error`; a rejection is
`INVALID_INCLUDE` at `path:arg`.

The built-in `limit` is parsed and validated like any declared argument and
lands in `PlanNode.Args` for the application to read — the engine itself never
applies it. The per-edge top-N the engine passes to a fetcher is the edge's own
`graph.Limit(n)`, which a client cannot change. The default of 20 is stamped
only on to-many edges that carry the `{items, hasMore}` frame (`Reverse`,
`ToMany`, non-bare); bare and in-array
edges compile to `Limit 0`.

**`Options.Limits`** — nested loading is bounded at four levels; the full
guide is [`docs/include.md` → Limits](docs/include.md#limits-bounding-nested-loading).

1. **Per edge, at declaration.** `Limit(n)` is the per-parent ceiling of an
   enveloped to-many edge (default 20) and the value a client `:limit()` is
   clamped to. `Bare()` fetches all rows (no ceiling); such an edge carries
   `EstimatedRows(n)` for the cost model (default 100), and `Compile` requires
   it when the edge sits on a node reached through a to-many edge.
2. **Per request, `Options.Limits`.** `DefaultLimits` is `MaxDepth: 4,
   MaxNodes: 50, MaxCost: 5000, MaxRows: 50000`; zero `MaxCost`/`MaxRows`
   mean the defaults. `MaxDepth`/`MaxNodes` count client edges. `MaxCost`
   caps a static row estimate for one root document — per-edge limits
   multiplied down every path and summed over all nodes, defaults included
   (`author,reviews.author` costs 41; `kids.kids.kids` costs 8 421 and is
   refused). `MaxRows` caps the rows actually materialized by one hydrate
   call, checked per level and up front in `HydrateByQuery` as
   `Cost × page size`.
3. **Per request, from the client.** `?include=reviews:limit(5)` lowers a
   limit; it is clamped to the edge ceiling and can never raise it.
4. **Read back.** `plan.Cost` is the estimate and `ctx.Rows()` the actual
   count — the two inputs for a cost-based rate limiter in the application;
   [`examples/costlimit/main.go`](examples/costlimit/main.go) is a reference
   reserve-then-settle bucket built on them.

Not bounded by the library: the root page size (`QueryArgs.Limit`), and the
rows a `Bare()` fetcher really returns beyond its `EstimatedRows`.

`include.DefaultOptions` is `{Limits: DefaultLimits}`: strict sort, strict args.

## Filters

The core carries a filter **model** and the two spellings that parse into it;
generating the SQL stays the application's job. A wire field with
`col:"…,filter"` is a filterable column, an edge with `.Filterable()` may be
traversed — to-one as a join, reverse / to-many with a quantifier per hop
(`any`, `all`, `none`) that the adapter renders as `EXISTS` / `NOT EXISTS`
(deny-by-default, independent of `Includable()`, never with `Guard()`).

`filters.ParseJSON` reads a JSON `where` node and `filters.ParseQuery` the
`?where[field][op]=` bracket query string — the `include/filters` subpackage,
where syntax lives apart from judgement; both produce an `include.Filter` tree
over client-side names. `include.ResolveFilter`
then checks that tree against the compiled graph and returns SQL-side names
only — so the parsers never decide what is filterable, and a hand-written
parser can still feed `ResolveFilter` the same tree.
`include.FilterOpsFor` reports the operators legal for a column type, which is
what `apidoc`'s `FilterSyntax` documentation is generated from.

Rendering the resolved tree as SQL (joins, `EXISTS`) is the application
adapter's job; the resolved filter reaches its root fetcher through
`include.QueryArgs.Where`.
[`examples/filter/main.go`](examples/filter/main.go) is the reference for the
piece an application owns — a SQL renderer over `ResolveFilter`, fed by
`filters.ParseJSON` — and [`examples/costlimit`](examples/costlimit/main.go)
shows `include.FilterSubqueries` in a cost bucket.
See [`docs/include.md` → Filters](docs/include.md#filters).

## Errors

Planning failures are `*include.Error{Code, Path, Status}` — a structured value
the HTTP layer maps onto its own error type. `Status` defaults to 400.

| Code | Status | Meaning |
| --- | --- | --- |
| `INVALID_INCLUDE` | 400 | An include path is unknown, not marked `Includable`, or carries a rejected argument. `Path` points at the offending segment (`"a.b"`, or `"a.b:arg"`). |
| `INCLUDE_TOO_DEEP` | 400 | The client tree exceeds `Limits.MaxDepth` or `Limits.MaxNodes`. |
| `INCLUDE_TOO_EXPENSIVE` | 400 | The plan's static row estimate exceeds `Limits.MaxCost`. Lower `:limit()` values or drop a nested collection. |
| `INCLUDE_BUDGET_EXCEEDED` | 400 | Rows materialized (or `Cost × page size` in `HydrateByQuery`) exceed `Limits.MaxRows`. |
| `NOT_FOUND` | 404 | `HydrateByID`'s fetch closure returned a nil doc. |
| `INVALID_FILTER` | 400 | `ResolveFilter`: unknown/non-filterable edge or column, root without columns, empty group, a to-many hop without a quantifier, a quantifier on a to-one hop, an unknown quantifier, unknown operator or operator illegal for the column type (`Path` is `"a.b.field"`, the path up to the bad edge, `"a.b.field:op"`, or `"a.b:quant"`). |
| `FILTER_TOO_DEEP` | 400 | `Limits.MaxFilterDepth`, `Limits.MaxFilterMany` **or** `Limits.MaxFilterNodes` exceeded. |
| `FILTER_TOO_EXPENSIVE` | 400 | `Limits.MaxFilterSubqueries` exceeded: the filter tree's to-many hops, summed over every condition, are more correlated subqueries than allowed. Drop conditions that cross to-many edges. |

## Boundaries

Known, deliberate limits — read these before porting an existing huma service.

- **Named body types only.** The bridge reflects a request/response body
  through the canonical reflector, which names a component after its Go type. A
  root **anonymous struct** body cannot be reflected into a component and
  panics at registration; give the body a named type.
- **Strict casing is process-global.** `NewConfig` sets
  `huma.ValidateStrictCasing = true` once, so property names are matched
  byte-for-byte, as JSON Schema says and as the emitted document claims. huma
  ships case-*insensitive* matching, under which the runtime and the document
  disagree. The flag is a huma package variable, so this affects **every** huma
  API in the process, wireleaf-backed or not.
- **`additionalProperties` is not emitted.** The canonical reflector
  deliberately writes no `"additionalProperties": false`, while stock huma does
  for structs. A body validated against wireleaf's components therefore accepts
  unknown properties where stock huma would have rejected them. That is canon,
  not an oversight — tighten a specific object with the `apidoc` DSL if you
  need it closed.
- **Config knobs that do not reach the bridge.** huma's
  `AllowAdditionalPropertiesByDefault` and `FieldsOptionalByDefault` are applied
  by huma only to its own `mapRegistry` and are inert here;
  additional-properties and required-ness are the reflector's verdict.
  `NewConfig` does not accept them.
- **Non-struct top-level bodies** (`[]T` and friends) are generated by huma's
  own `SchemaFromType` recursing back through the bridge, so they follow huma's
  nullability rules at the top level, not wireleaf's.
- **Components not bound to a struct type are served opaque — and unvalidated.**
  A component whose Go wire type is unknown or is not a struct (a map alias, a
  hand-assembled fragment, some `RegisterType` bindings) is handed to huma as
  one opaque `Extensions` fragment with no `type`, because huma's response
  machinery would otherwise panic building a wrapper struct from it. The
  *document* is byte-identical either way, but huma performs **no request-body
  validation** against such a component — silently. Bind a struct type if you
  need the body validated.
- **Null arms are documented as `{"type":"null","enum":[null]}`.** In
  huma-served schemas the null arm of a nullable reference carries the
  redundant-looking `"enum":[null]`. It is an equivalent form — the set of
  admitted values is unchanged — and it is there because huma's validator has no
  `"null"` type case: without the enum the arm would match *any* value and every
  nullable reference in the document would go unenforced at runtime.

## Development

The five modules are independent (no `go.work`), so each is built from its own
directory or with `go -C`. `scripts/check.sh` is the one-shot gate CI runs:
gofmt, then `go vet` and the tests of every module. Extra arguments are passed
to `go test` (`bash scripts/check.sh -race`).

```
bash scripts/check.sh
go test -C adapters/huma ./... -count=1
golangci-lint run ./...          # per module: cd adapters/huma && golangci-lint run ./...
go run -C examples ./basic
```

`scripts/go.sh` runs the same `go` inside a throwaway `golang` container for a
host without a Go toolchain (`bash scripts/go.sh -C reflector test ./...`).

Inside the repo every module builds against the sibling checkouts via `replace`
directives. External consumers resolve the versions pinned in each `go.mod`,
so the nested modules are tagged alongside the core: `vX.Y.Z`, `reflector/vX.Y.Z`,
`apidoc/crosscheck/vX.Y.Z`, `adapters/huma/vX.Y.Z`.

## License

MIT — see [LICENSE](LICENSE).
