package include

import "encoding/json"

// kv is one edge key/value pair appended onto a serialized scalar object during
// byte-assembly. Val is a raw JSON fragment (an object, an {items,hasMore}
// envelope, a flat array, or the literal null) — and, when the edge declares an
// Edge.Envelope, that fragment is itself a wrapper object `{"<Key>": …}`
// (optionally carrying a sibling pagination member).
type kv struct {
	Key string
	Val json.RawMessage
}

// assembleObject appends edge members onto a marshaled scalar object, so key
// order is the scalar struct's fields followed by the edges in plan order.
//
// The scalar must be a JSON object (possibly `{}`); assembly is byte-exact, and
// an empty edge value is written as the literal null.
func assembleObject(scalar json.RawMessage, edges []kv) json.RawMessage {
	if len(edges) == 0 {
		return scalar
	}

	// Strip the trailing '}' so edge members can be appended; a scalar that is
	// not an object body is returned as-is rather than corrupted.
	n := len(scalar)
	if n == 0 || scalar[n-1] != '}' {
		return scalar
	}
	body := scalar[:n-1] // e.g. `{"id":"1"` , or `{` for the empty object

	empty := len(body) == 1 // only the opening '{' remains: no comma before the first edge

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
