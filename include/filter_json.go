// filter_json.go — the JSON `where` grammar and the path helpers both filter
// parsers share. The grammar was the reference parser of examples/filter,
// promoted here so an application gets a syntax for free while ResolveFilter
// stays the ONE place that judges names, operators and limits.
//
//	{"and": [<node>, ...]}          {"or": [<node>, ...]}
//	{"<dotted.path>": {"<op>": <value>}}
//
// One key per object, always. A path is edge keys followed by the field.
// Quantifier suffixes: none = any, `*` = all, `~` = none; on the SEGMENT when
// the path crosses several to-many hops, on the FIELD when exactly one.

package include

import (
	"encoding/json"
	"reflect"
	"sort"
	"strconv"
	"strings"
)

// filterOpNames is the client spelling of every operator, in FilterOpsFor order.
var filterOpNames = []FilterOp{OpEq, OpNe, OpIn, OpNin, OpLt, OpLte, OpGt, OpGte}

var filterOpByName = func() map[string]FilterOp {
	m := make(map[string]FilterOp, len(filterOpNames))
	for _, op := range filterOpNames {
		m[string(op)] = op
	}
	return m
}()

// FilterOpsFor lists the operators a column of type t admits, in a fixed
// order (eq ne in nin lt lte gt gte); nil for a type outside FilterableType.
// It is opAllowed read forwards: the resolver asks "is this operator allowed
// on this column", a documentation or input-schema caller asks "which
// operators may a client name here", and both must answer from one matrix.
func FilterOpsFor(t reflect.Type) []FilterOp {
	var out []FilterOp
	for _, op := range filterOpNames {
		if opAllowed(op, t) {
			out = append(out, op)
		}
	}
	return out
}

// ParseFilterJSON parses one filter node (the value of a `where` key) into a
// Filter. Errors are *Error{INVALID_FILTER} whose Path carries the offending
// client text in the conventions ResolveFilter uses — the key, "<key>:<op>"
// for an operator fault, "<path>:<quant>" for a quantifier fault, "" for a
// structural fault with no key to name — clipped by clientEcho and never
// prose: an error path is machine-readable, and its size is never the
// client's to choose. Unknown edges and fields pass through for ResolveFilter
// to report: the parser owns the SYNTAX, the resolver owns every judgement
// about names.
func ParseFilterJSON(root Resource, raw []byte) (Filter, error) {
	// No root means no graph to resolve the field-suffix sugar against: refuse
	// rather than panic on the first Edges() call, as ResolveFilter does.
	if root == nil {
		return nil, NewError(INVALID_FILTER, "")
	}
	return parseJSONNode(root, raw)
}

func parseJSONNode(root Resource, raw json.RawMessage) (Filter, error) {
	var obj map[string]json.RawMessage
	if err := json.Unmarshal(raw, &obj); err != nil {
		// A node that is not an object has no key to name.
		return nil, NewError(INVALID_FILTER, "")
	}
	if len(obj) != 1 {
		// This is what forbids {"and": [...], "title": {...}}: a group key and
		// a field key in one object have no defined precedence.
		return nil, NewError(INVALID_FILTER, keysEcho(obj))
	}
	var key string
	for k := range obj {
		key = k
	}
	if key == "and" || key == "or" {
		var items []json.RawMessage
		if err := json.Unmarshal(obj[key], &items); err != nil {
			// The group key is a known spelling: it goes back whole.
			return nil, NewError(INVALID_FILTER, key)
		}
		members := make([]Filter, 0, len(items))
		for _, it := range items {
			m, err := parseJSONNode(root, it)
			if err != nil {
				return nil, err
			}
			members = append(members, m)
		}
		if key == "and" {
			return FilterAnd(members), nil
		}
		return FilterOr(members), nil
	}
	var body map[string]json.RawMessage
	if err := json.Unmarshal(obj[key], &body); err != nil || len(body) != 1 {
		// The value of a path key must be exactly one {op: value}; the key is
		// the only client text there is to name.
		return nil, NewError(INVALID_FILTER, clientEcho(key))
	}
	var opKey string
	for k := range body {
		opKey = k
	}
	op, ok := filterOpByName[opKey]
	if !ok {
		return nil, NewError(INVALID_FILTER, clientEcho(key)+":"+clientEcho(opKey))
	}
	// The value passes through exactly as encoding/json decoded it — float64,
	// string, bool, []any, nil. No coercion: an adapter binds it by
	// Column.Type, and the engine judges the OPERATOR, never the value.
	var value any
	if err := json.Unmarshal(body[opKey], &value); err != nil {
		return nil, NewError(INVALID_FILTER, clientEcho(key)+":"+opKey)
	}
	steps, field, err := filterPath(root, key)
	if err != nil {
		return nil, err
	}
	return FilterCond{Path: steps, Field: field, Op: op, Value: value}, nil
}

// keysEcho names a multi-key object in an error path: the first key in sorted
// order, clipped, and how many more there were. Echoing them ALL would let a
// client with a thousand keys choose the size of the 400 body — the same
// reason clientEcho and truncPath exist — and the count is what names the
// fault anyway. An empty object has no key to name.
func keysEcho(m map[string]json.RawMessage) string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	if len(keys) == 0 {
		return ""
	}
	sort.Strings(keys)
	out := clientEcho(keys[0])
	if len(keys) > 1 {
		out += " (+" + strconv.Itoa(len(keys)-1) + " keys)"
	}
	return out
}

// splitQuant strips a trailing `*` (all) or `~` (none) from one path segment.
func splitQuant(s string) (string, Quant) {
	switch {
	case strings.HasSuffix(s, "*"):
		return strings.TrimSuffix(s, "*"), QuantAll
	case strings.HasSuffix(s, "~"):
		return strings.TrimSuffix(s, "~"), QuantNone
	}
	return s, ""
}

// filterPath splits a dotted client path into hop steps and the leaf field,
// resolving the field-suffix quantifier sugar against the graph. A path the
// graph does not know is returned as-is (quantifier-free) for ResolveFilter.
func filterPath(root Resource, path string) ([]FilterStep, string, error) {
	segs := strings.Split(path, ".")
	field, quant := splitQuant(segs[len(segs)-1])
	steps := make([]FilterStep, 0, len(segs)-1)
	for _, s := range segs[:len(segs)-1] {
		key, q := splitQuant(s)
		steps = append(steps, FilterStep{Key: key, Quant: q})
	}
	_, many, known := walkFilterPath(root, steps)
	switch {
	// A field-suffix quantifier is only unambiguous over a known path with
	// exactly one to-many segment that nobody has already quantified. All
	// three faults are reported as "<path>:<quant>", the convention
	// ResolveFilter uses for a quantifier fault.
	case quant != "" && !known,
		quant != "" && len(many) != 1,
		quant != "" && steps[many[0]].Quant != "":
		return nil, "", NewError(INVALID_FILTER, clientEcho(path)+":"+string(quant))
	case quant != "":
		steps[many[0]].Quant = quant
	}
	// A to-many segment nobody quantified is `any`, the no-suffix default.
	if known {
		for _, i := range many {
			if steps[i].Quant == "" {
				steps[i].Quant = QuantAny
			}
		}
	}
	if len(steps) == 0 {
		steps = nil
	}
	return steps, field, nil
}

// walkFilterPath follows steps through Edges(); it returns the resource the
// path lands on, the indices of the to-many steps, and whether every step was
// known. A key the graph does not know stops the walk: the rest is unknowable
// here, and ResolveFilter is the one that reports it, with a bounded path.
// The bracket parser uses the landing resource to type the value.
func walkFilterPath(root Resource, steps []FilterStep) (Resource, []int, bool) {
	cur := root
	var many []int
	for i, st := range steps {
		e, ok := cur.Edges()[st.Key]
		if !ok || e.Target == nil {
			return nil, many, false
		}
		if e.Many {
			many = append(many, i)
		}
		cur = e.Target()
		if cur == nil {
			return nil, many, false
		}
	}
	return cur, many, true
}
