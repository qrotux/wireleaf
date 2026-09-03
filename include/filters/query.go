// query.go — the bracket query-string grammar:
//
//	where[<path>][<op>]=<value>
//	where[or][<i>][<path>][<op>]=<value>     where[and][<i>]…   (groups nest)
//
// Siblings at one level are AND-ed — a group standing next to a condition
// included, since bracket keys carry no precedence of their own; the members of
// an or-group are the distinct indices, each itself an AND of what shares that
// index. Values are URL strings, coerced by the leaf column's Go type when the
// path is known; in/nin split on ",". Everything else is ResolveFilter's job:
// this parser owns the SYNTAX, never a judgement about names.

package filters

import (
	"net/url"
	"reflect"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/qrotux/wireleaf/include"
	"github.com/qrotux/wireleaf/include/internal/clip"
)

const whereKeyPrefix = "where["

var timeType = reflect.TypeFor[time.Time]()

type qEntry struct {
	segs  []string
	value string
	key   string // the original query key, what a structural fault names
}

// ParseQuery parses the where[...] keys of values into an include.Filter; nil
// when there are none. Errors are *include.Error{INVALID_FILTER} whose Path
// carries the offending client text in the conventions include.ResolveFilter
// uses — the path for a condition fault, "<path>:<op>" for an operator fault,
// "<path>:<value>" for a coercion fault, "<path>:<quant>" for a quantifier
// fault, and the whole bracket key for a structural fault, where no path has
// been read yet — clipped by clip.Echo and never prose: an error path is
// machine-readable, and its size is never the client's to choose.
func ParseQuery(root include.Resource, values url.Values) (include.Filter, error) {
	// Sorted keys make both the AST order and the FIRST fault reported
	// deterministic; url.Values is a map, and map order is not.
	keys := make([]string, 0, len(values))
	for k := range values {
		if strings.HasPrefix(k, whereKeyPrefix) {
			keys = append(keys, k)
		}
	}
	if len(keys) == 0 {
		return nil, nil
	}
	sort.Strings(keys)
	// No root means no graph to walk for the quantifier sugar or the value's
	// type: refuse rather than panic on the first Edges() call, as ParseJSON
	// does.
	if root == nil {
		return nil, include.NewError(include.INVALID_FILTER, "")
	}
	entries := make([]qEntry, 0, len(keys))
	for _, k := range keys {
		if len(values[k]) != 1 {
			return nil, include.NewError(include.INVALID_FILTER, clip.Echo(k))
		}
		segs, err := bracketSegments(k)
		if err != nil {
			return nil, err
		}
		entries = append(entries, qEntry{segs: segs, value: values[k][0], key: k})
	}
	return buildQueryLevel(root, entries)
}

// bracketSegments splits "where[a][b][c]" into ["a","b","c"].
func bracketSegments(key string) ([]string, error) {
	rest := strings.TrimPrefix(key, "where")
	var segs []string
	for rest != "" {
		if rest[0] != '[' {
			return nil, include.NewError(include.INVALID_FILTER, clip.Echo(key))
		}
		end := strings.IndexByte(rest, ']')
		if end < 0 || end == 1 || strings.IndexByte(rest[1:end], '[') >= 0 {
			// An unclosed bracket, an empty one and one opened inside another
			// are all malformed, and the key is all there is to name them with.
			return nil, include.NewError(include.INVALID_FILTER, clip.Echo(key))
		}
		segs = append(segs, rest[1:end])
		rest = rest[end+1:]
	}
	return segs, nil
}

// buildQueryLevel turns the entries of one nesting level into a Filter: the
// groups (and then or, by numeric index) first, then the conditions in key
// order, all AND-ed; a single member is returned bare.
func buildQueryLevel(root include.Resource, entries []qEntry) (include.Filter, error) {
	groups := map[string]map[int][]qEntry{} // "and"/"or" → index → entries
	var conds []include.Filter
	for _, e := range entries {
		switch {
		case len(e.segs) >= 3 && (e.segs[0] == "and" || e.segs[0] == "or"):
			idx, ok := groupIndex(e.segs[1])
			if !ok {
				return nil, include.NewError(include.INVALID_FILTER, clip.Echo(e.key))
			}
			if groups[e.segs[0]] == nil {
				groups[e.segs[0]] = map[int][]qEntry{}
			}
			groups[e.segs[0]][idx] = append(groups[e.segs[0]][idx], qEntry{segs: e.segs[2:], value: e.value, key: e.key})
		case e.segs[0] == "and" || e.segs[0] == "or":
			// A group key with too few segments (where[or][0]=x) is a
			// structural fault, not a condition on a path called "or".
			return nil, include.NewError(include.INVALID_FILTER, clip.Echo(e.key))
		case len(e.segs) == 2:
			c, err := queryCond(root, e)
			if err != nil {
				return nil, err
			}
			conds = append(conds, c)
		default:
			// Neither where[path][op] nor where[and|or][i]…: one bracket too
			// few, one too many, or a group key without an index.
			return nil, include.NewError(include.INVALID_FILTER, clip.Echo(e.key))
		}
	}
	var members []include.Filter
	for _, label := range []string{"and", "or"} {
		byIdx := groups[label]
		if byIdx == nil {
			continue
		}
		idxs := make([]int, 0, len(byIdx))
		for i := range byIdx {
			idxs = append(idxs, i)
		}
		sort.Ints(idxs)
		parts := make([]include.Filter, 0, len(idxs))
		for _, i := range idxs {
			sub, err := buildQueryLevel(root, byIdx[i])
			if err != nil {
				return nil, err
			}
			parts = append(parts, sub)
		}
		if label == "and" {
			members = append(members, include.FilterAnd(parts))
		} else {
			members = append(members, include.FilterOr(parts))
		}
	}
	members = append(members, conds...)
	if len(members) == 1 {
		return members[0], nil
	}
	return include.FilterAnd(members), nil
}

// groupIndex reads a group index: plain digits only, so "00" and "+1" do not
// merge into the members of "0" and "1" the way strconv.Atoi would let them.
func groupIndex(s string) (int, bool) {
	if s == "" || (len(s) > 1 && s[0] == '0') {
		return 0, false
	}
	for i := 0; i < len(s); i++ {
		if s[i] < '0' || s[i] > '9' {
			return 0, false
		}
	}
	n, err := strconv.Atoi(s)
	return n, err == nil
}

// queryCond builds one condition from a where[path][op] entry, typing the value
// by the leaf column when the graph knows the path. A fault here has a path to
// name, so it names THAT and not the bracket key around it — the convention
// ParseJSON follows, so one error shape covers both syntaxes.
func queryCond(root include.Resource, e qEntry) (include.Filter, error) {
	op, ok := opByName[e.segs[1]]
	if !ok {
		return nil, include.NewError(include.INVALID_FILTER, clip.Echo(e.segs[0])+":"+clip.Echo(e.segs[1]))
	}
	steps, field, err := filterPath(root, e.segs[0])
	if err != nil {
		return nil, err
	}
	// An unknown edge or field leaves colType nil and the value a string: the
	// name is ResolveFilter's to reject, not the parser's.
	var colType reflect.Type
	if target, _, known := walkFilterPath(root, steps); known {
		if c, ok := include.ColumnsOf(target)[field]; ok {
			colType = c.Type
		}
	}
	var value any
	if op == include.OpIn || op == include.OpNin {
		parts := strings.Split(e.value, ",")
		list := make([]any, 0, len(parts))
		for _, p := range parts {
			// "1,,2" is a typo, not a member "": the empty string would reach
			// the adapter as a legitimate value of any string column.
			v, err := coerceQueryValue(p, colType)
			if err != nil || p == "" {
				return nil, include.NewError(include.INVALID_FILTER, clip.Echo(e.segs[0])+":"+clip.Echo(p))
			}
			list = append(list, v)
		}
		value = list
	} else {
		v, err := coerceQueryValue(e.value, colType)
		if err != nil {
			return nil, include.NewError(include.INVALID_FILTER, clip.Echo(e.segs[0])+":"+clip.Echo(e.value))
		}
		value = v
	}
	return include.FilterCond{Path: steps, Field: field, Op: op, Value: value}, nil
}

// coerceQueryValue converts one URL string by the column's Go type; a nil or
// string-like type keeps the string. The widths are the ones the AST carries
// (int64/uint64/float64), not the column's own — narrowing is the adapter's.
func coerceQueryValue(s string, t reflect.Type) (any, error) {
	if t == nil {
		return s, nil
	}
	if t == timeType {
		return time.Parse(time.RFC3339, s)
	}
	switch t.Kind() {
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		return strconv.ParseInt(s, 10, 64)
	case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64:
		return strconv.ParseUint(s, 10, 64)
	case reflect.Float32, reflect.Float64:
		return strconv.ParseFloat(s, 64)
	case reflect.Bool:
		return strconv.ParseBool(s)
	}
	return s, nil
}
