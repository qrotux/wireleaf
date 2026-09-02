package apidoc

// RequestBodyProvider is a write-body struct that declares its WHOLE
// requestBody schema (oneOf/discriminator/$ref/inline object) — the request-side
// mirror of BodySchemaProvider. An application's operation layer reads it to
// emit the requestBody document; a body struct that does not implement it falls
// back to plain-object reflection.
type RequestBodyProvider interface {
	RequestBodySchema() Schema
}
