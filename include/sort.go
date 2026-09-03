// sort.go — reverse-edge `:sort()` support.
//
// Design:
//   - An edge declares its DEFAULT sort in WIRE form (Edge.Sort, e.g.
//     "startDate" / "-createdAt") plus a WHITELIST of sortable wire keys
//     (Edge.SortCols: wire json key → SQL-side sort key). graph.Compile derives
//     that whitelist from the `sort` option of the `col` struct tags (legacy
//     `sortCol`) on the node's wire struct — tag presence IS whitelist
//     membership (deny-by-default).
//   - A client `:sort(...)` arg overrides the default. Resolution here is
//     tolerant: an unknown or malformed client key falls back to the edge's
//     default. Under SortStrict such a key never reaches this step — the plan
//     is rejected at resolve time; under SortFallback the fallback applies.
//   - ESCAPE HATCH: an edge may DECLARE "sort" as an ordinary EdgeArg. Its
//     own Validate then decides acceptance and neither SortCols nor SortStrict
//     is consulted at resolve time — the way an edge whose ordering is not a
//     plain column (a computed ranking, a multi-key order) accepts a client
//     sort at all. Resolution below still runs, so the value reaching
//     EdgeQuery.Sort is still a SortCols lookup or the edge default: the hatch
//     relaxes acceptance, never the SQL-side safety.
//   - The resolved SQL-side key (with the '-' desc prefix re-applied) is what
//     EdgeQuery.Sort carries. The client string NEVER reaches SQL text — it is
//     only ever used as a map lookup key; the SQL-side value is a compile-time
//     constant from the wire struct tag, matched against static CASE arms (or
//     a closed switch) inside the data layer.

package include

import "strings"

// reverseSortKey resolves the SQL-side sort key for one reverse-edge plan node:
// client args["sort"] first (string form only; a []string or unknown key is
// ignored), then the edge's declared default. Both go through the SortCols
// whitelist. "" means "no sort resolved" → the fetcher's own default order.
func reverseSortKey(child *PlanNode) string {
	e := child.Edge
	if e == nil || e.SortCols == nil {
		return ""
	}
	if arg, ok := child.Args["sort"].(string); ok {
		if k, ok := lookupSortCol(arg, e.SortCols); ok {
			return k
		}
	}
	k, _ := lookupSortCol(e.Sort, e.SortCols)
	return k
}

// lookupSortCol maps one wire-form sort value ("key" / "-key") through cols,
// re-applying the '-' desc prefix onto the SQL-side key. ok=false when the
// value is empty or the key is not whitelisted.
func lookupSortCol(v string, cols map[string]string) (string, bool) {
	if v == "" {
		return "", false
	}
	key, desc := strings.CutPrefix(v, "-")
	col, ok := cols[key]
	if !ok {
		return "", false
	}
	if desc {
		return "-" + col, true
	}
	return col, true
}
