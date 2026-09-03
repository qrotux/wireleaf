// Package loader is a request-scoped batch cache for side data that is not
// part of a resource graph (permissions, counters, feature flags, anything a
// MapFn or EnrichFn needs by key). See Loader's doc for the full contract.
package loader
