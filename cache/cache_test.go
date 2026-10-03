package cache

import (
	"errors"
	"sync"
	"sync/atomic"
	"testing"
)

func TestSingleflight(t *testing.T) {
	c := New(8)
	var calls int64
	release := make(chan struct{})
	planFn := func() (*Entry, error) {
		atomic.AddInt64(&calls, 1)
		<-release // hold the planning pass until all callers arrived
		return &Entry{Explain: "p"}, nil
	}
	const n = 16
	var wg sync.WaitGroup
	results := make([]*Entry, n)
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			e, _, err := c.GetOrPlan("k", 1, planFn)
			if err != nil {
				t.Error(err)
				return
			}
			results[i] = e
		}(i)
	}
	// Let all callers arrive, then release the single planning pass.
	for i := 0; i < n; i++ {
		release <- struct{}{}
		if atomic.LoadInt64(&calls) == 1 {
			break // only the leader consumes; others wait on the channel
		}
	}
	close(release)
	wg.Wait()
	if got := atomic.LoadInt64(&calls); got != 1 {
		t.Fatalf("planFn called %d times, want 1", got)
	}
	for i, e := range results {
		if e == nil || e.Explain != "p" {
			t.Fatalf("caller %d got bad entry: %v", i, e)
		}
	}
}

func TestFailedPlanNotCached(t *testing.T) {
	c := New(8)
	boom := errors.New("boom")
	if _, _, err := c.GetOrPlan("k", 1, func() (*Entry, error) { return nil, boom }); !errors.Is(err, boom) {
		t.Fatalf("want boom, got %v", err)
	}
	if c.Len() != 0 {
		t.Fatalf("failed plan cached; len=%d", c.Len())
	}
	// A later successful plan must work.
	e, cached, err := c.GetOrPlan("k", 1, func() (*Entry, error) { return &Entry{Explain: "ok"}, nil })
	if err != nil || cached || e.Explain != "ok" {
		t.Fatalf("got %v cached=%v err=%v", e, cached, err)
	}
}

func TestEpochIsolation(t *testing.T) {
	c := New(8)
	if _, _, err := c.GetOrPlan("k", 1, func() (*Entry, error) { return &Entry{Explain: "v1"}, nil }); err != nil {
		t.Fatal(err)
	}
	if e := c.Get("k", 1); e == nil {
		t.Fatal("entry missing at its own epoch")
	}
	if e := c.Get("k", 2); e != nil {
		t.Fatalf("entry from epoch 1 served at epoch 2: %v", e)
	}
	if c.Len() != 0 {
		t.Fatalf("stale entry not evicted; len=%d", c.Len())
	}
}

func TestEviction(t *testing.T) {
	c := New(2)
	mk := func(k string) {
		if _, _, err := c.GetOrPlan(k, 1, func() (*Entry, error) { return &Entry{Explain: k}, nil }); err != nil {
			t.Fatal(err)
		}
	}
	mk("a")
	mk("b")
	mk("c") // evicts "a"
	if c.Get("a", 1) != nil {
		t.Fatal("oldest entry not evicted")
	}
	if c.Get("b", 1) == nil || c.Get("c", 1) == nil {
		t.Fatal("wrong entry evicted")
	}
}

func TestDisabledCache(t *testing.T) {
	c := New(0)
	var calls int64
	planFn := func() (*Entry, error) { calls++; return &Entry{}, nil }
	if _, _, err := c.GetOrPlan("k", 1, planFn); err != nil {
		t.Fatal(err)
	}
	if _, _, err := c.GetOrPlan("k", 1, planFn); err != nil {
		t.Fatal(err)
	}
	if calls != 2 {
		t.Fatalf("disabled cache should plan every time, planned %d", calls)
	}
}
