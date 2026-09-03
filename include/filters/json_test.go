package filters_test

import (
	"errors"
	"reflect"
	"strconv"
	"strings"
	"testing"

	"github.com/qrotux/wireleaf/include"
	"github.com/qrotux/wireleaf/include/filters"
)

func TestParseJSONGrammar(t *testing.T) {
	root := filterRoot(t)
	cases := map[string]struct {
		in   string
		want include.Filter
	}{
		"cond":                {`{"name":{"eq":"x"}}`, include.FilterCond{Field: "name", Op: include.OpEq, Value: "x"}},
		"and":                 {`{"and":[{"name":{"eq":"x"}},{"age":{"gt":3}}]}`, include.FilterAnd{include.FilterCond{Field: "name", Op: include.OpEq, Value: "x"}, include.FilterCond{Field: "age", Op: include.OpGt, Value: float64(3)}}},
		"or":                  {`{"or":[{"name":{"eq":"x"}}]}`, include.FilterOr{include.FilterCond{Field: "name", Op: include.OpEq, Value: "x"}}},
		"hop default any":     {`{"works.title":{"eq":"x"}}`, include.FilterCond{Path: []include.FilterStep{{Key: "works", Quant: include.QuantAny}}, Field: "title", Op: include.OpEq, Value: "x"}},
		"hop all on segment":  {`{"works*.title":{"eq":"x"}}`, include.FilterCond{Path: []include.FilterStep{{Key: "works", Quant: include.QuantAll}}, Field: "title", Op: include.OpEq, Value: "x"}},
		"hop none on field":   {`{"works.title~":{"eq":"x"}}`, include.FilterCond{Path: []include.FilterStep{{Key: "works", Quant: include.QuantNone}}, Field: "title", Op: include.OpEq, Value: "x"}},
		"to-one hop no quant": {`{"self.name":{"eq":"x"}}`, include.FilterCond{Path: []include.FilterStep{{Key: "self"}}, Field: "name", Op: include.OpEq, Value: "x"}},
		// An unknown edge key is NOT the parser's fault to report: the path
		// passes through quantifier-free for ResolveFilter to reject with a
		// bounded path.
		"unknown edge passes through": {`{"ghost.title":{"eq":"x"}}`, include.FilterCond{Path: []include.FilterStep{{Key: "ghost"}}, Field: "title", Op: include.OpEq, Value: "x"}},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			got, err := filters.ParseJSON(root, []byte(tc.in))
			if err != nil {
				t.Fatalf("err = %v", err)
			}
			if !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("got %#v\nwant %#v", got, tc.want)
			}
		})
	}
}

func TestParseJSONErrors(t *testing.T) {
	root := filterRoot(t)
	for name, in := range map[string]string{
		"not object":                    `[1]`,
		"two keys":                      `{"name":{"eq":"x"},"age":{"gt":1}}`,
		"no keys":                       `{}`,
		"and not array":                 `{"and":{}}`,
		"nested member bad":             `{"and":[{"name":{"like":"x"}}]}`,
		"cond value not object":         `{"name":"x"}`,
		"two ops":                       `{"name":{"eq":"x","ne":"y"}}`,
		"unknown op":                    `{"name":{"like":"x"}}`,
		"field quant with unknown path": `{"ghost.title~":{"eq":1}}`,
		"field quant on to-one path":    `{"self.name~":{"eq":1}}`,
		"quant given twice":             `{"works*.title~":{"eq":1}}`,
	} {
		t.Run(name, func(t *testing.T) {
			_, err := filters.ParseJSON(root, []byte(in))
			var ie *include.Error
			if !errors.As(err, &ie) || ie.Code != include.INVALID_FILTER || ie.Status != 400 {
				t.Fatalf("err = %v, want *Error INVALID_FILTER 400", err)
			}
		})
	}
}

// TestParseJSONErrorPaths pins Error.Path to the conventions INVALID_FILTER
// documents — the key, "<key>:<op>", "<path>:<quant>", or "" for a structural
// fault with no key — and never an English sentence.
func TestParseJSONErrorPaths(t *testing.T) {
	root := filterRoot(t)
	for name, tc := range map[string]struct{ in, want string }{
		"not object":                    {`[1]`, ""},
		"no keys":                       {`{}`, ""},
		"two keys":                      {`{"age":{"gt":1},"name":{"eq":"x"}}`, "age (+1 keys)"},
		"and not array":                 {`{"and":{}}`, "and"},
		"cond value not object":         {`{"name":"x"}`, "name"},
		"two ops":                       {`{"name":{"eq":"x","ne":"y"}}`, "name"},
		"unknown op":                    {`{"name":{"like":"x"}}`, "name:like"},
		"field quant with unknown path": {`{"ghost.title~":{"eq":1}}`, "ghost.title~:none"},
		"field quant on to-one path":    {`{"self.name~":{"eq":1}}`, "self.name~:none"},
		"quant given twice":             {`{"works*.title~":{"eq":1}}`, "works*.title~:none"},
	} {
		t.Run(name, func(t *testing.T) {
			_, err := filters.ParseJSON(root, []byte(tc.in))
			var ie *include.Error
			if !errors.As(err, &ie) {
				t.Fatalf("err = %v, want *Error", err)
			}
			if ie.Path != tc.want {
				t.Errorf("path = %q, want %q", ie.Path, tc.want)
			}
		})
	}
}

// TestParseJSONErrorPathBounded: every echoed path is raw client text, so
// neither one long key nor a thousand keys may size the 400 body.
func TestParseJSONErrorPathBounded(t *testing.T) {
	root := filterRoot(t)
	long := strings.Repeat("z", 200)
	keys := make([]string, 0, 50)
	for i := 0; i < 50; i++ {
		keys = append(keys, `"`+long+strconv.Itoa(i)+`":{"eq":1}`)
	}
	for name, in := range map[string]string{
		"long key":  `{"` + long + `":{"like":1}}`,
		"many keys": `{` + strings.Join(keys, ",") + `}`,
	} {
		t.Run(name, func(t *testing.T) {
			_, err := filters.ParseJSON(root, []byte(in))
			var ie *include.Error
			if !errors.As(err, &ie) {
				t.Fatalf("err = %v, want *Error", err)
			}
			if len(ie.Path) > 48 {
				t.Errorf("path = %q (%d bytes), want the client text bounded", ie.Path, len(ie.Path))
			}
		})
	}
}

// TestParseJSONNilRoot: without a root there is no graph to resolve the
// field-suffix sugar against, the same refusal ResolveFilter makes.
func TestParseJSONNilRoot(t *testing.T) {
	_, err := filters.ParseJSON(nil, []byte(`{"name":{"eq":"x"}}`))
	var ie *include.Error
	if !errors.As(err, &ie) || ie.Code != include.INVALID_FILTER || ie.Path != "" || ie.Status != 400 {
		t.Fatalf("err = %v, want *Error INVALID_FILTER with empty path", err)
	}
}
