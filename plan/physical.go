package plan

import (
	"fmt"
	"strings"

	"github.com/guoyyit/query-optimizer-go/expr"
	"github.com/guoyyit/query-optimizer-go/value"
)

// Physical is an executable plan node.
type Physical interface {
	Schema() []expr.ColRef
	Children() []Physical
	// Fingerprint is a canonical string uniquely identifying the plan shape
	// and operators; used for deterministic cost tie-breaking.
	Fingerprint() string
	pnodeKind() string
}

// SeqScan reads all rows of a table.
type SeqScan struct {
	Table   string
	Alias   string
	Columns []expr.ColRef
}

// IndexScan probes a hash index for rows matching Key = Value.
type IndexScan struct {
	Table   string
	Alias   string
	Columns []expr.ColRef
	Key     expr.ColRef
	Value   value.Value
}

// FilterOp applies a residual predicate.
type FilterOp struct {
	Pred  expr.Expr
	Input Physical
}

// ProjectOp selects/reorders columns.
type ProjectOp struct {
	Cols  []expr.ColRef
	Input Physical
}

// HashJoin builds a hash table on Right (build side) and probes with Left.
type HashJoin struct {
	Cond        expr.Expr // equality conjuncts used as hash keys + residuals
	LeftKeys    []expr.ColRef
	RightKeys   []expr.ColRef
	Residual    expr.Expr
	Left, Right Physical
}

// NestedLoopJoin is the fallback join (also used for cross products).
type NestedLoopJoin struct {
	Cond        expr.Expr // nil for cross product
	Left, Right Physical
}

func (s *SeqScan) Schema() []expr.ColRef   { return s.Columns }
func (s *SeqScan) Children() []Physical    { return nil }
func (s *SeqScan) Fingerprint() string     { return "seq(" + s.Alias + ":" + colsString(s.Columns) + ")" }
func (s *SeqScan) pnodeKind() string       { return "SeqScan" }
func (s *IndexScan) Schema() []expr.ColRef { return s.Columns }
func (s *IndexScan) Children() []Physical  { return nil }
func (s *IndexScan) Fingerprint() string {
	return fmt.Sprintf("idx(%s:%s=%s)", s.Alias, s.Key.String(), s.Value.String())
}
func (s *IndexScan) pnodeKind() string    { return "IndexScan" }
func (f *FilterOp) Schema() []expr.ColRef { return f.Input.Schema() }
func (f *FilterOp) Children() []Physical  { return []Physical{f.Input} }
func (f *FilterOp) Fingerprint() string {
	return "filter(" + expr.Canonical(f.Pred) + ";" + f.Input.Fingerprint() + ")"
}
func (f *FilterOp) pnodeKind() string      { return "Filter" }
func (p *ProjectOp) Schema() []expr.ColRef { return p.Cols }
func (p *ProjectOp) Children() []Physical  { return []Physical{p.Input} }
func (p *ProjectOp) Fingerprint() string {
	return "proj(" + colsString(p.Cols) + ";" + p.Input.Fingerprint() + ")"
}
func (p *ProjectOp) pnodeKind() string { return "Project" }
func (h *HashJoin) Schema() []expr.ColRef {
	return append(append([]expr.ColRef{}, h.Left.Schema()...), h.Right.Schema()...)
}
func (h *HashJoin) Children() []Physical { return []Physical{h.Left, h.Right} }
func (h *HashJoin) Fingerprint() string {
	return "hashjoin(" + h.Left.Fingerprint() + "," + h.Right.Fingerprint() + ")"
}
func (h *HashJoin) pnodeKind() string { return "HashJoin" }
func (n *NestedLoopJoin) Schema() []expr.ColRef {
	return append(append([]expr.ColRef{}, n.Left.Schema()...), n.Right.Schema()...)
}
func (n *NestedLoopJoin) Children() []Physical { return []Physical{n.Left, n.Right} }
func (n *NestedLoopJoin) Fingerprint() string {
	return "nljoin(" + n.Left.Fingerprint() + "," + n.Right.Fingerprint() + ")"
}
func (n *NestedLoopJoin) pnodeKind() string { return "NestedLoopJoin" }

func colsString(cols []expr.ColRef) string {
	parts := make([]string, len(cols))
	for i, c := range cols {
		parts[i] = c.String()
	}
	return strings.Join(parts, ",")
}
