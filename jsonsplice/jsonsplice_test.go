package jsonsplice

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"
)

var doc = []byte(`{"id":"a1","title":"round trip","author":{"trip":"x"},"tags":["a"]}`)

func TestMemberFindsTopLevelOnly(t *testing.T) {
	v, ok, err := Member(doc, "author")
	if err != nil || !ok || !bytes.Equal(v, []byte(`{"trip":"x"}`)) {
		t.Fatalf("got %s ok=%v err=%v", v, ok, err)
	}
	if _, ok, _ := Member(doc, "trip"); ok { // nested key must not match
		t.Fatal("nested key matched at top level")
	}
}

func TestSpliceReplacesInPlace(t *testing.T) {
	out, err := Splice(doc, "title", []byte(`"T2"`))
	if err != nil {
		t.Fatal(err)
	}
	want := `{"id":"a1","title":"T2","author":{"trip":"x"},"tags":["a"]}`
	if string(out) != want {
		t.Fatalf("got %s", out)
	}
}

func TestSpliceAppendsMissingKey(t *testing.T) {
	out, _ := Splice([]byte(`{}`), "k", []byte(`1`))
	if string(out) != `{"k":1}` {
		t.Fatalf("got %s", out)
	}
	out, _ = Splice(doc, "extra", []byte(`null`))
	if string(out) != `{"id":"a1","title":"round trip","author":{"trip":"x"},"tags":["a"],"extra":null}` {
		t.Fatalf("got %s", out)
	}
}

func TestDeleteFirstMiddleAbsent(t *testing.T) {
	out, _ := Delete(doc, "id")
	if string(out) != `{"title":"round trip","author":{"trip":"x"},"tags":["a"]}` {
		t.Fatalf("got %s", out)
	}
	out, _ = Delete(doc, "author")
	if string(out) != `{"id":"a1","title":"round trip","tags":["a"]}` {
		t.Fatalf("got %s", out)
	}
	out, err := Delete(doc, "nope") // absent: no-op, no error
	if err != nil || !bytes.Equal(out, doc) {
		t.Fatalf("got %s err=%v", out, err)
	}
}

func TestInsertAfterAndFallback(t *testing.T) {
	out, _ := InsertAfter(doc, "id", "x", []byte(`2`))
	if string(out) != `{"id":"a1","x":2,"title":"round trip","author":{"trip":"x"},"tags":["a"]}` {
		t.Fatalf("got %s", out)
	}
	out, _ = InsertAfter(doc, "nope", "x", []byte(`2`)) // fallback = append
	if string(out) != `{"id":"a1","title":"round trip","author":{"trip":"x"},"tags":["a"],"x":2}` {
		t.Fatalf("got %s", out)
	}
}

func TestReorder(t *testing.T) {
	out, _ := Reorder(doc, "tags", "id")
	if string(out) != `{"tags":["a"],"id":"a1","title":"round trip","author":{"trip":"x"}}` {
		t.Fatalf("got %s", out)
	}
}

func TestEscapePreservation(t *testing.T) {
	d := []byte(`{"a":"x\"y&<>","b":1}`)
	out, _ := Splice(d, "c", []byte(`true`))
	if string(out) != `{"a":"x\"y&<>","b":1,"c":true}` {
		t.Fatalf("got %s", out)
	}
}

func TestMalformedErrors(t *testing.T) {
	for _, bad := range [][]byte{[]byte(`[1]`), []byte(`{"a":`), []byte(`{"a`), []byte(`x`)} {
		if _, err := Splice(bad, "k", []byte(`1`)); err == nil {
			t.Errorf("no error for %s", bad)
		}
		if _, _, err := Member(bad, "k"); err == nil {
			t.Errorf("Member: no error for %s", bad)
		}
	}
}

// --- extended coverage -------------------------------------------------

// Every mutator must reject malformed input, not just Splice/Member.
func TestMalformedErrorsAllFunctions(t *testing.T) {
	bads := [][]byte{
		[]byte(``),
		[]byte(`[1]`),
		[]byte(`{"a":`),
		[]byte(`{"a`),
		[]byte(`x`),
		[]byte(`{"a":1`),           // unterminated object
		[]byte(`{"a":[1,2}`),       // unbalanced nested container
		[]byte(`{"a":"unterm}`),    // unterminated string value
		[]byte(`{a:1}`),            // unquoted key
		[]byte(`{"a" 1}`),          // missing colon
		[]byte(`{"a":1} trailing`), // trailing garbage
		[]byte(`{,}`),              // leading comma
		[]byte(`{"a":1,}`),         // trailing comma
		[]byte(`{,,}`),             // commas only
		[]byte(`{"a":1,,"b":2}`),   // doubled comma
		[]byte(`{"a":1 "b":2}`),    // missing comma between members
	}
	for _, bad := range bads {
		if _, err := Splice(bad, "k", []byte(`1`)); err == nil {
			t.Errorf("Splice: no error for %q", bad)
		}
		if _, err := Delete(bad, "k"); err == nil {
			t.Errorf("Delete: no error for %q", bad)
		}
		if _, err := InsertAfter(bad, "a", "k", []byte(`1`)); err == nil {
			t.Errorf("InsertAfter: no error for %q", bad)
		}
		if _, err := Reorder(bad, "a"); err == nil {
			t.Errorf("Reorder: no error for %q", bad)
		}
		if _, _, err := Member(bad, "k"); err == nil {
			t.Errorf("Member: no error for %q", bad)
		}
	}
}

// Strict comma placement must not reject well-formed documents, whitespace
// variants included.
func TestStrictCommasAcceptValidDocs(t *testing.T) {
	for _, good := range [][]byte{
		[]byte(`{}`),
		[]byte(`{"a":1}`),
		[]byte(`{"a":1,"b":2}`),
		[]byte(" {\n\t\"a\" : 1 ,\r\n\"b\" : 2\n} "),
	} {
		if _, _, err := Member(good, "a"); err != nil {
			t.Errorf("Member(%q): unexpected error %v", good, err)
		}
	}
	for _, good := range [][]byte{
		[]byte(`[]`),
		[]byte(`[1]`),
		[]byte(`[1,2]`),
		[]byte(" [\n\t1 ,\r\n2\n] "),
	} {
		if _, err := Elements(good); err != nil {
			t.Errorf("Elements(%q): unexpected error %v", good, err)
		}
	}
}

func TestMemberAbsentNoError(t *testing.T) {
	v, ok, err := Member(doc, "nope")
	if err != nil || ok || v != nil {
		t.Fatalf("got %s ok=%v err=%v", v, ok, err)
	}
}

func TestMemberAliasesDoc(t *testing.T) {
	d := []byte(`{"a":{"b":1}}`)
	v, ok, err := Member(d, "a")
	if err != nil || !ok {
		t.Fatalf("ok=%v err=%v", ok, err)
	}
	if &v[0] != &d[5] {
		t.Fatalf("returned value does not alias doc")
	}
}

// Escaped keys in the document are matched by their decoded value.
func TestEscapedKeyMatching(t *testing.T) {
	d := []byte(`{"a\"b":1,"c\u0041d":2}`)
	v, ok, err := Member(d, `a"b`)
	if err != nil || !ok || string(v) != "1" {
		t.Fatalf("got %s ok=%v err=%v", v, ok, err)
	}
	v, ok, err = Member(d, "cAd") // A == 'A'
	if err != nil || !ok || string(v) != "2" {
		t.Fatalf("got %s ok=%v err=%v", v, ok, err)
	}
	// Replacement keeps the document's original raw key spelling.
	out, err := Splice(d, `a"b`, []byte(`9`))
	if err != nil || string(out) != `{"a\"b":9,"c\u0041d":2}` {
		t.Fatalf("got %s err=%v", out, err)
	}
	// Reorder re-emits raw key bytes verbatim.
	out, err = Reorder(d, "cAd")
	if err != nil || string(out) != `{"c\u0041d":2,"a\"b":1}` {
		t.Fatalf("got %s err=%v", out, err)
	}
	// Delete by decoded name.
	out, err = Delete(d, "cAd")
	if err != nil || string(out) != `{"a\"b":1}` {
		t.Fatalf("got %s err=%v", out, err)
	}
}

// A key spelled out inside a nested string value must never be seen as a member.
func TestDecoyInsideStringValue(t *testing.T) {
	d := []byte(`{"a":"{\"k\":1}","b":"x,\"k\":2"}`)
	if _, ok, err := Member(d, "k"); ok || err != nil {
		t.Fatalf("decoy matched: ok=%v err=%v", ok, err)
	}
	out, err := Splice(d, "k", []byte(`3`))
	if err != nil || string(out) != `{"a":"{\"k\":1}","b":"x,\"k\":2","k":3}` {
		t.Fatalf("got %s err=%v", out, err)
	}
}

func TestDeleteLastAndOnly(t *testing.T) {
	out, err := Delete(doc, "tags")
	if err != nil || string(out) != `{"id":"a1","title":"round trip","author":{"trip":"x"}}` {
		t.Fatalf("got %s err=%v", out, err)
	}
	out, err = Delete([]byte(`{"only":1}`), "only")
	if err != nil || string(out) != `{}` {
		t.Fatalf("got %s err=%v", out, err)
	}
}

func TestSpliceOnEmptyObjectVariants(t *testing.T) {
	out, err := Splice([]byte(`{ }`), "k", []byte(`1`))
	if err != nil || string(out) != `{ "k":1}` {
		t.Fatalf("got %q err=%v", out, err)
	}
	// Whitespace-bearing doc: untouched bytes stay byte-identical.
	d := []byte("{\n  \"a\" : 1\n}")
	out, err = Splice(d, "a", []byte(`2`))
	if err != nil || string(out) != "{\n  \"a\" : 2\n}" {
		t.Fatalf("got %q err=%v", out, err)
	}
}

func TestInsertAfterOnEmptyObject(t *testing.T) {
	out, err := InsertAfter([]byte(`{}`), "any", "k", []byte(`1`))
	if err != nil || string(out) != `{"k":1}` {
		t.Fatalf("got %s err=%v", out, err)
	}
}

func TestReorderUnknownAndDuplicateKeys(t *testing.T) {
	out, err := Reorder(doc, "nope", "tags", "tags", "id")
	if err != nil || string(out) != `{"tags":["a"],"id":"a1","title":"round trip","author":{"trip":"x"}}` {
		t.Fatalf("got %s err=%v", out, err)
	}
	// No keys listed: original order preserved (normalized, no whitespace).
	out, err = Reorder([]byte(`{}`))
	if err != nil || string(out) != `{}` {
		t.Fatalf("got %s err=%v", out, err)
	}
}

func TestReorderPreservesRawValues(t *testing.T) {
	d := []byte(`{"a":"x\"y&<>","b":[1,{"c":"}"}]}`)
	out, err := Reorder(d, "b")
	if err != nil || string(out) != `{"b":[1,{"c":"}"}],"a":"x\"y&<>"}` {
		t.Fatalf("got %s err=%v", out, err)
	}
}

func TestSpliceDoesNotMutateInput(t *testing.T) {
	orig := append([]byte(nil), doc...)
	if _, err := Splice(doc, "title", []byte(`"T2"`)); err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(doc, orig) {
		t.Fatalf("input mutated: %s", doc)
	}
}

// --- Elements ----------------------------------------------------------

func TestElements(t *testing.T) {
	arr := []byte(`[{"a":[1,2],"b":"]"},"x\"y",3,null,[["nested"]]]`)
	els, err := Elements(arr)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{`{"a":[1,2],"b":"]"}`, `"x\"y"`, `3`, `null`, `[["nested"]]`}
	if len(els) != len(want) {
		t.Fatalf("got %d elements: %q", len(els), els)
	}
	for i := range want {
		if string(els[i]) != want[i] {
			t.Errorf("element %d: got %s want %s", i, els[i], want[i])
		}
	}
	// Returned slices alias arr.
	if &els[0][0] != &arr[1] {
		t.Fatal("elements do not alias arr")
	}
}

func TestElementsEmptyAndWhitespace(t *testing.T) {
	els, err := Elements([]byte(`[]`))
	if err != nil || len(els) != 0 {
		t.Fatalf("got %q err=%v", els, err)
	}
	els, err = Elements([]byte("[ 1 , 2 ]"))
	if err != nil || len(els) != 2 || string(els[0]) != "1" || string(els[1]) != "2" {
		t.Fatalf("got %q err=%v", els, err)
	}
}

func TestElementsErrors(t *testing.T) {
	for _, bad := range [][]byte{
		[]byte(``),
		[]byte(`{"a":1}`),
		[]byte(`[1`),
		[]byte(`[[1]`),
		[]byte(`["unterminated]`),
		[]byte(`x`),
		[]byte(`[1] junk`),
		[]byte(`[,]`),    // leading comma
		[]byte(`[1,]`),   // trailing comma
		[]byte(`[,1]`),   // leading comma before element
		[]byte(`[1,,2]`), // doubled comma
		[]byte(`[1 2]`),  // missing comma between elements
	} {
		if _, err := Elements(bad); err == nil {
			t.Errorf("no error for %q", bad)
		}
	}
}

// encodeKey must NOT HTML-escape. A key containing '<', '>' or '&' is written
// with its raw bytes, matching what the response encoder emits (the adapter's
// jsonFormat also disables escaping); json.Marshal would produce
// "a\u003cb\u0026c" instead.
func TestSpliceKeyIsNotHTMLEscaped(t *testing.T) {
	const key = "a<b&c>d"
	got, err := Splice([]byte(`{"id":"x"}`), key, []byte(`1`))
	if err != nil {
		t.Fatalf("Splice: %v", err)
	}
	want := `{"id":"x","a<b&c>d":1}`
	if string(got) != want {
		t.Fatalf("Splice = %s, want %s", got, want)
	}
	for _, esc := range []string{"\\u003c", "\\u003e", "\\u0026"} {
		if strings.Contains(string(got), esc) {
			t.Errorf("spliced key carries the HTML escape %s: %s", esc, got)
		}
	}
	// The document still round-trips: the raw bytes are legal JSON.
	var m map[string]any
	if err := json.Unmarshal(got, &m); err != nil {
		t.Fatalf("result is not valid JSON: %v (%s)", err, got)
	}
	if _, ok := m[key]; !ok {
		t.Errorf("decoded document has no key %q: %v", key, m)
	}
}

// InsertAfter shares encodeKey, so it inherits the same guarantee.
func TestInsertAfterKeyIsNotHTMLEscaped(t *testing.T) {
	got, err := InsertAfter([]byte(`{"id":"x","z":0}`), "id", "a&b", []byte(`1`))
	if err != nil {
		t.Fatalf("InsertAfter: %v", err)
	}
	if want := `{"id":"x","a&b":1,"z":0}`; string(got) != want {
		t.Fatalf("InsertAfter = %s, want %s", got, want)
	}
}

func TestDuplicateTopLevelKeyIsMalformed(t *testing.T) {
	dup := []byte(`{"a":1,"a":2}`)
	if _, _, err := Member(dup, "a"); err == nil || !strings.Contains(err.Error(), "malformed") {
		t.Fatalf("Member: want malformed error, got %v", err)
	}
	if _, err := Splice(dup, "a", []byte(`3`)); err == nil {
		t.Fatal("Splice accepted duplicate key")
	}
	if _, err := Delete(dup, "a"); err == nil {
		t.Fatal("Delete accepted duplicate key")
	}
	if _, err := InsertAfter(dup, "a", "b", []byte(`1`)); err == nil {
		t.Fatal("InsertAfter accepted duplicate key")
	}
	if _, err := Reorder(dup, "a"); err == nil {
		t.Fatal("Reorder accepted duplicate key")
	}
	// Escaped spelling of the same key is still a duplicate.
	if _, _, err := Member([]byte(`{"a":1,"\u0061":2}`), "a"); err == nil {
		t.Fatal("escaped duplicate accepted")
	}
	// Nested duplicates are out of scope: only the top level is validated.
	if _, _, err := Member([]byte(`{"o":{"a":1,"a":2}}`), "o"); err != nil {
		t.Fatalf("nested duplicate rejected: %v", err)
	}
}

func TestScalarTokensAreValidated(t *testing.T) {
	bad := []string{`{"":A}`, `{"a":tru}`, `{"a":01}`, `{"a":1.}`, `{"a":-}`, `{"a":1e}`, `{"a":+1}`, `{"a":.5}`, `{"a":nul}`}
	for _, d := range bad {
		if _, _, err := Member([]byte(d), "a"); err == nil {
			t.Errorf("%s: accepted", d)
		}
	}
	if _, err := Elements([]byte(`[A]`)); err == nil {
		t.Error("[A]: accepted")
	}
	good := []byte(`{"a":true,"b":false,"c":null,"d":0,"e":-1.5e+3,"f":12,"g":1E2,"h":-0}`)
	for k, want := range map[string]string{"a": "true", "b": "false", "c": "null", "d": "0", "e": "-1.5e+3", "f": "12", "g": "1E2", "h": "-0"} {
		v, ok, err := Member(good, k)
		if err != nil || !ok || string(v) != want {
			t.Errorf("%s: got %s ok=%v err=%v", k, v, ok, err)
		}
	}
}
