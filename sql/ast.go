// Package sql implements a small SQL subset: SELECT with FROM/JOIN/WHERE/
// GROUP BY/HAVING/ORDER BY/LIMIT, predicates, aggregates and arithmetic.
package sql

// Node is implemented by every AST node.
type Node interface{ node() }

// ---------- expressions ----------

type Expr interface{ Node; exprNode() }

// Literal is a constant: int, float, string, bool or NULL.
type Literal struct {
	Kind  LiteralKind
	Int   int64
	Float float64
	Str   string
	Bool  bool
}

type LiteralKind int

const (
	LitNull LiteralKind = iota
	LitInt
	LitFloat
	LitString
	LitBool
)

// ColumnRef is a column reference, optionally qualified by table alias.
type ColumnRef struct {
	Table string // optional alias/table name, lower-cased
	Name  string // column name, lower-cased
}

// BinOp is a binary arithmetic or comparison expression.
type BinOp struct {
	Op       string // + - * / = <> < > <= >= AND OR
	Left     Expr
	Right    Expr
}

// UnaryOp is NOT or unary minus.
type UnaryOp struct {
	Op  string // NOT, -
	Expr Expr
}

// Aggregate is COUNT/SUM/AVG/MIN/MAX([DISTINCT] expr).
type Aggregate struct {
	Name     string // COUNT SUM AVG MIN MAX
	Distinct bool
	Expr     Expr // nil for COUNT(*)
}

// Star represents * or table.* in SELECT.
type Star struct {
	Table string // empty for bare *
}

// InList is expr IN (v1, v2, ...).
type InList struct {
	Expr Expr
	List []Expr
}

// Between is expr BETWEEN low AND high.
type Between struct {
	Expr Expr
	Low  Expr
	High Expr
}

// IsNull is expr IS [NOT] NULL.
type IsNull struct {
	Expr Expr
	Not  bool
}

// Like is expr [NOT] LIKE pattern (% and _ wildcards).
type Like struct {
	Expr    Expr
	Pattern string
	Not     bool
}

// Alias wraps an expression with an output column name.
type Alias struct {
	Expr Expr
	Name string
}

func (*Literal) node()   {}
func (*ColumnRef) node() {}
func (*BinOp) node()     {}
func (*UnaryOp) node()   {}
func (*Aggregate) node() {}
func (*Star) node()      {}
func (*InList) node()    {}
func (*Between) node()   {}
func (*IsNull) node()    {}
func (*Like) node()      {}
func (*Alias) node()     {}

func (*Literal) exprNode()   {}
func (*ColumnRef) exprNode() {}
func (*BinOp) exprNode()     {}
func (*UnaryOp) exprNode()   {}
func (*Aggregate) exprNode() {}
func (*Star) exprNode()      {}
func (*InList) exprNode()    {}
func (*Between) exprNode()   {}
func (*IsNull) exprNode()    {}
func (*Like) exprNode()      {}
func (*Alias) exprNode()     {}

// ---------- query ----------

// Select is the top-level query node.
type Select struct {
	Distinct bool
	Select   []Expr // Alias, Star, or plain Expr
	From     []*TableRef
	Joins    []*Join
	Where    Expr
	GroupBy  []Expr
	Having   Expr
	OrderBy  []*OrderItem
	Limit    *int64
	Offset   *int64
}

func (*Select) node() {}

// TableRef is a FROM table with optional alias.
type TableRef struct {
	Name  string
	Alias string // alias if given, else name
}

// JoinType classifies a join.
type JoinType int

const (
	JoinInner JoinType = iota
	JoinLeft
	JoinCross
)

// Join is a JOIN clause.
type Join struct {
	Type  JoinType
	Table *TableRef
	On    Expr // nil for cross join
}

// OrderItem is one ORDER BY key.
type OrderItem struct {
	Expr Expr
	Desc bool
}
