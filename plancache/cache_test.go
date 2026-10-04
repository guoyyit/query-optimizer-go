package plancache_test

import (
	"context"
	"testing"

	"github.com/guoyyit/query-optimizer-go/catalog"
	"github.com/guoyyit/query-optimizer-go/config"
	"github.com/guoyyit/query-optimizer-go/plancache"
	"github.com/guoyyit/query-optimizer-go/plan"
	"github.com/guoyyit/query-optimizer-go/rows"
	"github.com/guoyyit/query-optimizer-go/stats"
	"github.com/guoyyit/query-optimizer-go/testhelper"
)

func TestCacheHitAndMiss(t *testing.T) {
	ctx := context.Background()
	cat := testhelper.BuildCatalog([]testhelper.TableDef{
		{
			Name: "t", Stats: true,
			Columns: []rows.Column{testhelper.Col("id", rows.TypeInt)},
			Rows:    []rows.Row{testhelper.IntRow(1), testhelper.IntRow(2)},
		},
	})
	p := plan.NewPlanner(cat, config.Default())
	cache := plancache.New()
	p.SetCache(cache)

	q := "SELECT id FROM t WHERE id = 1"
	r1, err := p.Plan(ctx, q)
	if err != nil {
		t.Fatal(err)
	}
	if r1.CacheHit {
		t.Error("first plan should be a cache miss")
	}
	r2, err := p.Plan(ctx, q)
	if err != nil {
		t.Fatal(err)
	}
	if !r2.CacheHit {
		t.Error("second identical plan should be a cache hit")
	}
	hits, misses := cache.Stats()
	if hits != 1 || misses != 1 {
		t.Errorf("expected 1 hit 1 miss, got %d hits %d misses", hits, misses)
	}
}

func TestCacheInvalidationOnStatsChange(t *testing.T) {
	ctx := context.Background()
	cat := testhelper.BuildCatalog([]testhelper.TableDef{
		{
			Name: "t", Stats: true,
			Columns: []rows.Column{testhelper.Col("id", rows.TypeInt), testhelper.Col("val", rows.TypeInt)},
			Rows:    []rows.Row{testhelper.IntRow(1, 10), testhelper.IntRow(2, 20)},
		},
	})
	p := plan.NewPlanner(cat, config.Default())
	cache := plancache.New()
	p.SetCache(cache)

	q := "SELECT id FROM t WHERE id = 1"
	_, _ = p.Plan(ctx, q)
	_, _ = p.Plan(ctx, q) // hit

	// change stats -> version bump -> miss
	tbl, _ := cat.Get("t")
	_ = cat.SetStats("t", stats.Collect("t", tbl.Schema, tbl.Rows))
	r3, _ := p.Plan(ctx, q)
	if r3.CacheHit {
		t.Error("plan should miss after stats change")
	}
}

func TestCacheInvalidationOnSchemaChange(t *testing.T) {
	ctx := context.Background()
	cat := testhelper.BuildCatalog([]testhelper.TableDef{
		{
			Name: "t", Stats: true,
			Columns: []rows.Column{testhelper.Col("id", rows.TypeInt)},
			Rows:    []rows.Row{testhelper.IntRow(1)},
		},
	})
	p := plan.NewPlanner(cat, config.Default())
	cache := plancache.New()
	p.SetCache(cache)

	q := "SELECT id FROM t"
	_, _ = p.Plan(ctx, q)
	_, _ = p.Plan(ctx, q)

	// add a column -> schema change -> miss
	cat.AddTable(&catalog.Table{
		Name: "t",
		Schema: rows.Schema{Columns: []rows.Column{
			{Name: "id", Type: rows.TypeInt},
			{Name: "extra", Type: rows.TypeString},
		}},
		Rows: []rows.Row{{Vals: []rows.Value{rows.IntValue(1), rows.StringValue("x")}}},
	})
	r3, _ := p.Plan(ctx, q)
	if r3.CacheHit {
		t.Error("plan should miss after schema change")
	}
}

func TestCacheInvalidationOnConfigChange(t *testing.T) {
	ctx := context.Background()
	cat := testhelper.BuildCatalog([]testhelper.TableDef{
		{
			Name: "t", Stats: true,
			Columns: []rows.Column{testhelper.Col("id", rows.TypeInt)},
			Rows:    []rows.Row{testhelper.IntRow(1), testhelper.IntRow(2)},
		},
	})
	p := plan.NewPlanner(cat, config.Default())
	cache := plancache.New()
	p.SetCache(cache)

	q := "SELECT id FROM t WHERE id = 1"
	_, _ = p.Plan(ctx, q)
	_, _ = p.Plan(ctx, q)

	cfg2 := config.Default()
	cfg2.Weights.NLJPerPair = 999.0
	_ = p.UpdateConfig(cfg2)
	r3, _ := p.Plan(ctx, q)
	if r3.CacheHit {
		t.Error("plan should miss after config change")
	}
}

func TestCacheNoStaleReuse(t *testing.T) {
	ctx := context.Background()
	cat := testhelper.BuildCatalog([]testhelper.TableDef{
		{
			Name: "t", Stats: true,
			Columns: []rows.Column{testhelper.Col("id", rows.TypeInt)},
			Rows:    []rows.Row{testhelper.IntRow(1)},
		},
	})
	p := plan.NewPlanner(cat, config.Default())
	cache := plancache.New()
	p.SetCache(cache)

	q := "SELECT id FROM t"
	r1, _ := p.Plan(ctx, q)

	// invalidate cache explicitly
	cache.Invalidate()
	r2, _ := p.Plan(ctx, q)
	if r2.CacheHit {
		t.Error("plan should miss after explicit invalidation")
	}
	_ = r1
}
