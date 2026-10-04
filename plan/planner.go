package plan

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"strings"
	"sync"

	"github.com/guoyyit/query-optimizer-go/catalog"
	"github.com/guoyyit/query-optimizer-go/config"
	"github.com/guoyyit/query-optimizer-go/plancache"
	"github.com/guoyyit/query-optimizer-go/plan/physical"
	"github.com/guoyyit/query-optimizer-go/sql"
)

// Result is the outcome of planning a query.
type Result struct {
	SQL      string
	Logical  LogicalNode
	Physical physical.PhysicalNode
	Notes    []RewriteNote
	Explain  string
	Degraded []string // reasons for degraded statistics
	CacheHit bool
	Cost     float64
}

// Planner turns SQL into physical plans. It is safe for concurrent use.
type Planner struct {
	cat   *catalog.Catalog
	cfgMu sync.RWMutex
	cfg   *config.Config
	cache *plancache.Cache
}

// NewPlanner creates a planner.
func NewPlanner(c *catalog.Catalog, cfg *config.Config) *Planner {
	if cfg == nil {
		cfg = config.Default()
	}
	return &Planner{cat: c, cfg: cfg}
}

// SetCache attaches a plan cache.
func (p *Planner) SetCache(cache *plancache.Cache) {
	p.cache = cache
}

// Cache returns the attached plan cache (if any).
func (p *Planner) Cache() *plancache.Cache { return p.cache }

// UpdateConfig validates and replaces the configuration. The configuration
// hash changes, so cached plans automatically miss.
func (p *Planner) UpdateConfig(cfg *config.Config) error {
	if err := cfg.Validate(); err != nil {
		return err
	}
	p.cfgMu.Lock()
	p.cfg = cfg
	p.cfgMu.Unlock()
	return nil
}

func (p *Planner) config() *config.Config {
	p.cfgMu.RLock()
	defer p.cfgMu.RUnlock()
	return p.cfg
}

// Plan parses, rewrites and cost-plans a single query.
func (p *Planner) Plan(ctx context.Context, sqlText string) (*Result, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	sel, err := sql.Parse(sqlText)
	if err != nil {
		return nil, err
	}
	cfg := p.config()

	var key plancache.Key
	if cfg.PlanCache && p.cache != nil {
		key = plancache.Key{
			QueryHash:      hashSQL(sqlText),
			CatalogVersion: p.cat.Version(),
			ConfigHash:     cfg.Hash(),
		}
		if entry, ok := p.cache.Get(key); ok {
			res := entry.Value.(*Result)
			hit := *res
			hit.CacheHit = true
			return &hit, nil
		}
	}

	logical, err := BuildLogical(sel)
	if err != nil {
		return nil, err
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}

	enabled := map[RuleName]bool{
		RulePredicatePushdown: cfg.Rules.PredicatePushdown,
		RuleProjectionPruning: cfg.Rules.ProjectionPruning,
		RuleJoinReorder:       cfg.Rules.JoinReorder,
		RuleConstantFolding:   cfg.Rules.ConstantFolding,
	}
	logical, notes := ApplyRules(logical, enabled)
	if err := ctx.Err(); err != nil {
		return nil, err
	}

	pp := &physicalPlanner{cat: p.cat, cfg: cfg}
	phys, err := pp.plan(logical)
	if err != nil {
		return nil, err
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}

	cost := totalCost(phys)
	allNotes := append(append([]RewriteNote{}, notes...), pp.notes...)
	explain := Explain(phys, allNotes, pp.degraded)
	res := &Result{
		SQL:      sqlText,
		Logical:  logical,
		Physical: phys,
		Notes:    dedupeNotes(allNotes),
		Explain:  explain,
		Degraded: dedupeStrings(pp.degraded),
		Cost:     cost,
	}
	if cfg.PlanCache && p.cache != nil && ctx.Err() == nil {
		p.cache.Put(key, res)
	}
	return res, nil
}

func hashSQL(sqlText string) string {
	normalized := strings.Join(strings.Fields(strings.TrimSpace(sqlText)), " ")
	sum := sha256.Sum256([]byte(normalized))
	return hex.EncodeToString(sum[:])
}

func dedupeStrings(in []string) []string {
	seen := map[string]bool{}
	var out []string
	for _, s := range in {
		if !seen[s] {
			seen[s] = true
			out = append(out, s)
		}
	}
	return out
}

func dedupeNotes(in []RewriteNote) []RewriteNote {
	seen := map[string]bool{}
	var out []RewriteNote
	for _, n := range in {
		key := string(n.Rule) + "|" + n.Detail
		if !seen[key] {
			seen[key] = true
			out = append(out, n)
		}
	}
	return out
}
