package include

import (
	"errors"
	"net/url"
	"reflect"
	"strings"
	"testing"
	"time"
)

func TestParseFilterQueryGrammar(t *testing.T) {
	root := filterRoot(t)
	cases := map[string]struct {
		in   string
		want Filter
	}{
		"cond":                 {"where[name][eq]=x", FilterCond{Field: "name", Op: OpEq, Value: "x"}},
		"coerce":               {"where[age][gt]=3", FilterCond{Field: "age", Op: OpGt, Value: int64(3)}},
		"bool":                 {"where[active][eq]=true", FilterCond{Field: "active", Op: OpEq, Value: true}},
		"in":                   {"where[age][in]=1,2", FilterCond{Field: "age", Op: OpIn, Value: []any{int64(1), int64(2)}}},
		"nin":                  {"where[name][nin]=a,b", FilterCond{Field: "name", Op: OpNin, Value: []any{"a", "b"}}},
		"and top":              {"where[age][gt]=3&where[name][eq]=x", FilterAnd{FilterCond{Field: "age", Op: OpGt, Value: int64(3)}, FilterCond{Field: "name", Op: OpEq, Value: "x"}}},
		"or":                   {"where[or][0][name][eq]=x&where[or][1][age][gt]=3", FilterOr{FilterCond{Field: "name", Op: OpEq, Value: "x"}, FilterCond{Field: "age", Op: OpGt, Value: int64(3)}}},
		"or member and":        {"where[or][0][age][gt]=3&where[or][0][name][eq]=x", FilterOr{FilterAnd{FilterCond{Field: "age", Op: OpGt, Value: int64(3)}, FilterCond{Field: "name", Op: OpEq, Value: "x"}}}},
		"and group":            {"where[and][0][age][gt]=3&where[and][1][name][eq]=x", FilterAnd{FilterCond{Field: "age", Op: OpGt, Value: int64(3)}, FilterCond{Field: "name", Op: OpEq, Value: "x"}}},
		"group plus cond":      {"where[name][eq]=x&where[or][0][age][gt]=3", FilterAnd{FilterOr{FilterCond{Field: "age", Op: OpGt, Value: int64(3)}}, FilterCond{Field: "name", Op: OpEq, Value: "x"}}},
		"hop":                  {"where[works.title][eq]=x", FilterCond{Path: []FilterStep{{Key: "works", Quant: QuantAny}}, Field: "title", Op: OpEq, Value: "x"}},
		"hop quant":            {"where[works.title~][eq]=x", FilterCond{Path: []FilterStep{{Key: "works", Quant: QuantNone}}, Field: "title", Op: OpEq, Value: "x"}},
		"unknown stays string": {"where[ghost][eq]=3", FilterCond{Field: "ghost", Op: OpEq, Value: "3"}},
		"other keys ignored":   {"page=2&where[name][eq]=x", FilterCond{Field: "name", Op: OpEq, Value: "x"}},
		// Indices are ordered numerically, not lexically: 2 comes before 10.
		"indices numeric": {"where[or][10][name][eq]=b&where[or][2][name][eq]=a", FilterOr{FilterCond{Field: "name", Op: OpEq, Value: "a"}, FilterCond{Field: "name", Op: OpEq, Value: "b"}}},
		// Groups nest: an or-group whose member is an and-group.
		"nested groups": {"where[or][0][and][0][age][gt]=3&where[or][0][and][1][name][eq]=x", FilterOr{FilterAnd{FilterCond{Field: "age", Op: OpGt, Value: int64(3)}, FilterCond{Field: "name", Op: OpEq, Value: "x"}}}},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			v, err := url.ParseQuery(tc.in)
			if err != nil {
				t.Fatalf("ParseQuery: %v", err)
			}
			got, err := ParseFilterQuery(root, v)
			if err != nil {
				t.Fatalf("err = %v", err)
			}
			if !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("got %#v\nwant %#v", got, tc.want)
			}
		})
	}
}

// TestParseFilterQueryCoerceTime: a time.Time column takes RFC 3339.
func TestParseFilterQueryCoerceTime(t *testing.T) {
	root := &fRes{name: "Stamped", cols: map[string]Column{"at": {Col: "at", Type: reflect.TypeFor[time.Time](), Filterable: true}}}
	f, err := ParseFilterQuery(root, url.Values{"where[at][gt]": {"2024-01-02T03:04:05Z"}})
	if err != nil {
		t.Fatalf("err = %v", err)
	}
	want := FilterCond{Field: "at", Op: OpGt, Value: time.Date(2024, 1, 2, 3, 4, 5, 0, time.UTC)}
	if !reflect.DeepEqual(f, want) {
		t.Fatalf("got %#v\nwant %#v", f, want)
	}
	if _, err := ParseFilterQuery(root, url.Values{"where[at][gt]": {"yesterday"}}); err == nil {
		t.Fatal("a non-RFC-3339 time must be refused")
	}
}

func TestParseFilterQueryEmpty(t *testing.T) {
	f, err := ParseFilterQuery(filterRoot(t), url.Values{"page": {"1"}})
	if err != nil || f != nil {
		t.Fatalf("no where keys must yield nil, got %v %v", f, err)
	}
}

// TestParseFilterQueryNilRoot: without a root there is no graph to walk, the
// same refusal ParseFilterJSON makes — but no where keys is still no filter.
func TestParseFilterQueryNilRoot(t *testing.T) {
	if f, err := ParseFilterQuery(nil, url.Values{"page": {"1"}}); f != nil || err != nil {
		t.Fatalf("got %v %v, want nil nil", f, err)
	}
	_, err := ParseFilterQuery(nil, url.Values{"where[name][eq]": {"x"}})
	var ie *Error
	if !errors.As(err, &ie) || ie.Code != INVALID_FILTER || ie.Path != "" || ie.Status != 400 {
		t.Fatalf("err = %v, want *Error INVALID_FILTER with empty path", err)
	}
}

func TestParseFilterQueryErrors(t *testing.T) {
	root := filterRoot(t)
	for name, in := range map[string]string{
		"missing op":       "where[name]=x",
		"empty bracket":    "where[][eq]=x",
		"extra bracket":    "where[name][eq][x]=1",
		"unclosed bracket": "where[name][eq=x",
		"trailing junk":    "where[name]x=1",
		"unknown op":       "where[name][like]=x",
		"dup key":          "where[name][eq]=x&where[name][eq]=y",
		"bad index":        "where[or][a][name][eq]=x",
		"bad coercion":     "where[age][gt]=three",
		"bad list member":  "where[age][in]=1,two",
		"bad quantifier":   "where[self.name~][eq]=x",
	} {
		t.Run(name, func(t *testing.T) {
			v, err := url.ParseQuery(in)
			if err != nil {
				t.Fatalf("ParseQuery: %v", err)
			}
			_, err = ParseFilterQuery(root, v)
			var ie *Error
			if !errors.As(err, &ie) || ie.Code != INVALID_FILTER || ie.Status != 400 {
				t.Fatalf("err = %v, want INVALID_FILTER", err)
			}
		})
	}
}

// TestParseFilterQueryErrorPaths pins Error.Path to the INVALID_FILTER
// conventions — clipped client text and never prose: the path for a condition
// fault, "<path>:<op>" for an operator fault, "<path>:<value>" for a coercion
// fault, "<path>:<quant>" for a quantifier fault, and the whole bracket key for
// a structural fault, which has read no path yet.
func TestParseFilterQueryErrorPaths(t *testing.T) {
	root := filterRoot(t)
	for name, tc := range map[string]struct{ in, want string }{
		"missing op":       {"where[name]=x", "where[name]"},
		"empty bracket":    {"where[][eq]=x", "where[][eq]"},
		"extra bracket":    {"where[name][eq][x]=1", "where[name][eq][…"},
		"unclosed bracket": {"where[name][eq=x", "where[name][eq"},
		"trailing junk":    {"where[name]x=1", "where[name]x"},
		"unknown op":       {"where[name][like]=x", "name:like"},
		"dup key":          {"where[name][eq]=x&where[name][eq]=y", "where[name][eq]"},
		"bad index":        {"where[or][a][name][eq]=x", "where[or][a][nam…"},
		"bad coercion":     {"where[age][gt]=three", "age:three"},
		"bad list member":  {"where[age][in]=1,two", "age:two"},
		"bad quantifier":   {"where[self.name~][eq]=x", "self.name~:none"},
	} {
		t.Run(name, func(t *testing.T) {
			v, err := url.ParseQuery(tc.in)
			if err != nil {
				t.Fatalf("ParseQuery: %v", err)
			}
			_, err = ParseFilterQuery(root, v)
			var ie *Error
			if !errors.As(err, &ie) {
				t.Fatalf("err = %v, want *Error", err)
			}
			if ie.Path != tc.want {
				t.Errorf("path = %q, want %q", ie.Path, tc.want)
			}
		})
	}
}

// TestParseFilterQueryErrorPathBounded: every echoed fragment is raw client
// text, so no key and no value may size the 400 body.
func TestParseFilterQueryErrorPathBounded(t *testing.T) {
	root := filterRoot(t)
	long := strings.Repeat("z", 200)
	for name, in := range map[string]string{
		"long key":   "where[" + long + "][like]=1",
		"long value": "where[age][gt]=" + long,
	} {
		t.Run(name, func(t *testing.T) {
			v, err := url.ParseQuery(in)
			if err != nil {
				t.Fatalf("ParseQuery: %v", err)
			}
			_, err = ParseFilterQuery(root, v)
			var ie *Error
			if !errors.As(err, &ie) {
				t.Fatalf("err = %v, want *Error", err)
			}
			if len(ie.Path) > 48 {
				t.Errorf("path = %q (%d bytes), want the client text bounded", ie.Path, len(ie.Path))
			}
		})
	}
}
