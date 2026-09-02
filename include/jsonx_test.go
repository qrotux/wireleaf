package include

import (
	"strings"
	"testing"
)

// TestMarshalNoEscape pins the wire-marshal contract: `&`, `<`, `>` stay RAW
// (json.Marshal would otherwise emit their \u escapes) and the json.Encoder
// trailing newline is stripped.
func TestMarshalNoEscape(t *testing.T) {
	b, err := MarshalNoEscape(map[string]string{"title": "Sense & <Sensibility>"})
	if err != nil {
		t.Fatalf("MarshalNoEscape: %v", err)
	}
	want := `{"title":"Sense & <Sensibility>"}`
	if string(b) != want {
		t.Errorf("got %s, want %s", b, want)
	}
	if strings.ContainsAny(string(b), "\n") {
		t.Error("trailing newline must be stripped")
	}
}
