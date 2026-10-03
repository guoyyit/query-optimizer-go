package rules

import (
	"fmt"

	"github.com/guoyyit/query-optimizer-go/expr"
	"github.com/guoyyit/query-optimizer-go/plan"
)

// Prune removes columns not needed by the query output or by predicates
// above each node. Scans shrink to the columns actually read; redundant
// Project nodes collapse.
func Prune(root plan.Logical, needed []expr.ColRef, log *Log) plan.Logical {
	out, _ := pruneNode(root, toSet(needed), log)
	return out
}

func toSet(cols []expr.ColRef) map[expr.ColRef]bool {
	s := map[expr.ColRef]bool{}
	for _, c := range cols {
		s[c] = true
	}
	return s
}

func union(a, b map[expr.ColRef]bool) map[expr.ColRef]bool {
	out := map[expr.ColRef]bool{}
	for k := range a {
		out[k] = true
	}
	for k := range b {
		out[k] = true
	}
	return out
}

// pruneNode rewrites node so it outputs exactly the needed columns (order
// preserved from the node's original schema) and returns the new node plus
// the columns it requires from its own input.
func pruneNode(node plan.Logical, needed map[expr.ColRef]bool, log *Log) (plan.Logical, map[expr.ColRef]bool) {
	switch n := node.(type) {
	case *plan.Project:
		// A project defines what is visible above; intersect.
		childNeed := map[expr.ColRef]bool{}
		var kept []expr.ColRef
		for _, c := range n.Cols {
			if needed[c] {
				kept = append(kept, c)
				childNeed[c] = true
			}
		}
		child, _ := pruneNode(n.Input, childNeed, log)
		if len(kept) == len(n.Cols) {
			return &plan.Project{Cols: n.Cols, Input: child}, childNeed
		}
		log.Add("projection-pruning", fmt.Sprintf("project keeps %d of %d columns", len(kept), len(n.Cols)))
		return &plan.Project{Cols: kept, Input: child}, childNeed
	case *plan.Filter:
		need := union(needed, toSet(n.Pred.Columns()))
		child, _ := pruneNode(n.Input, need, log)
		return &plan.Filter{Pred: n.Pred, Input: child}, need
	case *plan.Join:
		need := needed
		if n.Cond != nil {
			need = union(needed, toSet(n.Cond.Columns()))
		}
		leftNeed := intersect(need, n.Left.Schema())
		rightNeed := intersect(need, n.Right.Schema())
		left, _ := pruneNode(n.Left, leftNeed, log)
		right, _ := pruneNode(n.Right, rightNeed, log)
		return &plan.Join{How: n.How, Cond: n.Cond, Left: left, Right: right}, need
	case *plan.Scan:
		var kept []expr.ColRef
		for _, c := range n.Columns {
			if needed[c] {
				kept = append(kept, c)
			}
		}
		if len(kept) == 0 {
			// A join side must produce at least one column to stay a valid
			// plan node; keep the first. (Joins on zero-column scans are
			// degenerate but must remain executable.)
			kept = n.Columns[:1]
		}
		if len(kept) < len(n.Columns) {
			log.Add("projection-pruning", fmt.Sprintf("scan of %s reads %d of %d columns", n.Alias, len(kept), len(n.Columns)))
		}
		return &plan.Scan{Table: n.Table, Alias: n.Alias, Columns: kept}, toSet(kept)
	}
	return node, needed
}

func intersect(need map[expr.ColRef]bool, cols []expr.ColRef) map[expr.ColRef]bool {
	out := map[expr.ColRef]bool{}
	for _, c := range cols {
		if need[c] {
			out[c] = true
		}
	}
	return out
}
