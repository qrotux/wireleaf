package include

import (
	"encoding/json"
	"fmt"
	"testing"
)

// mustJSON marshals v to a JSON string for test assertions.
// Go's json.Marshal sorts map keys alphabetically, which is fine for comparison.
func mustJSON(v any) string {
	b, err := json.Marshal(v)
	if err != nil {
		panic(fmt.Sprintf("mustJSON: %v", err))
	}
	return string(b)
}

func TestParseInclude(t *testing.T) {
	ok := map[string]string{ // input → canonical JSON of tree
		"":                  `{}`,
		"book":              `{"book":{}}`,
		"book.cover,author": `{"author":{},"book":{"cover":{}}}`,
		"book:limit(5)":     `{"book":{":limit":"5"}}`,
		"book:tags(a|b)":    `{"book":{":tags":["a","b"]}}`,
	}
	for in, want := range ok {
		tree, err := ParseInclude(in)
		if err != nil {
			t.Fatalf("%q: %v", in, err)
		}
		if got := mustJSON(tree); got != want {
			t.Errorf("%q: got %s want %s", in, got, want)
		}
	}
	bad := []string{"123field", "a..b", "a:x(y,z)", ":rootArg"}
	for _, in := range bad {
		if _, err := ParseInclude(in); err == nil {
			t.Errorf("%q: expected INVALID_INCLUDE", in)
		}
	}
}

func TestParseExclude(t *testing.T) {
	got, err := ParseExclude("a.b,c")
	if err != nil {
		t.Fatal(err)
	}
	if fmt.Sprint(got) != "[[a b] [c]]" {
		t.Errorf("got %v", got)
	}
	if _, err := ParseExclude("1bad"); err == nil {
		t.Error("expected error")
	}
}

// --- additional coverage for acceptance criteria ---

// Whitespace trimming: surrounding spaces on tokens and segment names.
func TestParseInclude_WhitespaceTrimming(t *testing.T) {
	tree, err := ParseInclude(" book , author ")
	if err != nil {
		t.Fatal(err)
	}
	got := mustJSON(tree)
	want := `{"author":{},"book":{}}`
	if got != want {
		t.Errorf("whitespace: got %s want %s", got, want)
	}
}

// Empty parens: a:x() → arg value is the omitted marker "".
func TestParseInclude_EmptyParens(t *testing.T) {
	tree, err := ParseInclude("a:x()")
	if err != nil {
		t.Fatal(err)
	}
	got := mustJSON(tree)
	// empty parens → omitted marker → value is ""
	want := `{"a":{":x":""}}`
	if got != want {
		t.Errorf("empty parens: got %s want %s", got, want)
	}
}

// Bare arg (no parens): a:x → arg value is omitted marker "".
func TestParseInclude_BareArg(t *testing.T) {
	tree, err := ParseInclude("book:limit")
	if err != nil {
		t.Fatal(err)
	}
	got := mustJSON(tree)
	want := `{"book":{":limit":""}}`
	if got != want {
		t.Errorf("bare arg: got %s want %s", got, want)
	}
}

// Single-value parens: a:x(val) → value is string (not []string).
func TestParseInclude_SingleValueParens(t *testing.T) {
	tree, err := ParseInclude("book:limit(10)")
	if err != nil {
		t.Fatal(err)
	}
	got := mustJSON(tree)
	want := `{"book":{":limit":"10"}}`
	if got != want {
		t.Errorf("single-value: got %s want %s", got, want)
	}
}

// Multi-level nesting.
func TestParseInclude_DeepNesting(t *testing.T) {
	tree, err := ParseInclude("book.cover.image")
	if err != nil {
		t.Fatal(err)
	}
	got := mustJSON(tree)
	want := `{"book":{"cover":{"image":{}}}}`
	if got != want {
		t.Errorf("deep nesting: got %s want %s", got, want)
	}
}

// Empty exclude string → empty slice.
func TestParseExclude_Empty(t *testing.T) {
	got, err := ParseExclude("")
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 0 {
		t.Errorf("expected empty, got %v", got)
	}
}

func TestParseInclude_ArgValueBytes(t *testing.T) {
	bad := []string{"r:filter(a\x00b)", "r:filter(a\nb)", "r:filter(a\x7fb)", "r:filter(\xff)", "r:filter(x|a\tb)"}
	for _, in := range bad {
		if _, err := ParseInclude(in); err == nil {
			t.Errorf("%q: expected INVALID_INCLUDE", in)
		}
	}
	ok := map[string]string{
		"r:filter(1' OR '1'='1)": `{"r":{":filter":"1' OR '1'='1"}}`,
		"r:filter(привет)":       `{"r":{":filter":"привет"}}`,
	}
	for in, want := range ok {
		tree, err := ParseInclude(in)
		if err != nil {
			t.Fatalf("%q: %v", in, err)
		}
		if got := mustJSON(tree); got != want {
			t.Errorf("%q: got %s want %s", in, got, want)
		}
	}
}
