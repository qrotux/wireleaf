// inputs.go — the compiled INPUT vocabulary of a root resource: which list
// parameters it accepts and with which keys. graph.Compile derives it from the
// node's Inputs declaration and its col tags; ResolveInputs enforces it;
// apidoc.InputParams documents it. Both read the same value, so the served
// document and the accepted requests cannot drift.

package include

// PageMode selects the pagination parameters a list accepts.
type PageMode string

const (
	// PageModeOffset: ?page= and ?limit=; the fetcher receives QueryArgs.Page.
	PageModeOffset PageMode = "offset"
	// PageModeCursor: ?cursor= and ?limit=; the fetcher receives
	// QueryArgs.Cursor (opaque) and returns the next/prev tokens in ListPage.
	PageModeCursor PageMode = "cursor"
)

// Pagination defaults applied when a node declares no limits (or no Inputs).
const (
	DefaultPageLimit    = 20
	DefaultMaxPageLimit = 100
)

// SortInputs is the resolved sort vocabulary: wire key → SQL sort key.
type SortInputs struct {
	Enabled bool
	// Default is the sort applied when the client sends none, in WIRE form
	// (json key with an optional "-" prefix); "" leaves the fetcher's order.
	Default string
	Keys    map[string]string
}

// FilterInputs is the resolved filter vocabulary: the FILTERABLE columns only.
type FilterInputs struct {
	Enabled bool
	Fields  map[string]Column
}

// PageInputs is the resolved pagination contract.
type PageInputs struct {
	Mode         PageMode
	DefaultLimit int
	MaxLimit     int
}

// Inputs is what a root resource accepts on a list operation.
type Inputs struct {
	Sort   SortInputs
	Filter FilterInputs
	Page   PageInputs
}

// InputSource is the OPTIONAL Resource seam exposing Inputs. graph.Compile's
// nodes implement it; the bool reports whether the node DECLARED inputs.
type InputSource interface {
	Inputs() (Inputs, bool)
}

// DefaultInputs is the contract of a node that declared nothing: offset
// pagination with the default limits, no sort, no filter.
func DefaultInputs() Inputs {
	return Inputs{Page: PageInputs{Mode: PageModeOffset, DefaultLimit: DefaultPageLimit, MaxLimit: DefaultMaxPageLimit}}
}

// InputsOf returns res's inputs, or DefaultInputs() and false when res is not
// an InputSource. Callers treat the two cases identically: "no declaration"
// is one well-defined behaviour, not a special case.
func InputsOf(res Resource) (Inputs, bool) {
	in, ok := inputsOf(res)
	in.Page = settlePage(in.Page)
	return in, ok
}

// settlePage fills the zeros of a declared page contract the way graph.Compile
// does, so a hand-written InputSource yields the same shape: a zero Mode is
// offset, a zero MaxLimit the package cap, a zero DefaultLimit the package
// default capped by MaxLimit. Nothing here can make Limit zero downstream.
func settlePage(p PageInputs) PageInputs {
	if p.Mode == "" {
		p.Mode = PageModeOffset
	}
	if p.MaxLimit <= 0 {
		p.MaxLimit = DefaultMaxPageLimit
	}
	if p.DefaultLimit <= 0 {
		p.DefaultLimit = min(DefaultPageLimit, p.MaxLimit)
	}
	return p
}

func inputsOf(res Resource) (Inputs, bool) {
	if s, ok := res.(InputSource); ok {
		return s.Inputs()
	}
	return DefaultInputs(), false
}
