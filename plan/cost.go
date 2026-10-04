package plan

import (
	"math"

	"github.com/guoyyit/query-optimizer-go/config"
	"github.com/guoyyit/query-optimizer-go/plan/physical"
)

// costPlan recomputes and attaches costs (and output row estimates) to every
// node of a physical plan.
func costPlan(root physical.PhysicalNode, w config.Weights) {
	switch x := root.(type) {
	case *physical.SeqScan:
		x.Cost = float64(x.Rows) * w.SeqScanPerRow
	case *physical.IndexScan:
		x.Cost = w.IndexSetup + float64(x.Rows)*w.IndexPerRow
	case *physical.Filter:
		costPlan(x.Child, w)
		inRows := childRows(x.Child)
		x.Rows = int64(float64(inRows) * x.Sel)
		if x.Rows < 1 && inRows > 0 {
			x.Rows = 1
		}
		x.Cost = nodeCostOf(x.Child) + float64(inRows)*w.SeqScanPerRow
	case *physical.Project:
		costPlan(x.Child, w)
		x.Rows = childRows(x.Child)
		x.Cost = nodeCostOf(x.Child) + float64(x.Rows)*w.ProjectPerRow
	case *physical.HashJoin:
		costPlan(x.Left, w)
		costPlan(x.Right, w)
		lr := childRows(x.Left)
		rr := childRows(x.Right)
		x.Rows = int64(float64(lr) * float64(rr) * x.Sel)
		if x.Rows < 1 && lr > 0 && rr > 0 {
			x.Rows = 1
		}
		buildRows := lr
		probeRows := rr
		if !x.BuildLeft {
			buildRows = rr
			probeRows = lr
		}
		x.Cost = nodeCostOf(x.Left) + nodeCostOf(x.Right) +
			float64(buildRows)*w.HashJoinBuildPerRow + float64(probeRows)*w.HashJoinProbePerRow
	case *physical.NLJ:
		costPlan(x.Left, w)
		costPlan(x.Right, w)
		lr := childRows(x.Left)
		rr := childRows(x.Right)
		x.Rows = int64(float64(lr) * float64(rr) * x.Sel)
		if x.Rows < 1 && lr > 0 && rr > 0 {
			x.Rows = 1
		}
		x.Cost = nodeCostOf(x.Left) + nodeCostOf(x.Right) + float64(lr*rr)*w.NLJPerPair
	case *physical.HashAggregate:
		costPlan(x.Child, w)
		inRows := childRows(x.Child)
		if len(x.Group) == 0 {
			x.Rows = 1
		}
		x.Cost = nodeCostOf(x.Child) + float64(inRows)*w.AggPerRow
	case *physical.Sort:
		costPlan(x.Child, w)
		inRows := childRows(x.Child)
		x.Rows = inRows
		n := float64(inRows)
		if n < 2 {
			n = 2
		}
		x.Cost = nodeCostOf(x.Child) + n*math.Log2(n)*w.SortPerRow
	case *physical.Limit:
		costPlan(x.Child, w)
		x.Rows = childRows(x.Child)
		if x.Limit >= 0 {
			lim := x.Limit
			if x.Offset > 0 {
				lim += x.Offset
			}
			if int64(lim) < x.Rows {
				x.Rows = int64(lim)
			}
		}
		x.Cost = nodeCostOf(x.Child)
	}
}

func nodeCostOf(n physical.PhysicalNode) float64 {
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
