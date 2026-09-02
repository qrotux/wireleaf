package apidoc

import "reflect"

// RefPrefix is the OAS-3.1 component $ref prefix shared by the whole doc layer.
const RefPrefix = "#/components/schemas/"

// DerefType strips leading pointer indirections from t.
func DerefType(t reflect.Type) reflect.Type {
	for t != nil && t.Kind() == reflect.Pointer {
		t = t.Elem()
	}
	return t
}
