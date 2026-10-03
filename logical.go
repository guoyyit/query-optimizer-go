package optimizer

import (
	"fmt"

	"github.com/guoyyit/query-optimizer-go/catalog"
	"github.com/guoyyit/query-optimizer-go/expr"
	"github.com/guoyyit/query-optimizer-go/plan"
	"github.com/guoyyit/query-optimizer-go/sql"
)

// PlanError describes a semantic problem with a query (unknown table,
// unknown or ambiguous column).
type PlanError struct{ Msg string }

func (e *PlanError) Error() string { return "plan: " + e.Msg }

// BuildLogical converts a parsed statement into a resolved logical plan:
//
//	Project(Scan/Join tree + residual Filter)
//
// Name resolution happens here: unqualified column references are bound to
// the unique table providing them; ambiguous names are rejected with a
// distinguishable error.
func BuildLogical(stmt *sql.SelectStmt, cat *catalog.Catalog) (plan.Logical, error) {
	if len(stmt.Tables) == 0 {
		return nil, &PlanError{"query has no FROM tables"}
	}
	// Resolve table schemas.
	type tbl struct {
		alias string
		cols  []expr.ColRef
	}
	tables := make([]tbl, 0, len(stmt.Tables))
	seenAlias := map[string]bool{}
	for _, tr := range stmt.Tables {
		t := cat.Table(tr.Table)
		if t == nil {
			return nil, &PlanError{fmt.Sprintf("unknown table %q", tr.Table)}
		}
		if seenAlias[tr.Alias] {
			return nil, &PlanError{fmt.Sprintf("duplicate table alias %q", tr.Alias)}
		}
		seenAlias[tr.Alias] = true
		cols := make([]expr.ColRef, len(t.Columns))
		for i, c := range t.Columns {
			cols[i] = expr.ColRef{Table: tr.Alias, Column: c.Name}
		}
		tables = append(tables, tbl{tr.Alias, cols})
	}
	allCols := []expr.ColRef{}
	for _, tb := range tables {
		allCols = append(allCols, tb.cols...)
	}

	// resolve binds a (possibly unqualified) column reference.
	resolve := func(c expr.ColumnRef) (expr.ColRef, error) {
		if c.Table != "" {
			if !seenAlias[c.Table] {
				return expr.ColRef{}, &PlanError{fmt.Sprintf("unknown table alias %q in column %s", c.Table, c.String())}
			}
			for _, col := range allCols {
				if col.Table == c.Table && col.Column == c.Column {
					return col, nil
				}
			}
			return expr.ColRef{}, &PlanError{fmt.Sprintf("unknown column %s", c.String())}
		}
		var matches []expr.ColRef
		for _, col := range allCols {
			if col.Column == c.Column {
				matches = append(matches, col)
			}
		}
		switch len(matches) {
		case 0:
			return expr.ColRef{}, &PlanError{fmt.Sprintf("unknown column %q", c.Column)}
		case 1:
			return matches[0], nil
		}
		return expr.ColRef{}, &PlanError{fmt.Sprintf("ambiguous column %q: provided by %v; qualify it", c.Column, matches)}
	}

	// resolveExpr rewrites ColumnRefs inside an expression tree.
	var resolveExpr func(e expr.Expr) (expr.Expr, error)
	resolveExpr = func(e expr.Expr) (expr.Expr, error) {
		switch t := e.(type) {
		case *expr.ColumnRef:
			ref, err := resolve(*t)
			if err != nil {
				return nil, err
			}
			return &expr.ColumnRef{Table: ref.Table, Column: ref.Column}, nil
		case *expr.Binary:
			l, err := resolveExpr(t.L)
			if err != nil {
				return nil, err
			}
			r, err := resolveExpr(t.R)
			if err != nil {
				return nil, err
			}
			return &expr.Binary{Op: t.Op, L: l, R: r}, nil
		case *expr.Not:
			sub, err := resolveExpr(t.E)
			if err != nil {
				return nil, err
			}
			return &expr.Not{E: sub}, nil
		default:
			return e, nil
		}
	}

	// Build the left-deep join tree over scans in FROM order.
	var root plan.Logical = &plan.Scan{Table: stmt.Tables[0].Table, Alias: tables[0].alias, Columns: tables[0].cols}
	for i, jc := range stmt.Joins {
		right := &plan.Scan{Table: stmt.Tables[i+1].Table, Alias: tables[i+1].alias, Columns: tables[i+1].cols}
		var cond expr.Expr
		if jc.On != nil {
			rc, err := resolveExpr(jc.On)
			if err != nil {
				return nil, err
			}
			cond = rc
		}
		root = &plan.Join{How: plan.InnerJoin, Cond: cond, Left: root, Right: right}
	}
	if stmt.Where != nil {
		w, err := resolveExpr(stmt.Where)
		if err != nil {
			return nil, err
		}
		root = &plan.Filter{Pred: w, Input: root}
	}
	// Projection.
	var projCols []expr.ColRef
	if stmt.Star {
		projCols = allCols
	} else {
		for _, p := range stmt.Projections {
			ref, err := resolve(p)
			if err != nil {
				return nil, err
			}
			projCols = append(projCols, ref)
		}
	}
	root = &plan.Project{Cols: projCols, Input: root}
	return root, nil
}

// OutputSchema deduplicates/qualifies output column names for results:
// columns whose bare name appears more than once are reported qualified.
func OutputSchema(cols []expr.ColRef) []string {
	count := map[string]int{}
	for _, c := range cols {
		count[c.Column]++
	}
	out := make([]string, len(cols))
	for i, c := range cols {
		if count[c.Column] > 1 {
			out[i] = c.String()
		} else {
			out[i] = c.Column
		}
	}
	return out
}
