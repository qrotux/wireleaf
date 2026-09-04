# apidoc — the code-first OpenAPI 3.1 document layer

## Overview

`apidoc` turns the graph declared with `graph`/`include` into OpenAPI 3.1
components and include-aware response schemas. Wire structs own the field
inventory; enumerated `Schema` deltas own the representation facts struct tags
cannot express; the emitted document is a build artifact, never hand-edited.
The package imports no reflection library: struct rendering goes through the
`Reflector` interface, and the canonical implementation lives in the separate
`reflector` module, keeping the core zero-dependency.

Relation to its siblings: `graph.Compile` produces the `include.Resource`
values this package walks; `include` supplies the edge model (`include.Edge`,
`include.IncludeTree`, `include.Limits`) that emission, `SchemaFor` and
`IncludePaths` consume; `reflector` implements `apidoc.Reflector`.

## Core concepts

### The IR

Every schema inside the doc layer is a typed `*IRNode`, never a
`map[string]any`. An `IRNode` has a `Kind` discriminator:

| Kind | Encodes | Required fields |
| --- | --- | --- |
| `KindObject` | `{type:"object", properties, required}` | `Props` (ordered `[]Prop`; non-nil, empty allowed) |
| `KindRef` | `{"$ref": …}` | `Ref` — the bare component name; `RefPrefix` (`#/components/schemas/`) is applied only at serialization |
| `KindArray` | `{type:"array", items}` | `Items` |
| `KindCombinator` | exactly one of `anyOf`/`oneOf`/`allOf` | one non-empty arm list |
| `KindScalar` | `{type:[…]}` | `Types` |
| `KindEnum` | `{type:[…], enum:[…]}` | `Types` and `Enum` |
| `KindOpaque` | a raw JSON fragment held verbatim | `Opaque` bytes and **nothing else** |

Beyond the structural fields, `IRNode` carries the typed
validation/annotation set (`Minimum`…`MaxProperties`, `Pattern`, `Format`,
`Description`, `Const`, `Default`, `Examples`, `DependentRequired`,
`ReadOnly`/`WriteOnly`/`Deprecated`, …) and an `Extensions map[string]any` for
`x-*` keys only. `required` has no standalone field: it is derived from the
`Prop.Required` flags, in `Props` order, at serialization.

Invariants philosophy: invalid IR fails **early and loudly**. Kind↔field
invariants (`checkInvariants`) are enforced shallowly at fragment ingress and
after every `Schema.Set`, and recursively over the whole tree at component
registration. Declaration-time API (`RawFragment`, the `Schema` deltas,
`Components.Add`) **panics** on a violation; conversion-pipeline API
(`OpaqueFragment`, `RegisterReflected`, emission, `SchemaFor`, `Verify`)
returns **errors**.

```go
func (n *IRNode) SetExtension(key string, v any) error
func (n *IRNode) MarshalJSON() ([]byte, error)
```

`SetExtension` is the one gate every `Extensions` writer goes through: it
refuses standard keywords and non-`x-` keys, and re-collects `$ref`s hiding
inside extension values so `Verify` sees them. `Nullable(n *IRNode) *IRNode`
(exported for external `Reflector` implementations) returns the canonical
nullable form of a node — see "Nullability" below.

### Opaque nodes and byte-exactness

An opaque node wraps a fragment the IR does not model. Its bytes are validated
as JSON, scanned once for `$ref` occurrences (which still participate in
`Verify`), and thereafter held **byte-exact**: `MarshalJSON` returns a copy of
the original bytes, nothing inside them is reshaped, inlined, or re-escaped.
The flip side: an opaque node may carry no other fields, cannot be addressed by
`Set`, is never inlined as an auxiliary, and `Describe` on an opaque property
panics (put the description inside the fragment).

Byte-exactness has a caller obligation: `encoding/json` re-escapes `<`, `>`,
`&` inside a nested `json.RawMessage`, so the final document emitter must use
`json.Encoder` with `SetEscapeHTML(false)` (this applies to `Merge` output and
to feeding an `*IRNode` to plain `json.Marshal`).

### Components

`Components` is a passed-around registry value — there is no package global —
holding named IR fragments plus the type indexes the huma bridge needs.

### Nullability of wire fields

`Verdict` / `NullabilityPolicy` / `DefaultNullability(f reflect.StructField) Verdict`
is the one field-nullability policy shared by `graph.Compile` and the
reflector: pointer without `omitempty`/`omitzero` → `VerdictNull` (present, may
be null); `omitempty`/`omitzero` → `VerdictOptional` (may be absent, never
null); else `VerdictPlain`. Callers must filter `json:"-"` and unexported
fields first. Known approximation: `omitempty` on a non-omittable kind (e.g.
`time.Time`) is documented as optional though the encoder always emits it.

## Schema construction

`Schema` is a fluent wrapper over one `*IRNode`. **Delta methods mutate the
underlying node and return the receiver** — the convention is single-owner per
node; do not share a `Schema` between components.

```go
func (s Schema) IR() *IRNode
func (s Schema) Map() map[string]any
```

`IR` exposes the live node. `Map` returns a **detached copy** via a JSON round
trip: writes to it are lost, every number comes back as `float64`, and key
order is lost — use `IR().MarshalJSON()` when order or exact types matter.

Constructors:

| Constructor | Contract |
| --- | --- |
| `RefTo(component string) Schema` | bare `$ref` to a named component. |
| `AllOf(parts ...Schema) Schema`, `OneOf(...)`, `AnyOf(...)` | combinator over the parts' nodes, composed **by reference**; an empty part panics. |
| `AnyOfNull(inner Schema) Schema` | `anyOf[inner, {"type":"null"}]`; idempotent when inner is already an anyOf with a null arm. |
| `RawFragment(m map[string]any) Schema` | hand-built fragment → typed IR, **forgiving** mode; panics on an unconvertible fragment. Properties are ordered by **sorted key** (a Go map has no order). |
| `OpaqueFragment(raw json.RawMessage) (Schema, error)` | wraps bytes verbatim as one opaque node; returns an error (its callers are pipelines, not declarations). |

`RawFragment` conversion rule: a fragment made only of keywords the IR models
parses into a typed node; any standard-but-untyped keyword (`if`,
`patternProperties`, `$defs`, …), unknown non-`x-` key, or keyword combination
the kind dispatch cannot keep whole (`{"$ref":…, "properties":…}`,
`{"anyOf":…, "type":…}`) makes the **whole** fragment one opaque node — a
partial parse would silently drop keys. The typed ingress paths (`Set`, and
values handed to it) run the same parser in **strict** mode and reject exotica
instead.

## The delta DSL

All deltas target an object schema; a non-object receiver panics, and every
named key must exist (`mustProp` panics on a typo — a delta is never a silent
no-op). Grouped by concern:

**Required-set** — the emitted `required` list always follows property order,
not argument order:

| Method | Contract |
| --- | --- |
| `Required(keys ...string)` | **replaces** the required set with exactly `keys`. |
| `RequireAlso(keys ...string)` | **adds** to the existing set — reach for it after a merge built a required union that `Required` would discard. |
| `Optional(keys ...string)` | removes keys from required (keys must still exist). |
| `RequiredAll()` | marks every current property required. |

**Per-property rewrites:**

| Method | Contract |
| --- | --- |
| `Nullable(keys ...string)` | folds `"null"` in using the canonical idiom per shape (below). Idempotent. An opaque property is wrapped via `AnyOfNull` instead of transformed. |
| `AnyOfNull(keys ...string)` | rewrites each property to `anyOf[<current>, {"type":"null"}]` regardless of shape. |
| `NullableEnum(key string, values ...string)` | replaces the property with a nullable string enum — `"null"` joins the type list **and** a `null` member joins the enum (without it the enum makes the null type unreachable). |
| `Enum(key string, values ...string)` | replaces the property with a non-null string enum. |
| `Ref(key, component string)` | replaces the property with a bare `$ref`. |
| `NoFormat(keys ...string)` | drops the `format` annotation. |
| `Describe(key, text string)` | sets the description; panics on an opaque property. |

**Shape-level:**

| Method | Contract |
| --- | --- |
| `Pick(keys ...string)` | projects properties onto `keys` (variant-view base); required follows the survivors, relative order kept. |
| `Omit(keys ...string)` | drops keys; required follows. |
| `OpenObject()` | removes `additionalProperties`, leaving the object open. |

**`Nullable` vs `AnyOfNull`.** `Nullable` picks the canonical OAS-3.1 idiom
for the node's kind: scalar/enum/array/object widen their type array (an enum
also gains a `null` member); a bare `$ref` becomes `anyOf[$ref, null]`; an
existing anyOf/oneOf gains a null arm; an allOf is wrapped whole as
`anyOf[allOf, null]` (appending null inside the allOf would make it
unsatisfiable). `AnyOfNull` always produces the wrapper form — the shape
reflected enums, `$ref`s and inline objects take in a committed document. Both
are idempotent.

**`Set(path string, v any) Schema`** is the enumerated escape hatch: a dotted
path into the typed IR. Structural segments `properties.<name>`, `items`,
`not`, `anyOf.<i>`/`oneOf.<i>`/`allOf.<i>` descend; the final segment lands in
its typed field (value-type-checked) or, for an `x-` key, in `Extensions`. A
final `properties.<name>` naming a new property **upserts** it; the value may
be a fragment map, a `Schema` (its node spliced in directly), or an `*IRNode`.
Every failure panics: unresolvable path, mistyped value, keyword outside the
typed set, addressing inside an opaque fragment, or a post-assignment
invariant violation. `properties`/`anyOf`/`oneOf`/`allOf` must be addressed
one member at a time.

## The Reflector seam

```go
type Reflector interface {
    ReflectComponents(types []reflect.Type, overrides map[reflect.Type]string) (map[string]*IRNode, error)
}
```

The one seam through which struct reflection reaches the doc layer; the
external `reflector` module is the canonical implementation. Contract every
implementation must honor: nullable scalar fields emit as type-arrays
(`{"type":["T","null"]}`); a non-omitempty pointer-to-struct field emits
`anyOf[$ref, {"type":"null"}]`; auxiliary nested-struct components are emitted
alongside the requested tops, deduped by name; `overrides` forces the
component name for the given types; `IRNode.Ref` carries the bare name, never
`RefPrefix`. Returning IR rather than maps is what preserves property order
and opaque bytes end to end. `apidoc.Nullable` exists so an implementation
need not reimplement the widening rules, and `reflectortest.Run` (below) is
the executable contract.

Helpers on this seam: `DerefType(t reflect.Type) reflect.Type` strips pointer
indirections; `RefPrefix` is the shared `#/components/schemas/` constant.

The canonical `reflector` implementation also accepts a `doc` struct tag as an
alias of `description` (see docs/reflector.md), so wire types, bodies and
input structs can share one tag dialect.

## Components: registry, verification, merge

```go
func NewComponents() *Components                       // zero value works too
func (c *Components) Add(name string, s Schema)        // PANICS on duplicates and invalid IR
func (c *Components) RegisterReflected(name string, n *IRNode, t reflect.Type) error
func (c *Components) Get(name string) (Schema, bool)   // shares the stored node — read-only unless you own it
func (c *Components) ExternalRefs(names ...string)
func (c *Components) Verify() error
func (c *Components) Merge(dst map[string]any) error
func ValidateComponent(name string, n *IRNode) error
```

- `Add` is declaration-time: a duplicate name or invariant-violating IR
  panics — two writers for one fact is a wiring bug. `RegisterReflected` is
  the bridge-facing sibling: an **identical** re-registration
  (`reflect.DeepEqual` over the IR) is a no-op, a conflicting one is an error;
  `t` may be nil for an auxiliary with no owning wire type.
- `ValidateComponent` is the same recursive invariant walk registration runs,
  exported so a `Reflector` (and `reflectortest`) can check output before
  handing it to assembly.
- `Verify` reports **every** unresolved `$ref` at once. A ref resolves when it
  names a registered entry or an `ExternalRefs` whitelist member. The walk
  covers all typed sub-schemas plus refs collected from opaque bytes and from
  extension values.
- `Merge` inserts each fragment into `dst` (a `components/schemas` map) in
  sorted name order, as `json.RawMessage` produced by the component's own
  `MarshalJSON` — never a generic map. A name already in `dst` is an error,
  checked over all names before the first insert, so a rejected merge leaves
  `dst` untouched. The final emitter must encode with `SetEscapeHTML(false)`.

Type indexes (consumed mainly by the huma adapter's registry bridge):
`RegisterType(t, name)` binds wire type → component (same-name rebind is a
no-op, conflicting bind panics); `TypeName(t)` / `TypeOf(name)` are the two
directions (first binding wins for `TypeOf`), `TypeNames()` is every binding
as a fresh map the caller owns; `RegisterNode[W](c, component)`
binds both `W` and the wrapper `Node[W]`; `RegisterWrapperType(t, component)` /
`NodeComponent(t)` are the wrapper-side pair. `Components` is not safe for
concurrent mutation — registration time only.

## Emission: graph → components

```go
func EmitComponents(r Reflector, roots []include.Resource) (map[string]Schema, error)
func EmitComponent(r Reflector, node include.Resource) (string, Schema, error)
```

Emission runs in three passes: (a) reflect every reachable node's scalar-only
wire struct (acyclic — edge fields do not exist on wire structs); (b) stitch
each **includable or default** edge into the node's `Props` as a `$ref`-shaped
value, so cycles live in refs the reflector never traverses; (c) inline
auxiliary components by fixpoint substitution. Reachability follows includable
**and default** edges' targets (the engine materializes `Defaults()` ∪ client
keys, so a non-includable default is part of the wire shape), dedupes by
`Name()`, skips computed edges and doc-external targets, and errors on two
distinct nodes sharing one name. Edge keys are stitched in sorted order, so
repeated emission is byte-identical.

Reflecting a node's wire struct needs a zero-value sample: a node supplies it
by implementing the optional `WireProvider` Resource seam (`WireSample() any`,
which `graph.Compile`'s nodes satisfy).

Stitched edge keys are optional — the value shape alone encodes nullability —
except a `graph.Required()` to-one edge, which is stitched as a bare `$ref`
**and** listed in `required`. An edge key colliding with a wire field replaces
the field's schema but never revokes its requiredness.

**Auxiliary inlining** collapses reflector-emitted helper components (nested
structs that are not graph nodes) into their referencing schemas. Three rules
bound it: only a **bare** `$ref` is inlined (a ref carrying a description or
validation stays a ref — the annotation merge has no single right answer); an
**opaque** auxiliary is never inlined; a **cyclic** auxiliary (found by an SCC
pre-pass) is pinned as its own component. Survivors stay in the returned map;
unreferenced auxiliaries are dropped. Inlined subtrees are **shared by
pointer** across components: treat emitted IR as immutable and clone before
mutating, or a delta on one component silently rewrites others.

**`EmitComponent` caveat.** The singular form returns one `Schema` and nothing
else, so an auxiliary that survives inlining (sole-ref, opaque, or cyclic)
would leave a guaranteed dangling ref — that is an **error** naming the
survivors and pointing at `EmitComponents`. It also knows only one node name,
so a wire struct embedding another graph node's wire type gets that sibling
*inlined* where `EmitComponents` would keep the `$ref`. Use it only for a node
whose wire struct references plain, acyclic, non-opaque helpers (or none).

## Edge shapes and envelopes

One table (edgeshape.go) owns the value schema for every edge kind, so the
static component and the `SchemaFor` recompute cannot drift:

| Edge kind | Value schema |
| --- | --- |
| to-one | `anyOf[$ref, null]`; `Required()` → a bare `$ref`, no null arm |
| reverse / to-many (enveloped) | `anyOf[{items: array<$ref>, hasMore: boolean, nextCursor?: string}, null]` — `items` and `hasMore` required, `nextCursor` **optional** (omitted when the fetcher returns no continuation token) |
| bare (`graph.Bare()`) | `anyOf[array<$ref>, null]` |
| in-array | `anyOf[array<{subField: anyOf[$ref, null]}>, null]`, `subField` required |
| computed | the declared `ComputedSchema`'s IR, spliced verbatim by pointer |

A non-plain `include.Edge.Envelope` switches every kind to the wrapped table
(`wrappedEdgeShape`), used by both `EmitComponents` and `SchemaFor`; node
components themselves never change — the wrapper lives at the edge site:

| Edge kind | Value schema (`Key`=`data`, `Pagination`=`pagination`) |
| --- | --- |
| to-one | `{data: anyOf[$ref, null]}`, `data` required; `Required()` → `{data: $ref}` — no outer null arm |
| reverse / to-many | `{data: array<{data: $ref}>, pagination: {hasNextPage: boolean, nextCursor?: string}}`, `data` and `pagination` required — no outer null arm |
| bare, or `Pagination == ""` | `{data: array<{data: $ref}>}` |
| in-array | `anyOf[array<{subField: {data: anyOf[$ref, null]}}>, null]`, `subField` and `data` required |
| computed | unchanged — the declared `ComputedSchema` |

The pagination block is inline (no library component), mirroring how the plain
`{items, hasMore}` envelope is inlined today.

The to-one `Required()` shape deliberately does **not** vary with the engine's
`MissingRequiredPolicy`: the document says non-null; a tolerant engine emitting
`null` anyway is the declaring application's visible responsibility.

Related provider interfaces, one method each, read by an application's
operation layer (implementors also marshal their hydrated bytes verbatim):
`EnvelopeSchemaProvider` (`EnvelopeSchema() Schema` — a data field supplying
its own inline schema for a success envelope), `BodySchemaProvider`
(`BodySchema() Schema` — the entire 200 body, no `{data}` wrapper; also honored
per Union variant), and `RequestBodyProvider` (`RequestBodySchema() Schema` —
the request-side mirror).

`Node[W]` is the byte carrier for engine-hydrated JSON: `NodeOf[W](raw)` wraps
without copying, `MarshalJSON` writes the bytes verbatim (empty → `null`) —
it exists because `encoding/json` re-escapes a nested `RawMessage`.

## SchemaFor: include-aware response schemas

```go
func SchemaFor(r Reflector, root include.Resource, tree include.IncludeTree, lim include.Limits) (Schema, error)
```

Recomputes the root's response schema for one specific client include tree,
mirroring the planner (`include.ResolvePlan`): the children of every level are
the **effective** set `Defaults()` ∪ client keys — defaults first in
declaration order, client-only keys after, sorted (the byte order the engine
emits). Every effective child is present in the response, so its key is
promoted to `required`. A child whose own effective subtree is non-empty is
**inlined** in place of the `$ref` (it carries its own requiredness delta); an
effective leaf reuses the exact static edge shape. Inputs are the same
reflector, the resource, and a parsed `include.IncludeTree`.

Planner parity details: a client key that names no includable edge is an error
(the `INVALID_INCLUDE` analogue); a stale default is silently skipped; the
default-only expansion is cycle-guarded by the planner's target-name chain
(client edges are exempt); a computed edge's declared schema is spliced in as
required, and sub-includes under it are an error. The client tree limits
(`MaxDepth`/`MaxNodes`) are enforced before any schema work; pass the same
`include.Limits` as to the planner (zero value → `include.DefaultLimits`). The
base object runs the **same** auxiliary inlining as
`EmitComponents`, and hands the reflector the same node-name overrides for
every node reachable from root, so a node's wire struct nested as a plain
field is `$ref`'d by node name on both sides and every `$ref` `SchemaFor`
emits resolves against that component map. `SchemaFor` never writes through a
borrowed node.

## Unions

```go
type UnionVariant interface { VariantType() reflect.Type; VariantStatus() int }
type Variant[T any] struct{ Status int }        // implements UnionVariant
type Union struct { Discriminator string; Variants []UnionVariant }
func UnionOf(discriminator string, vs ...UnionVariant) Union   // panics on < 2 variants
func (u Union) Schema(r Reflector) (Schema, error)
```

A `Union` models a multi-status response: one declared sum type is the single
fact both document arms derive from — `Schema` renders each variant through
the reflector and composes `{"oneOf": [...]}` in declaration order, while the
per-variant statuses are read by the application's operation layer for
envelope/response derivation. The runtime half (marshalling the returned value
and picking its status) is not in this package. `Discriminator` is stored but
**not** emitted into the fragment. A variant must be a struct; one that
implements `BodySchemaProvider` pins its entire arm to that schema instead of
reflection (needed when an arm carries nested named components that would
otherwise reflect into dangling type-name refs).

## Include paths and input parameters

```go
const XIncludePaths = "x-include-paths"
func IncludePaths(root include.Resource, limits include.Limits) []string
func IncludeParamSchema(paths []string) map[string]any
```

`IncludePaths` enumerates every valid client `?include=` path from root — a
depth-first walk over includable edges, bounded by `limits.MaxDepth` (which is
also what terminates cyclic graphs) — sorted lexicographically. A computed
edge is an includable leaf. `IncludeParamSchema` wraps the list as a
`{"type":"string", "x-include-paths":[…]}` parameter fragment; attaching it to
an operation is the application's (or the huma adapter's) concern.

### InputParams

```go
type FilterSyntax string

const (
	FilterJSON    FilterSyntax = "json"    // ?where=<JSON object>
	FilterBracket FilterSyntax = "bracket" // ?where[path][op]=value
)

const (
	XFilterFields = "x-filter-fields" // {"<wire key>": ["eq", …]}
	XFilterSyntax = "x-filter-syntax" // "json" | "bracket"
)

type InputParam struct {
	Name        string
	Description string
	Schema      map[string]any
	Style       string
	Explode     bool
}

func InputParams(res include.Resource, limits include.Limits, syntax FilterSyntax) []InputParam
```

`InputParams` documents the list parameters of a root resource. It reads the
same `include.Inputs` (through `include.InputsOf`) that `include.ResolveInputs`
enforces, so the served document and the accepted requests cannot drift: a node
that declared no `Inputs` documents the default contract, and one that declared
a sort or a filter documents exactly the keys it will accept. Each `InputParam`
is a query parameter whose `Schema` is a JSON-schema fragment; `Style` and
`Explode` are set only for the bracket `where`. Attaching the parameters to an
operation is the application's (or the huma adapter's) concern.

The returned order is fixed:

| # | Name | Present when | Schema |
|---|------|--------------|--------|
| 1 | `include` | always | `IncludeParamSchema(IncludePaths(res, limits))` |
| 2 | `sort` | `Inputs.Sort.Enabled` | `{"type":"string","enum":["k","-k",…]}`, plus `"default"` when `Sort.Default` is set |
| 3a | `page` | `Page.Mode == PageModeOffset` | `{"type":"integer","minimum":1,"default":1}` |
| 3b | `cursor` | `Page.Mode == PageModeCursor` | `{"type":"string"}` |
| 4 | `limit` | always | `{"type":"integer","minimum":1,"maximum":Page.MaxLimit,"default":Page.DefaultLimit}` |
| 5 | `where` | `Inputs.Filter.Enabled` | per syntax, below |

Slots 3a and 3b are **alternatives**, never both: the page mode picks one, so
position 3 holds `page` on an offset list and `cursor` on a cursor list.

The `sort` enum lists each wire key in both directions (`k` and `-k`), sorted
by key. A node that declared nothing yields `include`, `page`, `limit` only,
with the package defaults (`include.DefaultPageLimit` /
`include.DefaultMaxPageLimit`) on `limit`.

Both `where` syntaxes carry the same operator matrix under `x-filter-fields` —
`{"<wire key>": ["eq", …]}` from `include.FilterOpsFor(Column.Type)`, keys
sorted, so a client can see per field which operators are legal — and name the
spelling under `x-filter-syntax`:

- **`FilterJSON`** → `{"type":"string", "x-filter-syntax":"json", "x-filter-fields":{…}}`.
  The value is one JSON object: `{"<path>": {"<op>": <value>}}`, `{"and": […]}`,
  `{"or": […]}`.
- **`FilterBracket`** → `{"type":"object", "properties":{…}, "x-filter-syntax":"bracket",
  "x-filter-fields":{…}}` with `Style: "deepObject"` and `Explode: true`, spelling
  the nesting out for a deepObject parameter: one property per filterable field,
  each an object whose properties are that field's operators. An operator's
  schema is the column's JSON scalar type (`bool` → `boolean`, any int/uint →
  `integer`, floats → `number`, everything else → `string`); `in` and `nin` take
  `{"type":"array","items":{"type":T}}` instead.

Only the fields listed in `Inputs.Filter.Fields` — the filterable columns of
the root itself — are enumerated. Whether a filter path may cross an edge is
`include.ResolveFilter`'s judgement, not this fragment's: the enumeration
documents the root vocabulary, it is not the schema a request is validated
against.

## Serialization guarantees

`IRNode.MarshalJSON` emits keywords in one fixed order (`$ref`, `type`,
`enum`/`const`, validations, `properties`+`required`, `items`, combinator
arms, `not`, `additionalProperties`, annotations, then extensions sorted by
key), preserves property declaration order, and writes opaque nodes byte-exact
— the same IR always serializes to the same bytes. A single-entry `Types`
emits as a bare string; an object with zero properties omits `properties`
entirely (`{"type":"object"}` round-trips to itself). Values are encoded
without HTML escaping — see the `SetEscapeHTML(false)` caveat for anything
that re-encodes the produced bytes.

## Subpackages

### crosscheck/

Test-only nested module (own `go.mod`, depends on `santhosh-tekuri/jsonschema/v6`)
that answers the one question the doc layer cannot answer about itself: does a
sample a handler actually returns satisfy the emitted component?
`crosscheck.Compile(components map[string]apidoc.Schema, name string)` places
every component under `$defs` of one synthetic draft-2020-12 root, rewrites
`#/components/schemas/X` pointers to `#/$defs/X` (inside extensions too), and
compiles the named component with a real validator; `(*Validator).Validate(sample []byte)`
checks one sample. `format` and `contentEncoding`/`contentMediaType` are
turned into assertions, and a dangling component ref fails compilation with
the component named. Run it in your test suite whenever the emitted document
changes (`bash scripts/go.sh -C apidoc/crosscheck test ./...` for the module's
own tests). Caveat: component names share one namespace with any
component-local `$defs`.

### reflectortest/

The executable specification of the `Reflector` contract — the conformance
harness a custom implementation must pass:

```go
func TestMyReflector(t *testing.T) { reflectortest.Run(t, myreflector.New()) }
```

`Run` drives named subtests over the fixture types (exported via
`FixtureTypes()`), asserting on the returned IR, never on serialized bytes.
What is checked: pointer-without-omitempty widens a scalar to
`{"type":["T","null"]}` and a struct ref to `anyOf[$ref, null]` (never a
`nullable` keyword or a `$ref` with a sibling null type); `omitempty` means
absent, not null; default naming by Go type name with per-type overrides;
untagged embedded structs flatten, tagged ones nest; nested named structs
become deduped auxiliary components (`time.Time` stays
`{"type":"string","format":"date-time"}`); constraint tags land in typed
fields, never `Extensions`; a self-referential type reaches itself through a
`$ref`; properties keep declaration order; every component passes
`apidoc.ValidateComponent`; output is deterministic under input reordering.
