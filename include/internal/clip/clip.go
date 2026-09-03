// Package clip bounds client text echoed in error payloads: an error body
// must never be sized by the client. One rule for every parser and resolver
// in include and its subpackages.
package clip

// Max is the byte budget of one echoed fragment.
const Max = 16

// Echo returns s cut to Max bytes on a rune boundary, with "…" appended when
// anything was cut.
func Echo(s string) string {
	if len(s) <= Max {
		return s
	}
	end := 0
	for i := range s { // i is the byte offset of each rune
		if i > Max {
			break
		}
		end = i
	}
	return s[:end] + "…"
}
