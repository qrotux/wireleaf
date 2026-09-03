// inputs.go — the node's LIST-INPUT declaration and its compilation into
// include.Inputs. The declaration switches roles on and bounds them; the
// vocabulary itself is derived from the wire struct's `col` tags, so a sort key
// or filter field can never name a column the node does not bind.

package graph

import (
	"strings"

	"github.com/qrotux/wireleaf/include"
)

// Inputs declares what a node accepts on a LIST operation: sort keys, filter
// fields and pagination. The vocabulary itself comes from the wire struct's
// `col` tags (`col:"title,sort,filter"`); the declaration switches roles on
// and bounds them. Compile derives include.Inputs from the two, ResolveInputs
// enforces it and apidoc.InputParams documents it. A node without Inputs
// accepts offset pagination with the default limits and nothing else.
type Inputs struct {
	Sort       SortInput
	Filter     FilterInput
	Pagination PageInput
}

// SortInput enables ?sort= over the node's `sort` columns.
type SortInput struct {
	Enabled bool
	// Default is applied when the client sends no sort: a wire json key with
	// an optional "-" prefix ("-year"). Must name a `sort` column.
	Default string
}

// FilterInput enables ?where= over the node's `filter` columns.
type FilterInput struct{ Enabled bool }

// PageInput bounds pagination. A zero Mode is offset. A zero MaxLimit takes
// the include default (100); a zero DefaultLimit takes the include default
// (20) capped by MaxLimit, so PageInput{MaxLimit: 10} compiles to 10/10. An
// EXPLICIT DefaultLimit above MaxLimit is a compile finding.
type PageInput struct {
	Mode         include.PageMode
	DefaultLimit int
	MaxLimit     int
}

// Inputs declares the node's list inputs. Nothing is validated here; Compile
// checks the declaration against the col tags.
func (h *NodeHandle[Row, W]) Inputs(in Inputs) *NodeHandle[Row, W] {
	h.b.mustLive()
	h.spec.inputs, h.spec.inputsSet = in, true
	return h
}

// compileInputs derives the resolved include.Inputs of one node from its
// declaration and column bindings, reporting inconsistencies as findings. An
// undeclared node yields the defaults and false.
func compileInputs(fs *findingList, n *nodeSpec, cols map[string]include.Column) (include.Inputs, bool) {
	out := include.DefaultInputs()
	if !n.inputsSet {
		return out, false
	}
	in := n.inputs

	if in.Sort.Enabled {
		keys := map[string]string{}
		for name, c := range cols {
			if c.Sortable {
				keys[name] = c.Col
			}
		}
		if len(keys) == 0 {
			fs.add(n.name, "", `Inputs.Sort enabled but no wire field carries col:"…,sort"`)
		}
		out.Sort = include.SortInputs{Enabled: true, Keys: keys}
		if d := in.Sort.Default; d != "" {
			if _, ok := keys[strings.TrimPrefix(d, "-")]; !ok {
				fs.add(n.name, "", "Inputs.Sort.Default %q is not a sortable column", d)
			}
			out.Sort.Default = d
		}
	} else if in.Sort.Default != "" {
		fs.add(n.name, "", "Inputs.Sort.Default set but Sort is not enabled")
	}

	if in.Filter.Enabled {
		fields := map[string]include.Column{}
		for name, c := range cols {
			if c.Filterable {
				fields[name] = c
			}
		}
		if len(fields) == 0 {
			fs.add(n.name, "", `Inputs.Filter enabled but no wire field carries col:"…,filter"`)
		}
		out.Filter = include.FilterInputs{Enabled: true, Fields: fields}
	}

	p := in.Pagination
	switch p.Mode {
	case "", include.PageModeOffset:
		out.Page.Mode = include.PageModeOffset
	case include.PageModeCursor:
		out.Page.Mode = include.PageModeCursor
	default:
		fs.add(n.name, "", "Inputs.Pagination.Mode %q is not offset or cursor", string(p.Mode))
	}
	if p.DefaultLimit < 0 || p.MaxLimit < 0 {
		fs.add(n.name, "", "Inputs.Pagination limits must not be negative")
	}
	if p.DefaultLimit > 0 {
		out.Page.DefaultLimit = p.DefaultLimit
	}
	if p.MaxLimit > 0 {
		out.Page.MaxLimit = p.MaxLimit
	}
	// An UNDECLARED default settles under the cap: a node that only lowers
	// MaxLimit (PageInput{MaxLimit: 10}) must compile, and its default page is
	// the cap, not the package default it exceeds. An EXPLICIT default above
	// the cap stays a finding — that one is a contradiction in the
	// declaration, not an omission.
	if p.DefaultLimit == 0 && out.Page.DefaultLimit > out.Page.MaxLimit {
		out.Page.DefaultLimit = out.Page.MaxLimit
	}
	if out.Page.DefaultLimit > out.Page.MaxLimit {
		fs.add(n.name, "", "Inputs.Pagination.DefaultLimit %d exceeds MaxLimit %d", out.Page.DefaultLimit, out.Page.MaxLimit)
	}
	return out, true
}
