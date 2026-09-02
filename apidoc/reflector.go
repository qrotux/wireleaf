package apidoc

import "reflect"

// Reflector renders Go struct types into normalized typed IR nodes. Contract
// every implementation must honor:
//   - nullable scalar fields emit as type-arrays ({"type":["T","null"]});
//   - a non-omitempty pointer-to-struct field emits anyOf[$ref, {"type":"null"}];
//   - auxiliary nested struct components are emitted alongside the requested
//     tops and deduped by name;
//   - overrides force the component name for the given struct types; other
//     types use the implementation's default naming;
//   - a $ref carries the bare component NAME (RefPrefix is applied by the
//     serializer, never stored on IRNode.Ref).
//
// Returning IR rather than map[string]any is what lets components assembly keep
// property order and Opaque byte-exactness: nothing round-trips through a map.
type Reflector interface {
	ReflectComponents(types []reflect.Type, overrides map[reflect.Type]string) (map[string]*IRNode, error)
}
