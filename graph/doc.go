// Package graph is the typed builder over the include engine: applications
// declare nodes, wire types and edges with generics, and Compile validates the
// whole graph at wiring time and produces the engine's resource registry.
// Side data a mapping needs by key is batched by the loader package instead.
package graph
