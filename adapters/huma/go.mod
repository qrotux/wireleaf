module github.com/qrotux/wireleaf/adapters/huma

go 1.26

require (
	github.com/danielgtaylor/huma/v2 v2.39.1
	github.com/qrotux/wireleaf v0.1.0
	// TEST-ONLY: the validation-equivalence suite (equivalence_test.go) validates
	// the same instances against the emitted document schema with a real
	// draft-2020-12 validator. No non-test file in this module imports it.
	github.com/qrotux/wireleaf/apidoc/crosscheck v0.1.0
	github.com/qrotux/wireleaf/reflector v0.1.0
)

require (
	github.com/santhosh-tekuri/jsonschema/v6 v6.0.3 // indirect
	github.com/swaggest/jsonschema-go v0.3.79 // indirect
	github.com/swaggest/refl v1.4.0 // indirect
	golang.org/x/text v0.40.0 // indirect
)

// Dev-time override: inside this repo the adapter always builds against the
// sibling core and reflector checkouts. Consumers resolve the versions
// above (or pin their own commit with a replace).
replace github.com/qrotux/wireleaf => ../..

replace github.com/qrotux/wireleaf/reflector => ../../reflector

replace github.com/qrotux/wireleaf/apidoc/crosscheck => ../../apidoc/crosscheck
