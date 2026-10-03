// Package rules implements logical rewrite rules: predicate pushdown,
// projection pruning and join reordering. Each rule reports human-readable
// explanations when it cannot (fully) apply.
package rules

// Toggles switch individual rewrite rules on or off.
type Toggles struct {
	PredicatePushdown bool
	ProjectionPruning bool
	JoinReordering    bool
}

// DefaultToggles enables every rule.
func DefaultToggles() Toggles {
	return Toggles{PredicatePushdown: true, ProjectionPruning: true, JoinReordering: true}
}

// Note is a human-readable explanation emitted during rewriting.
type Note struct {
	Rule   string
	Detail string
}

// Log accumulates rewrite notes.
type Log struct{ Notes []Note }

// Add appends a note.
func (l *Log) Add(rule, detail string) { l.Notes = append(l.Notes, Note{rule, detail}) }

// StatsView provides the statistics join reordering needs.
type StatsView interface {
	// Rows returns the estimated row count of a base table and whether
	// real statistics were available (false => conservative default used).
	Rows(table string) (int64, bool)
	// NDV returns the estimated distinct count of table.column.
	NDV(table, column string) (int64, bool)
}
