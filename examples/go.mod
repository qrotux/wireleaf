module github.com/qrotux/wireleaf/examples

go 1.26

require (
	github.com/danielgtaylor/huma/v2 v2.39.1
	github.com/qrotux/wireleaf v0.2.1
	github.com/qrotux/wireleaf/adapters/huma v0.2.1
	github.com/qrotux/wireleaf/reflector v0.2.1
)

require (
	github.com/swaggest/jsonschema-go v0.3.79 // indirect
	github.com/swaggest/refl v1.4.0 // indirect
)

// Examples always build against the sibling checkouts; the require versions
// above are never fetched.
replace (
	github.com/qrotux/wireleaf => ..
	github.com/qrotux/wireleaf/adapters/huma => ../adapters/huma
	github.com/qrotux/wireleaf/apidoc/crosscheck => ../apidoc/crosscheck
	github.com/qrotux/wireleaf/reflector => ../reflector
)
