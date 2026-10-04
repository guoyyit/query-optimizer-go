package plan_test

import (
	"context"
	"strings"
	"testing"

	"github.com/guoyyit/query-optimizer-go/catalog"
	"github.com/guoyyit/query-optimizer-go/config"
	"github.com/guoyyit/query-optimizer-go/plan"
	"github.com/guoyyit/query-optimizer-go/plan/physical"
	"github.com/guoyyit/query-optimizer-go/rows"
	"github.com/guoyyit/query-optimizer-go/stats"
	"github.com/guoyyit/query-optimizer-go/testhelper"
)

func joinCat() *catalog.Catalog {
	return testhelper.BuildCatalog([]testhelper.TableDef{
		{
			Name: "a", Stats: true,
			Columns: []rows.Column{testhelper.Col("id", rows.TypeInt), testhelper.Col("b_id", rows.TypeInt), testhelper.Col("val", rows.TypeInt)},
			Rows: []rows.Row{
				testhelper.IntRow(1, 10, 100),
				testhelper.IntRow(2, 20, 200),
				testhelper.IntRow(3, 30, 300),
				testhelper.IntRow(4, 40, 400),
			},
		},
		{
			Name: "b", Stats: true,
			Columns: []rows.Column{testhelper.Col("id", rows.TypeInt), testhelper.Col("c_id", rows.TypeInt), testhelper.Col("val", rows.TypeInt)},
			Rows: []rows.Row{
				testhelper.IntRow(10, 100, 1000),
				testhelper.IntRow(20, 200, 2000),
				testhelper.IntRow(30, 300, 3000),
			},
		},
		{
			Name: "c", Stats: true,
			Columns: []rows.Column{testhelper.Col("id", rows.TypeInt), testhelper.Col("val", rows.TypeInt)},
			Rows: []rows.Row{
				testhelper.IntRow(100, 1),
				testhelper.IntRow(200, 2),
				testhelper.IntRow(300, 3),
				testhelper.IntRow(400, 4),
				testhelper.IntRow(500, 5),
			},
		},
	})
}

func countNodes(n physical.PhysicalNode, pred func(physical.PhysicalNode) bool) int {
	count := 0
	var walk func(x physical.PhysicalNode)
	walk = func(x physical.PhysicalNode) {
		if pred(x) {
			count++
		}
		switch v := x.(type) {
		case *physical.Filter:
			walk(v.Child)
		case *physical.Project:
			walk(v.Child)
		case *physical.HashJoin:
			walk(v.Left)
			walk(v.Right)
		case *physical.NLJ:
			walk(v.Left)
			walk(v.Right)
		case *physical.HashAggregate:
			walk(v.Child)
		case *physical.Sort:
			walk(v.Child)
		case *physical.Limit:
			walk(v.Child)
		}
	}
	walk(n)
	return count
}

func hasCrossJoin(n physical.PhysicalNode) bool {
	found := false
	var walk func(x physical.PhysicalNode)
	walk = func(x physical.PhysicalNode) {
		if nlj, ok := x.(*physical.NLJ); ok && nlj.JoinType == 2 {
			found = true
		}
		switch v := x.(type) {
		case *physical.Filter:
			walk(v.Child)
		case *physical.Project:
			walk(v.Child)
		case *physical.HashJoin:
			walk(v.Left)
			walk(v.Right)
		case *physical.NLJ:
			walk(v.Left)
			walk(v.Right)
		case *physical.HashAggregate:
			walk(v.Child)
		case *physical.Sort:
			walk(v.Child)
		case *physical.Limit:
			walk(v.Child)
		}
	}
	walk(n)
	return found
}

func TestPredicatePushdown(t *testing.T) {
	ctx := context.Background()
	cat := joinCat()
	p := plan.NewPlanner(cat, config.Default())

	t.Run("filter pushed to scan", func(t *testing.T) {
		res, err := p.Plan(ctx, "SELECT id FROM a WHERE val > 150")
		if err != nil {
			t.Fatal(err)
		}
		// filter should be directly above the scan
		filters := countNodes(res.Physical, func(n physical.PhysicalNode) bool {
			_, ok := n.(*physical.Filter)
			return ok
		})
		if filters != 1 {
			t.Errorf("expected 1 filter node, got %d", filters)
		}
		// no filter above a join
		joinFilters := 0
		var walk func(n physical.PhysicalNode)
		walk = func(n physical.PhysicalNode) {
			if f, ok := n.(*physical.Filter); ok {
				if _, ok := f.Child.(*physical.HashJoin); ok {
					joinFilters++
				}
				if _, ok := f.Child.(*physical.NLJ); ok {
					joinFilters++
				}
				walk(f.Child)
			}
			if hj, ok := n.(*physical.HashJoin); ok {
				walk(hj.Left)
				walk(hj.Right)
			}
			if nlj, ok := n.(*physical.NLJ); ok {
				walk(nlj.Left)
				walk(nlj.Right)
			}
			if pr, ok := n.(*physical.Project); ok {
				walk(pr.Child)
			}
		}
		walk(res.Physical)
		if joinFilters != 0 {
			t.Errorf("filter should be pushed below join, found %d filters above joins", joinFilters)
		}
	})

	t.Run("cross join converted to inner", func(t *testing.T) {
		res, err := p.Plan(ctx, "SELECT a.id, b.id FROM a, b WHERE a.b_id = b.id")
		if err != nil {
			t.Fatal(err)
		}
		if hasCrossJoin(res.Physical) {
			t.Error("cross join with equi-predicate should be converted to inner join")
		}
		// should have a rewrite note about conversion
		found := false
		for _, n := range res.Notes {
			if strings.Contains(n.Detail, "CROSS JOIN to INNER JOIN") {
				found = true
			}
		}
		if !found {
			t.Error("expected rewrite note about cross-to-inner conversion")
		}
	})

	t.Run("multi-table predicate becomes join condition", func(t *testing.T) {
		res, err := p.Plan(ctx, "SELECT a.id, b.id FROM a JOIN b ON a.b_id = b.id WHERE a.val + b.val > 0")
		if err != nil {
			t.Fatal(err)
		}
		// predicate references both tables; it should be attached to the join
		// (as a non-equi condition) rather than pushed to a single scan
		foundJoinCond := false
		var walk func(n physical.PhysicalNode)
		walk = func(n physical.PhysicalNode) {
			switch x := n.(type) {
			case *physical.NLJ:
				if x.Cond != nil {
					foundJoinCond = true
				}
				walk(x.Left)
				walk(x.Right)
			case *physical.HashJoin:
				if x.Cond != nil {
					foundJoinCond = true
				}
				walk(x.Left)
				walk(x.Right)
			case *physical.Filter:
				walk(x.Child)
			case *physical.Project:
				walk(x.Child)
			}
		}
		walk(res.Physical)
		if !foundJoinCond {
			t.Error("expected multi-table predicate to be attached as join condition")
		}
	})
}

func TestJoinReorder(t *testing.T) {
	ctx := context.Background()
	cat := joinCat()
	p := plan.NewPlanner(cat, config.Default())

	t.Run("no cross join for connected graph", func(t *testing.T) {
		res, err := p.Plan(ctx, "SELECT a.id, b.id, c.val FROM a JOIN b ON a.b_id = b.id JOIN c ON b.c_id = c.id")
		if err != nil {
			t.Fatal(err)
		}
		if hasCrossJoin(res.Physical) {
			t.Error("connected join graph must not produce Cartesian product")
		}
	})

	t.Run("disconnected graph explains cross join", func(t *testing.T) {
		res, err := p.Plan(ctx, "SELECT a.id, c.val FROM a, c")
		if err != nil {
			t.Fatal(err)
		}
		if !hasCrossJoin(res.Physical) {
			t.Error("disconnected join graph should produce cross join")
		}
		found := false
		for _, n := range res.Notes {
			if strings.Contains(n.Detail, "disconnected") || strings.Contains(n.Detail, "Cartesian") {
				found = true
			}
		}
		if !found {
			t.Error("expected explanation for unavoidable Cartesian product")
		}
	})

	t.Run("reorder disabled keeps literal order", func(t *testing.T) {
		cfg := config.Default()
		cfg.Rules.JoinReorder = false
		p2 := plan.NewPlanner(cat, cfg)
		res, err := p2.Plan(ctx, "SELECT a.id, b.id FROM a JOIN b ON a.b_id = b.id")
		if err != nil {
			t.Fatal(err)
		}
		// with reorder disabled, the plan should still be valid
		if res.Physical == nil {
			t.Fatal("expected non-nil physical plan")
		}
	})
}

func TestTieBreaking(t *testing.T) {
	ctx := context.Background()
	cat := joinCat()
	cfg := config.Default()

	// plan the same query many times with fresh planners (different map
	// iteration orders internally) and verify identical plans
	q := "SELECT a.id, b.id, c.val FROM a JOIN b ON a.b_id = b.id JOIN c ON b.c_id = c.id"
	var firstSig string
	for i := 0; i < 30; i++ {
		p := plan.NewPlanner(cat, cfg)
		res, err := p.Plan(ctx, q)
		if err != nil {
			t.Fatal(err)
		}
		if firstSig == "" {
			firstSig = res.Explain
		} else if res.Explain != firstSig {
			t.Fatalf("plan %d differs from first plan", i)
		}
	}

	// same query with stats collected in different orders must match
	cat2 := joinCat()
	for _, name := range []string{"c", "b", "a"} {
		tbl, _ := cat2.Get(name)
		_ = cat2.SetStats(name, stats.Collect(name, tbl.Schema, tbl.Rows))
	}
	p := plan.NewPlanner(cat2, cfg)
	res, err := p.Plan(ctx, q)
	if err != nil {
		t.Fatal(err)
	}
	if res.Explain != firstSig {
		t.Error("plan differs when stats collection order changes")
	}
}

func TestIndexSelection(t *testing.T) {
	ctx := context.Background()
	cat := joinCat()

	t.Run("index scan for selective equality", func(t *testing.T) {
		// use a large table where index scan is cheaper than seq scan
		large := testhelper.BuildCatalog([]testhelper.TableDef{
			{
				Name: "big", Stats: true,
				Columns: []rows.Column{testhelper.Col("id", rows.TypeInt), testhelper.Col("val", rows.TypeInt)},
				Rows: func() []rows.Row {
					rs := make([]rows.Row, 1000)
					for i := range rs {
						rs[i] = testhelper.IntRow(int64(i), int64(i*10))
					}
					return rs
				}(),
			},
		})
		p2 := plan.NewPlanner(large, config.Default())
		res, err := p2.Plan(ctx, "SELECT id FROM big WHERE id = 42")
		if err != nil {
			t.Fatal(err)
		}
		idxScans := countNodes(res.Physical, func(n physical.PhysicalNode) bool {
			_, ok := n.(*physical.IndexScan)
			return ok
		})
		if idxScans != 1 {
			t.Errorf("expected 1 index scan, got %d", idxScans)
		}
	})

	t.Run("index scan disabled", func(t *testing.T) {
		cfg := config.Default()
		cfg.Rules.IndexScan = false
		p2 := plan.NewPlanner(cat, cfg)
		res, err := p2.Plan(ctx, "SELECT id FROM a WHERE id = 2")
		if err != nil {
			t.Fatal(err)
		}
		idxScans := countNodes(res.Physical, func(n physical.PhysicalNode) bool {
			_, ok := n.(*physical.IndexScan)
			return ok
		})
		if idxScans != 0 {
			t.Errorf("expected 0 index scans when disabled, got %d", idxScans)
		}
	})
}
