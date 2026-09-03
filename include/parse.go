package include

import (
	"reflect"
	"regexp"
	"strings"
	"unicode/utf8"
)

// IncludeTree is the raw parser output of a ?include= string.
// Keys starting with ":" are edge args (value: string or []string).
// All other keys are child edge names (value: IncludeTree).
type IncludeTree = map[string]any

// nameRE matches valid segment and arg names.
var nameRE = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)

// argValueDelimRE matches grammar delimiters forbidden inside arg values.
var argValueDelimRE = regexp.MustCompile(`[,.:()|]`)

// argValueBytesOK reports whether v is safe to carry as an opaque arg value:
// no control bytes (< 0x20, 0x7F) and valid UTF-8. Grammar delimiters are
// rejected separately by argValueDelimRE. Everything else — including SQL
// metacharacters — is passed through verbatim: the value is UNVALIDATED
// client input and the fetcher must parameterize it.
func argValueBytesOK(v string) bool {
	for i := 0; i < len(v); i++ {
		if c := v[i]; c < 0x20 || c == 0x7f {
			return false
		}
	}
	return utf8.ValidString(v)
}

// ParseInclude parses a ?include= query string into an IncludeTree.
//
// Grammar rules:
//   - Comma separates sibling paths: "a,b" → {a:{}, b:{}}
//   - Dot separates nesting: "a.b" → {a:{b:{}}}
//   - ":name(val)" syntax adds args into the node's child tree
//   - "|" inside parens splits multi-values (≥2 → []string, 1 → string)
//   - No parens, or empty parens → arg value is "" (omitted marker / schema default)
//   - Whitespace is trimmed from tokens and segment names
//   - Empty input → empty tree
//
// Returns NewError(INVALID_INCLUDE, ...) on grammar violations.
func ParseInclude(raw string) (IncludeTree, error) {
	if strings.TrimSpace(raw) == "" {
		return IncludeTree{}, nil
	}

	root := IncludeTree{}

	for _, tokenRaw := range strings.Split(raw, ",") {
		token := strings.TrimSpace(tokenRaw)
		if token == "" {
			continue
		}

		segStrs := strings.Split(token, ".")
		segments, err := parseSegments(segStrs)
		if err != nil {
			return nil, err
		}

		cursor := root
		for _, seg := range segments {
			// A non-":" key always holds an IncludeTree (arg keys carry the ":"
			// prefix, and nameRE bars ":" from segment names), so the assertion
			// never fails on an existing entry.
			childTree, ok := cursor[seg.name].(IncludeTree)
			if !ok {
				childTree = IncludeTree{}
				cursor[seg.name] = childTree
			}

			for argName, argVal := range seg.args {
				argKey := ":" + argName
				if existing, ok := childTree[argKey]; ok {
					// Values are string or []string, so DeepEqual is plain
					// value equality.
					if !reflect.DeepEqual(existing, argVal) {
						return nil, NewError(INVALID_INCLUDE, "conflicting arg: "+argKey)
					}
				} else {
					childTree[argKey] = argVal
				}
			}

			cursor = childTree
		}
	}

	return root, nil
}

// ParseExclude parses a ?exclude= query string into a slice of dotted path segments.
//
// Format: "a.b,c" → [["a","b"], ["c"]]. No args are allowed.
// Returns NewError(INVALID_INCLUDE, ...) on name violations.
func ParseExclude(raw string) ([][]string, error) {
	if strings.TrimSpace(raw) == "" {
		return nil, nil
	}

	var result [][]string
	for _, tokenRaw := range strings.Split(raw, ",") {
		token := strings.TrimSpace(tokenRaw)
		if token == "" {
			continue
		}
		segStrs := strings.Split(token, ".")
		path := make([]string, 0, len(segStrs))
		for _, s := range segStrs {
			name := strings.TrimSpace(s)
			if !nameRE.MatchString(name) {
				return nil, NewError(INVALID_INCLUDE, "invalid exclude segment: "+name)
			}
			path = append(path, name)
		}
		result = append(result, path)
	}
	return result, nil
}

// parsedSegment holds the name and parsed args for one dot-separated segment.
type parsedSegment struct {
	name string
	args map[string]any // keys WITHOUT ":" prefix; value is string or []string
}

// parseSegments parses a slice of raw segment strings (split on ".") into
// parsedSegments. Returns INVALID_INCLUDE on any violation.
func parseSegments(segStrs []string) ([]parsedSegment, error) {
	if len(segStrs) == 0 {
		return nil, NewError(INVALID_INCLUDE, "empty include path")
	}
	segments := make([]parsedSegment, 0, len(segStrs))
	for _, s := range segStrs {
		seg, err := parseOneSegment(s)
		if err != nil {
			return nil, err
		}
		segments = append(segments, seg)
	}
	return segments, nil
}

// parseOneSegment parses "name:arg1(v1):arg2(v2|v3)" into a parsedSegment.
// The first ":" split yields the edge name; subsequent parts are args.
func parseOneSegment(raw string) (parsedSegment, error) {
	// Guard against the empty-segment case that ".." would produce.
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return parsedSegment{}, NewError(INVALID_INCLUDE, "empty segment in include path")
	}

	parts := strings.Split(raw, ":")
	name := strings.TrimSpace(parts[0])

	// A leading ":" means parts[0] is "" → invalid name.
	if !nameRE.MatchString(name) {
		return parsedSegment{}, NewError(INVALID_INCLUDE, "invalid include segment name: "+name)
	}

	args := map[string]any{}
	for i := 1; i < len(parts); i++ {
		param := parts[i]
		var argName string
		var value any = "" // default: omitted marker

		openIdx := strings.Index(param, "(")
		if openIdx == -1 {
			// Bare arg: ":limit" (no parens) → omitted marker "".
			argName = strings.TrimSpace(param)
		} else {
			// Must end with ")".
			if !strings.HasSuffix(param, ")") {
				return parsedSegment{}, NewError(INVALID_INCLUDE, "malformed include arg: "+param)
			}
			argName = strings.TrimSpace(param[:openIdx])
			inner := param[openIdx+1 : len(param)-1] // between "(" and ")"
			if len(inner) > 0 {
				vals := strings.Split(inner, "|")
				for _, v := range vals {
					if argValueDelimRE.MatchString(v) {
						return parsedSegment{}, NewError(INVALID_INCLUDE, "delimiter in include arg value: "+v)
					}
					if !argValueBytesOK(v) {
						return parsedSegment{}, NewError(INVALID_INCLUDE, "control byte or invalid UTF-8 in include arg value")
					}
				}
				if len(vals) >= 2 {
					value = vals
				} else {
					value = vals[0]
				}
			}
			// empty inner → value stays "" (omitted marker)
		}

		if !nameRE.MatchString(argName) {
			return parsedSegment{}, NewError(INVALID_INCLUDE, "invalid include arg name: "+argName)
		}
		if _, dup := args[argName]; dup {
			return parsedSegment{}, NewError(INVALID_INCLUDE, "duplicate arg: "+argName)
		}
		args[argName] = value
	}

	return parsedSegment{name: name, args: args}, nil
}
