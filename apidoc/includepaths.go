package apidoc

import (
	"sort"

	"github.com/qrotux/wireleaf/include"
)

// XIncludePaths is the OpenAPI extension key carrying the enumerated valid
// ?include= paths for an operation's include query parameter.
const XIncludePaths = "x-include-paths"

// IncludePaths enumerates every valid client include path from root — a
// depth-first walk over includable edges bounded by limits.MaxDepth — sorted
// lexicographically. Cycles are allowed in the graph; the depth bound
// terminates the walk.
//
// A COMPUTED edge is an includable LEAF: it has no Target Resource at all (the
// thunk is a nil func), so the Target-nil guard must not swallow it — the key
// is a legal include path, it just contributes no deeper paths.
func IncludePaths(root include.Resource, limits include.Limits) []string {
	var out []string
	var walk func(node include.Resource, prefix string, depth int)
	walk = func(node include.Resource, prefix string, depth int) {
		if depth >= limits.MaxDepth {
			return
		}
		for key, edge := range node.Edges() {
			if !edge.Includable {
				continue
			}
			if edge.Target == nil && !edge.Computed {
				continue // a non-computed edge with no target is a wiring bug
			}
			path := key
			if prefix != "" {
				path = prefix + "." + key
			}
			out = append(out, path)
			if edge.Computed {
				continue // no target to descend into
			}
			if t := edge.Target(); t != nil {
				walk(t, path, depth+1)
			}
		}
	}
	walk(root, "", 0)
	sort.Strings(out)
	return out
}

// IncludeParamSchema builds the JSON-schema fragment for an include query
// parameter: a plain string carrying the valid paths under XIncludePaths.
// Attaching it to an operation is the application's concern.
func IncludeParamSchema(paths []string) map[string]any {
	vals := make([]any, len(paths))
	for i, p := range paths {
		vals[i] = p
	}
	return map[string]any{"type": "string", XIncludePaths: vals}
}
