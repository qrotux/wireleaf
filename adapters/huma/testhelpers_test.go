package huma

// The ONE place the package's shared test helpers live. Do not redefine them
// elsewhere in the package (redeclaration is a compile error).

import (
	"fmt"
	"strings"
	"testing"
)

func assertPanics(t *testing.T, fn func()) {
	t.Helper()
	defer func() {
		if recover() == nil {
			t.Fatalf("expected panic")
		}
	}()
	fn()
}

// assertPanicsWith is assertPanics that also PINS the reason: a bare recover
// passes on any panic, including an unrelated one from a later regression.
func assertPanicsWith(t *testing.T, want string, fn func()) {
	t.Helper()
	defer func() {
		r := recover()
		if r == nil {
			t.Fatalf("expected a panic containing %q", want)
		}
		if msg := fmt.Sprint(r); !strings.Contains(msg, want) {
			t.Fatalf("panic = %q, want it to contain %q", msg, want)
		}
	}()
	fn()
}
