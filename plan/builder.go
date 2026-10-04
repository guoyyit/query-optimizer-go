package plan

import (
	"fmt"

	"github.com/guoyyit/query-optimizer-go/sql"
)

// BuildLogical converts a parsed SELECT into a logical plan tree.
func BuildLogical(sel *sql.Select) (LogicalNode, error) {
	if len(sel.From) == 0 {
		return nil, fmt.Errorf("plan: SELECT requires a FROM clause")
	}

	// --- relations ---
	var root LogicalNode
	for _, t := range sel.From {
		root = crossOrJoin(root, &Scan{Table: t.Name, Alias: t.Alias})
	}
	for _, j := range sel.Joins {
		leaf := &Scan{Table: j.Table.Name, Alias: j.Table.Alias}
		switch j.Type {
		case sql.JoinCross:
			root = crossOrJoin(root, leaf)
		case sql.JoinLeft:
			root = &Join{Type: sql.JoinLeft, Left: root, Right: leaf, Cond: j.On}
		default:
			root = &Join{Type: sql.JoinInner, Left: root, Right: leaf, Cond: j.On}
		}
	}

	// --- WHERE ---
	if sel.Where != nil {
		root = &Filter{Child: root, Pred: sel.Where}
	}

	// --- aggregates ---
	aggs := collectAggExprs(sel.Select)
	if sel.Having != nil {
		aggs = append(aggs, sql.CollectAggregates(sel.Having)...)
	}
	hasAgg := len(sel.GroupBy) > 0 || len(aggs) > 0
	if hasAgg {
		root = &Aggregate{
			Child: root,
			Group: sel.GroupBy,
			Aggs:  dedupeAggs(aggs),
		}
	}

	// --- HAVING ---
	if sel.Having != nil {
		if !hasAgg {
			return nil, fmt.Errorf("plan: HAVING without GROUP BY or aggregates")
		}
		root = &Filter{Child: root, Pred: sel.Having}
	}

	// --- DISTINCT ---
	if sel.Distinct {
		root = &Distinct{Child: root}
	}

	// --- ORDER BY ---
	if len(sel.OrderBy) > 0 {
		root = &Sort{Child: root, Keys: sel.OrderBy}
	}

	// --- LIMIT / OFFSET ---
	if sel.Limit != nil || sel.Offset != nil {
		lim := int64(-1)
		if sel.Limit != nil {
			lim = *sel.Limit
		}
		off := int64(0)
		if sel.Offset != nil {
			off = *sel.Offset
		}
		root = &Limit{Child: root, Limit: lim, Offset: off}
	}

	// --- SELECT list ---
	exprs, names, err := buildProjectExprs(sel.Select)
	if err != nil {
		return nil, err
	}
	root = &Project{Child: root, Exprs: exprs, Names: names}

	return root, nil
}

func crossOrJoin(left, right LogicalNode) LogicalNode {
	if left == nil {
		return right
	}
	return &Join{Type: sql.JoinCross, Left: left, Right: right}
}

func collectAggExprs(list []sql.Expr) []*sql.Aggregate {
	var out []*sql.Aggregate
	for _, e := range list {
		out = append(out, sql.CollectAggregates(e)...)
	}
	return out
}

func dedupeAggs(aggs []*sql.Aggregate) []sql.Expr {
	seen := map[string]bool{}
	var out []sql.Expr
	for _, a := range aggs {
		key := sql.ExprString(a)
		if !seen[key] {
			seen[key] = true
			out = append(out, a)
		}
	}
	return out
}

func buildProjectExprs(list []sql.Expr) ([]sql.Expr, []string, error) {
	var exprs []sql.Expr
	var names []string
	for _, e := range list {
		switch x := e.(type) {
		case *sql.Star:
			exprs = append(exprs, x)
			names = append(names, "*")
		case *sql.Alias:
			exprs = append(exprs, x.Expr)
			names = append(names, x.Name)
		default:
			exprs = append(exprs, e)
			names = append(names, defaultColumnName(e))
		}
	}
	return exprs, names, nil
}

func defaultColumnName(e sql.Expr) string {
	switch x := e.(type) {
	case *sql.ColumnRef:
		return x.Name
	case *sql.Aggregate:
		return sql.ExprString(x)
	}
	return "expr"
}
