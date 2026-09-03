// inputparams.go — the list parameters of a resource as JSON-schema
// fragments. InputParams reads the SAME include.Inputs that
// include.ResolveInputs enforces, so the served document and the accepted
// requests cannot drift: a node that declared nothing documents the default
// contract (offset pagination, no sort, no filter), and one that declared a
// sort or a filter documents exactly the keys it will accept.

package apidoc

import (
	"maps"
	"reflect"
	"slices"

	"github.com/qrotux/wireleaf/include"
)

// FilterSyntax names how a ?where filter is spelled on the wire.
type FilterSyntax string

const (
	FilterJSON    FilterSyntax = "json"    // ?where=<JSON object>
	FilterBracket FilterSyntax = "bracket" // ?where[path][op]=value
)

// Extension keys carried by the where parameter.
const (
	XFilterFields = "x-filter-fields" // {"<wire key>": ["eq", …]}
	XFilterSyntax = "x-filter-syntax" // "json" | "bracket"
)

// InputParam is one documented list parameter: a query parameter of the
// given name whose schema is a JSON-schema fragment. Style/Explode are set
// only for the bracket where (deepObject).
type InputParam struct {
	Name        string
	Description string
	Schema      map[string]any
	Style       string
	Explode     bool
}

// InputParams documents the list parameters of res from its Inputs (defaults
// when it declared none): include first, then sort, page or cursor, limit,
// where. Documentation only — include.ResolveInputs enforces the same facts.
func InputParams(res include.Resource, limits include.Limits, syntax FilterSyntax) []InputParam {
	in, _ := include.InputsOf(res)
	out := []InputParam{{
		Name:        "include",
		Description: "Comma-separated relation paths to embed; every valid path is listed under x-include-paths.",
		Schema:      IncludeParamSchema(IncludePaths(res, limits)),
	}}
	if in.Sort.Enabled {
		keys := slices.Sorted(maps.Keys(in.Sort.Keys))
		enum := make([]any, 0, 2*len(keys))
		for _, k := range keys {
			enum = append(enum, k, "-"+k)
		}
		s := map[string]any{"type": "string", "enum": enum}
		if in.Sort.Default != "" {
			s["default"] = in.Sort.Default
		}
		out = append(out, InputParam{Name: "sort", Description: "Sort key; a leading '-' sorts descending.", Schema: s})
	}
	if in.Page.Mode == include.PageModeCursor {
		out = append(out, InputParam{Name: "cursor", Description: "Opaque continuation token from a previous response; omit for the first page.", Schema: map[string]any{"type": "string"}})
	} else {
		out = append(out, InputParam{Name: "page", Description: "Page number, 1-based.", Schema: map[string]any{"type": "integer", "minimum": 1, "default": 1}})
	}
	out = append(out, InputParam{Name: "limit", Description: "Items per page.", Schema: map[string]any{"type": "integer", "minimum": 1, "maximum": in.Page.MaxLimit, "default": in.Page.DefaultLimit}})
	if in.Filter.Enabled {
		out = append(out, whereParam(in.Filter, syntax))
	}
	return out
}

// whereParam builds the where parameter for one filter vocabulary. Both
// syntaxes carry the same x-filter-fields matrix ({wire key: allowed ops});
// only the bracket form spells the field/op nesting out as a schema, since
// that is what a deepObject parameter validates against.
func whereParam(f include.FilterInputs, syntax FilterSyntax) InputParam {
	fields := map[string]any{}
	props := map[string]any{}
	for _, name := range slices.Sorted(maps.Keys(f.Fields)) {
		col := f.Fields[name]
		ops := include.FilterOpsFor(col.Type)
		names := make([]any, 0, len(ops))
		opProps := map[string]any{}
		for _, op := range ops {
			names = append(names, string(op))
			scalar := map[string]any{"type": jsonTypeOf(col.Type)}
			if op == include.OpIn || op == include.OpNin {
				opProps[string(op)] = map[string]any{"type": "array", "items": scalar}
			} else {
				opProps[string(op)] = scalar
			}
		}
		fields[name] = names
		props[name] = map[string]any{"type": "object", "properties": opProps}
	}
	if syntax == FilterBracket {
		return InputParam{
			Name:        "where",
			Description: "Filter conditions as where[path][op]=value; siblings are AND-ed, where[or][i][...] groups alternatives.",
			Schema:      map[string]any{"type": "object", "properties": props, XFilterSyntax: "bracket", XFilterFields: fields},
			Style:       "deepObject",
			Explode:     true,
		}
	}
	return InputParam{
		Name:        "where",
		Description: `Filter as a JSON object: {"<path>": {"<op>": <value>}}, {"and": [...]}, {"or": [...]}.`,
		Schema:      map[string]any{"type": "string", XFilterSyntax: "json", XFilterFields: fields},
	}
}

// jsonTypeOf is the JSON scalar type a filterable column's values take.
func jsonTypeOf(t reflect.Type) string {
	if t == nil {
		return "string"
	}
	switch t.Kind() {
	case reflect.Bool:
		return "boolean"
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64,
		reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64:
		return "integer"
	case reflect.Float32, reflect.Float64:
		return "number"
	}
	return "string"
}
