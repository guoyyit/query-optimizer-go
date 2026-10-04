package plan_test

import (
	"context"
	"testing"

	"github.com/guoyyit/query-optimizer-go/config"
	"github.com/guoyyit/query-optimizer-go/plan"
	"github.com/guoyyit/query-optimizer-go/rows"
	"github.com/guoyyit/query-optimizer-go/testhelper"
)

func TestStatsMissingDegradation(t *testing.T) {
	ctx := context.Background()
	// table WITHOUT stats
	cat := testhelper.BuildCatalog([]testhelper.TableDef{
		{
			Name: "t", Stats: false,
			Columns: []rows.Column{testhelper.Col("id", rows.TypeInt), testhelper.Col("val", rows.TypeInt)},
			Rows: []rows.Row{
				testhelper.IntRow(1, 100),
				testhelper.IntRow(2, 200),
				testhelper.IntRow(3, 300),
			},
		},
	})
	p := plan.NewPlanner(cat, config.Default())

	t.Run("degraded marker present", func(t *testing.T) {
		res, err := p.Plan(ctx, "SELECT id FROM t WHERE id = 1")
		if err != nil {
			t.Fatal(err)
		}
		if len(res.Degraded) == 0 {
			t.Error("expected degraded warnings when stats are missing")
		}
		found := false
		for _, d := range res.Degraded {
			if contains(d, "missing statistics") {
				found = true
			}
		}
		if !found {
			t.Errorf("expected 'missing statistics' warning, got %v", res.Degraded)
		}
	})

	t.Run("results still correct", func(t *testing.T) {
		q := "SELECT id, val FROM t WHERE id > 1 ORDER BY id"
		opt, _, err := testhelper.OptExecute(ctx, p, cat, q)
		if err != nil {
			t.Fatal(err)
		}
		naive, _, err := testhelper.NaiveExecute(ctx, cat, q)
		if err != nil {
			t.Fatal(err)
		}
		if !testhelper.RowsEqual(opt, naive) {
			t.Errorf("results mismatch with missing stats\n  opt:   %v\n  naive: %v", opt, naive)
		}
	})

	t.Run("degraded marker in explain", func(t *testing.T) {
		res, err := p.Plan(ctx, "SELECT id FROM t")
		if err != nil {
			t.Fatal(err)
		}
		if !contains(res.Explain, "Degraded") {
			t.Error("explain should contain degraded statistics section")
		}
	})
}

func TestStatsPresentNoDegradation(t *testing.T) {
	ctx := context.Background()
	cat := testhelper.BuildCatalog([]testhelper.TableDef{
		{
			Name: "t", Stats: true,
			Columns: []rows.Column{testhelper.Col("id", rows.TypeInt)},
			Rows:    []rows.Row{testhelper.IntRow(1), testhelper.IntRow(2)},
		},
	})
	p := plan.NewPlanner(cat, config.Default())
	res, err := p.Plan(ctx, "SELECT id FROM t WHERE id = 1")
	if err != nil {
		t.Fatal(err)
	}
	for _, d := range res.Degraded {
		if contains(d, "missing statistics") {
			t.Errorf("should not report missing stats when present: %s", d)
		}
	}
}

func contains(s, sub string) bool {
	return len(s) >= len(sub) && (s == sub || indexOf(s, sub) >= 0)
}

func indexOf(s, sub string) int {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return i
		}
	}
	return -1
}
