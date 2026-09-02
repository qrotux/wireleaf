# jsonsplice — order-preserving byte splices over a JSON object's top-level members

## Overview

`jsonsplice` amends a materialized JSON document by splicing bytes, not by
decoding it. A round-trip through `encoding/json` — unmarshal into a map,
re-marshal — destroys exactly the properties a wire document is supposed to
keep: member order becomes map-iteration order, string escapes are rewritten,
`<`, `>`, `&` are HTML-escaped, and number formatting (`1.50`, `1e3`) is
normalized. This package instead scans the document's frame, locates the span
of one top-level member, and rewrites only that span; every untouched byte is
copied verbatim.

Its primary consumer is the include engine's materializer: computed edges
(`graph.Computed`) have no target node, so application code produces their
value and uses `jsonsplice` to write it into the hydrated document after the
fact. The same tools serve post-hydration masking.

Two rules bound the whole package:

- **Top-level only.** Every operation addresses top-level members of one JSON
  object (or, for `Elements`, top-level elements of one array). Nested objects
  and arrays are scanned as opaque spans and never descended into, so a key
  name occurring at depth ≥ 2, or as a substring inside a string value, cannot
  be mistaken for a member.
- **Bytes are preserved.** The document is never unmarshalled and never
  re-marshalled. Key spellings, escapes (`\"`, `A`, raw `&<>`, U+2028),
  number formatting, and insignificant whitespace outside the rewritten span
  survive verbatim. The one documented exception is `Reorder` (see below).

## Functions

All functions take the document as `[]byte` and, except where noted, return a
new slice — the input is never mutated. All of them run the same frame
validation first (see Validation & errors); an absent key is never an error.

### Member

```go
func Member(doc []byte, key string) (val []byte, ok bool, err error)
```

Returns the raw bytes of the value of the top-level member named `key`.
`ok == false` (with `err == nil`) when the member is absent.

Gotcha: the returned slice **aliases `doc`** — it is a view into the caller's
buffer, not a copy. Treat it as read-only; copy before retaining or mutating.

### Splice

```go
func Splice(doc []byte, key string, val []byte) ([]byte, error)
```

Sets the top-level member `key` to the raw JSON value `val`. If the key
exists, only its value span is replaced — the member keeps its position and
its original key spelling. Otherwise `"key":val` is appended immediately
before the closing `}`, with a leading comma only when the object is
non-empty.

Gotcha: `val` must already be a well-formed JSON value. It is copied verbatim
and **never validated or re-escaped** — splicing garbage produces a document
that this package itself will subsequently reject.

### Delete

```go
func Delete(doc []byte, key string) ([]byte, error)
```

Removes the top-level member `key`, preserving order and exact bytes of the
remaining members. One adjacent comma is removed with it — the one before the
member when it has a preceding sibling, otherwise the one after — so the
result stays well-formed. Deleting an absent key is a no-op: `doc` is returned
unchanged (the same slice) with a nil error.

### InsertAfter

```go
func InsertAfter(doc []byte, afterKey, key string, val []byte) ([]byte, error)
```

Inserts `"key":val` immediately after the member named `afterKey`. When the
anchor is absent, only the *position* degrades — the call behaves exactly like
`Splice(doc, key, val)` (replace in place if `key` exists, else append);
a missing anchor is never an error.

Gotcha: when the anchor exists, **no de-duplication is performed** — if `key`
already exists elsewhere, the result contains it twice. `Delete` it first when
that matters.

### Reorder

```go
func Reorder(doc []byte, keys ...string) ([]byte, error)
```

Re-emits the object with its top-level members in the order given by `keys`;
members not listed follow in their original relative order. Unknown and
repeated entries in `keys` are ignored. Duplicate keys in the *document* are
never collapsed — every occurrence is re-emitted; a listed key moves its
**last** occurrence to the listed position, and the earlier ones follow with
the unlisted members.

Each key and value is still copied byte-for-byte — no re-marshal, no
re-escaping — but because the object frame is rebuilt, this is the one
function that drops insignificant whitespace between members. The result is
compact: `{"k":v,"k2":v2}`.

### Elements

```go
func Elements(arr []byte) ([][]byte, error)
```

Returns the raw bytes of the top-level elements of the JSON array `arr`, in
order. An empty array yields a zero-length (non-nil) slice and a nil error.
Nested containers are returned whole; structural bytes inside nested strings
are handled correctly.

Gotcha: the returned slices **alias `arr`**, like `Member`'s result.

### Key encoding on insert

Keys inserted by `Splice` and `InsertAfter` are encoded via `json.Encoder`
with `SetEscapeHTML(false)`, not `json.Marshal`: `<`, `>` and `&` in a key
stay raw. This keeps a spliced key byte-identical to the same key written by
the response encoder (`include.MarshalNoEscape`, the engine's default).

## Guarantees

- Untouched regions are copied verbatim — only the span an operation
  explicitly rewrites changes (frame rebuild in `Reorder` excepted, and even
  there keys and values are byte-exact).
- Input slices are never mutated by any function. Mutators return freshly
  allocated results (except `Delete` of an absent key, which returns the input
  slice itself); the read-only accessors `Member` and `Elements` return
  aliasing views instead.
- Key matching decodes JSON escapes in the *stored* key: the raw member key
  `"cAd"` matches the lookup key `cAd`. Lookup keys you pass in are plain
  Go strings, never escape sequences.
- Whitespace: JSON insignificant whitespace (space, tab, `\n`, `\r`) is
  tolerated everywhere the grammar allows it and preserved outside rewritten
  spans. Appends and inserts add no whitespace of their own (`,"key":val`).

## Validation & errors

Every exported function runs one shared frame scan before doing anything, so
malformed input is reported consistently instead of being passed through.

**Validated** (errors):

- The document frame: input must be a single JSON object (`Elements`: a
  single array), with nothing but whitespace after it — trailing bytes are
  malformed.
- Strings: an unterminated string is malformed.
- Containers: unbalanced or type-mismatched brackets (`[1,2}`) — including
  inside nested values, since the scanner must find their extent.
- Object member syntax: unquoted keys and missing colons are malformed.
- Comma placement is **strict**: exactly one comma between members/elements,
  none before the first, none before the closing bracket. Leading (`{,}`),
  trailing (`{"a":1,}`), doubled (`{"a":1,,"b":2}`), and missing
  (`{"a":1 "b":2}`) commas are all malformed.

**Not validated:** the interiors of scalar values and the contents of nested
containers. Scalars are consumed to their extent without token validation
(`{"a":bogus}` passes), and `val` arguments are never checked — full JSON
validity of values is the caller's contract.

**Error shapes:**

- Not an object / not an array: sentinel errors with the messages
  `jsonsplice: not a JSON object` and `jsonsplice: not a JSON array`.
- Everything else: `jsonsplice: malformed JSON at byte %d`, carrying the
  offset of the offending byte.
- Absent keys are *not* errors: `Member` reports `ok=false`, `Delete` is a
  no-op, `Splice` appends, `InsertAfter` falls back to `Splice` placement.

The package makes no performance or allocation promises beyond the copying
behavior described above.
