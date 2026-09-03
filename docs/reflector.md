# reflector — the canonical Go-struct → IR reflector

## Overview

`github.com/qrotux/wireleaf/reflector` turns Go wire structs into typed
`apidoc` IR components. It is the canonical implementation of the
`apidoc.Reflector` interface — the one seam the doc core leaves open — and it
is a **separate Go module** (its own `go.mod`, nested in the repo) so that its
engine, `swaggest/jsonschema-go`, never reaches consumers of the zero-dependency
core. Install it explicitly:

```
go get github.com/qrotux/wireleaf/reflector
```

jsonschema-go is a *hidden* engine: no jsonschema type appears in any exported
identifier, and its draft-07 idioms are normalized to OAS 3.1 at the boundary.
The package's governing rule is **typed ingress**: a standard JSON Schema
keyword the IR does not model is a conversion *error naming the keyword*, never
a silent drop. The single deliberate exception is the typeless schema (see
"Opaque fallback").

## Entry points

```go
type Reflector struct {
    // Nullability decides, per struct field, whether the field is required
    // and whether it admits null. nil means apidoc.DefaultNullability.
    Nullability apidoc.NullabilityPolicy
}

func (r *Reflector) ReflectComponents(types []reflect.Type, overrides map[reflect.Type]string) (map[string]*apidoc.IRNode, error)

func Reflect[T any]() apidoc.Schema
func ReflectType(t reflect.Type) apidoc.Schema
```

- **`ReflectComponents`** is the `apidoc.Reflector` method and the primary
  path. It renders every requested type into a named component, *plus* every
  auxiliary component reachable from it (nested structs, through slices, maps
  and pointers), deduped by name. The result is independent of input order.
  Only struct types (possibly behind pointers) are accepted at the top level;
  anything else is an error.
- **`Reflect[T]` / `ReflectType`** are one-type conveniences that run the same
  pipeline and return the *top-level* schema of `T` as an `apidoc.Schema`
  (nested structs still reflect to `$ref`s). Gotchas: the result goes through
  `apidoc.RawFragment`, so properties come out in **sorted-key order** and
  numbers decode as `float64` — not byte-identical to the emitter's output.
  Both **panic** on a non-struct `T` or a reflection failure: they are meant
  for registration and test time, never per-request. Reach for
  `ReflectComponents` when declaration order or exact numeric types matter.

The zero value `&reflector.Reflector{}` is ready to use and stateless between
calls; every `ReflectComponents` call builds its own engine.

### Registering with apidoc / graph

The compiled graph never sees the reflector directly — you hand it to the doc
core's entry points:

```go
refl := &reflector.Reflector{}
frags, err := apidoc.EmitComponents(refl, g.Reachable(g.Resource("Book")))
schema, err := apidoc.SchemaFor(refl, g.Resource("Book"), tree, include.DefaultLimits)
```

The huma adapter (`adapters/huma`) routes huma's own body reflection through
the same instance via `wfhuma.WithRegistry`.

## Nullability

Nullability and required-ness are the **policy's** verdict, never the engine's.
`Reflector.Nullability` is an `apidoc.NullabilityPolicy`
(`func(reflect.StructField) apidoc.Verdict`); `nil` means
`apidoc.DefaultNullability`:

| Field shape | Verdict | Document effect |
| --- | --- | --- |
| plain value, no omit option | `VerdictPlain` | in `required`, no null |
| `omitempty` / `omitzero` in the json tag | `VerdictOptional` | not in `required`, no null |
| pointer without an omit option | `VerdictNull` | in `required`, admits null |

The canonical null forms (produced via `apidoc.Nullable` — the reflector no
longer carries a local copy): a nullable scalar widens to
`{"type":["T","null"]}`; a nullable component reference becomes
`anyOf[$ref, {"type":"null"}]`. The keyword `nullable` is never emitted.

**Precedence gotcha:** at the *property* level the policy overrides an
Exposer's own declared null — the engine's null type is stripped and re-derived
per field. Element-level nulls (array items, `additionalProperties` values,
combinator arms) are untouched: no field verdict applies there, so the
declaration stands.

## What Go maps to what

- **Structs → objects.** Property order is the struct's declaration order,
  with untagged embedded structs flattened *in place* (a promoted field takes
  the embed's position). Field visibility follows `encoding/json`: `json:"-"`
  and unexported fields are skipped; untagged exported fields are included
  under their Go name. Name conflicts across embedding depths resolve by
  depth dominance — the unique shallowest field wins; a same-depth tie is
  broken in favour of a single json-tagged candidate; a tie that survives both
  rules drops the name, exactly as `encoding/json` does.
- **Slices and arrays → array nodes** with an `items` schema for the element
  type.
- **Maps → object nodes** with `additionalProperties` set to the value type's
  schema.
- **Pointers** deref transparently for shape; their nullability is a policy
  question (above).
- **Nested structs → `$ref`s** to auxiliary components emitted alongside the
  tops.
- **`time.Time` → `{"type":"string","format":"date-time"}`**, inline — never a
  component of its own.
- **`interface{}` / `json.RawMessage`** are typeless → Opaque (below).
- **Constraint tags** are parsed by the engine and land in the IR's typed
  fields: `minimum`/`maximum`, `minLength`/`maxLength`/`pattern`, `format`,
  `title`/`description`, `enum:"a,b"` (an enum tag yields a `KindEnum` node;
  an enum without a type is an error), plus `const`, `default`, `examples`,
  `readOnly`/`writeOnly`/`deprecated`, item/property counts, `uniqueItems`,
  `contentEncoding`/`contentMediaType`. `x-…` keys go into IR Extensions (any
  `$ref` inside an extension value is rewritten into document coordinates so
  the dangling-ref check sees it); a non-`x-` unmatched key is an error.
  `doc:"…"` is accepted as an alias of `description:"…"` (huma's dialect), so
  wire types, bodies and input structs share one tag; both present with
  different text is a reflection error.

`required` from the engine's `required` tag is ignored — the policy decides.
`additionalProperties: false` is never synthesized for structs (see the README
"Boundaries" section for why that is canon).

## Rejected constructs (`rejectExotica`)

An unmodelled construct is a bug to surface, not something to drop. Conversion
fails with an error naming the keyword for: `$id`, `$schema`, `$comment`,
`additionalItems`, `contains`, `patternProperties`, `dependencies`,
`propertyNames`, `if`/`then`/`else`, boolean-form `not`, `definitions`.
Also rejected, with the same philosophy:

- **tuple-form `items`** (an array of schemas) — explicitly not representable;
- `$ref` combined with any structural keyword (properties, items, enum, arms);
- more than one of `anyOf`/`oneOf`/`allOf` on one schema, and boolean arms;
- properties on a schema with no struct type to order them by.

These surfaces are reachable only through jsonschema-go Exposers (types that
hand the engine a raw schema); plain Go structs never produce them. A schema
`not` arm *is* modelled: it converts through the same engine (same policy, same
overrides) and its errors propagate rather than silently widening the schema.

## Naming and collisions

A component is named after its **bare Go type name** — not the engine's
package-prefixed default. When two *different* types claim one name (the same
type name in two packages, or an `overrides` entry colliding with a Go type
name), `ReflectComponents` returns a hard error telling you to disambiguate,
because silently emitting one component for two shapes would make the survivor
depend on input order. The fix is the `overrides` map: an entry forces the
component name of *that* type only; every other type keeps its Go name.

An **anonymous struct** has no type name, so it yields no definition: it is
inlined where it appears, and as a root type it is simply absent from the
result map — an override cannot rescue it. Name the type if you need the
component.

## Opaque fallback

A **typeless** schema (`interface{}`, `json.RawMessage`) models "anything",
and the IR has no kind for that — this is the one deliberate escape hatch. The
whole fragment folds into a single Opaque node: a bare one is `{}`; one
carrying keywords folds them *into* the bytes (IR invariants forbid
annotations on an Opaque node). Nothing is dropped and nothing is refused.
Two normalizations guarantee determinism: every `$ref` inside the fragment is
rewritten from the engine's `#/definitions/` location into component
coordinates (so it resolves and is seen by the dangling-ref check), and the
bytes are re-marshalled through a map so keys come out **sorted** — the
fragment, and therefore the component, is byte-identical between runs.

An array whose items are typeless (or absent) gets the empty Opaque fragment
as its `items`.

## Conformance

This implementation passes `apidoc/reflectortest` in full
(`reflectortest.Run(t, &reflector.Reflector{})` is the module's own executable
specification). A custom `apidoc.Reflector` for another documentation stack
should pass the same harness; the harness and the interface contract are
documented with the `apidoc` module.
