package include

import "encoding/json"

// ------------------------------------------------------------------ kv

// kv is one edge key/value pair appended onto a serialized scalar object during
// byte-assembly. Val is a raw JSON fragment (an object, an {items,hasMore}
// envelope, a flat array, or the literal null) — and, when the edge declares an
// Edge.Envelope, that fragment is itself a wrapper object `{"<Key>": …}`
// (optionally carrying a sibling pagination member).
type kv struct {
	Key string
	Val json.RawMessage
}

// ------------------------------------------------------------------ assembleObject

// assembleObject appends edge keys (in order) onto a marshaled scalar object,
// preserving key order = the scalar struct's fields, then the edges in plan
// order. It is byte-exact.
//
// The scalar is the JSON produced by marshaling a resource's level-only wire
// struct — always a JSON object (e.g. `{"id":"1","name":"a"}` or the empty
// object `{}`). For each edge, `,"key":val` is appended before the closing `}`;
// the leading comma is omitted only when the scalar body is empty (`{}`) so the
// first edge does not produce a stray leading comma. A null edge value is
// json.RawMessage("null").
//
// With no edges the scalar is returned unchanged.
func assembleObject(scalar json.RawMessage, edges []kv) json.RawMessage {
	if len(edges) == 0 {
		return scalar
	}

	// Strip the trailing '}' so edge members can be appended. The scalar is
	// always a JSON object; guard defensively if it somehow is not.
	n := len(scalar)
	if n == 0 || scalar[n-1] != '}' {
		// Not a well-formed object body — return as-is rather than corrupt bytes.
		return scalar
	}
	body := scalar[:n-1] // e.g. `{"id":"1"` , or `{` for the empty object

	// empty reports whether the scalar was the empty object `{}` (body == `{`),
	// in which case the FIRST edge must NOT be preceded by a comma.
	empty := len(body) == 1 // only the opening '{' remains

	// Pre-size the buffer: scalar bytes + per-edge overhead.
	size := len(scalar)
	for _, e := range edges {
		size += len(e.Key) + len(e.Val) + 4 // `,"":` framing + quotes
	}
	out := make([]byte, 0, size)
	out = append(out, body...)

	for i, e := range edges {
		if !empty || i != 0 {
			out = append(out, ',')
		}
		out = append(out, '"')
		out = append(out, e.Key...)
		out = append(out, '"', ':')
		if len(e.Val) == 0 {
			out = append(out, 'n', 'u', 'l', 'l')
		} else {
			out = append(out, e.Val...)
		}
	}
	out = append(out, '}')
	return out
}
