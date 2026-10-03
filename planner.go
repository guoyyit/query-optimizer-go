package optimizer

import (
	"fmt"
	"sort"

	"github.com/guoyyit/query-optimizer-go/cost"
	"github.com/guoyyit/query-optimizer-go/expr"
	"github.com/guoyyit/query-optimizer-go/plan"
	"github.com/guoyyit/query-optimizer-go/rules"
	"github.com/guoyyit/query-optimizer-go/value"
)

// physPlanner converts an optimized logical plan into a physical plan,
// choosing join algorithms, build/probe sides and access paths by
// estimated cost. Every choice uses cost.Compare, so equal-cost
// alternatives resolve deterministically by plan fingerprint.
type physPlanner struct {
	cfg   Config
	stats *statsView
	log   *rules.Log
}

// planned is a candidate physical plan with its estimates.
type planned struct {
	node plan.Physical
	cost float64
	rows float64
}

func (p *physPlanner) better(a, b planned) planned {
	if cost.Compare(a.cost, a.node.Fingerprint(), b.cost, b.node.Fingerprint()) <= 0 {
		return a
	}
	return b
}

func (p *physPlanner) planNode(node plan.Logical) planned {
	switch n := node.(type) {
	case *plan.Scan:
		rows, _ := p.stats.Rows(n.Alias)
		s := &plan.SeqScan{Table: n.Table, Alias: n.Alias, Columns: n.Columns}
		return planned{node: s, cost: float64(rows) * p.cfg.Weights.SeqScanRow, rows: float64(rows)}
	case *plan.Filter:
		return p.planFilter(n)
	case *plan.Project:
		child := p.planNode(n.Input)
		proj := &plan.ProjectOp{Cols: n.Cols, Input: child.node}
		return planned{node: proj, cost: child.cost + child.rows*p.cfg.Weights.OutputRow, rows: child.rows}
	case *plan.Join:
		return p.planJoin(n)
	}
	panic(fmt.Sprintf("planner: unknown logical node %T", node))
}

// planFilter considers an index access path when the filter sits directly
// on a scan and contains `indexedCol = constant`.
func (p *physPlanner) planFilter(f *plan.Filter) planned {
	scan, isScan := f.Input.(*plan.Scan)
	if !isScan {
		child := p.planNode(f.Input)
		sel := p.selectivity(f.Pred, child.rows)
		rows := maxF(child.rows*sel, 0)
		op := &plan.FilterOp{Pred: f.Pred, Input: child.node}
		return planned{node: op, cost: child.cost + child.rows*p.cfg.Weights.FilterRow, rows: rows}
	}
	// Candidate 1: sequential scan + filter.
	base := p.planNode(scan)
	sel := p.selectivity(f.Pred, base.rows)
	seqRows := maxF(base.rows*sel, 0)
	seq := planned{
		node: &plan.FilterOp{Pred: f.Pred, Input: base.node},
		cost: base.cost + base.rows*p.cfg.Weights.FilterRow,
		rows: seqRows,
	}
	// Candidate 2: index scan + residual filter, if an equality conjunct
	// matches a hash index.
	key, val, residual, ok := splitIndexable(f.Pred, scan)
	if !ok {
		return seq
	}
	t := p.stats.cat.Table(scan.Table)
	if t == nil || t.Index(key.Column) == nil {
		return seq
	}
	totalRows, _ := p.stats.Rows(scan.Alias)
	ndv, _ := p.stats.NDV(scan.Alias, key.Column)
	matched := float64(totalRows) / float64(ndv)
	idx := &plan.IndexScan{
		Table: scan.Table, Alias: scan.Alias, Columns: scan.Columns,
		Key: key, Value: val,
	}
	var idxPlan planned
	idxCost := p.cfg.Weights.IndexProbe + matched*p.cfg.Weights.IndexRow
	if residual != nil {
		resRows := maxF(matched*p.selectivity(residual, matched), 0)
		idxPlan = planned{
			node: &plan.FilterOp{Pred: residual, Input: idx},
			cost: idxCost + matched*p.cfg.Weights.FilterRow,
			rows: resRows,
		}
	} else {
		idxPlan = planned{node: idx, cost: idxCost, rows: matched}
	}
	best := p.better(seq, idxPlan)
	if best.node == seq.node {
		p.log.Add("access-path", fmt.Sprintf("chose sequential scan on %s over index on %s (cost %.1f vs %.1f)", scan.Alias, key.Column, seq.cost, idxPlan.cost))
	} else {
		p.log.Add("access-path", fmt.Sprintf("chose index scan on %s.%s (cost %.1f vs seq %.1f)", scan.Alias, key.Column, idxPlan.cost, seq.cost))
	}
	return best
}

// splitIndexable finds a `col = literal` conjunct usable for a hash index
// probe and returns the remaining predicate.
func splitIndexable(pred expr.Expr, scan *plan.Scan) (expr.ColRef, value.Value, expr.Expr, bool) {
	conjuncts := expr.SplitConjuncts(pred)
	for i, c := range conjuncts {
		b, ok := c.(*expr.Binary)
		if !ok || b.Op != expr.Eq {
			continue
		}
		var col *expr.ColumnRef
		var lit *expr.Literal
		if cl, ok1 := b.L.(*expr.ColumnRef); ok1 {
			if li, ok2 := b.R.(*expr.Literal); ok2 {
				col, lit = cl, li
			}
		}
		if col == nil {
			if cr, ok1 := b.R.(*expr.ColumnRef); ok1 {
				if li, ok2 := b.L.(*expr.Literal); ok2 {
					col, lit = cr, li
				}
			}
		}
		if col == nil || col.Table != scan.Alias {
			continue
		}
		rest := append([]expr.Expr{}, conjuncts[:i]...)
		rest = append(rest, conjuncts[i+1:]...)
		return expr.ColRef{Table: col.Table, Column: col.Column}, lit.V, expr.AndAll(rest), true
	}
	return expr.ColRef{}, value.Value{}, nil, false
}

// planJoin chooses the join algorithm and (for hash joins) the build side.
func (p *physPlanner) planJoin(j *plan.Join) planned {
	left := p.planNode(j.Left)
	right := p.planNode(j.Right)
	lKeys, rKeys, residual := equiKeys(j.Cond, j.Left.Schema(), j.Right.Schema())

	var best planned
	if len(lKeys) > 0 {
		outRows := p.joinRows(left.rows, right.rows, lKeys, rKeys)
		// Hash join, right builds (default orientation).
		hjRight := &plan.HashJoin{Cond: j.Cond, LeftKeys: lKeys, RightKeys: rKeys, Residual: residual, Left: left.node, Right: right.node}
		costRight := left.cost + right.cost +
			right.rows*p.cfg.Weights.HashBuildRow +
			left.rows*p.cfg.Weights.HashProbeRow +
			outRows*p.cfg.Weights.OutputRow
		// Hash join, left builds: swap keys to match.
		rKeysSw, lKeysSw := rKeys, lKeys
		hjLeft := &plan.HashJoin{Cond: j.Cond, LeftKeys: rKeysSw, RightKeys: lKeysSw, Residual: residual, Left: right.node, Right: left.node}
		costLeft := left.cost + right.cost +
			left.rows*p.cfg.Weights.HashBuildRow +
			right.rows*p.cfg.Weights.HashProbeRow +
			outRows*p.cfg.Weights.OutputRow
		candRight := planned{node: hjRight, cost: costRight, rows: outRows}
		candLeft := planned{node: hjLeft, cost: costLeft, rows: outRows}
		best = p.better(candRight, candLeft)
		if best.node == candRight.node {
			p.log.Add("join-algorithm", fmt.Sprintf("hash join: right input builds (cost %.1f vs %.1f)", costRight, costLeft))
		} else {
			p.log.Add("join-algorithm", fmt.Sprintf("hash join: left input builds (cost %.1f vs %.1f)", costLeft, costRight))
		}
	}
	// Nested-loop candidate (always available; the only option without
	// equi-keys).
	outRowsNL := left.rows * right.rows
	if len(lKeys) > 0 {
		outRowsNL = best.rows
	}
	nl := &plan.NestedLoopJoin{Cond: j.Cond, Left: left.node, Right: right.node}
	nlCost := left.cost + right.cost + left.rows*right.rows*p.cfg.Weights.NestedLoop + outRowsNL*p.cfg.Weights.OutputRow
	nlCand := planned{node: nl, cost: nlCost, rows: outRowsNL}
	if best.node == nil {
		best = nlCand
		p.log.Add("join-algorithm", "nested-loop join: no equi-join key available")
	} else {
		prev := best
		best = p.better(best, nlCand)
		if best.node == nlCand.node {
			p.log.Add("join-algorithm", fmt.Sprintf("nested-loop join beats hash join (cost %.1f vs %.1f)", nlCost, prev.cost))
		}
	}
	return best
}

// equiKeys splits cond into equi-join keys across the two sides plus a
// residual condition.
func equiKeys(cond expr.Expr, leftSchema, rightSchema []expr.ColRef) (l, r []expr.ColRef, residual expr.Expr) {
	inLeft := schemaSet(leftSchema)
	inRight := schemaSet(rightSchema)
	var rest []expr.Expr
	for _, c := range expr.SplitConjuncts(cond) {
		b, ok := c.(*expr.Binary)
		if !ok || b.Op != expr.Eq {
			rest = append(rest, c)
			continue
		}
		cl, ok1 := b.L.(*expr.ColumnRef)
		cr, ok2 := b.R.(*expr.ColumnRef)
		if !ok1 || !ok2 {
			rest = append(rest, c)
			continue
		}
		lr := expr.ColRef{Table: cl.Table, Column: cl.Column}
		rr := expr.ColRef{Table: cr.Table, Column: cr.Column}
		switch {
		case inLeft[lr] && inRight[rr]:
			l = append(l, lr)
			r = append(r, rr)
		case inLeft[rr] && inRight[lr]:
			l = append(l, rr)
			r = append(r, lr)
		default:
			rest = append(rest, c)
		}
	}
	// Deterministic key order.
	sort.Slice(l, func(i, j int) bool { return l[i].String() < l[j].String() })
	sort.Slice(r, func(i, j int) bool { return r[i].String() < r[j].String() })
	return l, r, expr.AndAll(rest)
}

func schemaSet(cols []expr.ColRef) map[expr.ColRef]bool {
	s := map[expr.ColRef]bool{}
	for _, c := range cols {
		s[c] = true
	}
	return s
}

// joinRows estimates equi-join output cardinality:
// |L| * |R| / max(NDV pairs), one divisor per key.
func (p *physPlanner) joinRows(lRows, rRows float64, lKeys, rKeys []expr.ColRef) float64 {
	out := lRows * rRows
	for i := range lKeys {
		ndvL, _ := p.stats.NDV(lKeys[i].Table, lKeys[i].Column)
		ndvR, _ := p.stats.NDV(rKeys[i].Table, rKeys[i].Column)
		d := float64(max(ndvL, ndvR))
		if d < 1 {
			d = 1
		}
		out /= d
	}
	return maxF(out, 1)
}

// selectivity estimates the fraction of rows kept by pred.
// Documented heuristics: equality 1/NDV, range 1/3, NOT complements,
// AND multiplies, OR adds (capped at 1).
func (p *physPlanner) selectivity(pred expr.Expr, inRows float64) float64 {
	sel := 1.0
	for _, c := range expr.SplitConjuncts(pred) {
		sel *= p.conjunctSel(c)
	}
	if sel > 1 {
		sel = 1
	}
	return sel
}

func (p *physPlanner) conjunctSel(c expr.Expr) float64 {
	switch t := c.(type) {
	case *expr.Binary:
		switch t.Op {
		case expr.Eq:
			if col, ok := t.L.(*expr.ColumnRef); ok {
				if _, isLit := t.R.(*expr.Literal); isLit {
					ndv, _ := p.stats.NDV(col.Table, col.Column)
					return 1 / float64(ndv)
				}
			}
			if col, ok := t.R.(*expr.ColumnRef); ok {
				if _, isLit := t.L.(*expr.Literal); isLit {
					ndv, _ := p.stats.NDV(col.Table, col.Column)
					return 1 / float64(ndv)
				}
			}
			return 0.1 // col = col without stats guidance
		case expr.Ne:
			return 1 - p.conjunctSel(&expr.Binary{Op: expr.Eq, L: t.L, R: t.R})
		case expr.Lt, expr.Le, expr.Gt, expr.Ge:
			return 1.0 / 3.0
		case expr.Or:
			s := p.conjunctSel(t.L) + p.conjunctSel(t.R)
			if s > 1 {
				s = 1
			}
			return s
		}
	case *expr.Not:
		return 1 - p.conjunctSel(t.E)
	}
	return 1.0
}

func maxF(a, b float64) float64 {
	if a > b {
		return a
	}
	return b
}
