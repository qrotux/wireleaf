# wireleaf docs

Per-package reference documentation. The repository [README](../README.md) owns
the quick start, the module layout, and the edge-kind overview; these documents
go deeper — contracts, signatures, and the gotchas of each public surface.

Suggested reading order for a new consumer:

| Doc | Package | What it covers |
| --- | --- | --- |
| [graph.md](graph.md) | `graph` | The typed builder: nodes, wires, edge declaration, `Compile` validation (Finding taxonomy), fetch binds, the request-scoped `Loader`, and the `loadertest` conformance harness for fetchers. |
| [include.md](include.md) | `include` | The `?include=` engine: parse → resolve → materialize pipeline, the `Hydrate*` facades, `Ctx` and `Ctx.Policies`, fetcher contracts (`FetchByIDs`/`FetchByParents`, the limit+1 probe), byte-exactness guarantees, error taxonomy. |
| [apidoc.md](apidoc.md) | `apidoc` | The OpenAPI 3.1 layer: the IR model and its invariants, the schema DSL and deltas, components (`Build`/`Verify`/`EmitComponents`), include-aware `SchemaFor`, unions, envelopes, `x-include-paths`, plus the `crosscheck` and `reflectortest` subpackages. |
| [reflector.md](reflector.md) | `reflector` (module) | The jsonschema-go–backed Go-struct → IR reflector: entry points, nullability verdicts, the Go→IR mapping rules, the exotica-rejection philosophy, name collisions and overrides. |
| [jsonsplice.md](jsonsplice.md) | `jsonsplice` | Order-preserving byte splices over JSON documents: the six functions, byte-level guarantees, strict frame validation. |
| [adapters-huma.md](adapters-huma.md) | `adapters/huma` (module) | The huma v2 bridge: config, the schema-registry bridge, operation helpers, envelope conventions, wiring-time panics vs request-time errors. |
