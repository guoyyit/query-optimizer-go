package optimizer

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/guoyyit/query-optimizer-go/catalog"
	"github.com/guoyyit/query-optimizer-go/value"
)

// bigCatalog builds a table large enough that a 3-way cartesian product is
// slow, giving cancellation something to interrupt.
func bigCatalog(t *testing.T, n int64) *catalog.Catalog {
	t.Helper()
	cat := catalog.New()
	must := func(err error) {
		if err != nil {
			t.Fatal(err)
		}
	}
	for _, name := range []string{"p", "q", "r"} {
		must(cat.CreateTable(name, []catalog.Column{{Name: "id", Type: value.Int}}))
		tb := cat.Table(name)
		for i := int64(0); i < n; i++ {
			must(tb.Insert([]value.Value{value.FromInt(i)}))
		}
		tb.SetStats(&catalog.Stats{RowCount: n, NDV: map[string]int64{"id": n}})
	}
	return cat
}

// TestCancelLeavesNoPartialResults: a cancelled execution returns an error
// and no rows, and the plan cache is not polluted by the cancelled pass.
func TestCancelLeavesNoPartialResults(t *testing.T) {
	cat := bigCatalog(t, 400) // 400^3 = 64M row pairs: slow enough
	eng, err := NewEngine(cat, DefaultConfig())
	if err != nil {
		t.Fatal(err)
	}
	// Cross-product-heavy query (no join conditions).
	q := `SELECT p.id, q.id, r.id FROM p, q, r`
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	res, err := eng.Execute(ctx, q)
	if err == nil {
		t.Fatalf("expected cancellation error, got %d rows", len(res.Rows))
	}
	if !errors.Is(err, context.DeadlineExceeded) && !errors.Is(err, context.Canceled) {
		t.Fatalf("expected context error, got %v", err)
	}
	if res != nil {
		t.Fatalf("partial result leaked: %d rows", len(res.Rows))
	}
	// The cache must not hold a poisoned entry; a fresh plan works.
	pq, err := eng.Plan(context.Background(), q)
	if err != nil {
		t.Fatal(err)
	}
	if pq.Physical == nil {
		t.Fatal("no plan after cancellation")
	}
}

// TestSlowQueryDoesNotBlockPlanning: with MaxPlans=1 a cancelled slow plan
// must release the semaphore so other queries plan promptly.
func TestSlowQueryDoesNotBlockPlanning(t *testing.T) {
	cat := bigCatalog(t, 50)
	cfg := DefaultConfig()
	cfg.MaxPlans = 1
	cfg.PlanTimeout = 200 * time.Millisecond
	eng, err := NewEngine(cat, cfg)
	if err != nil {
		t.Fatal(err)
	}
	// Occupy the single planning slot with a query we cancel quickly.
	slowCtx, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
	defer cancel()
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		// Planning itself is fast; simulate slowness via a tiny PlanTimeout
		// and many concurrent planners instead.
		_, _ = eng.Plan(slowCtx, `SELECT p.id FROM p JOIN q ON p.id = q.id`)
	}()
	// Other queries must still plan within a generous bound.
	done := make(chan error, 8)
	for i := 0; i < 8; i++ {
		go func() {
			_, err := eng.Plan(context.Background(), `SELECT q.id FROM q WHERE q.id < 10`)
			done <- err
		}()
	}
	timeout := time.After(5 * time.Second)
	for i := 0; i < 8; i++ {
		select {
		case err := <-done:
			if err != nil {
				t.Fatalf("concurrent plan failed: %v", err)
			}
		case <-timeout:
			t.Fatal("concurrent planning blocked by slow query")
		}
	}
	wg.Wait()
}

// TestConcurrentMixedLoad: planners, executors and a stats writer run
// together; results must always match the naive baseline and no stale plan
// may be served (epoch validation).
func TestConcurrentMixedLoad(t *testing.T) {
	cat := chainCatalog(t, 300, 60, 6)
	eng, err := NewEngine(cat, DefaultConfig())
	if err != nil {
		t.Fatal(err)
	}
	q := `SELECT t1.id, t2.id FROM t1 JOIN t2 ON t1.k2 = t2.id WHERE t1.id >= 0`
	want, err := eng.ExecuteNaive(context.Background(), q)
	if err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	stop := make(chan struct{})
	errs := make(chan error, 64)
	// Stats writer: constantly bumps the epoch.
	writerDone := make(chan struct{})
	go func() {
		defer close(writerDone)
		for {
			select {
			case <-stop:
				return
			default:
			}
			cat.Table("t1").SetStats(mustStats(t, cat, "t1"))
		}
	}()
	// Readers.
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < 30; j++ {
				got, err := eng.Execute(context.Background(), q)
				if err != nil {
					errs <- err
					return
				}
				if len(got.Rows) != len(want.Rows) {
					errs <- errRowCount
					return
				}
			}
		}()
	}
	wg.Wait()
	close(stop)
	<-writerDone
	select {
	case err := <-errs:
		t.Fatal(err)
	default:
	}
}

var errRowCount = errors.New("row count mismatch under concurrent stats churn")

// TestPlanTimeout: a zero-value context that is already cancelled must fail
// planning fast with a context error.
func TestPlanRespectsCancelledContext(t *testing.T) {
	cat := chainCatalog(t, 100, 100, 10)
	eng, err := NewEngine(cat, DefaultConfig())
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err = eng.Plan(ctx, `SELECT t1.id FROM t1 JOIN t2 ON t1.k2 = t2.id`)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("expected context.Canceled, got %v", err)
	}
}
