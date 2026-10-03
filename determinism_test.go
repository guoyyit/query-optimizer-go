package optimizer

import (
	"context"
	"fmt"
	"sync"
	"testing"

	"github.com/guoyyit/query-optimizer-go/catalog"
	"github.com/guoyyit/query-optimizer-go/value"
)

// TestPlanDeterministicUnderConcurrency plans the same query from many
// goroutines in varying order; every result must be the identical plan.
func TestPlanDeterministicUnderConcurrency(t *testing.T) {
	cat := chainCatalog(t, 2000, 200, 20)
	eng, err := NewEngine(cat, DefaultConfig())
	if err != nil {
		t.Fatal(err)
	}
	queries := []string{
		`SELECT t1.id FROM t1 JOIN t2 ON t1.k2 = t2.id JOIN t3 ON t2.k3 = t3.id WHERE t1.id < 5000`,
		`SELECT t2.id, t3.id FROM t2 JOIN t3 ON t2.k3 = t3.id WHERE t3.id >= 3`,
		`SELECT t1.id, t2.id FROM t1, t2 WHERE t1.k2 = t2.id`,
	}
	const workers = 16
	fps := make([][]string, len(queries))
	for i := range fps {
		fps[i] = make([]string, workers)
	}
	var wg sync.WaitGroup
	for w := 0; w < workers; w++ {
		wg.Add(1)
		go func(w int) {
			defer wg.Done()
			for i, q := range queries {
				pq, err := eng.Plan(context.Background(), q)
				if err != nil {
					t.Error(err)
					return
				}
				fps[i][w] = pq.Physical.Fingerprint()
			}
		}(w)
	}
	wg.Wait()
	for i, q := range queries {
		for w := 1; w < workers; w++ {
			if fps[i][w] != fps[i][0] {
				t.Fatalf("query %q: worker 0 plan %s != worker %d plan %s", q, fps[i][0], w, fps[i][w])
			}
		}
	}
}

// TestCostTieBrokenDeterministically constructs two tables with identical
// statistics so join-orientation costs tie; the tie-break rule
// (lexicographically smaller plan fingerprint) must hold.
func TestCostTieBrokenDeterministically(t *testing.T) {
	cat := catalog.New()
	must := func(err error) {
		if err != nil {
			t.Fatal(err)
		}
	}
	for _, name := range []string{"x", "y"} {
		must(cat.CreateTable(name, []catalog.Column{{Name: "id", Type: value.Int}, {Name: "k", Type: value.Int}}))
		tb := cat.Table(name)
		for i := int64(0); i < 100; i++ {
			must(tb.Insert([]value.Value{value.FromInt(i), value.FromInt(i)}))
		}
		tb.SetStats(&catalog.Stats{RowCount: 100, NDV: map[string]int64{"id": 100, "k": 100}})
	}
	eng, err := NewEngine(cat, DefaultConfig())
	if err != nil {
		t.Fatal(err)
	}
	q := `SELECT x.id FROM x JOIN y ON x.k = y.k`
	var first string
	for i := 0; i < 50; i++ {
		eng.InvalidatePlans()
		pq, err := eng.Plan(context.Background(), q)
		if err != nil {
			t.Fatal(err)
		}
		fp := pq.Physical.Fingerprint()
		if i == 0 {
			first = fp
			continue
		}
		if fp != first {
			t.Fatalf("run %d: plan changed from %s to %s under identical stats", i, first, fp)
		}
	}
	t.Logf("stable tie-broken plan: %s", first)
}

// TestStatsMissingMarkersAndFallback verifies the conservative fallback is
// used and observably marked when statistics are absent or stale.
func TestStatsMissingMarkersAndFallback(t *testing.T) {
	cat := chainCatalog(t, 100, 100, 100)
	// Remove stats from t2 and make t3's stats stale.
	cat.Table("t2").SetStats(nil)
	stale := *mustStats(t, cat, "t3")
	stale.UpdatedAt = stale.UpdatedAt.Add(-2 * cfg().StatsMaxAge)
	cat.Table("t3").SetStats(&stale)

	eng, err := NewEngine(cat, DefaultConfig())
	if err != nil {
		t.Fatal(err)
	}
	pq, err := eng.Plan(context.Background(),
		`SELECT t1.id FROM t1 JOIN t2 ON t1.k2 = t2.id JOIN t3 ON t2.k3 = t3.id`)
	if err != nil {
		t.Fatal(err)
	}
	missing := map[string]bool{}
	for _, m := range pq.StatsMissing {
		missing[m] = true
	}
	if !missing["t2"] {
		t.Fatalf("expected stats-missing marker for t2, got %v", pq.StatsMissing)
	}
	if !missing["t3"] {
		t.Fatalf("expected stats-missing marker for stale t3, got %v", pq.StatsMissing)
	}
	if missing["t1"] {
		t.Fatalf("t1 has fresh stats but was marked missing: %v", pq.StatsMissing)
	}
	// Explain output must carry the markers too.
	if !containsAll(pq.Explain, "stats-missing: t2", "stats-missing: t3") {
		t.Fatalf("explain missing markers:\n%s", pq.Explain)
	}
	// Results must still be correct.
	assertEquivalent(t, eng, `SELECT t1.id FROM t1 JOIN t2 ON t1.k2 = t2.id JOIN t3 ON t2.k3 = t3.id`)
}

func mustStats(t *testing.T, cat *catalog.Catalog, table string) *catalog.Stats {
	t.Helper()
	st, _ := cat.Table(table).Stats()
	if st == nil {
		t.Fatalf("no stats for %s", table)
	}
	return st
}

func cfg() Config { return DefaultConfig() }

func containsAll(s string, subs ...string) bool {
	for _, sub := range subs {
		found := false
		for i := 0; i+len(sub) <= len(s); i++ {
			if s[i:i+len(sub)] == sub {
				found = true
				break
			}
		}
		if !found {
			return false
		}
	}
	return true
}

// TestCacheInvalidationOnStatsChange: a plan cached under old statistics
// must not be served after statistics change.
func TestCacheInvalidationOnStatsChange(t *testing.T) {
	cat := chainCatalog(t, 10000, 500, 10)
	eng, err := NewEngine(cat, DefaultConfig())
	if err != nil {
		t.Fatal(err)
	}
	q := `SELECT t1.id FROM t1 JOIN t2 ON t1.k2 = t2.id JOIN t3 ON t2.k3 = t3.id`
	pq1, err := eng.Plan(context.Background(), q)
	if err != nil {
		t.Fatal(err)
	}
	pq2, err := eng.Plan(context.Background(), q)
	if err != nil {
		t.Fatal(err)
	}
	if !pq2.FromCache {
		t.Fatal("second plan should come from cache")
	}
	if pq1.Physical.Fingerprint() != pq2.Physical.Fingerprint() {
		t.Fatal("cached plan differs from original")
	}

	// Change statistics drastically: t1 becomes tiny. The epoch bump must
	// invalidate the cached plan and the new plan must reflect new stats.
	cat.Table("t1").SetStats(&catalog.Stats{RowCount: 5, NDV: map[string]int64{"id": 5, "k2": 5}})
	pq3, err := eng.Plan(context.Background(), q)
	if err != nil {
		t.Fatal(err)
	}
	if pq3.FromCache {
		t.Fatal("plan served from cache after stats change")
	}
	if pq3.Physical.Fingerprint() == pq1.Physical.Fingerprint() {
		t.Fatalf("plan did not change after stats change:\nold %s\nnew %s",
			pq1.Physical.Fingerprint(), pq3.Physical.Fingerprint())
	}
	t.Logf("old: %s", pq1.Physical.Fingerprint())
	t.Logf("new: %s", pq3.Physical.Fingerprint())
}

// TestCacheInvalidationOnSchemaChange: inserting data bumps the epoch and
// invalidates cached plans.
func TestCacheInvalidationOnDataChange(t *testing.T) {
	cat := chainCatalog(t, 10, 10, 10)
	eng, err := NewEngine(cat, DefaultConfig())
	if err != nil {
		t.Fatal(err)
	}
	q := `SELECT t1.id FROM t1 JOIN t2 ON t1.k2 = t2.id`
	if _, err := eng.Plan(context.Background(), q); err != nil {
		t.Fatal(err)
	}
	hits0, _ := eng.CacheStats()
	if err := cat.Table("t1").Insert([]value.Value{value.FromInt(999), value.FromInt(1)}); err != nil {
		t.Fatal(err)
	}
	pq, err := eng.Plan(context.Background(), q)
	if err != nil {
		t.Fatal(err)
	}
	if pq.FromCache {
		t.Fatal("stale plan served after data change")
	}
	hits1, _ := eng.CacheStats()
	if hits1 != hits0 {
		t.Fatalf("cache hit counted for invalidated entry: %d -> %d", hits0, hits1)
	}
}

// TestConfigChangeDoesNotReusePlans: same query under different rule
// toggles must not share cache entries.
func TestConfigChangeDoesNotReusePlans(t *testing.T) {
	cat := chainCatalog(t, 1000, 100, 10)
	q := `SELECT t1.id FROM t1 JOIN t2 ON t1.k2 = t2.id JOIN t3 ON t2.k3 = t3.id`
	plans := map[string]bool{}
	for _, reorder := range []bool{true, false} {
		cfg := DefaultConfig()
		cfg.Rules.JoinReordering = reorder
		eng, err := NewEngine(cat, cfg)
		if err != nil {
			t.Fatal(err)
		}
		pq, err := eng.Plan(context.Background(), q)
		if err != nil {
			t.Fatal(err)
		}
		plans[fmt.Sprintf("%v:%s", reorder, pq.Physical.Fingerprint())] = true
	}
	if len(plans) != 2 {
		t.Fatalf("expected distinct plans per config, got %v", plans)
	}
}
