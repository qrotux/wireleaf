// The tests live in package crosscheck_test on purpose: everything a consumer
// (Task 20's conformance table) touches is exported, and an external test
// package is the proof.
package crosscheck_test

import (
	"strings"
	"testing"

	"github.com/qrotux/wireleaf/apidoc"
	"github.com/qrotux/wireleaf/apidoc/crosscheck"
)

// bookComponents is the fixture exercising the two shapes the doc layer emits
// for nullability: a type-array scalar ("note") and an anyOf-with-null arm
// around a $ref ("author").
func bookComponents() map[string]apidoc.Schema {
	book := apidoc.RawFragment(map[string]any{
		"type":     "object",
		"required": []any{"id"},
		"properties": map[string]any{
			"id":   map[string]any{"type": "string"},
			"note": map[string]any{"type": []any{"string", "null"}},
			"author": map[string]any{"anyOf": []any{
				map[string]any{"$ref": apidoc.RefPrefix + "Author"},
				map[string]any{"type": "null"},
			}},
		},
	})
	author := apidoc.RawFragment(map[string]any{
		"type":     "object",
		"required": []any{"id", "name"},
		"properties": map[string]any{
			"id":   map[string]any{"type": "string"},
			"name": map[string]any{"type": "string"},
		},
	})
	return map[string]apidoc.Schema{"Book": book, "Author": author}
}

func TestCompileValidatesGoodSamples(t *testing.T) {
	v, err := crosscheck.Compile(bookComponents(), "Book")
	if err != nil {
		t.Fatalf("Compile: %v", err)
	}
	good := []string{
		`{"id":"b1"}`,
		`{"id":"b1","note":null,"author":null}`,
		`{"id":"b1","note":"hi","author":{"id":"a1","name":"Ann"}}`,
	}
	for _, s := range good {
		if err := v.Validate([]byte(s)); err != nil {
			t.Errorf("Validate(%s): unexpected error: %v", s, err)
		}
	}
}

func TestCompileRejectsBadSamples(t *testing.T) {
	v, err := crosscheck.Compile(bookComponents(), "Book")
	if err != nil {
		t.Fatalf("Compile: %v", err)
	}
	bad := map[string]string{
		"missing required id":  `{"note":"hi"}`,
		"wrong type for id":    `{"id":42}`,
		"null where forbidden": `{"id":null}`,
		"author non-null-blob": `{"id":"b1","author":"Ann"}`,
		"author missing name":  `{"id":"b1","author":{"id":"a1"}}`,
		"note wrong type":      `{"id":"b1","note":7}`,
	}
	for name, s := range bad {
		if err := v.Validate([]byte(s)); err == nil {
			t.Errorf("%s: Validate(%s) accepted an invalid sample", name, s)
		}
	}
}

// TestFormatIsAsserted pins the controller decision: "format" is an annotation
// under 2020-12, and this validator asserts it. Without AssertFormat the bad
// sample would read green.
func TestFormatIsAsserted(t *testing.T) {
	comps := map[string]apidoc.Schema{
		"Id": apidoc.RawFragment(map[string]any{"type": "string", "format": "uuid"}),
	}
	v, err := crosscheck.Compile(comps, "Id")
	if err != nil {
		t.Fatalf("Compile: %v", err)
	}
	if err := v.Validate([]byte(`"7d3f8b0a-1c2e-4f5a-9b8c-0d1e2f3a4b5c"`)); err != nil {
		t.Errorf("Validate(valid uuid): unexpected error: %v", err)
	}
	if err := v.Validate([]byte(`"not-a-uuid"`)); err == nil {
		t.Error(`Validate("not-a-uuid"): accepted a malformed uuid — format is not asserted`)
	}
}

// TestContentEncodingIsAsserted covers the AssertContent half of the same
// decision.
func TestContentEncodingIsAsserted(t *testing.T) {
	comps := map[string]apidoc.Schema{
		"Blob": apidoc.RawFragment(map[string]any{
			"type":            "string",
			"contentEncoding": "base64",
		}),
	}
	v, err := crosscheck.Compile(comps, "Blob")
	if err != nil {
		t.Fatalf("Compile: %v", err)
	}
	if err := v.Validate([]byte(`"aGVsbG8="`)); err != nil {
		t.Errorf("Validate(valid base64): unexpected error: %v", err)
	}
	if err := v.Validate([]byte(`"!!!not base64!!!"`)); err == nil {
		t.Error("Validate(bad base64): accepted — contentEncoding is not asserted")
	}
}

func TestCompileDanglingRefFails(t *testing.T) {
	comps := bookComponents()
	delete(comps, "Author")
	_, err := crosscheck.Compile(comps, "Book")
	if err == nil {
		t.Fatal("Compile: expected an error for a dangling $ref")
	}
	if !strings.Contains(err.Error(), "Author") {
		t.Errorf("Compile error does not name the missing component: %v", err)
	}
}

// TestDanglingRefInsideExtension covers a ref that lives in an "x-" extension
// rather than in a schema position: the rewrite walks every string, so
// checkRefs must see it too. The fixture is also Opaque-shaped
// ("patternProperties" forces the whole fragment into one Opaque node), so this
// exercises the marshal-then-decode path for opaque bytes.
func TestDanglingRefInsideExtension(t *testing.T) {
	comps := map[string]apidoc.Schema{
		"Shelf": apidoc.RawFragment(map[string]any{
			"type": "object",
			"patternProperties": map[string]any{
				"^slot": map[string]any{"type": "string"},
			},
			"x-links": map[string]any{
				"owner": map[string]any{"$ref": apidoc.RefPrefix + "Owner"},
			},
		}),
	}
	_, err := crosscheck.Compile(comps, "Shelf")
	if err == nil {
		t.Fatal("Compile: expected an error for a dangling ref inside x-links")
	}
	if !strings.Contains(err.Error(), "Owner") {
		t.Errorf("Compile error does not name the missing component: %v", err)
	}

	// With the target present the same fixture compiles and still validates the
	// opaque body ("patternProperties" is enforced).
	comps["Owner"] = apidoc.RawFragment(map[string]any{
		"type":       "object",
		"required":   []any{"id"},
		"properties": map[string]any{"id": map[string]any{"type": "string"}},
	})
	v, err := crosscheck.Compile(comps, "Shelf")
	if err != nil {
		t.Fatalf("Compile with Owner present: %v", err)
	}
	if err := v.Validate([]byte(`{"slotA":"x"}`)); err != nil {
		t.Errorf("Validate(good): unexpected error: %v", err)
	}
	if err := v.Validate([]byte(`{"slotA":7}`)); err == nil {
		t.Error("Validate(bad): patternProperties not enforced through the Opaque path")
	}
}

func TestCompileUnknownRootFails(t *testing.T) {
	if _, err := crosscheck.Compile(bookComponents(), "Nope"); err == nil {
		t.Fatal("Compile: expected an error for an unknown root component")
	}
}

func TestValidateRejectsMalformedSample(t *testing.T) {
	v, err := crosscheck.Compile(bookComponents(), "Book")
	if err != nil {
		t.Fatalf("Compile: %v", err)
	}
	if err := v.Validate([]byte(`{"id":`)); err == nil {
		t.Fatal("Validate: expected an error for malformed JSON")
	}
}
