package apidoc

import "encoding/json"

// The wire-type → component-name and wrapper-type → component-name indexes used
// to live here as package globals. They now live on a Components VALUE
// (build.go: RegisterNode[W](c, name), c.RegisterType, c.TypeName,
// c.RegisterWrapperType, c.NodeComponent) so two documents in one process no
// longer share one registry. Node[W] itself carries BYTES, not schemas, so it
// is untouched by that move.

// Node carries engine-hydrated bytes inside a typed wire struct: MarshalJSON
// writes the raw bytes VERBATIM. That is the whole reason the type exists —
// encoding/json re-escapes a json.RawMessage nested inside a typed struct, so a
// plain RawMessage field would corrupt HTML-bearing values; a serving pipeline
// encoding with SetEscapeHTML(false) then passes these bytes through untouched.
type Node[W any] struct{ raw json.RawMessage }

// NodeOf wraps hydrated engine bytes (no copy — the engine hands ownership).
func NodeOf[W any](raw json.RawMessage) Node[W] { return Node[W]{raw: raw} }

func (n Node[W]) MarshalJSON() ([]byte, error) {
	// len covers both nil and empty non-nil raw: an empty RawMessage would
	// otherwise emit invalid JSON and fail far from the wiring bug.
	if len(n.raw) == 0 {
		return []byte("null"), nil
	}
	return n.raw, nil
}

func (n *Node[W]) UnmarshalJSON(b []byte) error {
	n.raw = append(n.raw[:0:0], b...)
	return nil
}
