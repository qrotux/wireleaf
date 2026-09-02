// Package graph is the typed builder over the include engine: applications
// declare nodes, wire types and edges with generics, Compile validates the
// whole graph at wiring time and produces the engine's resource registry, and
// Loader is the request-scoped hydration entry point.
package graph
