package filters

import (
	"strings"

	"github.com/qrotux/wireleaf/include"
	"github.com/qrotux/wireleaf/include/internal/clip"
)

// opByName is the client spelling of every operator. The set is closed:
// include.FilterOpsFor answers which of them a given column admits.
var opByName = map[string]include.FilterOp{
	string(include.OpEq):  include.OpEq,
	string(include.OpNe):  include.OpNe,
	string(include.OpIn):  include.OpIn,
	string(include.OpNin): include.OpNin,
	string(include.OpLt):  include.OpLt,
	string(include.OpLte): include.OpLte,
	string(include.OpGt):  include.OpGt,
	string(include.OpGte): include.OpGte,
}

// splitQuant strips a trailing `*` (all) or `~` (none) from one path segment.
func splitQuant(s string) (string, include.Quant) {
	switch {
	case strings.HasSuffix(s, "*"):
		return strings.TrimSuffix(s, "*"), include.QuantAll
	case strings.HasSuffix(s, "~"):
		return strings.TrimSuffix(s, "~"), include.QuantNone
	}
	return s, ""
}

// filterPath splits a dotted client path into hop steps and the leaf field,
// resolving the field-suffix quantifier sugar against the graph. A path the
// graph does not know is returned as-is (quantifier-free) for ResolveFilter.
func filterPath(root include.Resource, path string) ([]include.FilterStep, string, error) {
	segs := strings.Split(path, ".")
	field, quant := splitQuant(segs[len(segs)-1])
	steps := make([]include.FilterStep, 0, len(segs)-1)
	for _, s := range segs[:len(segs)-1] {
		key, q := splitQuant(s)
		steps = append(steps, include.FilterStep{Key: key, Quant: q})
	}
	_, many, known := walkFilterPath(root, steps)
	if quant != "" {
		// A field-suffix quantifier is only unambiguous over a known path
		// with exactly one to-many segment that nobody has already
		// quantified. Every fault is "<path>:<quant>", the convention
		// ResolveFilter uses; Reason says which of the four it is.
		reason := ""
		switch {
		case !known:
			reason = include.ReasonQuantifierOnUnknownPath
		case len(many) == 0:
			reason = include.ReasonQuantifierWithoutManyHop
		case len(many) > 1:
			reason = include.ReasonQuantifierAmbiguous
		case steps[many[0]].Quant != "":
			reason = include.ReasonQuantifierTwice
		}
		if reason != "" {
			return nil, "", include.NewError(include.INVALID_FILTER, clip.Echo(path)+":"+string(quant)).WithReason(reason)
		}
		steps[many[0]].Quant = quant
	}
	// A to-many segment nobody quantified is `any`, the no-suffix default.
	if known {
		for _, i := range many {
			if steps[i].Quant == "" {
				steps[i].Quant = include.QuantAny
			}
		}
	}
	if len(steps) == 0 {
		steps = nil
	}
	return steps, field, nil
}

// walkFilterPath follows steps through Edges(); it returns the resource the
// path lands on, the indices of the to-many steps, and whether every step was
// known. A key the graph does not know stops the walk: the rest is unknowable
// here, and ResolveFilter is the one that reports it, with a bounded path.
// The bracket parser uses the landing resource to type the value.
func walkFilterPath(root include.Resource, steps []include.FilterStep) (include.Resource, []int, bool) {
	cur := root
	var many []int
	for i, st := range steps {
		e, ok := cur.Edges()[st.Key]
		if !ok || e.Target == nil {
			return nil, many, false
		}
		if e.Many {
			many = append(many, i)
		}
		cur = e.Target()
		if cur == nil {
			return nil, many, false
		}
	}
	return cur, many, true
}
