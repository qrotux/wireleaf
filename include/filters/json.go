// json.go — the JSON `where` grammar:
//
//	{"and": [<node>, ...]}          {"or": [<node>, ...]}
//	{"<dotted.path>": {"<op>": <value>}}
//
// One key per object, always. A path is edge keys followed by the field.
// Quantifier suffixes: none = any, `*` = all, `~` = none; on the SEGMENT when
// the path crosses several to-many hops, on the FIELD when exactly one.

package filters

import (
	"encoding/json"
	"sort"
	"strconv"

	"github.com/qrotux/wireleaf/include"
	"github.com/qrotux/wireleaf/include/internal/clip"
)

// ParseJSON parses one filter node (the value of a `where` key) into an
// include.Filter. Errors are *include.Error{INVALID_FILTER} whose Path carries
// the offending client text in the conventions include.ResolveFilter uses —
// the key, "<key>:<op>" for an operator fault, "<path>:<quant>" for a
// quantifier fault, "" for a structural fault with no key to name — clipped by
// clip.Echo and never prose: an error path is machine-readable, and its size
// is never the client's to choose. Unknown edges and fields pass through for
// ResolveFilter to report: the parser owns the SYNTAX, the resolver owns every
// judgement about names.
func ParseJSON(root include.Resource, raw []byte) (include.Filter, error) {
	// No root means no graph to resolve the field-suffix sugar against: refuse
	// rather than panic on the first Edges() call, as ResolveFilter does.
	if root == nil {
		return nil, include.NewError(include.INVALID_FILTER, "")
	}
	return parseJSONNode(root, raw)
}

func parseJSONNode(root include.Resource, raw json.RawMessage) (include.Filter, error) {
	var obj map[string]json.RawMessage
	if err := json.Unmarshal(raw, &obj); err != nil {
		// A node that is not an object has no key to name.
		return nil, include.NewError(include.INVALID_FILTER, "")
	}
	if len(obj) != 1 {
		// This is what forbids {"and": [...], "title": {...}}: a group key and
		// a field key in one object have no defined precedence.
		return nil, include.NewError(include.INVALID_FILTER, keysEcho(obj))
	}
	var key string
	for k := range obj {
		key = k
	}
	if key == "and" || key == "or" {
		var items []json.RawMessage
		if err := json.Unmarshal(obj[key], &items); err != nil {
			// The group key is a known spelling: it goes back whole.
			return nil, include.NewError(include.INVALID_FILTER, key)
		}
		members := make([]include.Filter, 0, len(items))
		for _, it := range items {
			m, err := parseJSONNode(root, it)
			if err != nil {
				return nil, err
			}
			members = append(members, m)
		}
		if key == "and" {
			return include.FilterAnd(members), nil
		}
		return include.FilterOr(members), nil
	}
	var body map[string]json.RawMessage
	if err := json.Unmarshal(obj[key], &body); err != nil || len(body) != 1 {
		// The value of a path key must be exactly one {op: value}; the key is
		// the only client text there is to name.
		return nil, include.NewError(include.INVALID_FILTER, clip.Echo(key))
	}
	var opKey string
	for k := range body {
		opKey = k
	}
	op, ok := opByName[opKey]
	if !ok {
		return nil, include.NewError(include.INVALID_FILTER, clip.Echo(key)+":"+clip.Echo(opKey))
	}
	// The value passes through exactly as encoding/json decoded it — float64,
	// string, bool, []any, nil. No coercion: an adapter binds it by
	// Column.Type, and the engine judges the OPERATOR, never the value.
	var value any
	if err := json.Unmarshal(body[opKey], &value); err != nil {
		return nil, include.NewError(include.INVALID_FILTER, clip.Echo(key)+":"+opKey)
	}
	steps, field, err := filterPath(root, key)
	if err != nil {
		return nil, err
	}
	return include.FilterCond{Path: steps, Field: field, Op: op, Value: value}, nil
}

// keysEcho names a multi-key object in an error path: the first key in sorted
// order, clipped, and how many more there were. Echoing them ALL would let a
// client with a thousand keys choose the size of the 400 body — the same
// reason clip.Echo and include's truncPath exist — and the count is what names
// the fault anyway. An empty object has no key to name.
func keysEcho(m map[string]json.RawMessage) string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	if len(keys) == 0 {
		return ""
	}
	sort.Strings(keys)
	out := clip.Echo(keys[0])
	if len(keys) > 1 {
		out += " (+" + strconv.Itoa(len(keys)-1) + " keys)"
	}
	return out
}
