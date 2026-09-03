package huma

import (
	"encoding/json"
	"fmt"
	"sort"

	humav2 "github.com/danielgtaylor/huma/v2"

	"github.com/qrotux/wireleaf/apidoc"
)

// BuildInto verifies c and makes its components part of the live huma OpenAPI
// doc.
//
// Verify runs FIRST, always: an unresolved $ref must stop document
// assembly whichever path follows, and it is the one check that sees the whole
// set at once.
//
// Then there are two paths:
//
//   - the doc's Components.Schemas is already the wireleaf bridge over the SAME
//     Components (the NewConfig wiring): the components are ALREADY the doc's
//     schema map, so there is nothing to merge and this is a no-op;
//   - the doc carries a plain huma registry (standalone use): each component is
//     serialized and inserted through the Extensions bridge — a zero typed
//     humav2.Schema carrying the fragment as extensions marshals to exactly that
//     fragment. Names are merged in sorted order, and the clash check runs over
//     every name BEFORE the first insert, so a rejected merge leaves the doc
//     untouched.
func BuildInto(oapi *humav2.OpenAPI, c *apidoc.Components) error {
	if c == nil {
		return fmt.Errorf("adapters/huma: nil component registry")
	}
	if err := c.Verify(); err != nil {
		return err
	}
	if oapi == nil || oapi.Components == nil || oapi.Components.Schemas == nil {
		return fmt.Errorf("adapters/huma: huma OpenAPI doc has no Components.Schemas registry")
	}
	if b, ok := oapi.Components.Schemas.(*Registry); ok {
		if b.c == c {
			return nil
		}
		// A bridge over a DIFFERENT component set cannot be merged into: its
		// Map is a snapshot of its own Components, so an insertion there would
		// be silently dropped. Two component sets in one document is a wiring
		// mistake, not something to paper over.
		return fmt.Errorf("adapters/huma: the doc's registry is an wireleaf bridge over a different Components; assemble the document from one shared component set")
	}
	schemas := oapi.Components.Schemas.Map()
	fragments := map[string]any{}
	if err := c.Merge(fragments); err != nil {
		return err
	}
	names := make([]string, 0, len(fragments))
	for name := range fragments {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		if _, clash := schemas[name]; clash {
			return fmt.Errorf("adapters/huma: component %q collides with a pre-existing OpenAPI component; refusing to clobber", name)
		}
	}
	for _, name := range names {
		raw, err := decodeFragment(fragments[name])
		if err != nil {
			return fmt.Errorf("adapters/huma: component %q: %w", name, err)
		}
		schemas[name] = &humav2.Schema{Extensions: raw}
	}
	return nil
}

// decodeFragment turns one Merge output value into the generic map the
// Extensions bridge needs. Merge inserts pre-encoded json.RawMessage; a
// map[string]any (an older Merge, or a hand-built dst) is taken as is.
//
// NOTE: this path routes the fragment through a Go map, so keyword and property
// ORDER is lost — huma marshals a map. It is unavoidable here: the only vehicle
// into a huma registry is a *humav2.Schema, whose Extensions field is a map. The
// bridge path above has no such loss, which is one more reason it is the wiring
// wireleaf ships.
func decodeFragment(v any) (map[string]any, error) {
	switch f := v.(type) {
	case map[string]any:
		return f, nil
	case json.RawMessage:
		var m map[string]any
		if err := json.Unmarshal(f, &m); err != nil {
			return nil, err
		}
		return m, nil
	default:
		return nil, fmt.Errorf("unexpected fragment type %T", v)
	}
}
