package sql

import (
	"sort"
	"strconv"
	"strings"
)

// ExprString renders an expression as a readable string.
func ExprString(e Expr) string {
	switch x := e.(type) {
	case *Literal:
		switch x.Kind {
		case LitNull:
			return "null"
		case LitInt:
			return intStr(x.Int)
		case LitFloat:
			return floatStr(x.Float)
		case LitString:
			return "'" + x.Str + "'"
		case LitBool:
			if x.Bool {
				return "true"
			}
			return "false"
		}
	case *ColumnRef:
		if x.Table != "" {
			return x.Table + "." + x.Name
		}
		return x.Name
	case *Star:
		if x.Table != "" {
			return x.Table + ".*"
		}
		return "*"
	case *BinOp:
		return "(" + ExprString(x.Left) + " " + x.Op + " " + ExprString(x.Right) + ")"
	case *UnaryOp:
		if x.Op == "NOT" {
			return "(not " + ExprString(x.Expr) + ")"
		}
		return x.Op + ExprString(x.Expr)
	case *Aggregate:
		d := ""
		if x.Distinct {
			d = "distinct "
		}
		if x.Expr == nil {
			return x.Name + "(" + d + "*)"
		}
		return x.Name + "(" + d + ExprString(x.Expr) + ")"
	case *InList:
		parts := make([]string, len(x.List))
		for i, v := range x.List {
			parts[i] = ExprString(v)
		}
		return ExprString(x.Expr) + " in (" + strings.Join(parts, ", ") + ")"
	case *Between:
		return "(" + ExprString(x.Expr) + " between " + ExprString(x.Low) + " and " + ExprString(x.High) + ")"
	case *IsNull:
		if x.Not {
			return "(" + ExprString(x.Expr) + " is not null)"
		}
		return "(" + ExprString(x.Expr) + " is null)"
	case *Like:
		if x.Not {
			return "(" + ExprString(x.Expr) + " not like '" + x.Pattern + "')"
		}
		return "(" + ExprString(x.Expr) + " like '" + x.Pattern + "')"
	case *Alias:
		return ExprString(x.Expr) + " as " + x.Name
	}
	return "?"
}

// CanonicalExpr renders an expression with commutative operands sorted, so
// that logically equivalent predicates compare equal.
func CanonicalExpr(e Expr) string {
	switch x := e.(type) {
	case *BinOp:
		l := CanonicalExpr(x.Left)
		r := CanonicalExpr(x.Right)
		switch strings.ToUpper(x.Op) {
		case "=", "<>", "!=", "AND", "OR":
			if r < l {
				l, r = r, l
			}
		}
		return "(" + l + " " + x.Op + " " + r + ")"
	case *UnaryOp:
		return "(" + strings.ToLower(x.Op) + " " + CanonicalExpr(x.Expr) + ")"
	case *InList:
		parts := make([]string, len(x.List))
		for i, v := range x.List {
			parts[i] = CanonicalExpr(v)
		}
		sort.Strings(parts)
		return CanonicalExpr(x.Expr) + " in (" + strings.Join(parts, ", ") + ")"
	}
	return ExprString(e)
}

// SplitConjuncts flattens a predicate into AND-ed conjuncts.
func SplitConjuncts(e Expr) []Expr {
	if e == nil {
		return nil
	}
	if b, ok := e.(*BinOp); ok && strings.ToUpper(b.Op) == "AND" {
		return append(SplitConjuncts(b.Left), SplitConjuncts(b.Right)...)
	}
	return []Expr{e}
}

// Conjoin combines expressions with AND; nil entries are skipped.
func Conjoin(exprs []Expr) Expr {
	var out []Expr
	for _, e := range exprs {
		if e != nil {
			out = append(out, e)
		}
	}
	if len(out) == 0 {
		return nil
	}
	if len(out) == 1 {
		return out[0]
	}
	res := out[0]
	for _, e := range out[1:] {
		res = &BinOp{Op: "AND", Left: res, Right: e}
	}
	return res
}

// CollectColumns returns all column references in an expression.
func CollectColumns(e Expr) []*ColumnRef {
	var out []*ColumnRef
	var walk func(n Expr)
	walk = func(n Expr) {
		switch x := n.(type) {
		case *ColumnRef:
			out = append(out, x)
		case *BinOp:
			walk(x.Left)
			walk(x.Right)
		case *UnaryOp:
			walk(x.Expr)
		case *Aggregate:
			if x.Expr != nil {
				walk(x.Expr)
			}
		case *InList:
			walk(x.Expr)
			for _, v := range x.List {
				walk(v)
			}
		case *Between:
			walk(x.Expr)
			walk(x.Low)
			walk(x.High)
		case *IsNull:
			walk(x.Expr)
		case *Like:
			walk(x.Expr)
		case *Alias:
			walk(x.Expr)
		}
	}
	walk(e)
	return out
}

// CollectAggregates returns all aggregate expressions in an expression.
func CollectAggregates(e Expr) []*Aggregate {
	var out []*Aggregate
	var walk func(n Expr)
	walk = func(n Expr) {
		switch x := n.(type) {
		case *Aggregate:
			out = append(out, x)
		case *BinOp:
			walk(x.Left)
			walk(x.Right)
		case *UnaryOp:
			walk(x.Expr)
		case *InList:
			walk(x.Expr)
			for _, v := range x.List {
				walk(v)
			}
		case *Between:
			walk(x.Expr)
			walk(x.Low)
			walk(x.High)
		case *IsNull:
			walk(x.Expr)
		case *Like:
			walk(x.Expr)
		case *Alias:
			walk(x.Expr)
		}
	}
	walk(e)
	return out
}

// ContainsAggregate reports whether an expression contains an aggregate.
func ContainsAggregate(e Expr) bool {
	return len(CollectAggregates(e)) > 0
}

// IsConst reports whether an expression is a constant (literal with no
// column references).
func IsConst(e Expr) bool {
	return len(CollectColumns(e)) == 0
}

func intStr(v int64) string   { return strconv.FormatInt(v, 10) }
func floatStr(v float64) string { return strconv.FormatFloat(v, 'g', -1, 64) }
