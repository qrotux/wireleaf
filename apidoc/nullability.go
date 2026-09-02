package apidoc

import (
	"reflect"
	"strings"
)

// Verdict classifies a wire-struct field's nullability under the ONE policy
// shared by graph.Compile's shape derivation and the reflector's
// InterceptNullability hook (spec §5a). Bytes agree with the document because
// this is, by construction, the rule encoding/json already applies — with one
// documented approximation: the omitempty => Optional arm assumes an omittable
// kind (bool, numbers, string, pointer, interface, slice, map, array). On a
// non-omittable kind (e.g. a struct such as time.Time) encoding/json emits the
// field regardless, so "optional" in the document is a superset of the actual
// always-present bytes: every real response still validates, but the document
// permits an absence the encoder never produces.
//
// Precondition: callers must filter out fields tagged `json:"-"` and
// unexported fields BEFORE calling the policy. It answers "given a field that
// IS serialized, how does it appear", and does not itself detect skipped fields.
type Verdict int

const (
	VerdictPlain    Verdict = iota // always present, never null
	VerdictOptional                // omitempty: may be absent, never null
	VerdictNull                    // pointer w/o omitempty: present, may be null
)

// NullabilityPolicy decides a field's verdict. DefaultNullability is the v1
// policy; the type exists so tests and future policies can substitute.
type NullabilityPolicy func(f reflect.StructField) Verdict

// DefaultNullability is the v1 policy: a pointer without omitempty/omitzero
// => Null; omitempty or omitzero => Optional (absent, never null); else Plain.
func DefaultNullability(f reflect.StructField) Verdict {
	tag := f.Tag.Get("json")
	omit := false
	if idx := strings.Index(tag, ","); idx >= 0 {
		for _, opt := range strings.Split(tag[idx+1:], ",") {
			if opt == "omitempty" || opt == "omitzero" {
				omit = true
			}
		}
	}
	if omit {
		return VerdictOptional
	}
	if f.Type.Kind() == reflect.Pointer {
		return VerdictNull
	}
	return VerdictPlain
}
