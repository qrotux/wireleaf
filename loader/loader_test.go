package loader

import (
	"errors"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/qrotux/wireleaf/include"
)

// TestLoaderWarmGet: Warm loads requested keys, Get returns them, and a second
// Warm of the same keys does not refetch.
func TestLoaderWarmGet(t *testing.T) {
	var calls atomic.Int32
	var seen [][]string
	var mu sync.Mutex
	ld := New(func(c *include.Ctx, keys []string) (map[string]int, error) {
		calls.Add(1)
		mu.Lock()
		seen = append(seen, append([]string(nil), keys...))
		mu.Unlock()
		out := map[string]int{}
		for i, k := range keys {
			out[k] = len(k)*10 + i
		}
		return out, nil
	})

	c := &include.Ctx{}
	// "a" appears twice: duplicate keys in one call must be deduped.
	if err := ld.Warm(c, "a", "bb", "a"); err != nil {
		t.Fatalf("Warm: %v", err)
	}
	mu.Lock()
	first := seen[0]
	mu.Unlock()
	if len(first) != 2 {
		t.Fatalf("first fetch keys = %v; want 2 deduped keys", first)
	}
	if v, ok := ld.Get(c, "a"); !ok || v != 10 {
		t.Fatalf("Get(a) = %v, %v; want 10, true", v, ok)
	}
	if v, ok := ld.Get(c, "bb"); !ok || v != 21 {
		t.Fatalf("Get(bb) = %v, %v; want 21, true", v, ok)
	}
	if got := calls.Load(); got != 1 {
		t.Fatalf("fetch calls = %d; want 1", got)
	}

	// Re-warming cached keys must not refetch.
	if err := ld.Warm(c, "a", "bb"); err != nil {
		t.Fatalf("Warm 2: %v", err)
	}
	if got := calls.Load(); got != 1 {
		t.Fatalf("fetch calls after re-warm = %d; want 1", got)
	}

	// A mixed warm fetches only the uncached key.
	if err := ld.Warm(c, "a", "ccc"); err != nil {
		t.Fatalf("Warm 3: %v", err)
	}
	if got := calls.Load(); got != 2 {
		t.Fatalf("fetch calls after mixed warm = %d; want 2", got)
	}
	mu.Lock()
	last := seen[len(seen)-1]
	mu.Unlock()
	if len(last) != 1 || last[0] != "ccc" {
		t.Fatalf("second fetch keys = %v; want [ccc]", last)
	}

	// Warm with no keys is a no-op.
	if err := ld.Warm(c); err != nil {
		t.Fatalf("Warm 4: %v", err)
	}
	if got := calls.Load(); got != 2 {
		t.Fatalf("fetch calls after empty warm = %d; want 2", got)
	}
}

// TestLoaderNegativeCache: a successful fetch that omits a requested key
// caches "asked, absent" — Get reports (zero,false) and no refetch happens.
func TestLoaderNegativeCache(t *testing.T) {
	var calls atomic.Int32
	ld := New(func(c *include.Ctx, keys []string) (map[string]string, error) {
		calls.Add(1)
		return map[string]string{}, nil // never finds anything
	})

	c := &include.Ctx{}
	if err := ld.Warm(c, "missing"); err != nil {
		t.Fatalf("Warm: %v", err)
	}
	if v, ok := ld.Get(c, "missing"); ok || v != "" {
		t.Fatalf("Get = %q, %v; want \"\", false", v, ok)
	}
	if err := ld.Warm(c, "missing"); err != nil {
		t.Fatalf("Warm 2: %v", err)
	}
	if _, ok := ld.Get(c, "missing"); ok {
		t.Fatal("Get after re-warm reported found")
	}
	if got := calls.Load(); got != 1 {
		t.Fatalf("fetch calls = %d; want 1 (negative result must be cached)", got)
	}
}

// TestLoaderSingleFlight: 10 concurrent Warms of the same uncached key produce
// exactly one fetch.
func TestLoaderSingleFlight(t *testing.T) {
	var calls atomic.Int32
	var gate sync.WaitGroup
	gate.Add(1)
	ld := New(func(c *include.Ctx, keys []string) (map[string]int, error) {
		calls.Add(1)
		gate.Wait() // hold the fetch open so every goroutine piles up behind it
		return map[string]int{"k": 7}, nil
	})

	c := &include.Ctx{}
	var start, done sync.WaitGroup
	start.Add(1)
	errs := make([]error, 10)
	for i := range errs {
		done.Add(1)
		go func(i int) {
			defer done.Done()
			start.Wait()
			// "k" twice: a key already in flight must be waited on once.
			errs[i] = ld.Warm(c, "k", "k")
		}(i)
	}
	start.Done()
	// Give the goroutines a moment to converge on the in-flight entry, then
	// release the fetch.
	time.Sleep(20 * time.Millisecond)
	gate.Done()
	done.Wait()

	for i, err := range errs {
		if err != nil {
			t.Fatalf("Warm %d: %v", i, err)
		}
	}
	if got := calls.Load(); got != 1 {
		t.Fatalf("fetch calls = %d; want exactly 1", got)
	}
	if v, ok := ld.Get(c, "k"); !ok || v != 7 {
		t.Fatalf("Get = %v, %v; want 7, true", v, ok)
	}
}

// TestLoaderErrorNotPoisoned: a failed fetch caches nothing — Warm returns the
// error, Get reports miss, and the next Warm refetches (1 -> 2).
func TestLoaderErrorNotPoisoned(t *testing.T) {
	boom := errors.New("boom")
	var calls atomic.Int32
	ld := New(func(c *include.Ctx, keys []string) (map[string]int, error) {
		if calls.Add(1) == 1 {
			return nil, boom
		}
		return map[string]int{"k": 42}, nil
	})

	c := &include.Ctx{}
	if err := ld.Warm(c, "k"); !errors.Is(err, boom) {
		t.Fatalf("Warm 1 err = %v; want boom", err)
	}
	if _, ok := ld.Get(c, "k"); ok {
		t.Fatal("Get after failed Warm reported found")
	}
	if err := ld.Warm(c, "k"); err != nil {
		t.Fatalf("Warm 2: %v", err)
	}
	if got := calls.Load(); got != 2 {
		t.Fatalf("fetch calls = %d; want 2 (failure must not be cached)", got)
	}
	if v, ok := ld.Get(c, "k"); !ok || v != 42 {
		t.Fatalf("Get = %v, %v; want 42, true", v, ok)
	}
}

// TestLoaderErrorReachesWaiters: waiters on an in-flight fetch that fails get
// the same error back from their own Warm.
func TestLoaderErrorReachesWaiters(t *testing.T) {
	boom := errors.New("boom")
	var gate sync.WaitGroup
	gate.Add(1)
	var calls atomic.Int32
	ld := New(func(c *include.Ctx, keys []string) (map[string]int, error) {
		calls.Add(1)
		gate.Wait()
		return nil, boom
	})

	c := &include.Ctx{}
	var done sync.WaitGroup
	errs := make([]error, 8)
	for i := range errs {
		done.Add(1)
		go func(i int) {
			defer done.Done()
			errs[i] = ld.Warm(c, "k")
		}(i)
	}
	time.Sleep(20 * time.Millisecond)
	gate.Done()
	done.Wait()

	for i, err := range errs {
		if !errors.Is(err, boom) {
			t.Fatalf("Warm %d err = %v; want boom", i, err)
		}
	}
	if got := calls.Load(); got != 1 {
		t.Fatalf("fetch calls = %d; want 1", got)
	}
	if _, ok := ld.Get(c, "k"); ok {
		t.Fatal("Get reported found after failure")
	}
}

// TestLoaderGetNeverFetches: Get on a never-warmed key returns (zero,false)
// without touching the fetch fn.
func TestLoaderGetNeverFetches(t *testing.T) {
	var calls atomic.Int32
	ld := New(func(c *include.Ctx, keys []string) (map[string]int, error) {
		calls.Add(1)
		return map[string]int{"k": 1}, nil
	})

	c := &include.Ctx{}
	if v, ok := ld.Get(c, "k"); ok || v != 0 {
		t.Fatalf("Get = %v, %v; want 0, false", v, ok)
	}
	if got := calls.Load(); got != 0 {
		t.Fatalf("fetch calls = %d; want 0", got)
	}
}

// TestLoaderGetBlocksOnInflight: Get on a key another goroutine is warming
// blocks until that warm completes, then returns its outcome.
func TestLoaderGetBlocksOnInflight(t *testing.T) {
	release := make(chan struct{})
	fetchEntered := make(chan struct{})
	ld := New(func(c *include.Ctx, keys []string) (map[string]int, error) {
		close(fetchEntered)
		<-release
		return map[string]int{"k": 99}, nil
	})

	c := &include.Ctx{}
	go func() { _ = ld.Warm(c, "k") }()
	<-fetchEntered

	got := make(chan int, 1)
	go func() {
		v, ok := ld.Get(c, "k")
		if !ok {
			got <- -1
			return
		}
		got <- v
	}()

	select {
	case v := <-got:
		t.Fatalf("Get returned %v before the warm completed", v)
	case <-time.After(30 * time.Millisecond):
	}

	close(release)
	select {
	case v := <-got:
		if v != 99 {
			t.Fatalf("Get = %v; want 99", v)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Get did not return after the warm completed")
	}
}

// TestLoaderPerCtxIsolation: the cache is request-scoped — a second Ctx sees
// nothing the first warmed, and the zero Ctx value works.
func TestLoaderPerCtxIsolation(t *testing.T) {
	var calls atomic.Int32
	ld := New(func(c *include.Ctx, keys []string) (map[string]int, error) {
		calls.Add(1)
		return map[string]int{"k": 5}, nil
	})

	c1 := &include.Ctx{}
	c2 := &include.Ctx{}
	if err := ld.Warm(c1, "k"); err != nil {
		t.Fatalf("Warm c1: %v", err)
	}
	if _, ok := ld.Get(c2, "k"); ok {
		t.Fatal("c2 saw c1's cached key")
	}
	if err := ld.Warm(c2, "k"); err != nil {
		t.Fatalf("Warm c2: %v", err)
	}
	if got := calls.Load(); got != 2 {
		t.Fatalf("fetch calls = %d; want 2 (one per Ctx)", got)
	}

	// Two loaders on one Ctx keep separate state (keyed by loader identity).
	other := New(func(c *include.Ctx, keys []string) (map[string]int, error) {
		return map[string]int{"k": 1000}, nil
	})
	if err := other.Warm(c1, "k"); err != nil {
		t.Fatalf("Warm other: %v", err)
	}
	if v, _ := ld.Get(c1, "k"); v != 5 {
		t.Fatalf("ld.Get(c1) = %v; want 5", v)
	}
	if v, _ := other.Get(c1, "k"); v != 1000 {
		t.Fatalf("other.Get(c1) = %v; want 1000", v)
	}
}

// TestLoaderFetchPanic: a panicking fetch propagates the panic to Warm's
// caller, unblocks concurrent waiters with a miss instead of hanging them
// forever, and leaves the keys uncached so a later Warm refetches.
func TestLoaderFetchPanic(t *testing.T) {
	var calls atomic.Int32
	entered := make(chan struct{})
	release := make(chan struct{})
	ld := New(func(c *include.Ctx, keys []string) (map[string]int, error) {
		if calls.Add(1) == 1 {
			close(entered)
			<-release
			panic("kaboom")
		}
		return map[string]int{"k": 3}, nil
	})

	c := &include.Ctx{}
	panicked := make(chan any, 1)
	go func() {
		defer func() { panicked <- recover() }()
		_ = ld.Warm(c, "k")
	}()
	<-entered

	// A Get parked on the in-flight entry must survive the panic.
	got := make(chan bool, 1)
	go func() {
		_, ok := ld.Get(c, "k")
		got <- ok
	}()
	select {
	case <-got:
		t.Fatal("Get returned before the fetch settled")
	case <-time.After(20 * time.Millisecond):
	}

	close(release)
	if p := <-panicked; p == nil {
		t.Fatal("panic did not propagate out of Warm")
	}
	select {
	case ok := <-got:
		if ok {
			t.Fatal("Get reported found after a panicking fetch")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Get never unblocked after the fetch panicked")
	}

	// Nothing was cached: the next Warm refetches and succeeds.
	if err := ld.Warm(c, "k"); err != nil {
		t.Fatalf("Warm after panic: %v", err)
	}
	if got := calls.Load(); got != 2 {
		t.Fatalf("fetch calls = %d; want 2 (panic must not be cached)", got)
	}
	if v, ok := ld.Get(c, "k"); !ok || v != 3 {
		t.Fatalf("Get = %v, %v; want 3, true", v, ok)
	}
}

// TestLoaderNilFetch: a Loader built without New fails loudly instead of
// silently negative-caching every key.
func TestLoaderNilFetch(t *testing.T) {
	var ld Loader[string, int]
	c := &include.Ctx{}

	err := ld.Warm(c, "k")
	if err == nil {
		t.Fatal("Warm with nil fetch returned nil error")
	}
	if !strings.Contains(err.Error(), "nil fetch") {
		t.Fatalf("Warm err = %v; want a nil-fetch error", err)
	}
	if v, ok := ld.Get(c, "k"); ok || v != 0 {
		t.Fatalf("Get = %v, %v; want 0, false", v, ok)
	}
	// Nothing was cached, so a second Warm fails the same way.
	if err := ld.Warm(c, "k"); err == nil {
		t.Fatal("second Warm with nil fetch returned nil error")
	}
}

// TestLoaderPresentZeroValue: a key present in the fetch result with the zero
// value is a HIT, not a miss.
func TestLoaderPresentZeroValue(t *testing.T) {
	ld := New(func(c *include.Ctx, keys []string) (map[string]int, error) {
		return map[string]int{"k": 0}, nil
	})
	c := &include.Ctx{}
	if err := ld.Warm(c, "k", "absent"); err != nil {
		t.Fatalf("Warm: %v", err)
	}
	if v, ok := ld.Get(c, "k"); !ok || v != 0 {
		t.Fatalf("Get(k) = %v, %v; want 0, true", v, ok)
	}
	if _, ok := ld.Get(c, "absent"); ok {
		t.Fatal("Get(absent) reported found")
	}
}

// TestLoaderGetBlocksThroughFailure: a Get that parks on an in-flight fetch
// which then fails unblocks with (zero,false).
func TestLoaderGetBlocksThroughFailure(t *testing.T) {
	boom := errors.New("boom")
	entered := make(chan struct{})
	release := make(chan struct{})
	ld := New(func(c *include.Ctx, keys []string) (map[string]int, error) {
		close(entered)
		<-release
		return nil, boom
	})

	c := &include.Ctx{}
	warmErr := make(chan error, 1)
	go func() { warmErr <- ld.Warm(c, "k") }()
	<-entered

	got := make(chan bool, 1)
	go func() {
		_, ok := ld.Get(c, "k")
		got <- ok
	}()
	select {
	case <-got:
		t.Fatal("Get returned before the fetch settled")
	case <-time.After(20 * time.Millisecond):
	}

	close(release)
	if err := <-warmErr; !errors.Is(err, boom) {
		t.Fatalf("Warm err = %v; want boom", err)
	}
	select {
	case ok := <-got:
		if ok {
			t.Fatal("Get reported found after a failed fetch")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Get never unblocked after the fetch failed")
	}
}
