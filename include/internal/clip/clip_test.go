package clip

import (
	"strings"
	"testing"
)

func TestEcho(t *testing.T) {
	for name, tc := range map[string]struct{ in, want string }{
		"short passes through": {"name", "name"},
		"long is cut":          {strings.Repeat("x", 40), strings.Repeat("x", Max) + "…"},
		// The cut lands on a rune boundary, so a multibyte fragment comes back
		// as whole runes and never as half a code point.
		"multibyte cut on a rune boundary": {strings.Repeat("→", 20), strings.Repeat("→", 5) + "…"},
	} {
		t.Run(name, func(t *testing.T) {
			if got := Echo(tc.in); got != tc.want {
				t.Errorf("Echo = %q, want %q", got, tc.want)
			}
		})
	}
}
