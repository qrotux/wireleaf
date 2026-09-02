package huma

import (
	"encoding/json"
	"fmt"
	"reflect"

	humav2 "github.com/danielgtaylor/huma/v2"

	"github.com/qrotux/wireleaf/apidoc"
)

// Node carries engine-hydrated bytes inside a typed wire struct and reflects
// into a huma doc as {$ref: <component of W>}. The embedded apidoc.Node[W]
// supplies the verbatim-bytes marshalling.
type Node[W any] struct{ apidoc.Node[W] }

// NodeOf wraps hydrated engine bytes (no copy — the engine hands ownership).
func NodeOf[W any](raw json.RawMessage) Node[W] { return Node[W]{apidoc.NodeOf[W](raw)} }

// Schema implements humav2.SchemaProvider: the doc sees a $ref to the component
// registered for W.
//
// The lookup goes through the BRIDGE's Components, never through r.Schema: the
// latter would silently auto-register W by reflection instead of surfacing the
// missing RegisterNode call. An unregistered W — or a registry that is not the
// wireleaf bridge at all — is a wiring bug, so both panic.
func (Node[W]) Schema(r humav2.Registry) *humav2.Schema {
	b, ok := r.(*Registry)
	if !ok {
		panic(fmt.Sprintf("adapters/huma: Node[%s] reflected into a doc whose registry is not the wireleaf bridge (use NewRegistry)", reflect.TypeFor[W]()))
	}
	name, ok := b.c.NodeComponent(reflect.TypeFor[Node[W]]())
	if !ok {
		name, ok = b.c.TypeName(reflect.TypeFor[W]())
	}
	if !ok {
		panic(fmt.Sprintf("adapters/huma: Node[%s] reflected into doc but W is not registered via RegisterNode", reflect.TypeFor[W]()))
	}
	return &humav2.Schema{Ref: apidoc.RefPrefix + name}
}

// RegisterNode binds W to a component name on c and registers BOTH node wrapper
// types for reverse lookup: apidoc.Node[W] (what a core envelope-derivation pass
// sees on a struct field) and this package's Node[W].
func RegisterNode[W any](c *apidoc.Components, component string) {
	apidoc.RegisterNode[W](c, component)
	c.RegisterWrapperType(reflect.TypeFor[Node[W]](), component)
}
