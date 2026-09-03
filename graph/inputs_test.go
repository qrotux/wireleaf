package graph

import (
	"reflect"
	"strings"
	"testing"

	"github.com/qrotux/wireleaf/include"
)

type inRow struct {
	ID, Title string
	Year      int
}

type InWire struct {
	ID    string `json:"id" col:"id"`
	Title string `json:"title" col:"title,sort,filter"`
	Year  int    `json:"year" col:"pub_year,sort,filter"`
}

type InPlainWire struct {
	ID string `json:"id"`
}

func inputsGraph(t *testing.T, in Inputs) (*Graph, error) {
	t.Helper()
	b := NewBuilder()
	n := Node[inRow, InWire](b, "InBook").
		Wire(func(r inRow, _ *include.Ctx) InWire { return InWire{ID: r.ID, Title: r.Title, Year: r.Year} }).
		PrimaryKey(func(r inRow) string { return r.ID }).
		Inputs(in)
	b.Root(n)
	return b.Compile()
}

func TestInputsCompileDerivesKeys(t *testing.T) {
	g, err := inputsGraph(t, Inputs{
		Sort:       SortInput{Enabled: true, Default: "-year"},
		Filter:     FilterInput{Enabled: true},
		Pagination: PageInput{Mode: include.PageModeCursor, DefaultLimit: 5, MaxLimit: 50},
	})
	if err != nil {
		t.Fatalf("Compile: %v", err)
	}
	in, ok := include.InputsOf(g.Resource("InBook"))
	if !ok {
		t.Fatal("declared inputs not reported")
	}
	if in.Sort.Keys["title"] != "title" || in.Sort.Keys["year"] != "pub_year" || len(in.Sort.Keys) != 2 {
		t.Errorf("Sort.Keys = %v", in.Sort.Keys)
	}
	if in.Sort.Default != "-year" {
		t.Errorf("Sort.Default = %q", in.Sort.Default)
	}
	if _, ok := in.Filter.Fields["title"]; !ok || len(in.Filter.Fields) != 2 {
		t.Errorf("Filter.Fields = %v", in.Filter.Fields)
	}
	if in.Page != (include.PageInputs{Mode: include.PageModeCursor, DefaultLimit: 5, MaxLimit: 50}) {
		t.Errorf("Page = %+v", in.Page)
	}
}

func TestInputsDefaultsWhenUndeclared(t *testing.T) {
	b := NewBuilder()
	n := Node[inRow, InPlainWire](b, "InPlain").
		Wire(func(r inRow, _ *include.Ctx) InPlainWire { return InPlainWire{ID: r.ID} }).
		PrimaryKey(func(r inRow) string { return r.ID })
	b.Root(n)
	g, err := b.Compile()
	if err != nil {
		t.Fatal(err)
	}
	in, ok := include.InputsOf(g.Resource("InPlain"))
	// include.Inputs carries maps, so it is not comparable with ==.
	if ok || !reflect.DeepEqual(in, include.DefaultInputs()) {
		t.Fatalf("undeclared = %+v, %v", in, ok)
	}
}

func TestInputsFindings(t *testing.T) {
	cases := map[string]struct {
		in   Inputs
		want string
	}{
		"default not sortable": {Inputs{Sort: SortInput{Enabled: true, Default: "id"}}, `Inputs.Sort.Default "id" is not a sortable column`},
		"default without sort": {Inputs{Sort: SortInput{Default: "title"}}, "Inputs.Sort.Default set but Sort is not enabled"},
		"negative limit":       {Inputs{Pagination: PageInput{DefaultLimit: -1}}, "Inputs.Pagination limits must not be negative"},
		"default over max":     {Inputs{Pagination: PageInput{DefaultLimit: 50, MaxLimit: 10}}, "Inputs.Pagination.DefaultLimit 50 exceeds MaxLimit 10"},
		"unknown mode":         {Inputs{Pagination: PageInput{Mode: "keyset"}}, `Inputs.Pagination.Mode "keyset" is not offset or cursor`},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			_, err := inputsGraph(t, tc.in)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("err = %v, want %q", err, tc.want)
			}
		})
	}
}

func TestInputsFindingsNoColumns(t *testing.T) {
	b := NewBuilder()
	n := Node[inRow, InPlainWire](b, "InPlain").
		Wire(func(r inRow, _ *include.Ctx) InPlainWire { return InPlainWire{ID: r.ID} }).
		PrimaryKey(func(r inRow) string { return r.ID }).
		Inputs(Inputs{Sort: SortInput{Enabled: true}, Filter: FilterInput{Enabled: true}})
	b.Root(n)
	_, err := b.Compile()
	for _, want := range []string{"Inputs.Sort enabled but no wire field carries col:\"…,sort\"", "Inputs.Filter enabled but no wire field carries col:\"…,filter\""} {
		if err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("err = %v, want %q", err, want)
		}
	}
}

func TestInputsViaSpec(t *testing.T) {
	b := NewBuilder()
	h := Add(b, Spec[inRow, InWire]{
		Name:       "InBook",
		Wire:       func(r inRow, _ *include.Ctx) InWire { return InWire{ID: r.ID} },
		PrimaryKey: func(r inRow) string { return r.ID },
		Inputs:     &Inputs{Sort: SortInput{Enabled: true}},
	})
	b.Root(h)
	g, err := b.Compile()
	if err != nil {
		t.Fatal(err)
	}
	if in, ok := include.InputsOf(g.Resource("InBook")); !ok || !in.Sort.Enabled {
		t.Fatalf("Spec.Inputs not applied: %+v %v", in, ok)
	}
}

// TestInputsMaxLimitAloneCapsDefault: lowering only the cap is a complete
// declaration. The package default (20) would exceed a MaxLimit of 10, so the
// UNDECLARED default settles at the cap instead of failing compile; an
// explicit default above the cap stays a contradiction
// (TestInputsFindings/"default over max").
func TestInputsMaxLimitAloneCapsDefault(t *testing.T) {
	g, err := inputsGraph(t, Inputs{Pagination: PageInput{MaxLimit: 10}})
	if err != nil {
		t.Fatalf("PageInput{MaxLimit: 10} must compile: %v", err)
	}
	in, ok := include.InputsOf(g.Resource("InBook"))
	if !ok {
		t.Fatal("declared inputs not reported")
	}
	if in.Page != (include.PageInputs{Mode: include.PageModeOffset, DefaultLimit: 10, MaxLimit: 10}) {
		t.Errorf("Page = %+v, want DefaultLimit 10 / MaxLimit 10", in.Page)
	}
	// A cap ABOVE the package default leaves the default where it was.
	g, err = inputsGraph(t, Inputs{Pagination: PageInput{MaxLimit: 60}})
	if err != nil {
		t.Fatalf("PageInput{MaxLimit: 60}: %v", err)
	}
	in, _ = include.InputsOf(g.Resource("InBook"))
	if in.Page.DefaultLimit != include.DefaultPageLimit || in.Page.MaxLimit != 60 {
		t.Errorf("Page = %+v, want %d / 60", in.Page, include.DefaultPageLimit)
	}
}

// TestInputsDeclaredZeroIsTheDefaultContract pins that declaring Inputs{} is
// reported as declared and compiles to exactly the undeclared contract: offset
// pagination, 20/100, no sort, no filter.
func TestInputsDeclaredZeroIsTheDefaultContract(t *testing.T) {
	g, err := inputsGraph(t, Inputs{})
	if err != nil {
		t.Fatalf("Inputs{}: %v", err)
	}
	in, ok := include.InputsOf(g.Resource("InBook"))
	if !ok {
		t.Fatal("Inputs{} must count as declared")
	}
	if in.Sort.Enabled || in.Filter.Enabled || in.Page != (include.PageInputs{Mode: include.PageModeOffset, DefaultLimit: include.DefaultPageLimit, MaxLimit: include.DefaultMaxPageLimit}) {
		t.Fatalf("Inputs{} = %+v, want the undeclared contract", in)
	}
}
