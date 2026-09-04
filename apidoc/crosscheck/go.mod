module github.com/qrotux/wireleaf/apidoc/crosscheck

go 1.26

require (
	github.com/qrotux/wireleaf v0.2.3
	github.com/santhosh-tekuri/jsonschema/v6 v6.0.3
)

require golang.org/x/text v0.40.0 // indirect

// Dev-time override: this module is test-only and always builds against the
// sibling core checkout. Consumers resolve the version above (or pin
// their own commit with a replace of github.com/qrotux/wireleaf).
replace github.com/qrotux/wireleaf => ../..
