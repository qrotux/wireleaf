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
