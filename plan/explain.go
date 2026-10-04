package plan

import (
	"fmt"
	"strings"

	"github.com/guoyyit/query-optimizer-go/plan/physical"
	"github.com/guoyyit/query-optimizer-go/sql"
)

// Explain renders a readable description of a plan, its rewrite notes and
// degraded-statistics warnings.
func Explain(root physical.PhysicalNode, notes []RewriteNote, degraded []string) string {
	var b strings.Builder
	b.WriteString("Physical Plan:\n")
	explainNode(&b, root, 0)
	b.WriteString(fmt.Sprintf("Total Cost: %.4f\n", totalCost(root)))
	if len(notes) > 0 {
		b.WriteString("\nRewrite Rules:\n")
		for _, n := range notes {
			b.WriteString(fmt.Sprintf("  - [%s] %s\n", n.Rule, n.Detail))
		}
	}
	if len(degraded) > 0 {
		b.WriteString("\nDegraded Statistics Warnings:\n")
		seen := map[string]bool{}
		for _, d := range degraded {
			if seen[d] {
				continue
			}
			seen[d] = true
			b.WriteString("  ! " + d + "\n")
		}
	}
	return b.String()
}

func totalCost(n physical.PhysicalNode) float64 {
	switch x := n.(type) {
	case *physical.SeqScan:
		return x.Cost
	case *physical.IndexScan:
		return x.Cost
	case *physical.Filter:
		return x.Cost
	case *physical.Project:
		return x.Cost
	case *physical.HashJoin:
		return x.Cost
	case *physical.NLJ:
		return x.Cost
	case *physical.HashAggregate:
		return x.Cost
	case *physical.Sort:
		return x.Cost
	case *physical.Limit:
		return x.Cost
	}
	return 0
}

func indent(b *strings.Builder, level int) {
	for i := 0; i < level; i++ {
		b.WriteString("  ")
	}
}

func explainNode(b *strings.Builder, n physical.PhysicalNode, level int) {
	indent(b, level)
	switch x := n.(type) {
	case *physical.SeqScan:
		b.WriteString(fmt.Sprintf("SeqScan %s (rows=%d, cost=%.4f)\n", x.Alias, x.Rows, x.Cost))
	case *physical.IndexScan:
		b.WriteString(fmt.Sprintf("IndexScan %s using %s (rows=%d, cost=%.4f)\n", x.Alias, x.Index, x.Rows, x.Cost))
	case *physical.Filter:
		b.WriteString(fmt.Sprintf("Filter %s (rows=%d, cost=%.4f, sel=%.4f)\n", sql.ExprString(x.Pred), x.Rows, x.Cost, x.Sel))
		explainNode(b, x.Child, level+1)
	case *physical.Project:
		names := strings.Join(x.Names, ", ")
		b.WriteString(fmt.Sprintf("Project [%s] (cost=%.4f)\n", names, x.Cost))
		explainNode(b, x.Child, level+1)
	case *physical.HashJoin:
		build := "left"
		if !x.BuildLeft {
			build = "right"
		}
		jt := "inner"
		if x.JoinType == sql.JoinLeft {
			jt = "left"
		}
		cond := ""
		if x.Cond != nil {
			cond = sql.ExprString(x.Cond)
		}
		b.WriteString(fmt.Sprintf("HashJoin %s on %s (build=%s, rows=%d, cost=%.4f)\n", jt, cond, build, x.Rows, x.Cost))
		explainNode(b, x.Left, level+1)
		explainNode(b, x.Right, level+1)
	case *physical.NLJ:
		jt := "inner"
		if x.JoinType == sql.JoinCross {
			jt = "cross"
		}
		cond := ""
		if x.Cond != nil {
			cond = sql.ExprString(x.Cond)
		}
		b.WriteString(fmt.Sprintf("NestedLoopJoin %s on %s (rows=%d, cost=%.4f)\n", jt, cond, x.Rows, x.Cost))
		explainNode(b, x.Left, level+1)
		explainNode(b, x.Right, level+1)
	case *physical.HashAggregate:
		group := ""
		if len(x.Group) > 0 {
			parts := make([]string, len(x.Group))
			for i, g := range x.Group {
				parts[i] = sql.ExprString(g)
			}
			group = " group by " + strings.Join(parts, ", ")
		}
		b.WriteString(fmt.Sprintf("HashAggregate%s (rows=%d, cost=%.4f)\n", group, x.Rows, x.Cost))
		explainNode(b, x.Child, level+1)
	case *physical.Sort:
		keys := make([]string, len(x.Keys))
		for i, k := range x.Keys {
			keys[i] = sql.ExprString(k.Expr)
			if k.Desc {
				keys[i] += " desc"
			}
		}
		b.WriteString(fmt.Sprintf("Sort [%s] (rows=%d, cost=%.4f)\n", strings.Join(keys, ", "), x.Rows, x.Cost))
		explainNode(b, x.Child, level+1)
	case *physical.Limit:
		b.WriteString(fmt.Sprintf("Limit %d offset %d (rows=%d, cost=%.4f)\n", x.Limit, x.Offset, x.Rows, x.Cost))
		explainNode(b, x.Child, level+1)
	default:
		b.WriteString(fmt.Sprintf("%T\n", n))
	}
}
