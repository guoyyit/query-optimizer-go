package physical

import (
	"github.com/guoyyit/query-optimizer-go/rows"
	"github.com/guoyyit/query-optimizer-go/sql"
)

// PhysicalNode is implemented by every physical plan node.
type PhysicalNode interface {
	physicalNode()
	OutSchema() rows.Schema
}

// SeqScan scans a base table, optionally projecting columns.
type SeqScan struct {
	Table  string
	Alias  string
	Cols   []int // column indices to emit; nil = all
	Schema rows.Schema
	Rows   int64
	Cost   float64
}

// IndexScan looks up rows through an index for an equality predicate.
type IndexScan struct {
	Table   string
	Alias   string
	Index   string
	KeyCols []int
	KeyExpr sql.Expr
	Cols    []int
	Schema  rows.Schema
	Rows    int64
	Cost    float64
	Sel     float64
}

// Filter is a physical filter.
type Filter struct {
	Child  PhysicalNode
	Pred   sql.Expr
	Schema rows.Schema
	Rows   int64
	Cost   float64
	Sel    float64 // predicate selectivity
}

// Project evaluates expressions.
type Project struct {
	Child  PhysicalNode
	Exprs  []sql.Expr
	Names  []string
	Schema rows.Schema
	Rows   int64
	Cost   float64
}

// HashJoin builds a hash table on the build side and probes with the other.
type HashJoin struct {
	Left      PhysicalNode
	Right     PhysicalNode
	Cond      sql.Expr // equality predicate(s)
	BuildLeft bool     // true when Left is the build side
	JoinType  sql.JoinType
	Schema    rows.Schema
	Rows      int64
	Cost      float64
	Sel       float64
}

// NLJ is a nested-loop join (used for non-equi / cross joins).
type NLJ struct {
	Left     PhysicalNode
	Right    PhysicalNode
	Cond     sql.Expr
	JoinType sql.JoinType
	Schema   rows.Schema
	Rows     int64
	Cost     float64
	Sel      float64
}

// HashAggregate groups with a hash table.
type HashAggregate struct {
	Child  PhysicalNode
	Group  []sql.Expr
	Aggs   []sql.Expr
	Schema rows.Schema
	Rows   int64
	Cost   float64
}

// Sort materializes and sorts.
type Sort struct {
	Child  PhysicalNode
	Keys   []*sql.OrderItem
	Schema rows.Schema
	Rows   int64
	Cost   float64
}

// Limit applies offset/limit.
type Limit struct {
	Child  PhysicalNode
	Limit  int64
	Offset int64
	Rows   int64
	Cost   float64
}

func (*SeqScan) physicalNode()       {}
func (*IndexScan) physicalNode()     {}
func (*Filter) physicalNode()        {}
func (*Project) physicalNode()       {}
func (*HashJoin) physicalNode()      {}
func (*NLJ) physicalNode()           {}
func (*HashAggregate) physicalNode() {}
func (*Sort) physicalNode()          {}
func (*Limit) physicalNode()         {}

func (n *SeqScan) OutSchema() rows.Schema       { return n.Schema }
func (n *IndexScan) OutSchema() rows.Schema   { return n.Schema }
func (n *Filter) OutSchema() rows.Schema        { return n.Schema }
func (n *Project) OutSchema() rows.Schema       { return n.Schema }
func (n *HashJoin) OutSchema() rows.Schema      { return n.Schema }
func (n *NLJ) OutSchema() rows.Schema           { return n.Schema }
func (n *HashAggregate) OutSchema() rows.Schema { return n.Schema }
func (n *Sort) OutSchema() rows.Schema          { return n.Schema }
func (n *Limit) OutSchema() rows.Schema         { return n.Child.OutSchema() }
