package exec

import (
	"context"

	"github.com/guoyyit/query-optimizer-go/catalog"
	"github.com/guoyyit/query-optimizer-go/plan"
	"github.com/guoyyit/query-optimizer-go/plan/physical"
	"github.com/guoyyit/query-optimizer-go/rows"
	"github.com/guoyyit/query-optimizer-go/sql"
)

// NaiveExecute runs the literal query semantics without any optimization:
// it materializes the full Cartesian product of all FROM tables, then
// applies WHERE/GROUP BY/HAVING/DISTINCT/ORDER BY/LIMIT/SELECT in order.
// It is the reference implementation for semantic-equivalence tests.
func NaiveExecute(ctx context.Context, sel *sql.Select, cat *catalog.Catalog) ([]rows.Row, rows.Schema, error) {
	var node physical.PhysicalNode
	for _, t := range sel.From {
		node = naiveCross(node, naiveScan(cat, t.Name, t.Alias))
	}
	for _, j := range sel.Joins {
		scan := naiveScan(cat, j.Table.Name, j.Table.Alias)
		switch j.Type {
		case sql.JoinCross:
			node = naiveCross(node, scan)
		case sql.JoinLeft:
			node = &physical.HashJoin{
				Left: node, Right: scan, Cond: j.On, BuildLeft: false,
				JoinType: sql.JoinLeft, Schema: concatSchema(node.OutSchema(), scan.OutSchema()),
			}
		default:
			node = &physical.NLJ{
				Left: node, Right: scan, Cond: j.On,
				JoinType: sql.JoinInner, Schema: concatSchema(node.OutSchema(), scan.OutSchema()),
			}
		}
	}

	if sel.Where != nil {
		node = &physical.Filter{Child: node, Pred: sel.Where, Schema: node.OutSchema()}
	}

	aggs := collectAggExprs(sel.Select)
	if sel.Having != nil {
		aggs = append(aggs, sql.CollectAggregates(sel.Having)...)
	}
	hasAgg := len(sel.GroupBy) > 0 || len(aggs) > 0
	if hasAgg {
		aggExprs := make([]sql.Expr, len(aggs))
		for i, a := range aggs {
			aggExprs[i] = a
		}
		node = &physical.HashAggregate{
			Child:  node,
			Group:  sel.GroupBy,
			Aggs:   aggExprs,
			Schema: naiveAggSchema(node.OutSchema(), sel.GroupBy, aggs),
		}
	}

	if sel.Having != nil {
		node = &physical.Filter{Child: node, Pred: sel.Having, Schema: node.OutSchema()}
	}

	if sel.Distinct {
		schema := node.OutSchema()
		node = &physical.HashAggregate{
			Child:  node,
			Group:  allColumnRefs(schema),
			Aggs:   nil,
			Schema: schema,
		}
	}

	if len(sel.OrderBy) > 0 {
		node = &physical.Sort{Child: node, Keys: sel.OrderBy, Schema: node.OutSchema()}
	}

	if sel.Limit != nil || sel.Offset != nil {
		lim := int64(-1)
		if sel.Limit != nil {
			lim = *sel.Limit
		}
		off := int64(0)
		if sel.Offset != nil {
			off = *sel.Offset
		}
		node = &physical.Limit{Child: node, Limit: lim, Offset: off}
	}

	exprs, names, err := buildProjectExprs(sel.Select)
	if err != nil {
		return nil, rows.Schema{}, err
	}
	schema := plan.ProjectSchema(node.OutSchema(), exprs, names)
	node = &physical.Project{Child: node, Exprs: exprs, Names: names, Schema: schema}

	out, err := Execute(ctx, node, cat)
	if err != nil {
		return nil, rows.Schema{}, err
	}
	return out, schema, nil
}

func naiveScan(cat *catalog.Catalog, name, alias string) *physical.SeqScan {
	t, ok := cat.Get(name)
	if !ok {
		t = &catalog.Table{Name: name, Schema: rows.Schema{}}
	}
	cols := make([]rows.Column, len(t.Schema.Columns))
	for i, c := range t.Schema.Columns {
		cols[i] = rows.Column{Name: c.Name, Type: c.Type, Alias: alias}
	}
	return &physical.SeqScan{
		Table:  t.Name,
		Alias:  alias,
		Schema: rows.Schema{Columns: cols},
		Rows:   int64(len(t.Rows)),
	}
}

func naiveCross(left, right physical.PhysicalNode) physical.PhysicalNode {
	if left == nil {
		return right
	}
	return &physical.NLJ{
		Left: left, Right: right, Cond: nil, JoinType: sql.JoinCross,
		Schema: concatSchema(left.OutSchema(), right.OutSchema()),
	}
}

func naiveAggSchema(child rows.Schema, groups []sql.Expr, aggs []*sql.Aggregate) rows.Schema {
	cols := make([]rows.Column, 0, len(groups)+len(aggs))
	for _, g := range groups {
		name := "group"
		alias := ""
		if ref, ok := g.(*sql.ColumnRef); ok {
			name = ref.Name
			alias = ref.Table
		}
		cols = append(cols, rows.Column{Name: name, Type: plan.InferType(g, child), Alias: alias})
	}
	for _, a := range aggs {
		cols = append(cols, rows.Column{Name: plan.DefaultColumnName(a), Type: plan.AggType(a, child)})
	}
	return rows.Schema{Columns: cols}
}

func allColumnRefs(schema rows.Schema) []sql.Expr {
	out := make([]sql.Expr, 0, len(schema.Columns))
	for _, c := range schema.Columns {
		out = append(out, &sql.ColumnRef{Name: c.Name, Table: c.Alias})
	}
	return out
}

func collectAggExprs(list []sql.Expr) []*sql.Aggregate {
	var out []*sql.Aggregate
	for _, e := range list {
		out = append(out, sql.CollectAggregates(e)...)
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
			names = append(names, plan.DefaultColumnName(e))
		}
	}
	return exprs, names, nil
}
