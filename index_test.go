package optimizer

import (
	"context"
	"strings"
	"testing"

	"github.com/guoyyit/query-optimizer-go/catalog"
)

func testCtx() context.Context { return context.Background() }

// TestIndexScanChosen: with a hash index on t1.id and a highly selective
// equality predicate, the planner must pick the index scan.
func TestIndexScanChosen(t *testing.T) {
	cat := chainCatalog(t, 100000, 100, 10)
	if err := cat.Table("t1").CreateIndex("id"); err != nil {
		t.Fatal(err)
	}
	eng, err := NewEngine(cat, DefaultConfig())
	if err != nil {
		t.Fatal(err)
	}
	pq, err := eng.Plan(testCtx(), `SELECT t1.id FROM t1 WHERE t1.id = 42`)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(pq.Explain, "IndexScan t1") {
		t.Fatalf("expected index scan:\n%s", pq.Explain)
	}
	assertEquivalent(t, eng, `SELECT t1.id FROM t1 WHERE t1.id = 42`)
}

// TestSeqScanWhenSelectiveEnough: a low-selectivity predicate (tiny table)
// keeps the sequential scan.
func TestSeqScanWhenIndexNotWorthIt(t *testing.T) {
	cat := chainCatalog(t, 10, 100, 10)
	// NDV(id)=1 means the equality probe matches all 10 rows: probing the
	// index costs more than just scanning the tiny table.
	cat.Table("t1").SetStats(&catalog.Stats{RowCount: 10, NDV: map[string]int64{"id": 1, "k2": 10}})
	if err := cat.Table("t1").CreateIndex("id"); err != nil {
		t.Fatal(err)
	}
	eng, err := NewEngine(cat, DefaultConfig())
	if err != nil {
		t.Fatal(err)
	}
	pq, err := eng.Plan(testCtx(), `SELECT t1.id FROM t1 WHERE t1.id = 3`)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(pq.Explain, "SeqScan t1") {
		t.Fatalf("expected seq scan for tiny table:\n%s", pq.Explain)
	}
}

// TestIndexScanWithResidual: the index predicate is used for the probe and
// the remaining predicate stays as a residual filter.
func TestIndexScanWithResidual(t *testing.T) {
	cat := chainCatalog(t, 100000, 100, 10)
	if err := cat.Table("t1").CreateIndex("id"); err != nil {
		t.Fatal(err)
	}
	eng, err := NewEngine(cat, DefaultConfig())
	if err != nil {
		t.Fatal(err)
	}
	q := `SELECT t1.id FROM t1 WHERE t1.id = 42 AND t1.k2 >= 0`
	pq, err := eng.Plan(testCtx(), q)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(pq.Explain, "IndexScan t1") || !strings.Contains(pq.Explain, "Filter (t1.k2 >= 0)") {
		t.Fatalf("expected index scan + residual filter:\n%s", pq.Explain)
	}
	assertEquivalent(t, eng, q)
}
