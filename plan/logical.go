package plan

import "github.com/guoyyit/query-optimizer-go/sql"

// LogicalNode is implemented by every logical plan node.
type LogicalNode interface{ logicalNode() }

// Scan is a logical table scan.
type Scan struct {
	Table string
	Alias string
}

// Filter applies a predicate to its input.
type Filter struct {
	Child LogicalNode
	Pred  sql.Expr
}

// Project evaluates expressions and renames columns.
type Project struct {
	Child LogicalNode
	Exprs []sql.Expr
	Names []string
}

// Join combines two inputs.
type Join struct {
	Type  sql.JoinType
	Left  LogicalNode
	Right LogicalNode
	Cond  sql.Expr
}

// Aggregate groups and computes aggregates.
type Aggregate struct {
	Child LogicalNode
	Group []sql.Expr
	Aggs  []sql.Expr
}

// Sort orders its input.
type Sort struct {
	Child LogicalNode
	Keys  []*sql.OrderItem
}

// Limit applies OFFSET ... FETCH.
type Limit struct {
	Child  LogicalNode
	Limit  int64
	Offset int64
}

// Distinct removes duplicate rows.
type Distinct struct {
	Child LogicalNode
}

func (*Scan) logicalNode()      {}
func (*Filter) logicalNode()    {}
func (*Project) logicalNode()   {}
func (*Join) logicalNode()      {}
func (*Aggregate) logicalNode() {}
func (*Sort) logicalNode()      {}
func (*Limit) logicalNode()     {}
func (*Distinct) logicalNode()   {}
