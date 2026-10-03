package rules

import (
	"fmt"

	"github.com/guoyyit/query-optimizer-go/expr"
	"github.com/guoyyit/query-optimizer-go/plan"
)

// Pushdown moves filters as close to scans as possible:
//   - a conjunct referencing only one join side is pushed below the join;
//   - an equality conjunct over both sides becomes (or extends) the join
//     condition, enabling hash joins later;
//   - anything else stays as a residual filter above the join and the log
//     explains why it could not move.
func Pushdown(root plan.Logical, log *Log) plan.Logical {
	return pushdownNode(root, nil, log)
}

// pushdownNode rewrites node and then places the given pending conjuncts
// (from ancestors) as deep as possible.
func pushdownNode(node plan.Logical, pending []expr.Expr, log *Log) plan.Logical {
	switch n := node.(type) {
	case *plan.Filter:
		conjuncts := append(pending, expr.SplitConjuncts(n.Pred)...)
		return pushdownNode(n.Input, conjuncts, log)
	case *plan.Scan:
		var keep, push []expr.Expr
		for _, c := range pending {
			if refsOnly(c, n.Columns) {
				push = append(push, c)
			} else {
				keep = append(keep, c)
			}
		}
		var out plan.Logical = n
		if len(push) > 0 {
			out = &plan.Filter{Pred: expr.AndAll(push), Input: n}
			log.Add("predicate-pushdown", fmt.Sprintf("pushed %d predicate(s) to scan of %s", len(push), n.Alias))
		}
		if len(keep) > 0 {
			out = &plan.Filter{Pred: expr.AndAll(keep), Input: out}
		}
		return out
	case *plan.Join:
		// Recurse into children first (no pending from above yet).
		left := pushdownNode(n.Left, nil, log)
		right := pushdownNode(n.Right, nil, log)
		leftCols := left.Schema()
		rightCols := right.Schema()

		var leftPreds, rightPreds, residuals []expr.Expr
		joinConds := expr.SplitConjuncts(n.Cond)
		for _, c := range pending {
			switch {
			case refsOnly(c, leftCols):
				leftPreds = append(leftPreds, c)
			case refsOnly(c, rightCols):
				rightPreds = append(rightPreds, c)
			case isEquiJoinKey(c, leftCols, rightCols):
				joinConds = append(joinConds, c)
				log.Add("predicate-pushdown", "converted WHERE predicate "+c.String()+" into a join condition")
			default:
				residuals = append(residuals, c)
				log.Add("predicate-pushdown", "cannot push "+c.String()+": references columns on both join sides without an equality key; kept as residual filter")
			}
		}
		if len(leftPreds) > 0 {
			left = &plan.Filter{Pred: expr.AndAll(leftPreds), Input: left}
			log.Add("predicate-pushdown", fmt.Sprintf("pushed %d predicate(s) to left input of join", len(leftPreds)))
		}
		if len(rightPreds) > 0 {
			right = &plan.Filter{Pred: expr.AndAll(rightPreds), Input: right}
			log.Add("predicate-pushdown", fmt.Sprintf("pushed %d predicate(s) to right input of join", len(rightPreds)))
		}
		var out plan.Logical = &plan.Join{How: n.How, Cond: expr.AndAll(joinConds), Left: left, Right: right}
		if len(residuals) > 0 {
			out = &plan.Filter{Pred: expr.AndAll(residuals), Input: out}
		}
		return out
	case *plan.Project:
		// Filters above a project can only reference projected columns;
		// push through when all referenced columns are projected.
		child := pushdownNode(n.Input, nil, log)
		var above, below []expr.Expr
		for _, c := range pending {
			if refsOnly(c, n.Cols) {
				below = append(below, c)
			} else {
				above = append(above, c)
			}
		}
		var out plan.Logical = &plan.Project{Cols: n.Cols, Input: child}
		if len(below) > 0 {
			out = pushdownNode(out, below, log)
		}
		if len(above) > 0 {
			out = &plan.Filter{Pred: expr.AndAll(above), Input: out}
		}
		return out
	}
	if len(pending) > 0 {
		return &plan.Filter{Pred: expr.AndAll(pending), Input: node}
	}
	return node
}

// refsOnly reports whether every column referenced by e appears in cols.
func refsOnly(e expr.Expr, cols []expr.ColRef) bool {
	set := map[expr.ColRef]bool{}
	for _, c := range cols {
		set[c] = true
	}
	for _, ref := range e.Columns() {
		if !set[ref] {
			return false
		}
	}
	return true
}

// isEquiJoinKey reports whether c is `leftCol = rightCol` (or the reverse)
// with one column from each side.
func isEquiJoinKey(c expr.Expr, leftCols, rightCols []expr.ColRef) bool {
	b, ok := c.(*expr.Binary)
	if !ok || b.Op != expr.Eq {
		return false
	}
	l, lok := b.L.(*expr.ColumnRef)
	r, rok := b.R.(*expr.ColumnRef)
	if !lok || !rok {
		return false
	}
	lr := expr.ColRef{Table: l.Table, Column: l.Column}
	rr := expr.ColRef{Table: r.Table, Column: r.Column}
	inLeft := func(c expr.ColRef) bool {
		for _, x := range leftCols {
			if x == c {
				return true
			}
		}
		return false
	}
	inRight := func(c expr.ColRef) bool {
		for _, x := range rightCols {
			if x == c {
				return true
			}
		}
		return false
	}
	return (inLeft(lr) && inRight(rr)) || (inLeft(rr) && inRight(lr))
}
