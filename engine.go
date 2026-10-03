// Package optimizer ties parsing, logical planning, rule rewriting,
// cost-based physical planning, plan caching and execution together.
package optimizer

import (
	"context"
	"fmt"

	"github.com/guoyyit/query-optimizer-go/cache"
	"github.com/guoyyit/query-optimizer-go/catalog"
	"github.com/guoyyit/query-optimizer-go/exec"
	"github.com/guoyyit/query-optimizer-go/plan"
	"github.com/guoyyit/query-optimizer-go/rules"
	"github.com/guoyyit/query-optimizer-go/sql"
)

// Engine is the query optimizer facade. It is safe for concurrent use.
type Engine struct {
	cat     *catalog.Catalog
	cfg     Config
	pc      *cache.Cache
	planSem chan struct{}
}

// NewEngine validates cfg and returns an engine over cat.
func NewEngine(cat *catalog.Catalog, cfg Config) (*Engine, error) {
	if err := cfg.Validate(); err != nil {
		return nil, err
	}
	maxPlans := cfg.MaxPlans
	if maxPlans <= 0 {
		maxPlans = 4
	}
	return &Engine{
		cat:     cat,
		cfg:     cfg,
		pc:      cache.New(cfg.CacheSize),
		planSem: make(chan struct{}, maxPlans),
	}, nil
}

// PlannedQuery is the output of Plan: the physical plan plus a readable
// explanation and observability markers (e.g. missing statistics).
type PlannedQuery struct {
	Physical     plan.Physical
	Logical      plan.Logical
	Explain      string
	Notes        []rules.Note
	StatsMissing []string // tables planned with conservative defaults
	FromCache    bool
}

// Plan parses and plans a query, using the plan cache when possible. The
// same query under the same catalog epoch always yields the same plan,
// regardless of planning concurrency.
func (e *Engine) Plan(ctx context.Context, query string) (*PlannedQuery, error) {
	stmt, err := sql.Parse(query)
	if err != nil {
		return nil, err
	}
	if _, err := BuildLogical(stmt, e.cat); err != nil {
		return nil, err // fail fast on semantic errors before touching cache
	}
	key := stmt.Fingerprint() + "|" + configStamp(e.cfg)
	epoch := e.cat.Epoch()

	entry, fromCache, err := e.pc.GetOrPlan(key, epoch, func() (*cache.Entry, error) {
		return e.planOnce(ctx, stmt)
	})
	if err != nil {
		return nil, err
	}
	pq := &PlannedQuery{
		Physical:  entry.Plan,
		Explain:   entry.Explain,
		FromCache: fromCache,
	}
	if extra, ok := entry.Payload.(*planPayload); ok {
		pq.Notes = extra.notes
		pq.StatsMissing = extra.missing
		pq.Logical = extra.logical
	}
	return pq, nil
}

// planOnce runs the full planning pipeline. It is bounded by the planning
// semaphore (so one slow query cannot starve others) and by PlanTimeout.
func (e *Engine) planOnce(ctx context.Context, stmt *sql.SelectStmt) (*cache.Entry, error) {
	if e.cfg.PlanTimeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, e.cfg.PlanTimeout)
		defer cancel()
	}
	select {
	case e.planSem <- struct{}{}:
		defer func() { <-e.planSem }()
	case <-ctx.Done():
		return nil, ctx.Err()
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}

	logical, err := BuildLogical(stmt, e.cat)
	if err != nil {
		return nil, err
	}
	log := &rules.Log{}
	aliasToDb := map[string]string{}
	collectAliases(logical, aliasToDb)
	stats := newStatsView(e.cat, e.cfg, aliasToDb)

	opt := logical
	if e.cfg.Rules.PredicatePushdown {
		opt = rules.Pushdown(opt, log)
	}
	if e.cfg.Rules.JoinReordering {
		opt = rules.Reorder(opt, stats, log)
	} else {
		log.Add("join-reorder", "rule disabled by configuration; keeping FROM clause order")
	}
	if e.cfg.Rules.ProjectionPruning {
		// The root project defines what the query needs.
		if proj, ok := opt.(*plan.Project); ok {
			opt = rules.Prune(opt, proj.Cols, log)
		}
	} else {
		log.Add("projection-pruning", "rule disabled by configuration; scans read all columns")
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	pp := &physPlanner{cfg: e.cfg, stats: stats, log: log}
	phys := pp.planNode(opt).node

	return &cache.Entry{
		Plan:    phys,
		Explain: explainPlan(phys, log.Notes, stats.MissingTables()),
		Payload: &planPayload{
			notes:   log.Notes,
			missing: stats.MissingTables(),
			logical: opt,
		},
	}, nil
}

// Execute plans (with cache) and runs the query, honouring ctx
// cancellation. Cancelled queries return an error and no partial rows, and
// never populate the plan cache with a plan built from a cancelled pass.
func (e *Engine) Execute(ctx context.Context, query string) (*exec.Result, error) {
	pq, err := e.Plan(ctx, query)
	if err != nil {
		return nil, err
	}
	res, err := exec.Run(ctx, e.cat, pq.Physical)
	if err != nil {
		return nil, err
	}
	exec.SortRows(res.Rows)
	return res, nil
}

// ExecuteNaive bypasses all optimization (cartesian product + filter) and
// serves as the semantic equivalence baseline.
func (e *Engine) ExecuteNaive(ctx context.Context, query string) (*exec.Result, error) {
	stmt, err := sql.Parse(query)
	if err != nil {
		return nil, err
	}
	logical, err := BuildLogical(stmt, e.cat)
	if err != nil {
		return nil, err
	}
	res, err := exec.RunNaive(ctx, e.cat, logical)
	if err != nil {
		return nil, err
	}
	exec.SortRows(res.Rows)
	return res, nil
}

// InvalidatePlans drops all cached plans (e.g. after a bulk schema change
// the caller knows about). Normally invalidation is automatic via the
// catalog epoch.
func (e *Engine) InvalidatePlans() { e.pc.InvalidateAll() }

// CacheStats exposes plan-cache counters for observability.
func (e *Engine) CacheStats() (hits, misses int64) { return e.pc.Stats() }

func collectAliases(node plan.Logical, out map[string]string) {
	switch n := node.(type) {
	case *plan.Scan:
		out[n.Alias] = n.Table
	default:
		for _, c := range node.Children() {
			collectAliases(c, out)
		}
	}
}

// configStamp makes cache keys sensitive to optimizer configuration:
// changing rules or weights must not reuse plans built under other
// settings.
func configStamp(cfg Config) string {
	return fmt.Sprintf("r:%v%v%v|w:%s|d:%d/%d",
		cfg.Rules.PredicatePushdown, cfg.Rules.ProjectionPruning, cfg.Rules.JoinReordering,
		weightsStamp(cfg), cfg.DefaultRows, cfg.DefaultNDV)
}

func weightsStamp(cfg Config) string {
	w := cfg.Weights
	return fmt.Sprintf("%g,%g,%g,%g,%g,%g,%g,%g",
		w.SeqScanRow, w.IndexProbe, w.IndexRow, w.FilterRow,
		w.HashBuildRow, w.HashProbeRow, w.NestedLoop, w.OutputRow)
}

// planPayload is the planner metadata stored alongside a cached plan.
type planPayload struct {
	notes   []rules.Note
	missing []string
	logical plan.Logical
}
