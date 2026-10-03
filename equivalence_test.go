package optimizer

import (
	"context"
	"fmt"
	"testing"

	"github.com/guoyyit/query-optimizer-go/catalog"
	"github.com/guoyyit/query-optimizer-go/exec"
	"github.com/guoyyit/query-optimizer-go/value"
)

// richCatalog builds tables exercising NULLs, mixed numeric types,
// boundary values and duplicate column names across tables.
func richCatalog(t *testing.T) *catalog.Catalog {
	t.Helper()
	cat := catalog.New()
	must := func(err error) {
		if err != nil {
			t.Fatal(err)
		}
	}
	must(cat.CreateTable("a", []catalog.Column{
		{Name: "id", Type: value.Int},
		{Name: "val", Type: value.Int}, // duplicate column name with b.val
		{Name: "tag", Type: value.String},
	}))
	must(cat.CreateTable("b", []catalog.Column{
		{Name: "bid", Type: value.Int},
		{Name: "val", Type: value.Float}, // float here, int in a: coercion
		{Name: "flag", Type: value.Bool},
	}))
	must(cat.CreateTable("c", []catalog.Column{
		{Name: "cid", Type: value.Int},
		{Name: "ref", Type: value.Int},
	}))
	a := cat.Table("a")
	for _, r := range [][]value.Value{
		{value.FromInt(1), value.FromInt(100), value.FromString("x")},
		{value.FromInt(2), value.NullValue(), value.FromString("y")},                   // NULL val
		{value.FromInt(3), value.FromInt(-9223372036854775808), value.FromString("x")}, // min int64
		{value.FromInt(4), value.FromInt(9223372036854775807), value.NullValue()},      // max int64, NULL tag
		{value.FromInt(5), value.FromInt(0), value.FromString("")},
	} {
		must(a.Insert(r))
	}
	b := cat.Table("b")
	for _, r := range [][]value.Value{
		{value.FromInt(1), value.FromFloat(100.0), value.FromBool(true)},
		{value.FromInt(2), value.FromFloat(-0.5), value.FromBool(false)},
		{value.FromInt(3), value.NullValue(), value.FromBool(true)}, // NULL val
		{value.FromInt(6), value.FromFloat(1e18), value.FromBool(false)},
		{value.FromInt(7), value.FromFloat(0.0), value.NullValue()}, // NULL bool
	} {
		must(b.Insert(r))
	}
	cc := cat.Table("c")
	for _, r := range [][]value.Value{
		{value.FromInt(100), value.FromInt(1)},
		{value.FromInt(200), value.FromInt(2)},
		{value.FromInt(300), value.NullValue()}, // NULL ref
		{value.FromInt(400), value.FromInt(6)},
	} {
		must(cc.Insert(r))
	}
	a.SetStats(&catalog.Stats{RowCount: 5, NDV: map[string]int64{"id": 5, "val": 4, "tag": 3}})
	b.SetStats(&catalog.Stats{RowCount: 5, NDV: map[string]int64{"bid": 5, "val": 4, "flag": 2}})
	cc.SetStats(&catalog.Stats{RowCount: 4, NDV: map[string]int64{"cid": 4, "ref": 3}})
	return cat
}

func assertEquivalent(t *testing.T, eng *Engine, q string) {
	t.Helper()
	ctx := context.Background()
	got, err := eng.Execute(ctx, q)
	if err != nil {
		t.Fatalf("optimized execute: %v", err)
	}
	want, err := eng.ExecuteNaive(ctx, q)
	if err != nil {
		t.Fatalf("naive execute: %v", err)
	}
	if len(got.Schema) != len(want.Schema) {
		t.Fatalf("schema width mismatch: got %d want %d", len(got.Schema), len(want.Schema))
	}
	if len(got.Rows) != len(want.Rows) {
		t.Fatalf("query %s\nrow count mismatch: got %d want %d\ngot:  %v\nwant: %v",
			q, len(got.Rows), len(want.Rows), got.Rows, want.Rows)
	}
	for i := range got.Rows {
		if exec.CompareRows(got.Rows[i], want.Rows[i]) != 0 {
			t.Fatalf("query %s\nrow %d mismatch:\ngot:  %v\nwant: %v", q, i, got.Rows[i], want.Rows[i])
		}
	}
}

func TestEquivalenceSuite(t *testing.T) {
	cat := richCatalog(t)
	eng, err := NewEngine(cat, DefaultConfig())
	if err != nil {
		t.Fatal(err)
	}
	queries := []string{
		// NULL propagation in WHERE
		`SELECT a.id FROM a WHERE a.val > 50`,
		`SELECT a.id FROM a WHERE NOT a.val > 50`,
		// int vs float coercion across tables
		`SELECT a.id, b.bid FROM a JOIN b ON a.val = b.val`,
		// boundary values
		`SELECT a.id FROM a WHERE a.val = -9223372036854775808`,
		`SELECT a.id FROM a WHERE a.val >= 9223372036854775807`,
		// duplicate column names across tables, qualified
		`SELECT a.val, b.val FROM a JOIN b ON a.id = b.bid WHERE a.val < b.val`,
		// type-inconsistent comparison (int col vs string literal): no rows
		`SELECT a.id FROM a WHERE a.val = 'hello'`,
		// NULL join keys never match
		`SELECT a.id, c.cid FROM a JOIN c ON a.id = c.ref`,
		// three-table join with mixed predicates
		`SELECT a.id, b.bid, c.cid FROM a JOIN b ON a.id = b.bid JOIN c ON b.bid = c.ref WHERE a.val >= 0 AND b.flag = TRUE`,
		// OR / NOT
		`SELECT a.id FROM a WHERE a.tag = 'x' OR a.val < 0`,
		`SELECT a.id FROM a WHERE NOT (a.tag = 'x')`,
		// comma join converted by WHERE predicate
		`SELECT a.id, b.bid FROM a, b WHERE a.id = b.bid AND b.val < 50`,
		// star projection with duplicate names
		`SELECT * FROM a JOIN b ON a.id = b.bid`,
		// empty result
		`SELECT a.id FROM a WHERE a.id = 12345`,
		// bool column with NULL
		`SELECT b.bid FROM b WHERE b.flag = TRUE`,
	}
	for _, q := range queries {
		t.Run(q, func(t *testing.T) { assertEquivalent(t, eng, q) })
	}
}

func TestAmbiguousColumnRejected(t *testing.T) {
	cat := richCatalog(t)
	eng, err := NewEngine(cat, DefaultConfig())
	if err != nil {
		t.Fatal(err)
	}
	_, err = eng.Plan(context.Background(), `SELECT val FROM a JOIN b ON a.id = b.bid`)
	if err == nil {
		t.Fatal("expected ambiguity error")
	}
	pe, ok := err.(*PlanError)
	if !ok {
		t.Fatalf("expected *PlanError, got %T: %v", err, err)
	}
	t.Logf("got expected error: %v", pe)
}

func TestEquivalenceAcrossRuleToggles(t *testing.T) {
	cat := richCatalog(t)
	q := `SELECT a.id, b.bid, c.cid FROM a JOIN b ON a.id = b.bid JOIN c ON b.bid = c.ref WHERE a.val >= 0 AND b.flag = TRUE`
	base, err := NewEngine(cat, DefaultConfig())
	if err != nil {
		t.Fatal(err)
	}
	want, err := base.ExecuteNaive(context.Background(), q)
	if err != nil {
		t.Fatal(err)
	}
	// Every combination of rule toggles must produce identical results.
	for mask := 0; mask < 8; mask++ {
		cfg := DefaultConfig()
		cfg.Rules.PredicatePushdown = mask&1 != 0
		cfg.Rules.ProjectionPruning = mask&2 != 0
		cfg.Rules.JoinReordering = mask&4 != 0
		eng, err := NewEngine(cat, cfg)
		if err != nil {
			t.Fatal(err)
		}
		got, err := eng.Execute(context.Background(), q)
		if err != nil {
			t.Fatalf("mask %d: %v", mask, err)
		}
		if len(got.Rows) != len(want.Rows) {
			t.Fatalf("mask %d: got %d rows, want %d", mask, len(got.Rows), len(want.Rows))
		}
		for i := range got.Rows {
			if exec.CompareRows(got.Rows[i], want.Rows[i]) != 0 {
				t.Fatalf("mask %d row %d: got %v want %v", mask, i, got.Rows[i], want.Rows[i])
			}
		}
	}
}

func TestResultSortingDeterministic(t *testing.T) {
	cat := richCatalog(t)
	eng, _ := NewEngine(cat, DefaultConfig())
	q := `SELECT a.id, b.bid FROM a JOIN b ON a.id = b.bid`
	var prev [][]value.Value
	for i := 0; i < 20; i++ {
		res, err := eng.Execute(context.Background(), q)
		if err != nil {
			t.Fatal(err)
		}
		if prev != nil {
			if len(res.Rows) != len(prev) {
				t.Fatal("row count changed between runs")
			}
			for j := range res.Rows {
				if exec.CompareRows(res.Rows[j], prev[j]) != 0 {
					t.Fatalf("run %d row %d differs", i, j)
				}
			}
		}
		prev = res.Rows
	}
}

var _ = fmt.Sprintf
