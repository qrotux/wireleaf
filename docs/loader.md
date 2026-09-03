# loader — the request-scoped batch loader for side data

```go
func New[K comparable, V any](
    fetch func(c *include.Ctx, keys []K) (map[K]V, error)) *Loader[K, V]
func (l *Loader[K, V]) Warm(c *include.Ctx, keys ...K) error
func (l *Loader[K, V]) Get(c *include.Ctx, key K) (V, bool)
```

`Loader` batches side data that is not part of the graph (permissions,
counters, flags) for one request. The loader itself is a package-level
application value holding no data; per-request state lives on the
`*include.Ctx` via `Ctx.State`, keyed by the loader pointer, and dies
with the request. Two phases:

- **`Warm`** fetches: it batches every still-unknown key into one fetch call,
  awaits keys another goroutine is already fetching (single-flight), and
  skips keys already settled — found and not-found alike are cached, so a key
  that settles is fetched at most once per request. A fetch **error** caches
  nothing: the keys revert to unknown, every waiter on that in-flight fetch
  receives the error, and a later `Warm` retries. Zero keys → nil
  immediately (before even the nil-fetch check). A fetch **panic** is
  detected by a deferred settler: the claimed entries are failed and removed
  so waiters unblock, then the panic continues to unwind.
- **`Get`** never fetches. An unwarmed key yields `(zero, false)`; a key
  whose `Warm` is in flight blocks until it settles (a failed fetch also
  yields `(zero, false)`).

Fetch contract (checked by `loadertest.RunLoaderFetch`): safe for concurrent
use; return only requested keys; never call this loader back for keys it was
handed (self-deadlock); honor `c.StdContext()` cancellation — that is the only
way waiters unblock early; omit an absent key (negative-cached, never
refetched) rather than erroring. A hand-assembled `Loader{}` has a nil fetch:
every non-empty `Warm` returns `"loader: Loader has nil fetch (use New)"`
and every `Get` misses — loudly, instead of negative-caching everything.
