package apidoc

// A Union value is the single fact both document arms derive from: the oneOf
// fragment (Schema below) and the per-variant statuses an application's
// operation layer reads for envelope derivation. The runtime half — marshalling
// the value a handler actually returned and picking its status — is not part of
// this package.

import (
	"fmt"
	"reflect"
)

// UnionVariant is one arm of a Union: a wire type plus the HTTP status that
// carries it. Implemented by Variant[T].
type UnionVariant interface {
	VariantType() reflect.Type
	VariantStatus() int
}

// Variant declares one Union arm, e.g. apidoc.Variant[Created]{Status: 201}.
type Variant[T any] struct{ Status int }

func (v Variant[T]) VariantType() reflect.Type { return reflect.TypeFor[T]() }

func (v Variant[T]) VariantStatus() int { return v.Status }

// Union is a declared sum type over 2+ wire variants. Discriminator names the
// property that distinguishes the arms; it is STORED but not emitted into the
// document fragment.
type Union struct {
	Discriminator string
	Variants      []UnionVariant
}

// UnionOf declares a sum type. Fewer than 2 variants is a wiring bug — a
// 1-armed union is just the type itself — so it panics (declaration time, never
// per-request).
func UnionOf(discriminator string, vs ...UnionVariant) Union {
	if len(vs) < 2 {
		panic(fmt.Sprintf("apidoc: UnionOf(%q): need at least 2 variants, got %d", discriminator, len(vs)))
	}
	return Union{Discriminator: discriminator, Variants: vs}
}

// Schema renders each variant through r and composes {"oneOf": [...]} in
// declaration order (deterministic committed document). The Discriminator is
// deliberately NOT emitted here (see Union).
//
// A variant that implements BodySchemaProvider pins its ENTIRE arm body to that
// schema (e.g. RefTo a pre-registered named component) instead of struct
// reflection. Without this, an arm carrying a nested named-component slice
// reflects inline and emits a $ref to the Go type name, which no registered
// component matches — a dangling reference.
func (u Union) Schema(r Reflector) (Schema, error) {
	parts := make([]Schema, len(u.Variants))
	for i, v := range u.Variants {
		s, err := variantSchema(r, v.VariantType())
		if err != nil {
			return Schema{}, err
		}
		parts[i] = s
	}
	return OneOf(parts...), nil
}

// variantSchema honors a BodySchemaProvider whole-body override on the arm type
// before falling back to struct reflection. The reflector is asked to name the
// arm after its Go type so the top fragment can be picked out of the result
// regardless of the implementation's default naming.
func variantSchema(r Reflector, t reflect.Type) (Schema, error) {
	d := DerefType(t)
	if d == nil || d.Kind() != reflect.Struct {
		return Schema{}, fmt.Errorf("apidoc: union variant %v must be a struct", t)
	}
	if p, ok := reflect.New(d).Elem().Interface().(BodySchemaProvider); ok {
		return p.BodySchema(), nil
	}
	name := d.Name()
	out, err := r.ReflectComponents([]reflect.Type{d}, map[reflect.Type]string{d: name})
	if err != nil {
		return Schema{}, fmt.Errorf("apidoc: union variant %s: %w", name, err)
	}
	top, ok := out[name]
	if !ok || top == nil {
		return Schema{}, fmt.Errorf("apidoc: union variant %s: reflector emitted no top component", name)
	}
	return Schema{n: top}, nil
}
