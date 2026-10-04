package plan_test

import (
	"context"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/guoyyit/query-optimizer-go/config"
	"github.com/guoyyit/query-optimizer-go/exec"
	"github.com/guoyyit/query-optimizer-go/plancache"
	"github.com/guoyyit/query-optimizer-go/plan"
	"github.com/guoyyit/query-optimizer-go/rows"
	"github.com/guoyyit/query-optimizer-go/testhelper"
)

func TestConcurrentQueries(t *testing.T) {
	ctx := context.Background()
	cat := testhelper.BuildCatalog([]testhelper.TableDef{
		{
			Name: "t", Stats: true,
			Columns: []rows.Column{testhelper.Col("id", rows.TypeInt), testhelper.Col("val", rows.TypeInt)},
			Rows: func() []rows.Row {
				rs := make([]rows.Row, 100)
				for i := range rs {
					rs[i] = testhelper.IntRow(int64(i), int64(i*2))
				}
				return rs
			}(),
		},
	})
	p := plan.NewPlanner(cat, config.Default())
	cache := plancache.New()
	p.SetCache(cache)

	var wg sync.WaitGroup
	errCh := make(chan error, 50)
	for i := 0; i < 50; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			q := fmt.Sprintf("SELECT id FROM t WHERE id = %d", i%20)
			res, err := p.Plan(ctx, q)
			if err != nil {
				errCh <- err
				return
			}
			out, err := exec.Execute(ctx, res.Physical, cat)
			if err != nil {
				errCh <- err
				return
			}
			if len(out) != 1 {
				errCh <- fmt.Errorf("expected 1 row for id=%d, got %d", i%20, len(out))
			}
		}(i)
	}
	wg.Wait()
	close(errCh)
	for err := range errCh {
		t.Error(err)
	}
}

func TestSlowQueryDoesNotBlockFastQueries(t *testing.T) {
	ctx := context.Background()
	cat := testhelper.BuildCatalog([]testhelper.TableDef{
		{
			Name: "big1", Stats: true,
			Columns: []rows.Column{testhelper.Col("id", rows.TypeInt)},
			Rows: func() []rows.Row {
				rs := make([]rows.Row, 3000)
				for i := range rs {
					rs[i] = testhelper.IntRow(int64(i))
				}
				return rs
			}(),
		},
		{
			Name: "big2", Stats: true,
			Columns: []rows.Column{testhelper.Col("id", rows.TypeInt)},
			Rows: func() []rows.Row {
				rs := make([]rows.Row, 3000)
				for i := range rs {
					rs[i] = testhelper.IntRow(int64(i))
				}
				return rs
			}(),
		},
		{
			Name: "small", Stats: true,
			Columns: []rows.Column{testhelper.Col("id", rows.TypeInt)},
			Rows:    []rows.Row{testhelper.IntRow(1)},
		},
	})
	p := plan.NewPlanner(cat, config.Default())

	// start a slow cross join with a timeout
	slowCtx, cancel := context.WithTimeout(ctx, 100*time.Millisecond)
	defer cancel()
	res, err := p.Plan(slowCtx, "SELECT big1.id, big2.id FROM big1, big2")
	if err != nil {
		t.Fatal(err)
	}
	_, err = exec.Execute(slowCtx, res.Physical, cat)
	if err != context.DeadlineExceeded {
		t.Fatalf("expected DeadlineExceeded, got %v", err)
	}

	// fast query must still complete promptly
	fastCtx, fastCancel := context.WithTimeout(ctx, 5*time.Second)
	defer fastCancel()
	res2, err := p.Plan(fastCtx, "SELECT id FROM small WHERE id = 1")
	if err != nil {
		t.Fatal(err)
	}
	out, err := exec.Execute(fastCtx, res2.Physical, cat)
	if err != nil {
		t.Fatal(err)
	}
	if len(out) != 1 {
		t.Errorf("expected 1 row, got %d", len(out))
	}
}

func TestCanceledPlanNotCached(t *testing.T) {
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

	canceledCtx, cancel := context.WithCancel(ctx)
	cancel()
	_, err := p.Plan(canceledCtx, "SELECT id FROM t WHERE id = 1")
	if err != context.Canceled {
		t.Fatalf("expected context.Canceled, got %v", err)
	}
	if cache.Size() != 0 {
		t.Errorf("canceled plan should not be cached, cache size=%d", cache.Size())
	}
}
