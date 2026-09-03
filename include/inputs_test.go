package include

import "testing"

type inputsStub struct {
	Resource
	in Inputs
}

func (s inputsStub) Inputs() (Inputs, bool) { return s.in, true }

func TestInputsOf(t *testing.T) {
	def, ok := InputsOf(struct{ Resource }{})
	if ok {
		t.Fatal("a plain Resource must report no declared inputs")
	}
	if def.Page.Mode != PageModeOffset || def.Page.DefaultLimit != 20 || def.Page.MaxLimit != 100 || def.Sort.Enabled || def.Filter.Enabled {
		t.Fatalf("defaults = %+v", def)
	}
	want := Inputs{Sort: SortInputs{Enabled: true, Keys: map[string]string{"title": "title"}}, Page: PageInputs{Mode: PageModeCursor, DefaultLimit: 5, MaxLimit: 10}}
	got, ok := InputsOf(inputsStub{in: want})
	if !ok || got.Page.Mode != PageModeCursor || got.Sort.Keys["title"] != "title" {
		t.Fatalf("InputsOf(source) = %+v, %v", got, ok)
	}
}

// TestInputsOfSettlesZeroPage pins that a hand-written InputSource may leave
// Page fields zero and still get the compiled contract: zeros take the
// package defaults, and a lone MaxLimit below the default caps it. Both the
// resolver and apidoc.InputParams read through InputsOf, so neither sees a
// zero limit.
func TestInputsOfSettlesZeroPage(t *testing.T) {
	got, _ := InputsOf(inputsStub{in: Inputs{}})
	if got.Page != (PageInputs{Mode: PageModeOffset, DefaultLimit: DefaultPageLimit, MaxLimit: DefaultMaxPageLimit}) {
		t.Fatalf("zero Page = %+v", got.Page)
	}
	got, _ = InputsOf(inputsStub{in: Inputs{Page: PageInputs{MaxLimit: 10}}})
	if got.Page != (PageInputs{Mode: PageModeOffset, DefaultLimit: 10, MaxLimit: 10}) {
		t.Fatalf("MaxLimit-only Page = %+v", got.Page)
	}
}
