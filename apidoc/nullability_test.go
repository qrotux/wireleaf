package apidoc

import (
	"encoding/json"
	"reflect"
	"testing"
)

type nullFixture struct {
	Plain    string  `json:"plain"`
	Null     *string `json:"null"`
	Optional *string `json:"optional,omitempty"`
	OptVal   int     `json:"optVal,omitempty"`
	OptZero  int     `json:"optZero,omitzero"`
	ZeroPtr  *string `json:"zeroPtr,omitzero"`
}

func TestNullabilityVerdicts(t *testing.T) {
	ty := reflect.TypeOf(nullFixture{})
	cases := map[string]Verdict{
		"Plain": VerdictPlain, "Null": VerdictNull,
		"Optional": VerdictOptional, "OptVal": VerdictOptional,
		"OptZero": VerdictOptional, "ZeroPtr": VerdictOptional,
	}
	for name, want := range cases {
		f, ok := ty.FieldByName(name)
		if !ok {
			t.Fatalf("no field %s", name)
		}
		if got := DefaultNullability(f); got != want {
			t.Errorf("%s: got %v want %v", name, got, want)
		}
	}
}

// Pins that the policy IS the rule encoding/json applies (spec §5a).
func TestNullabilityMatchesEncodingJSON(t *testing.T) {
	b, err := json.Marshal(nullFixture{Plain: "x"})
	if err != nil {
		t.Fatal(err)
	}
	got := string(b)
	// Null present as null; Optional+OptVal (omitempty) and OptZero+ZeroPtr
	// (omitzero) all omitted at their zero values.
	want := `{"plain":"x","null":null}`
	if got != want {
		t.Fatalf("got %s want %s", got, want)
	}

	// ...and the omitzero fields are present once non-zero, i.e. Optional means
	// "may be absent", never "may be null".
	s := "y"
	b, err = json.Marshal(nullFixture{Plain: "x", OptZero: 1, ZeroPtr: &s})
	if err != nil {
		t.Fatal(err)
	}
	got = string(b)
	want = `{"plain":"x","null":null,"optZero":1,"zeroPtr":"y"}`
	if got != want {
		t.Fatalf("got %s want %s", got, want)
	}
}
