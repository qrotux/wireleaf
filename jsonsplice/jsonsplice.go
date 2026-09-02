// Package jsonsplice is an order-preserving, string-aware byte-splice toolkit
// for the top-level members of a JSON object.
//
// Scope: every operation addresses TOP-LEVEL members only. Nested objects and
// arrays are scanned as opaque spans and are never descended into, so a key
// name that also occurs at depth ≥ 2 — or as a substring inside a string value
// (`{"title":"round trip"}` vs. the key "trip") — can never be mistaken for a
// member.
//
// Byte preservation: the document is never unmarshalled into a map and never
// re-marshalled. Untouched bytes — key spellings, string escapes (\", \u0041,
// &<>, U+2028), number formatting, insignificant whitespace — are copied
// verbatim into the result. Only the spans an operation explicitly rewrites
// change. The one exception is [Reorder], which by definition re-emits the
// object's frame: it copies each key and value byte-for-byte but drops
// whitespace between members.
//
// These are the sanctioned tools for computed edges and post-hydration masking
// (spec §3c): they let a materialized JSON document be amended after the fact
// without a round-trip through a decoder that would reorder keys and re-escape
// strings.
//
// Errors: unlike ad-hoc splicing helpers that silently return the input
// unchanged, every function here validates the document FRAME and reports
// malformed input as an error — input that is not a JSON object (or, for
// [Elements], not a JSON array), an unterminated string, an unbalanced
// container, a missing colon, a misplaced comma (leading, trailing, or
// doubled), a duplicate top-level key, a top-level scalar that is not
// true/false/null/number, or trailing bytes after the top-level value.
// Nested containers are checked for balance and string termination only;
// their contents are copied through verbatim. Run json.Valid first if you
// need full validation of a document that did not come from a marshaler.
// An absent key is NOT an error: it is a no-op ([Delete]), an append
// ([Splice], [InsertAfter]), or ok=false ([Member]).
package jsonsplice

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
)

// member is one top-level member located by the shared scanner.
type member struct {
	keyRaw   []byte // raw key bytes incl. quotes, escapes preserved
	key      string // decoded key
	keyStart int    // index of the opening quote of the key
	valStart int    // index of the first byte of the value
	valEnd   int    // index just past the last byte of the value
}

var (
	errNotObject = errors.New("jsonsplice: not a JSON object")
	errNotArray  = errors.New("jsonsplice: not a JSON array")
)

func errMalformed(i int) error {
	return fmt.Errorf("jsonsplice: malformed JSON at byte %d", i)
}

// members scans doc as a JSON object and returns its top-level members in
// document order together with the index of the object's closing '}'.
// It is the single validation path shared by every exported function, so
// malformed input surfaces as one consistent error.
func members(doc []byte) ([]member, int, error) {
	n := len(doc)
	i := skipWS(doc, 0)
	if i >= n || doc[i] != '{' {
		return nil, 0, errNotObject
	}
	i++ // consume '{'

	var ms []member
	closeIdx := -1
	for {
		i = skipWS(doc, i)
		if i >= n {
			return nil, 0, errMalformed(i)
		}
		if doc[i] == '}' {
			closeIdx = i
			i++
			break
		}
		// Strict comma placement: exactly one comma between members, none
		// before the first and none before '}'. A leading comma ({,}) fails
		// the key check below; a trailing or doubled comma leaves the scan
		// on '}' or ',' where a key is required, which also fails it.
		if len(ms) > 0 {
			if doc[i] != ',' {
				return nil, 0, errMalformed(i)
			}
			i = skipWS(doc, i+1)
			if i >= n {
				return nil, 0, errMalformed(i)
			}
		}
		// Expect a key string.
		if doc[i] != '"' {
			return nil, 0, errMalformed(i)
		}
		keyStart := i
		keyEnd := scanString(doc, i) // index just past the closing quote
		if keyEnd < 0 {
			return nil, 0, errMalformed(i)
		}
		// Decode the raw key so comparisons respect JSON escapes in the
		// stored key (the raw key "c\u0041d" is the member named cAd).
		var thisKey string
		if err := json.Unmarshal(doc[keyStart:keyEnd], &thisKey); err != nil {
			return nil, 0, errMalformed(keyStart)
		}
		i = skipWS(doc, keyEnd)
		if i >= n || doc[i] != ':' {
			return nil, 0, errMalformed(i)
		}
		i++ // consume ':'
		i = skipWS(doc, i)
		valStart := i
		valEnd := scanValue(doc, i)
		if valEnd <= valStart {
			return nil, 0, errMalformed(valStart)
		}
		// Duplicate top-level keys are malformed: decoders read the LAST
		// occurrence while a splice would address the first, so any
		// mask/redact pass over such a document would silently miss.
		// Linear scan: member counts are small, and this avoids a per-call
		// map allocation.
		for k := range ms {
			if ms[k].key == thisKey {
				return nil, 0, errMalformed(keyStart)
			}
		}
		ms = append(ms, member{
			keyRaw:   doc[keyStart:keyEnd],
			key:      thisKey,
			keyStart: keyStart,
			valStart: valStart,
			valEnd:   valEnd,
		})
		i = valEnd // continue scanning siblings
	}
	// Nothing but whitespace may follow the top-level object.
	if i = skipWS(doc, i); i < n {
		return nil, 0, errMalformed(i)
	}
	return ms, closeIdx, nil
}

// find returns the member named key, or nil.
func find(ms []member, key string) *member {
	for i := range ms {
		if ms[i].key == key {
			return &ms[i]
		}
	}
	return nil
}

// Member returns the raw bytes of the top-level member named key in the JSON
// object doc. ok is false when the member is absent (not an error).
//
// The returned slice ALIASES doc — it is a view into the caller's buffer, not a
// copy. Treat it as read-only; callers that retain or mutate it must copy first.
func Member(doc []byte, key string) (val []byte, ok bool, err error) {
	ms, _, err := members(doc)
	if err != nil {
		return nil, false, err
	}
	m := find(ms, key)
	if m == nil {
		return nil, false, nil
	}
	return doc[m.valStart:m.valEnd], true, nil
}

// Splice returns a copy of doc with the top-level member key set to the raw
// JSON value val. If key already exists its value span is replaced in place, so
// the member keeps its position and its original key spelling; otherwise
// `"key":val` is appended immediately before the object's closing '}' (with no
// leading comma when the object is empty).
//
// val must be a well-formed JSON value; it is copied verbatim and never
// validated or re-escaped.
func Splice(doc []byte, key string, val []byte) ([]byte, error) {
	ms, closeIdx, err := members(doc)
	if err != nil {
		return nil, err
	}
	if m := find(ms, key); m != nil {
		return slices.Concat(doc[:m.valStart], val, doc[m.valEnd:]), nil
	}
	return insertMember(doc, closeIdx, len(ms) > 0, encodeKey(key), val), nil
}

// insertMember returns a copy of doc with `keyRaw:val` spliced in at index at,
// preceded by a ',' when comma is true (i.e. when a complete member already
// ends just before the insertion point). keyRaw must be the raw JSON string
// bytes of the key, quotes included; val is copied verbatim.
func insertMember(doc []byte, at int, comma bool, keyRaw, val []byte) []byte {
	var lead []byte
	if comma {
		lead = []byte{','}
	}
	return slices.Concat(doc[:at], lead, keyRaw, []byte{':'}, val, doc[at:])
}

// Delete returns a copy of doc without the top-level member named key,
// preserving the order and exact bytes of the remaining members. Deleting an
// absent key is a no-op: doc is returned unchanged with a nil error.
//
// The member's [keyStart,valEnd) span is removed together with one adjacent
// comma — the one BEFORE it when it has a preceding sibling, otherwise the one
// after it — so the result stays well-formed.
func Delete(doc []byte, key string) ([]byte, error) {
	ms, _, err := members(doc)
	if err != nil {
		return nil, err
	}
	m := find(ms, key)
	if m == nil {
		return doc, nil
	}
	n := len(doc)
	lo, hi := m.keyStart, m.valEnd
	j := m.keyStart - 1
	for j >= 0 && isJSONWS(doc[j]) {
		j--
	}
	if j >= 0 && doc[j] == ',' {
		lo = j
	} else if k := skipWS(doc, m.valEnd); k < n && doc[k] == ',' {
		hi = k + 1
	}
	return slices.Concat(doc[:lo], doc[hi:]), nil
}

// InsertAfter returns a copy of doc with `"key":val` inserted immediately after
// the top-level member named afterKey. When afterKey is ABSENT from doc, the
// anchor is simply ignored and the member is placed exactly where [Splice]
// would place it: replacing key's existing value if doc already has one, and
// otherwise appended before the closing '}'. Only the POSITION degrades; a
// missing anchor is never an error.
//
// The INPUT may not contain duplicate top-level keys (that is malformed, see
// the package doc). The OUTPUT is not de-duplicated: if key already exists in
// doc and afterKey is present, the result contains key twice. Delete it first
// when that matters.
func InsertAfter(doc []byte, afterKey, key string, val []byte) ([]byte, error) {
	ms, _, err := members(doc)
	if err != nil {
		return nil, err
	}
	anchor := find(ms, afterKey)
	if anchor == nil {
		return Splice(doc, key, val)
	}
	return insertMember(doc, anchor.valEnd, true, encodeKey(key), val), nil
}

// Reorder returns a copy of doc whose top-level members are re-emitted in the
// order given by keys; members whose key is not listed follow afterwards in
// their original relative order. Unknown and repeated entries in keys are
// ignored.
//
// Keys and values are copied byte-for-byte — no re-marshal, no re-escaping — so
// nested containers and string escapes survive exactly. Because the object
// frame is rebuilt, insignificant whitespace between members is not preserved.
func Reorder(doc []byte, keys ...string) ([]byte, error) {
	ms, _, err := members(doc)
	if err != nil {
		return nil, err
	}
	pos := make(map[string]int, len(ms))
	for i, m := range ms {
		pos[m.key] = i
	}
	used := make([]bool, len(ms))
	out := make([]byte, 0, len(doc))
	out = append(out, '{')
	first := true
	emit := func(m member) {
		if !first {
			out = append(out, ',')
		}
		first = false
		out = append(out, m.keyRaw...)
		out = append(out, ':')
		out = append(out, doc[m.valStart:m.valEnd]...)
	}
	for _, k := range keys {
		if idx, ok := pos[k]; ok && !used[idx] {
			used[idx] = true
			emit(ms[idx])
		}
	}
	for idx, m := range ms {
		if !used[idx] {
			emit(m)
		}
	}
	out = append(out, '}')
	return out, nil
}

// Elements returns the raw bytes of the top-level elements of the JSON array
// arr, in order. An empty array yields a zero-length slice and a nil error;
// input that is not a JSON array, or a malformed one (unterminated string,
// unbalanced container, trailing bytes), yields an error.
//
// The returned slices ALIAS arr — they are views into the caller's buffer, not
// copies. Nested objects and arrays are returned whole and are never descended
// into, so brackets and structural bytes inside nested strings are safe.
func Elements(arr []byte) ([][]byte, error) {
	n := len(arr)
	i := skipWS(arr, 0)
	if i >= n || arr[i] != '[' {
		return nil, errNotArray
	}
	i++ // consume '['
	els := [][]byte{}
	for {
		i = skipWS(arr, i)
		if i >= n {
			return nil, errMalformed(i)
		}
		if arr[i] == ']' {
			i++
			break
		}
		// Strict comma placement: exactly one comma between elements, none
		// before the first and none before ']'. A leading, trailing, or
		// doubled comma leaves the scan on ',' or ']', where scanValue below
		// yields end <= start and the offending byte is reported.
		if len(els) > 0 {
			if arr[i] != ',' {
				return nil, errMalformed(i)
			}
			i = skipWS(arr, i+1)
			if i >= n {
				return nil, errMalformed(i)
			}
		}
		start := i
		end := scanValue(arr, i)
		if end <= start {
			return nil, errMalformed(start)
		}
		els = append(els, arr[start:end])
		i = end
	}
	if i = skipWS(arr, i); i < n {
		return nil, errMalformed(i)
	}
	return els, nil
}

// encodeKey returns the raw JSON string bytes for key.
//
// It goes through json.Encoder with SetEscapeHTML(false), NOT json.Marshal:
// Marshal escapes '<', '>' and '&' into </>/&, which would make a
// spliced key differ byte-for-byte from the same key written by the response
// encoder (which also escapes nothing — see the adapter's jsonFormat). Encode
// appends a newline, which is trimmed back off. Encoding a string cannot fail.
func encodeKey(key string) []byte {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(key); err != nil { // unreachable for a string
		panic("jsonsplice: " + err.Error())
	}
	b := buf.Bytes()
	if n := len(b); n > 0 && b[n-1] == '\n' {
		b = b[:n-1]
	}
	return b
}

// skipWS returns the index of the first non-whitespace byte at or after i.
func skipWS(b []byte, i int) int {
	for i < len(b) && isJSONWS(b[i]) {
		i++
	}
	return i
}

// isJSONWS reports whether c is JSON insignificant whitespace.
func isJSONWS(c byte) bool {
	return c == ' ' || c == '\t' || c == '\n' || c == '\r'
}

// scanString scans a JSON string starting at b[i] == '"' and returns the index
// just past the closing quote, honoring backslash escapes. Returns -1 on
// malformed input (unterminated string).
func scanString(b []byte, i int) int {
	if i >= len(b) || b[i] != '"' {
		return -1
	}
	i++ // consume opening quote
	for i < len(b) {
		c := b[i]
		if c == '\\' {
			i += 2 // skip the escape and the escaped char
			continue
		}
		if c == '"' {
			return i + 1
		}
		i++
	}
	return -1 // unterminated
}

// scanValue scans a single JSON value (object, array, string, number, bool,
// null) starting at the non-whitespace byte b[i] and returns the index just past
// the value. Returns -1 on malformed input, and i itself when there is no value
// at all (b[i] is a structural byte) — callers treat end <= start as malformed.
func scanValue(b []byte, i int) int {
	n := len(b)
	if i >= n {
		return -1
	}
	switch b[i] {
	case '"':
		return scanString(b, i)
	case '{', '[':
		return scanContainer(b, i)
	default:
		// Scalar: number, true, false, null. The extent runs to the next
		// structural byte, whitespace (the isJSONWS set), or end; the token
		// is then validated against the JSON scalar grammar so a stray
		// identifier cannot be copied through as a "value".
		end := n
		if j := bytes.IndexAny(b[i:], ",}] \t\n\r"); j >= 0 {
			end = i + j
		}
		if !validScalar(b[i:end]) {
			return -1
		}
		return end
	}
}

// validScalar reports whether tok is exactly true, false, null, or a JSON
// number per RFC 8259 §6: -?(0|[1-9][0-9]*)(\.[0-9]+)?([eE][+-]?[0-9]+)?
func validScalar(tok []byte) bool {
	switch string(tok) {
	case "true", "false", "null":
		return true
	}
	i, n := 0, len(tok)
	if i < n && tok[i] == '-' {
		i++
	}
	if i >= n {
		return false
	}
	if tok[i] == '0' {
		i++
	} else if tok[i] >= '1' && tok[i] <= '9' {
		for i < n && tok[i] >= '0' && tok[i] <= '9' {
			i++
		}
	} else {
		return false
	}
	if i < n && tok[i] == '.' {
		i++
		if i >= n || tok[i] < '0' || tok[i] > '9' {
			return false
		}
		for i < n && tok[i] >= '0' && tok[i] <= '9' {
			i++
		}
	}
	if i < n && (tok[i] == 'e' || tok[i] == 'E') {
		i++
		if i < n && (tok[i] == '+' || tok[i] == '-') {
			i++
		}
		if i >= n || tok[i] < '0' || tok[i] > '9' {
			return false
		}
		for i < n && tok[i] >= '0' && tok[i] <= '9' {
			i++
		}
	}
	return i == n
}

// scanContainer scans a JSON object or array starting at b[i] ∈ {'{','['} and
// returns the index just past the matching close. It is string-aware (quotes
// open a string, skipped via scanString, so structural bytes inside strings are
// ignored) and bracket-type-aware (a '}' may only close a '{'). Returns -1 on
// malformed input (unbalanced or mismatched container).
func scanContainer(b []byte, i int) int {
	n := len(b)
	var stack []byte
	for i < n {
		switch c := b[i]; c {
		case '"':
			ni := scanString(b, i)
			if ni < 0 {
				return -1
			}
			i = ni
		case '{', '[':
			stack = append(stack, c)
			i++
		case '}', ']':
			if len(stack) == 0 {
				return -1
			}
			open := stack[len(stack)-1]
			if (c == '}') != (open == '{') {
				return -1 // mismatched close
			}
			stack = stack[:len(stack)-1]
			i++
			if len(stack) == 0 {
				return i
			}
		default:
			i++
		}
	}
	return -1 // unbalanced
}
