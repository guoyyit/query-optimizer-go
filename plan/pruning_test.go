package plan_test

import (
	"context"
	"testing"

	"github.com/guoyyit/query-optimizer-go/config"
	"github.com/guoyyit/query-optimizer-go/plan"
	"github.com/guoyyit/query-optimizer-go/plan/physical"
	"github.com/guoyyit/query-optimizer-go/rows"
	"github.com/guoyyit/query-optimizer-go/testhelper"
)

func TestProjectionPruning(t *testing.T) {
	ctx := context.Background()
	cat := testhelper.BuildCatalog([]testhelper.TableDef{
		{
			Name: "a", Stats: true,
			Columns: []rows.Column{
				testhelper.Col("id", rows.TypeInt),
				testhelper.Col("name", rows.TypeString),
				testhelper.Col("val", rows.TypeInt),
				testhelper.Col("extra", rows.TypeString),
			},
			Rows: []rows.Row{
				testhelper.MixedRow(rows.IntValue(1), rows.StringValue("x"), rows.IntValue(100), rows.StringValue("e1")),
				testhelper.MixedRow(rows.IntValue(2), rows.StringValue("y"), rows.IntValue(200), rows.StringValue("e2")),
			},
		},
	})
	p := plan.NewPlanner(cat, config.Default())

	t.Run("only needed columns emitted", func(t *testing.T) {
		res, err := p.Plan(ctx, "SELECT id FROM a WHERE val > 50")
		if err != nil {
			t.Fatal(err)
		}
		// find the seq scan and check its schema
		var scan *physical.SeqScan
		var walk func(n physical.PhysicalNode)
		walk = func(n physical.PhysicalNode) {
			switch x := n.(type) {
			case *physical.SeqScan:
				scan = x
			case *physical.Filter:
				walk(x.Child)
			case *physical.Project:
				walk(x.Child)
			}
		}
		walk(res.Physical)
		if scan == nil {
			t.Fatal("expected seq scan")
		}
		// needed columns: id (select), val (filter). name and extra pruned.
		if len(scan.Schema.Columns) != 2 {
			t.Errorf("expected 2 columns after pruning, got %d: %v", len(scan.Schema.Columns), scan.Schema.Columns)
		}
		names := map[string]bool{}
		for _, c := range scan.Schema.Columns {
			names[c.Name] = true
		}
		if !names["id"] || !names["val"] {
			t.Errorf("expected id and val columns, got %v", names)
		}
		if names["name"] || names["extra"] {
			t.Errorf("name and extra should be pruned, got %v", names)
		}
	})

	t.Run("count star emits no columns", func(t *testing.T) {
		res, err := p.Plan(ctx, "SELECT COUNT(*) FROM a")
		if err != nil {
			t.Fatal(err)
		}
		var scan *physical.SeqScan
		var walk func(n physical.PhysicalNode)
		walk = func(n physical.PhysicalNode) {
			switch x := n.(type) {
			case *physical.SeqScan:
				scan = x
			case *physical.Filter:
				walk(x.Child)
			case *physical.Project:
				walk(x.Child)
			case *physical.HashAggregate:
				walk(x.Child)
			}
		}
		walk(res.Physical)
		if scan == nil {
			t.Fatal("expected seq scan")
		}
		if len(scan.Schema.Columns) != 0 {
			t.Errorf("COUNT(*) should prune all columns, got %d", len(scan.Schema.Columns))
		}
	})

	t.Run("pruning disabled keeps all columns", func(t *testing.T) {
		cfg := config.Default()
		cfg.Rules.ProjectionPruning = false
		p2 := plan.NewPlanner(cat, cfg)
		res, err := p2.Plan(ctx, "SELECT id FROM a WHERE val > 50")
		if err != nil {
			t.Fatal(err)
		}
		var scan *physical.SeqScan
		var walk func(n physical.PhysicalNode)
		walk = func(n physical.PhysicalNode) {
			switch x := n.(type) {
			case *physical.SeqScan:
				scan = x
			case *physical.Filter:
				walk(x.Child)
			case *physical.Project:
				walk(x.Child)
			}
		}
		walk(res.Physical)
		if scan == nil {
			t.Fatal("expected seq scan")
		}
		if len(scan.Schema.Columns) != 4 {
			t.Errorf("expected 4 columns with pruning disabled, got %d", len(scan.Schema.Columns))
		}
	})
}
