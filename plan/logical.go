// Package plan defines logical and physical plan trees.
package plan

import (
	"github.com/guoyyit/query-optimizer-go/expr"
)

// JoinType is the join algorithm-independent join kind.
type JoinType int

const (
	InnerJoin JoinType = iota
)

// Logical is a node in the logical plan tree.
type Logical interface {
	// Schema is the node's output column list, in order.
	Schema() []expr.ColRef
	// Children returns child nodes (0, 1 or 2).
	Children() []Logical
	// nodeKind is a stable name used in fingerprints and explain output.
	nodeKind() string
}

// Scan reads a base table (possibly a column subset after pruning).
type Scan struct {
	Table   string
	Alias   string
	Columns []expr.ColRef // output columns; prunable
}

// Filter keeps rows matching Pred.
type Filter struct {
	Pred  expr.Expr
	Input Logical
}

// Project selects/reorders columns.
type Project struct {
	Cols  []expr.ColRef
	Input Logical
}

// Join combines two inputs on Cond.
type Join struct {
	How         JoinType
	Cond        expr.Expr // nil means cross product (kept only when unavoidable)
	Left, Right Logical
}

func (s *Scan) Schema() []expr.ColRef    { return s.Columns }
func (s *Scan) Children() []Logical      { return nil }
func (s *Scan) nodeKind() string         { return "Scan" }
func (f *Filter) Schema() []expr.ColRef  { return f.Input.Schema() }
func (f *Filter) Children() []Logical    { return []Logical{f.Input} }
func (f *Filter) nodeKind() string       { return "Filter" }
func (p *Project) Schema() []expr.ColRef { return p.Cols }
func (p *Project) Children() []Logical   { return []Logical{p.Input} }
func (p *Project) nodeKind() string      { return "Project" }
func (j *Join) Schema() []expr.ColRef {
	return append(append([]expr.ColRef{}, j.Left.Schema()...), j.Right.Schema()...)
}
func (j *Join) Children() []Logical { return []Logical{j.Left, j.Right} }
func (j *Join) nodeKind() string    { return "Join" }
