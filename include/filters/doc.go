// Package filters holds the two shipped spellings of a client filter — a JSON
// object and the bracket query-string form — as parsers producing the
// include.Filter AST. Syntax is a product decision and lives here; judging
// names, operators and limits is include.ResolveFilter's job and stays there.
package filters
