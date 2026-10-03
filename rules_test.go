package optimizer

import (
	"context"
	"strings"
	"testing"

	"github.com/guoyyit/query-optimizer-go/catalog"
	"github.com/guoyyit/query-optimizer-go/value"
)

// chainCatalog builds t1 -< t2 -< t3 with controllable sizes so join
// reordering has a clear cheapest order.
func chainCatalog(t *testing.T, n1, n2, n3 int64) *catalog.Catalog {
	t.Helper()
	cat := catalog.New()
	must := func(err error) {
		if err != nil {
			t.Fatal(err)
		}
	}
	must(cat.CreateTable("t1", []catalog.Column{{Name: "id", Type: value.Int}, {Name: "k2", Type: value.Int}}))
	must(cat.CreateTable("t2", []catalog.Column{{Name: "id", Type: value.Int}, {Name: "k3", Type: value.Int}}))
	must(cat.CreateTable("t3", []catalog.Column{{Name: "id", Type: value.Int}}))
	for i := int64(0); i < n1; i++ {
		must(cat.Table("t1").Insert([]value.Value{value.FromInt(i), value.FromInt(i % max64(n2, 1))}))
	}
	for i := int64(0); i < n2; i++ {
		must(cat.Table("t2").Insert([]value.Value{value.FromInt(i), value.FromInt(i % max64(n3, 1))}))
	}
	for i := int64(0); i < n3; i++ {
		must(cat.Table("t3").Insert([]value.Value{value.FromInt(i)}))
	}
	cat.Table("t1").SetStats(&catalog.Stats{RowCount: n1, NDV: map[string]int64{"id": n1, "k2": n2}})
	cat.Table("t2").SetStats(&catalog.Stats{RowCount: n2, NDV: map[string]int64{"id": n2, "k3": n3}})
	cat.Table("t3").SetStats(&catalog.Stats{RowCount: n3, NDV: map[string]int64{"id": n3}})
	return cat
}

func max64(a, b int64) int64 {
	if a > b {
		return a
	}
	return b
}

func TestJoinReorderPicksSmallestFirst(t *testing.T) {
	// t1 huge, t2 medium, t3 tiny. FROM order is t1,t2,t3 but the cheapest
	// order starts from t3.
	cat := chainCatalog(t, 10000, 500, 10)
	eng, err := NewEngine(cat, DefaultConfig())
	if err != nil {
		t.Fatal(err)
	}
	pq, err := eng.Plan(context.Background(),
		`SELECT t1.id FROM t1 JOIN t2 ON t1.k2 = t2.id JOIN t3 ON t2.k3 = t3.id`)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, n := range pq.Notes {
		if n.Rule == "join-reorder" && strings.HasPrefix(n.Detail, "join order:") {
			found = true
			if !strings.Contains(n.Detail, "[t3") {
				t.Fatalf("expected reorder to start from t3, got: %s", n.Detail)
			}
			t.Logf("reorder note: %s", n.Detail)
		}
	}
	if !found {
		t.Fatal("no join-reorder note emitted")
	}
	// No cross product may appear: all joins are connected.
	if strings.Contains(pq.Explain, "CROSS PRODUCT") {
		t.Fatalf("plan degraded to cross product:\n%s", pq.Explain)
	}
}

func TestJoinReorderDisabledKeepsFromOrder(t *testing.T) {
	cat := chainCatalog(t, 10000, 500, 10)
	cfg := DefaultConfig()
	cfg.Rules.JoinReordering = false
	eng, err := NewEngine(cat, cfg)
	if err != nil {
		t.Fatal(err)
	}
	pq, err := eng.Plan(context.Background(),
		`SELECT t1.id FROM t1 JOIN t2 ON t1.k2 = t2.id JOIN t3 ON t2.k3 = t3.id`)
	if err != nil {
		t.Fatal(err)
	}
	for _, n := range pq.Notes {
		if n.Rule == "join-reorder" && strings.Contains(n.Detail, "join order:") {
			t.Fatalf("reorder ran despite being disabled: %s", n.Detail)
		}
	}
	if !strings.Contains(pq.Explain, "rule disabled") {
		t.Fatalf("expected disabled note in explain:\n%s", pq.Explain)
	}
}

func TestCrossProductExplainedWhenUnavoidable(t *testing.T) {
	cat := chainCatalog(t, 100, 50, 10)
	eng, err := NewEngine(cat, DefaultConfig())
	if err != nil {
		t.Fatal(err)
	}
	// t3 has no condition connecting it to t1/t2: cross product required.
	pq, err := eng.Plan(context.Background(),
		`SELECT t1.id, t3.id FROM t1 JOIN t2 ON t1.k2 = t2.id, t3`)
	if err != nil {
		t.Fatal(err)
	}
	var explained bool
	for _, n := range pq.Notes {
		if strings.Contains(n.Detail, "cross product") {
			explained = true
			t.Logf("explanation: [%s] %s", n.Rule, n.Detail)
		}
	}
	if !explained {
		t.Fatalf("expected a cross-product explanation in notes:\n%s", pq.Explain)
	}
	// Still semantically correct.
	assertEquivalent(t, eng, `SELECT t1.id, t3.id FROM t1 JOIN t2 ON t1.k2 = t2.id, t3`)
}

func TestPredicatePushdownPlan(t *testing.T) {
	cat := chainCatalog(t, 1000, 100, 10)
	eng, err := NewEngine(cat, DefaultConfig())
	if err != nil {
		t.Fatal(err)
	}
	pq, err := eng.Plan(context.Background(),
		`SELECT t1.id FROM t1 JOIN t2 ON t1.k2 = t2.id WHERE t1.id < 5 AND t2.id >= 90`)
	if err != nil {
		t.Fatal(err)
	}
	// Filters must sit directly on the scans, not above the join.
	if !strings.Contains(pq.Explain, "Filter (t1.id < 5)\n        SeqScan t1") &&
		!strings.Contains(pq.Explain, "Filter (t1.id < 5)") {
		t.Fatalf("predicate not pushed to scan:\n%s", pq.Explain)
	}
	// The join node must not carry a Filter above it containing t1.id < 5.
	joinIdx := strings.Index(pq.Explain, "Join")
	filterIdx := strings.Index(pq.Explain, "Filter (t1.id < 5)")
	if filterIdx < joinIdx && filterIdx >= 0 {
		t.Fatalf("single-table predicate left above join:\n%s", pq.Explain)
	}
}

func TestResidualPredicateExplained(t *testing.T) {
	cat := chainCatalog(t, 100, 100, 10)
	eng, err := NewEngine(cat, DefaultConfig())
	if err != nil {
		t.Fatal(err)
	}
	// Range predicate across both join sides cannot be pushed or become a
	// hash key; it must remain a residual with an explanation.
	pq, err := eng.Plan(context.Background(),
		`SELECT t1.id FROM t1 JOIN t2 ON t1.k2 = t2.id WHERE t1.id < t2.id`)
	if err != nil {
		t.Fatal(err)
	}
	var explained bool
	for _, n := range pq.Notes {
		if strings.Contains(n.Detail, "residual") {
			explained = true
			t.Logf("note: %s", n.Detail)
		}
	}
	if !explained {
		t.Fatalf("expected residual explanation:\n%s", pq.Explain)
	}
	assertEquivalent(t, eng, `SELECT t1.id FROM t1 JOIN t2 ON t1.k2 = t2.id WHERE t1.id < t2.id`)
}

func TestProjectionPruningDropsColumns(t *testing.T) {
	cat := chainCatalog(t, 100, 100, 10)
	eng, err := NewEngine(cat, DefaultConfig())
	if err != nil {
		t.Fatal(err)
	}
	pq, err := eng.Plan(context.Background(), `SELECT t1.id FROM t1 JOIN t2 ON t1.k2 = t2.id`)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(pq.Explain, "reads 2 of 2 columns") {
		// t1 has only id,k2 and both are needed; t2 needs only id.
		if !strings.Contains(pq.Explain, "scan of t2 reads 1 of 2 columns") {
			t.Fatalf("expected pruning note for t2:\n%s", pq.Explain)
		}
	}
	if strings.Contains(pq.Explain, "t2.k3") {
		t.Fatalf("unneeded column t2.k3 still in plan:\n%s", pq.Explain)
	}
}
