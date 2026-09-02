module github.com/qrotux/wireleaf/reflector

go 1.26

require (
	github.com/qrotux/wireleaf v0.1.0
	github.com/swaggest/jsonschema-go v0.3.79
)

require github.com/swaggest/refl v1.4.0 // indirect

// Dev-time override: inside this repo the reflector always builds against the
// sibling core checkout. Consumers resolve the version above (or pin
// their own commit with a replace of github.com/qrotux/wireleaf).
replace github.com/qrotux/wireleaf => ../
