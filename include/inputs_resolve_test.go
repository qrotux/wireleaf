package include

import (
	"errors"
	"strings"
	"testing"
)

// wantInputsErr asserts a rejection is a *Error carrying the exact code, path
// and status. Path is part of the contract — it is what a client reads to fix
// the request — so every case pins it, not just the code.
func wantInputsErr(t *testing.T, err error, code Code, path string) {
	t.Helper()
	var ie *Error
	if !errors.As(err, &ie) {
		t.Fatalf("err = %v, want *Error %s", err, code)
	}
	if ie.Code != code || ie.Path != path || ie.Status != 400 {
		t.Fatalf("err = {%s %q %d}, want {%s %q 400}", ie.Code, ie.Path, ie.Status, code, path)
	}
}

// inputsRes wraps a plain Resource with a DECLARED Inputs value. Columns is
// forwarded explicitly because ColumnsOf type-asserts ColumnSource on the
// OUTER value, and an embedded interface does not promote through the
// assertion when the wrapper is the one being handed to the resolver.
type inputsRes struct {
	Resource
	in Inputs
}

func (r inputsRes) Inputs() (Inputs, bool)     { return r.in, true }
func (r inputsRes) Columns() map[string]Column { return ColumnsOf(r.Resource) }

func TestResolveInputs(t *testing.T) {
	base := filterRoot(t)
	res := inputsRes{Resource: base, in: Inputs{
		Sort:   SortInputs{Enabled: true, Default: "-name", Keys: map[string]string{"name": "a_name", "age": "age"}},
		Filter: FilterInputs{Enabled: true, Fields: ColumnsOf(base)},
		Page:   PageInputs{Mode: PageModeOffset, DefaultLimit: 10, MaxLimit: 50},
	}}
	q, err := ResolveInputs(res, RawInputs{}, DefaultOptions)
	if err != nil || q.Sort != "-a_name" || q.Page != 1 || q.Limit != 10 || q.Where != nil {
		t.Fatalf("defaults: %+v %v", q, err)
	}
	q, err = ResolveInputs(res, RawInputs{Sort: "age", Page: 3, Limit: 50, Where: FilterCond{Field: "name", Op: OpEq, Value: "x"}}, DefaultOptions)
	if err != nil || q.Sort != "age" || q.Page != 3 || q.Limit != 50 || q.Where == nil {
		t.Fatalf("explicit: %+v %v", q, err)
	}
	for name, tc := range map[string]struct {
		raw  RawInputs
		code Code
		path string
	}{
		"bad sort":         {RawInputs{Sort: "-ghost"}, INVALID_SORT, "-ghost"},
		"long bad sort":    {RawInputs{Sort: "-" + strings.Repeat("g", 20)}, INVALID_SORT, "-" + strings.Repeat("g", 15) + "…"},
		"limit over max":   {RawInputs{Limit: 51}, INVALID_PAGINATION, "limit=51"},
		"negative limit":   {RawInputs{Limit: -1}, INVALID_PAGINATION, "limit=-1"},
		"negative page":    {RawInputs{Page: -2}, INVALID_PAGINATION, "page=-2"},
		"cursor in offset": {RawInputs{Cursor: "abc"}, INVALID_PAGINATION, "cursor=abc"},
		"bad where":        {RawInputs{Where: FilterCond{Field: "ghost", Op: OpEq, Value: 1}}, INVALID_FILTER, "ghost"},
	} {
		t.Run(name, func(t *testing.T) {
			_, err := ResolveInputs(res, tc.raw, DefaultOptions)
			wantInputsErr(t, err, tc.code, tc.path)
		})
	}
}

func TestResolveInputsCursorMode(t *testing.T) {
	res := inputsRes{Resource: filterRoot(t), in: Inputs{Page: PageInputs{Mode: PageModeCursor, DefaultLimit: 20, MaxLimit: 100}}}
	q, err := ResolveInputs(res, RawInputs{Cursor: "tok"}, DefaultOptions)
	if err != nil || q.Cursor != "tok" || q.Page != 0 || q.Limit != 20 {
		t.Fatalf("cursor: %+v %v", q, err)
	}
	// ?page= is not a parameter of a cursor list: refused, not ignored, so a
	// client cannot silently get the other mode's semantics.
	_, err = ResolveInputs(res, RawInputs{Page: 2}, DefaultOptions)
	wantInputsErr(t, err, INVALID_PAGINATION, "page=2")
}

func TestResolveInputsUndeclared(t *testing.T) {
	root := filterRoot(t) // plain Resource, no InputSource
	q, err := ResolveInputs(root, RawInputs{}, DefaultOptions)
	if err != nil || q.Page != 1 || q.Limit != 20 || q.Sort != "" {
		t.Fatalf("undeclared defaults: %+v %v", q, err)
	}
	// Sort is not enabled, so ANY non-empty key is refused, and the path
	// echoes the client's key verbatim (it was never looked up).
	_, err = ResolveInputs(root, RawInputs{Sort: "name"}, DefaultOptions)
	wantInputsErr(t, err, INVALID_SORT, "name")
	// The only case that exercises the "filter not enabled" rule: the where is
	// a condition the GRAPH admits, refused because the node accepts no filter
	// at all — hence the parameter name as the path, not a field path.
	_, err = ResolveInputs(root, RawInputs{Where: FilterCond{Field: "name", Op: OpEq, Value: "x"}}, DefaultOptions)
	wantInputsErr(t, err, INVALID_FILTER, "where")
}

func TestResolveInputsZeroDeclaredPage(t *testing.T) {
	res := inputsRes{Resource: filterRoot(t), in: Inputs{}}
	q, err := ResolveInputs(res, RawInputs{}, DefaultOptions)
	if err != nil || q.Limit != DefaultPageLimit || q.Page != 1 {
		t.Fatalf("zero Page: %+v, %v", q, err)
	}
	_, err = ResolveInputs(res, RawInputs{Limit: DefaultMaxPageLimit + 1}, DefaultOptions)
	wantInputsErr(t, err, INVALID_PAGINATION, "limit=101")
}

// TestResolveInputsDefaultWithSortDisabled: a hand-written InputSource can
// declare a Default while leaving Sort disabled (graph.Compile refuses the
// pair). The rejection names the Default, so the fault is attributable even
// though the client sent no sort.
func TestResolveInputsDefaultWithSortDisabled(t *testing.T) {
	res := inputsRes{Resource: filterRoot(t), in: Inputs{Sort: SortInputs{Default: "-name"}}}
	_, err := ResolveInputs(res, RawInputs{}, DefaultOptions)
	wantInputsErr(t, err, INVALID_SORT, "-name")
}
