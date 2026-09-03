// jsonx.go — the engine's wire-byte marshalers.
//
// MarshalNoEscape (JSON.stringify-parity bytes) is the engine default;
// MarshalStd restores encoding/json's HTML escaping. Residual divergence: Go's
// encoder always escapes U+2028/U+2029 while JSON.stringify emits them raw —
// unfixable with the stdlib encoder, tolerated.

package include

import (
	"bytes"
	"encoding/json"
)

// MarshalNoEscape is json.Marshal with HTML escaping disabled (raw `&`, `<`,
// `>` in string values, matching JSON.stringify semantics). The trailing
// newline appended by json.Encoder.Encode is stripped.
//
// The bytes are meant for application/json bodies. Do not embed them in HTML
// without escaping at the embedding site; use MarshalStd when the output may
// land inside a <script> element or an HTML template.
func MarshalNoEscape(v any) ([]byte, error) {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(v); err != nil {
		return nil, err
	}
	b := buf.Bytes()
	if n := len(b); n > 0 && b[n-1] == '\n' {
		b = b[:n-1]
	}
	return b, nil
}

// MarshalStd is encoding/json's Marshal verbatim: HTML-escaping ON, so `&`,
// `<` and `>` come out as their backslash-u escape sequences. Install it as
// Ctx.Marshal to opt back into the stdlib behavior; the engine default is
// MarshalNoEscape.
func MarshalStd(v any) ([]byte, error) { return json.Marshal(v) }
