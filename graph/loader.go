package graph

import (
	"errors"
	"sync"

	"github.com/qrotux/wireleaf/include"
)

// errNilFetch is returned by Warm on a Loader built without NewLoader, so a
// missing fetch fails loudly instead of negative-caching every key.
var errNilFetch = errors.New("graph: Loader has nil fetch (use NewLoader)")

// errFetchPanicked settles entries whose fetch panicked, so waiters unblock
// with a miss instead of hanging forever.
var errFetchPanicked = errors.New("graph: loader fetch panicked")

// Loader is a request-scoped batch loader for side data that is not part of
// the graph itself (permissions, counters, feature flags, anything a MapFn or
// EnrichFn needs by key). A Loader is a package-level application value — it
// is NOT registered on the Builder and holds no data of its own; every cached
// value lives on the *include.Ctx of one request and dies with it.
//
// The contract is two-phase: Warm fetches, Get reads. Warm batches every
// still-unknown key into ONE fetch call and single-flights concurrent Warms of
// the same key, so a key that SETTLES is fetched at most once per request —
// found or not-found alike are both cached. A fetch that returns an ERROR
// caches nothing: those keys go back to unknown and a later Warm refetches them
// (see NewLoader's last bullet). Get never fetches; it only reports what Warm
// already learned (blocking if a Warm of that key is still in flight).
//
// Warm with ZERO keys returns nil before anything else — before the nil-fetch
// check included, so a Loader built without NewLoader does not fail there.
//
// A Loader value is safe for concurrent use and may be warmed from several
// goroutines of the same request.
type Loader[K comparable, V any] struct {
	fetch func(*include.Ctx, []K) (map[K]V, error)
}

// NewLoader returns a Loader that batch-fetches values with fetch.
//
// The fetch contract:
//   - fetch MUST be safe for concurrent use: the engine may warm the same
//     loader from several goroutines of one request, and one Loader is shared
//     by every request of the process.
//   - fetch MUST return only keys it was asked for. Unrequested keys are
//     ignored (they would never be read anyway); the Task 9 test harness
//     reports them as a contract violation.
//   - fetch MUST NOT call this Loader for the keys it was handed — those keys
//     are in flight and it would wait on itself (self-deadlock).
//   - Honoring c.StdContext() cancellation inside fetch is the only way
//     waiters (Get and concurrent Warm) unblock early.
//   - Omitting a requested key means "asked, absent": it is cached negatively
//     for the request and never refetched. Returning an error instead caches
//     nothing, so a later Warm retries.
func NewLoader[K comparable, V any](fetch func(c *include.Ctx, keys []K) (map[K]V, error)) *Loader[K, V] {
	return &Loader[K, V]{fetch: fetch}
}

// entry is one key's slot in a request's loader state. done is closed exactly
// once, when the fetch that owns the entry settles; val/ok/err are written
// only before that close and read only after it.
type entry[V any] struct {
	done chan struct{}
	val  V
	ok   bool
	err  error
}

// loaderState is one Loader's per-request state, stored on the Ctx under the
// *Loader pointer.
type loaderState[K comparable, V any] struct {
	mu      sync.Mutex
	entries map[K]*entry[V]
}

// state returns this loader's state for c, creating it on first use.
func (l *Loader[K, V]) state(c *include.Ctx) *loaderState[K, V] {
	st := c.LoaderState(l, func() any {
		return &loaderState[K, V]{entries: make(map[K]*entry[V])}
	})
	return st.(*loaderState[K, V])
}

// Warm loads keys that this request has not yet learned about, in one batched
// fetch call. Keys already cached (found or absent) are skipped; keys another
// goroutine is currently fetching are awaited rather than refetched.
//
// On a fetch error nothing is cached: Warm returns the error, every waiter on
// that same in-flight fetch receives it too, and a later Warm retries the
// keys. When both an own fetch error and a waited-on error occur, the own
// error is returned.
func (l *Loader[K, V]) Warm(c *include.Ctx, keys ...K) error {
	if len(keys) == 0 {
		return nil
	}
	if l.fetch == nil {
		return errNilFetch
	}
	st := l.state(c)

	var mine []K
	var claimed []*entry[V] // claimed[i] is mine[i]'s freshly created entry
	var pending []*entry[V] // in-flight entries owned by someone else
	seen := make(map[K]struct{}, len(keys))

	st.mu.Lock()
	for _, k := range keys {
		if _, dup := seen[k]; dup {
			continue
		}
		seen[k] = struct{}{}
		if e, exists := st.entries[k]; exists {
			select {
			case <-e.done: // already settled: cached, nothing to do
			default:
				pending = append(pending, e)
			}
			continue
		}
		e := &entry[V]{done: make(chan struct{})}
		st.entries[k] = e
		mine = append(mine, k)
		claimed = append(claimed, e)
	}
	st.mu.Unlock()

	var firstErr error
	if len(mine) > 0 {
		firstErr = l.runFetch(c, st, mine, claimed)
	}
	for _, e := range pending {
		<-e.done
		if e.err != nil && firstErr == nil {
			firstErr = e.err
		}
	}
	return firstErr
}

// runFetch performs one batched fetch for the keys this call claimed and
// settles their entries (claimed[i] belongs to mine[i]). On success every
// claimed key is cached — present keys with their value, requested-but-absent
// keys negatively. On error the waiters are handed the error and the entries
// are removed, so the keys stay uncached and a later Warm refetches them.
// A panic escaping fetch would otherwise leave the claimed entries pending
// forever (Get has no cancellation escape), so a deferred settler fails them
// and removes them before the panic continues to unwind.
func (l *Loader[K, V]) runFetch(c *include.Ctx, st *loaderState[K, V], mine []K, claimed []*entry[V]) error {
	// fail settles every claimed entry with err and drops it from the state
	// (no poisoning: waiters unblock and a later Warm refetches the keys).
	fail := func(err error) {
		st.mu.Lock()
		for i, k := range mine {
			e := claimed[i]
			e.err = err
			close(e.done)
			if cur, still := st.entries[k]; still && cur == e {
				delete(st.entries, k)
			}
		}
		st.mu.Unlock()
	}

	settled := false
	defer func() {
		if settled {
			return
		}
		// A panic is unwinding: settle the claimed entries so nobody waits on
		// a fetch that will never report, then let the panic continue.
		fail(errFetchPanicked)
	}()

	vals, err := l.fetch(c, mine)
	if err != nil {
		fail(err)
		settled = true
		return err
	}

	st.mu.Lock()
	for i, k := range mine {
		e := claimed[i]
		if v, found := vals[k]; found {
			e.val, e.ok = v, true
		}
		close(e.done)
	}
	st.mu.Unlock()
	settled = true
	return nil
}

// Get reports the value this request learned for key. It NEVER fetches: a key
// that was never warmed yields the zero value and false. If a Warm of key is
// still in flight, Get blocks until it settles and then returns its outcome
// (a failed fetch yields the zero value and false). A Loader built without
// NewLoader (nil fetch) always misses and caches nothing.
func (l *Loader[K, V]) Get(c *include.Ctx, key K) (V, bool) {
	var zero V
	if l.fetch == nil {
		return zero, false
	}
	st := l.state(c)

	st.mu.Lock()
	e, exists := st.entries[key]
	st.mu.Unlock()
	if !exists {
		return zero, false
	}
	<-e.done
	if e.err != nil {
		return zero, false
	}
	if !e.ok {
		return zero, false
	}
	return e.val, true
}
