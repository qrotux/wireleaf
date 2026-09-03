package include

import (
	"context"
	"net/http"
	"net/url"
	"sync"
)

// Registry locates fetchers for resources at hydrate time.
type Registry interface {
	// FetchByIDs returns the forward-batch fetcher for r, if one is registered.
	FetchByIDs(r Resource) (FetchByIDs, bool)
	// FetchByParents returns the batched multi-parent reverse fetcher for r, if
	// one is registered.
	FetchByParents(r Resource) (FetchByParents, bool)
	// FetchByEdge returns the reverse fetcher bound to the edge parent.<key>,
	// if one is registered. A reverse join belongs to the edge, not to the
	// target node — author.books and tag.tagged both load Book rows but by
	// different keys — so the engine asks here first and falls back to
	// FetchByParents(target) for an edge with no fetcher of its own. A
	// registry with node-level fetchers only returns (nil, false). A registry
	// that WRAPS another must forward this method too: a graph whose reverse
	// edges are bound per edge has no node-level fetcher to fall back to.
	FetchByEdge(parent Resource, key string) (FetchByParents, bool)
}

// EdgeQuery carries the resolved per-edge query handed to a batched reverse
// fetcher. Every field is already validated and resolved by the engine; a
// fetcher applies it as given and never re-reads client input.
type EdgeQuery struct {
	// Limit is the effective per-parent top-N: clamp(client :limit, 1,
	// Edge.Limit), or Edge.Limit when the client supplied none. A BARE edge
	// resolves to 0, meaning fetch-all (and its HasMore is ignored).
	//
	// A NON-BARE edge whose Edge.Limit is 0 does NOT mean fetch-all: 0 reads as
	// "unset" and the engine substitutes the DEFAULT of 20. graph.Compile
	// always writes an explicit Limit, so this only bites a HAND-BUILT
	// include.Edge that left the field zero — set Edge.Bare to fetch all.
	//
	// The limit+1 probe belongs to the FETCHER: it decides HasMore (typically
	// by selecting Limit+1 rows) and returns at most Limit rows per parent.
	// The engine truncates defensively if more come back.
	Limit int
	// Sort is the resolved SQL-side sort key ('-' prefix = descending, "" =
	// the fetcher's own default order). It has passed the Edge.SortCols
	// whitelist, so it is never a raw client string.
	Sort string
	// Args are the remaining declared edge arguments, validated at plan time
	// and passed verbatim. The built-ins ("limit", "sort") are NOT included —
	// they are already resolved into Limit/Sort. nil when nothing remains.
	// The values are shared with the plan — fetchers must not mutate them.
	Args map[string]any

	// Edge identifies the reverse edge being loaded: the OWNER node (whose
	// ids parentIDs holds) and the include key. A fetcher bound per edge
	// (EdgeFetchRegistry) already knows which edge it serves and may ignore
	// it; a NODE-level fetcher shared by several inbound reverse edges
	// (author.books and tag.tagged both landing on Book) switches on it to
	// pick the join. Zero only on a hand-built query (e.g. a test harness).
	Edge EdgeRef
}

// EdgeRef names one edge: the resource that declares it and its include key.
type EdgeRef struct {
	Parent string // owner resource name, e.g. "Author"
	Key    string // include key on that owner, e.g. "books"
}

// String renders "Parent.key".
func (r EdgeRef) String() string { return r.Parent + "." + r.Key }

// reverseFetcher resolves the fetcher for the reverse edge parent.<key> into
// target: the per-edge bind when the registry has one, else the target's
// node-level fetcher. A registry answering (nil, true) is treated as "none".
func reverseFetcher(reg Registry, parent Resource, key string, target Resource) (FetchByParents, bool) {
	if fn, ok := reg.FetchByEdge(parent, key); ok && fn != nil {
		return fn, true
	}
	return reg.FetchByParents(target)
}

// ParentRows is one parent's slice of a batched reverse fetch result.
type ParentRows struct {
	// Rows are that parent's child rows in the fetcher's sort order, at most
	// EdgeQuery.Limit of them when Limit > 0.
	Rows []any
	// HasMore reports whether more rows exist beyond Rows (the fetcher's own
	// limit+1 probe). Ignored for bare edges, which never emit an envelope.
	HasMore bool
	// NextCursor is an OPAQUE continuation token. The engine copies it into
	// the envelope verbatim when non-empty and never interprets it; an empty
	// value omits the "nextCursor" key entirely. Ignored for bare edges.
	NextCursor string
}

// FetchByParents is the batched reverse fetcher: ONE call covers every parent
// id of a level, returning each parent's rows keyed by parent id. A parent
// absent from the map has no children (an empty collection, never null); keys
// that were not requested are dropped by the engine.
//
// Implementations MUST be safe for concurrent use: the engine may call the
// same fetcher from several goroutines for sibling edges (the v1 engine still
// loads sibling edges sequentially, but the contract is concurrency-safe).
type FetchByParents func(c *Ctx, parentIDs []string, q EdgeQuery) (map[string]ParentRows, error)

// MarshalFunc serializes one wire value into JSON bytes.
type MarshalFunc func(v any) ([]byte, error)

// Request is the HTTP-shaped snapshot of the request being served, filled by
// an adapter and read by Wire functions, Guards, Enrich hooks and fetchers.
// It is RAW and UNTRUSTED client input: a header is whatever the client (or
// the edge proxy) sent. Interpreted, trusted per-request state (the
// authenticated viewer, the tenant) belongs in Ctx.Env. Only stdlib types:
// the engine stays free of any HTTP framework. nil outside HTTP (tests,
// workers, CLIs) — use the Ctx accessors, which tolerate that.
type Request struct {
	Method      string            // "GET"
	Path        string            // the URL path as requested: "/books/b1"
	Route       string            // the matched template: "/books/{id}"
	OperationID string            // the operation's id when the framework has one
	PathParams  map[string]string // {"id": "b1"}
	Query       url.Values
	Header      http.Header // canonical keys
	RemoteAddr  string
}

// Ctx is the per-request context threaded through every engine call.
//
// One Ctx is shared by every fetcher of a request and MUST be treated as
// read-only by them: the engine never mutates it after construction, and
// fetchers may be invoked concurrently. Anything a fetcher needs to write per
// request belongs behind its own synchronization in Env.
//
// The one sanctioned exception is the request-scoped state reached through
// State: it is unexported, mutex-protected and concurrent-safe by
// construction, so library plumbing in other packages (the loader package's
// per-request cache) can keep per-request state without any
// application-visible mutation of Ctx. The zero Ctx value is usable — the map
// initializes lazily under the mutex.
//
// The second exception is the row counter behind Rows: Materialize
// increments it (single-threaded, no lock) as levels are serialized, so the
// application can read the real cost of a request after hydration.
//
// The third is Registry: a Hydrator method given a Ctx whose Registry is nil
// writes its own registry there, once, before any fetcher runs. That write is
// unsynchronized, so a Ctx handed to several Hydrators — or to one from
// several goroutines — must carry its Registry already; the fill-in is for
// the one-Hydrator-per-request caller who builds &Ctx{Context: ctx}.
//
// Ctx contains a mutex: always pass *Ctx, never copy it by value.
type Ctx struct {
	// Context is the request Go context passed to fetchers (cancellation,
	// tracing). nil is mapped to context.Background().
	Context context.Context
	// Registry locates fetchers; nil is valid only for plans without child edges.
	Registry Registry
	// Request is the HTTP request snapshot an adapter fills; nil outside HTTP.
	// Read it through Header / PathParam / QueryValue, which tolerate nil.
	Request *Request
	// Env carries application per-request state (viewer identity, locale,
	// base URLs, request-scoped caches). The engine never reads it; the
	// closures installed by graph.Serialize and graph.Enrich, Edge.Guard and
	// fetchers assert it to the application's own type.
	Env any
	// Marshal serializes wire values; nil → MarshalNoEscape, i.e. raw `&`,
	// `<`, `>` (JSON.stringify-parity bytes) WITHOUT any opt-in. Install
	// MarshalStd to get encoding/json's HTML escaping back.
	//
	// SECURITY: the default output is for application/json response bodies.
	// It is NOT safe to embed in HTML (an SSR <script> bootstrap, a mail
	// template) — a string value containing `</script>` would terminate the
	// script context. Escape at the embedding site, or install MarshalStd.
	Marshal MarshalFunc
	// Policies is the engine-wide materialize-time policy fallback set
	// (missing-required, missing-foreign, fetcher-contract). The zero value
	// is the permissive default for every policy; a per-edge override on
	// Edge wins where set. Set it at construction — the read-only contract
	// above applies.
	Policies Policies

	// mu guards state — the only mutable state on Ctx.
	mu sync.Mutex
	// state holds request-scoped state objects, keyed by the identity of
	// whatever owns them (a *loader.Loader pointer). Created lazily on first
	// use.
	state map[any]any

	// rows counts documents materialized by this request (root included);
	// maxRows is the budget copied off the root PlanNode when Materialize
	// enters the root level (0 = unlimited).
	rows, maxRows int
}

// Rows reports how many documents Materialize has serialized for this
// request so far, root documents included — the actual-cost counterpart of
// PlanNode.Cost, for cost-based rate limiting.
func (c *Ctx) Rows() int { return c.rows }

// Header returns one request header ("" when there is no Request).
func (c *Ctx) Header(name string) string {
	if c.Request == nil || c.Request.Header == nil {
		return ""
	}
	return c.Request.Header.Get(name)
}

// PathParam returns one matched path parameter ("" when absent).
func (c *Ctx) PathParam(name string) string {
	if c.Request == nil {
		return ""
	}
	return c.Request.PathParams[name]
}

// QueryValue returns the first value of one query parameter ("" when absent).
func (c *Ctx) QueryValue(name string) string {
	if c.Request == nil || c.Request.Query == nil {
		return ""
	}
	return c.Request.Query.Get(name)
}

// State returns this request's state object for key, creating it with mk on
// first use. It is the request-scoped slot library plumbing in other packages
// (the loader package's per-request cache) hangs its data on: the map and the
// entry are created under Ctx's mutex, so the zero Ctx value works and
// concurrent callers observe one object. mk runs under the mutex — it must
// only allocate, never touch the Ctx. NOT for application use; put
// application state in Env.
func (c *Ctx) State(key any, mk func() any) any {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.state == nil {
		c.state = make(map[any]any)
	}
	st, ok := c.state[key]
	if !ok {
		st = mk()
		c.state[key] = st
	}
	return st
}

// StdContext returns the request Go context, falling back to
// context.Background().
//
// It has NO caller inside wireleaf — it is EXTERNAL API, and deliberately so.
// Fetchers, MapFns and EnrichFns live in the application, receive only a *Ctx,
// and need a context.Context to reach the database and to honor cancellation
// (loader.Loader's fetch contract names this method for exactly that). Do not
// remove it as unused.
func (c *Ctx) StdContext() context.Context {
	if c.Context != nil {
		return c.Context
	}
	return context.Background()
}

// marshal serializes v via Marshal, defaulting to MarshalNoEscape.
func (c *Ctx) marshal(v any) ([]byte, error) {
	if c.Marshal != nil {
		return c.Marshal(v)
	}
	return MarshalNoEscape(v)
}
