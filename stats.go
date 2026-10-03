package optimizer

import (
	"sort"
	"time"

	"github.com/guoyyit/query-optimizer-go/catalog"
)

// statsView adapts the catalog to rules.StatsView, applying the
// conservative fallback policy: when statistics are missing or older than
// StatsMaxAge, estimates come from DefaultRows/DefaultNDV and the table is
// recorded in the missing set (surfaced as an observability marker).
type statsView struct {
	cat       *catalog.Catalog
	maxAge    time.Duration
	defRows   int64
	defNDV    int64
	now       time.Time
	missing   map[string]bool // alias or table -> used defaults
	aliasToDb map[string]string
}

func newStatsView(cat *catalog.Catalog, cfg Config, aliasToDb map[string]string) *statsView {
	return &statsView{
		cat:       cat,
		maxAge:    cfg.StatsMaxAge,
		defRows:   cfg.DefaultRows,
		defNDV:    cfg.DefaultNDV,
		now:       time.Now(),
		missing:   map[string]bool{},
		aliasToDb: aliasToDb,
	}
}

// freshStats returns the table's stats when present and fresh.
func (s *statsView) freshStats(table string) *catalog.Stats {
	t := s.cat.Table(table)
	if t == nil {
		return nil
	}
	st, _ := t.Stats()
	if st == nil {
		return nil
	}
	if s.maxAge > 0 && s.now.Sub(st.UpdatedAt) > s.maxAge {
		return nil // stale: treated as missing, observably
	}
	return st
}

// Rows implements rules.StatsView. The key may be an alias or table name.
func (s *statsView) Rows(key string) (int64, bool) {
	table := s.resolve(key)
	st := s.freshStats(table)
	if st == nil {
		s.missing[key] = true
		return s.defRows, false
	}
	return st.RowCount, true
}

// NDV implements rules.StatsView.
func (s *statsView) NDV(table, column string) (int64, bool) {
	st := s.freshStats(s.resolve(table))
	if st == nil {
		s.missing[table] = true
		return s.defNDV, false
	}
	if ndv, ok := st.NDV[column]; ok && ndv > 0 {
		return ndv, true
	}
	s.missing[table] = true
	return s.defNDV, false
}

func (s *statsView) resolve(key string) string {
	if t, ok := s.aliasToDb[key]; ok {
		return t
	}
	return key
}

// MissingTables returns the sorted list of tables/aliases for which
// conservative defaults were used.
func (s *statsView) MissingTables() []string {
	out := make([]string, 0, len(s.missing))
	for t := range s.missing {
		out = append(out, t)
	}
	sort.Strings(out)
	return out
}
