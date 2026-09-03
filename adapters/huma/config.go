package huma

// config.go — the huma Config wireleaf ships.
//
// It is built DIRECTLY, not derived from huma.DefaultConfig, because two of
// DefaultConfig's decisions are wrong for an wireleaf document:
//
//   - the SchemaLinkTransformer. DefaultConfig installs it through a CreateHook;
//     it writes a "$schema" property into every served object schema and a
//     "$schema" FIELD into every response body, and it dereferences
//     registry.TypeFromRef outside its own recover(). wireleaf's document is the
//     contract the TypeScript client is generated from — an injected field that
//     the graph never emits is drift, and the transformer's type lookup is a
//     latent panic on any hand-assembled component.
//   - the /schemas route it exists to point at.
//
// Everything else is DefaultConfig's: the OpenAPI 3.1 skeleton, /openapi,
// /docs, and the two JSON format keys ("application/json" and "json"), in a
// fresh map so a caller's edit cannot reach the package global. The format
// itself is wireleaf's (jsonFormat below), not huma.DefaultJSONFormat. CBOR is
// deliberately absent — huma only adds it when the application imports the
// format package, and wireleaf's contract is JSON.
//
// No ConfigOpt exposes Config.AllowAdditionalPropertiesByDefault or
// Config.FieldsOptionalByDefault: both are inert against the bridge (see the
// LIMITATION note in registry.go), and an option that looks like it changes
// something but does not is worse than no option at all.

import (
	"bytes"
	"encoding/json"
	"io"
	"sync"

	humav2 "github.com/danielgtaylor/huma/v2"

	"github.com/qrotux/wireleaf/apidoc"
	"github.com/qrotux/wireleaf/reflector"
)

// strictCasingOnce guards the one process-global write wireleaf makes into huma.
var strictCasingOnce sync.Once

// enableStrictCasing turns huma's case-INSENSITIVE property matching off.
//
// huma ships ValidateStrictCasing = false (validate.go:40-45) so a legacy client
// may send {"Foo":…} where the server declared "foo". JSON Schema has no such
// rule: a property name is matched byte-for-byte, and the emitted document says
// exactly that. Left at huma's default, the runtime and the document DISAGREE IN
// BOTH DIRECTIONS — the unsafe one included: a document that allows an extra
// "A" alongside a declared "a" (no "additionalProperties":false) is accepted by
// the 2020-12 validator and REJECTED by huma, which case-folds "A" onto "a" and
// validates it against the wrong subschema. wireleaf's documents are the
// contract, so the runtime is made to agree with them.
//
// NOTE — the effect is PROCESS-GLOBAL: huma keeps the flag in a package
// variable, not in Config, so this changes validation for every huma API in the
// process, wireleaf-backed or not. It is written once, never read back, and
// never restored; an application that deliberately wants case-insensitive
// validation must set the variable back itself, after building its config.
func enableStrictCasing() {
	strictCasingOnce.Do(func() { humav2.ValidateStrictCasing = true })
}

// jsonFormat is wireleaf's replacement for humav2.DefaultJSONFormat. It pins
// TWO properties of every JSON body this adapter writes:
//
//   - NO TRAILING NEWLINE. json.Encoder.Encode appends one, which
//     stock huma's format lets through; a response body must be exactly the
//     document's bytes, because the graph layer splices and measures those
//     bytes. The single "\n" Encode adds is stripped back off here.
//   - NO HTML ESCAPING. SetEscapeHTML(false) is not an aesthetic choice: the
//     Opaque/pre-rendered JSON contract (jsonsplice) carries raw fragments
//     through, and an encoder that rewrote "<" into "<" would produce a
//     body that no longer matches the fragment it was spliced from.
//
// Unmarshal is huma's own (json.Unmarshal); nothing on the request side needs
// to differ.
var jsonFormat = humav2.Format{
	Marshal: func(w io.Writer, v any) error {
		var buf bytes.Buffer
		enc := json.NewEncoder(&buf)
		enc.SetEscapeHTML(false)
		if err := enc.Encode(v); err != nil {
			return err
		}
		b := buf.Bytes()
		// Encode writes exactly one trailing newline; drop it, and only it.
		if n := len(b); n > 0 && b[n-1] == '\n' {
			b = b[:n-1]
		}
		_, err := w.Write(b)
		return err
	},
	Unmarshal: json.Unmarshal,
}

// ConfigOpt customizes NewConfig.
type ConfigOpt func(*configOpts)

type configOpts struct {
	components *apidoc.Components
}

// WithRegistry makes the document's schema registry the wireleaf bridge over c
// — the shared component set the graph layer registers into. Without it,
// NewConfig builds a bridge over a fresh apidoc.NewComponents(), which is only
// useful for a document with no graph-derived components.
func WithRegistry(c *apidoc.Components) ConfigOpt {
	return func(o *configOpts) { o.components = c }
}

// NewConfig returns the huma configuration for an wireleaf-backed API: one
// document, one component set, one reflector.
//
// The library components (the pagination blocks and Error) are installed on the
// component set here, because the operation layer references them by name.
func NewConfig(title, version string, opts ...ConfigOpt) humav2.Config {
	enableStrictCasing()
	o := configOpts{}
	for _, opt := range opts {
		if opt != nil {
			opt(&o)
		}
	}
	c := o.components
	if c == nil {
		c = apidoc.NewComponents()
	}
	registerLibraryComponents(c)
	bridge := NewRegistry(c, &reflector.Reflector{})

	return humav2.Config{
		OpenAPI: &humav2.OpenAPI{
			OpenAPI: "3.1.0",
			Info: &humav2.Info{
				Title:   title,
				Version: version,
			},
			Components: &humav2.Components{Schemas: bridge},
		},
		OpenAPIPath: "/openapi",
		DocsPath:    "/docs",
		// SchemasPath stays empty: there is no /schemas route without the link
		// transformer that would point at it.
		Formats: map[string]humav2.Format{
			"application/json": jsonFormat,
			"json":             jsonFormat,
		},
		DefaultFormat: "application/json",
	}
}

// BridgeOf returns the wireleaf registry bridge behind a huma document, or
// panics when the document was not built by NewConfig. The post-registration
// helpers (ApplyRequestBodyDoc) need it to document a type through the same
// reflector the rest of the document came from.
func BridgeOf(oapi *humav2.OpenAPI) *Registry {
	if oapi == nil || oapi.Components == nil || oapi.Components.Schemas == nil {
		panic("adapters/huma: OpenAPI doc has no Components.Schemas registry (build it with NewConfig)")
	}
	b, ok := oapi.Components.Schemas.(*Registry)
	if !ok {
		panic("adapters/huma: OpenAPI doc's registry is not the wireleaf bridge (build the config with NewConfig/WithRegistry)")
	}
	return b
}
