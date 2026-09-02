// Package collide exists only for the reflector's tests: it declares a type
// whose NAME collides with a same-named type in another package, which is the
// component-naming collision the reflector must refuse rather than resolve.
package collide

// Dup collides by name with the test package's own Dup.
type Dup struct {
	B string `json:"b"`
}
