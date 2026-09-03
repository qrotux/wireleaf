package include

import "testing"

// TestErrorText pins the message shape: the reason, when present, sits in
// brackets between the path and the status so the three stay separable.
func TestErrorText(t *testing.T) {
	for _, tc := range []struct {
		err  *Error
		want string
	}{
		{NewError(INVALID_FILTER, "reviews"), "INVALID_FILTER: reviews (status 400)"},
		{NewError(INVALID_FILTER, ""), "INVALID_FILTER (status 400)"},
		{NewError(INVALID_FILTER, "author:any").WithReason(ReasonQuantifierOnToOne), "INVALID_FILTER: author:any [quantifier-on-to-one] (status 400)"},
		{&Error{Code: NOT_FOUND, Path: "b9", Status: 404}, "NOT_FOUND: b9 (status 404)"},
	} {
		if got := tc.err.Error(); got != tc.want {
			t.Errorf("Error() = %q, want %q", got, tc.want)
		}
	}
}
