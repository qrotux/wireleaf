package apidoc

// EnvelopeSchemaProvider is a data-field (or array-element) type that supplies
// its own inline data schema for a success envelope — the allOf/oneOf/anyOf
// generalization of the Node[W] $ref path. Implementors also marshal their
// hydrated bytes verbatim (Node[W] semantics), so an application's operation
// layer keeps the runtime bytes while documenting the composed shape.
type EnvelopeSchemaProvider interface{ EnvelopeSchema() Schema }

// BodySchemaProvider is an output type that supplies its ENTIRE 200
// response-body schema (no {data} wrapper) — for root-level anyOf and
// polymorphic bodies the {data:X} envelope cannot express. Implementors also
// marshal their hydrated bytes verbatim.
type BodySchemaProvider interface{ BodySchema() Schema }
