package optimizer

import (
	"fmt"
	"strings"

	"github.com/guoyyit/query-optimizer-go/expr"
	"github.com/guoyyit/query-optimizer-go/plan"
	"github.com/guoyyit/query-optimizer-go/rules"
)

// explainPlan renders a physical plan as an indented tree followed by the
// rewrite notes and observability markers collected during planning.
func explainPlan(p plan.Physical, notes []rules.Note, missing []string) string {
	var b strings.Builder
	b.WriteString("PLAN\n")
	explainNode(&b, p, 1)
	if len(notes) > 0 {
		b.WriteString("NOTES\n")
		for _, n := range notes {
			fmt.Fprintf(&b, "  [%s] %s\n", n.Rule, n.Detail)
		}
	}
	if len(missing) > 0 {
		b.WriteString("MARKERS\n")
		for _, m := range missing {
			fmt.Fprintf(&b, "  stats-missing: %s (conservative defaults used)\n", m)
		}
	}
	return b.String()
}

func explainNode(b *strings.Builder, p plan.Physical, depth int) {
	indent := strings.Repeat("  ", depth)
	switch n := p.(type) {
	case *plan.SeqScan:
		fmt.Fprintf(b, "%sSeqScan %s AS %s cols=[%s]\n", indent, n.Table, n.Alias, colsStr(n.Columns))
	case *plan.IndexScan:
		fmt.Fprintf(b, "%sIndexScan %s AS %s ON %s = %s cols=[%s]\n", indent, n.Table, n.Alias, n.Key.String(), n.Value.String(), colsStr(n.Columns))
	case *plan.FilterOp:
		fmt.Fprintf(b, "%sFilter %s\n", indent, n.Pred.String())
		explainNode(b, n.Input, depth+1)
	case *plan.ProjectOp:
		fmt.Fprintf(b, "%sProject [%s]\n", indent, colsStr(n.Cols))
		explainNode(b, n.Input, depth+1)
	case *plan.HashJoin:
		fmt.Fprintf(b, "%sHashJoin keys L=[%s] R=[%s]", indent, colsStr(n.LeftKeys), colsStr(n.RightKeys))
		if n.Residual != nil {
			fmt.Fprintf(b, " residual=%s", n.Residual.String())
		}
		b.WriteString("\n")
		explainNode(b, n.Left, depth+1)
		explainNode(b, n.Right, depth+1)
	case *plan.NestedLoopJoin:
		if n.Cond != nil {
			fmt.Fprintf(b, "%sNestedLoopJoin cond=%s\n", indent, n.Cond.String())
		} else {
			fmt.Fprintf(b, "%sNestedLoopJoin CROSS PRODUCT (no join condition)\n", indent)
		}
		explainNode(b, n.Left, depth+1)
		explainNode(b, n.Right, depth+1)
	}
}

func colsStr(cols []expr.ColRef) string {
	parts := make([]string, len(cols))
	for i, c := range cols {
		parts[i] = c.String()
	}
	return strings.Join(parts, ", ")
}
