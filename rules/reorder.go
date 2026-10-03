package rules

import (
	"fmt"
	"sort"

	"github.com/guoyyit/query-optimizer-go/expr"
	"github.com/guoyyit/query-optimizer-go/plan"
)

// Reorder flattens inner-join chains and reorders the leaves greedily by
// estimated size, so joins connected by equi-conditions are executed before
// any unavoidable cross product. The rule is deterministic: candidates are
// considered in (estimated rows, alias) order, and every decision that
// cannot avoid a cross product is explained in the log.
func Reorder(root plan.Logical, stats StatsView, log *Log) plan.Logical {
	switch n := root.(type) {
	case *plan.Project:
		return &plan.Project{Cols: n.Cols, Input: Reorder(n.Input, stats, log)}
	case *plan.Filter:
		return &plan.Filter{Pred: n.Pred, Input: Reorder(n.Input, stats, log)}
	case *plan.Join:
		return reorderJoin(n, stats, log)
	}
	return root
}

// leaf is a non-join plan node (scan with any pushed-down filters).
type leaf struct {
	node  plan.Logical
	alias string // scan alias; leaves under filters resolve to their scan
	rows  int64
}

func reorderJoin(root *plan.Join, stats StatsView, log *Log) plan.Logical {
	// Flatten the join chain.
	var leaves []*leaf
	var conds []expr.Expr
	var walk func(n plan.Logical)
	walk = func(n plan.Logical) {
		if j, ok := n.(*plan.Join); ok && j.How == plan.InnerJoin {
			walk(j.Left)
			walk(j.Right)
			conds = append(conds, expr.SplitConjuncts(j.Cond)...)
			return
		}
		leaves = append(leaves, &leaf{node: n, alias: scanAlias(n)})
	}
	walk(root)
	if len(leaves) <= 2 {
		return root // nothing to reorder
	}
	for _, lf := range leaves {
		rows, ok := stats.Rows(lf.alias)
		if !ok {
			log.Add("join-reorder", fmt.Sprintf("statistics missing for %s; using conservative default row estimate", lf.alias))
		}
		lf.rows = rows
	}
	// Attach single-leaf conditions as filters on that leaf.
	var joinConds []expr.Expr
	for _, c := range conds {
		tabs := leafSetOf(c, leaves)
		if len(tabs) == 1 {
			for _, lf := range leaves {
				if lf.alias == tabs[0] {
					lf.node = &plan.Filter{Pred: c, Input: lf.node}
					log.Add("join-reorder", "attached single-table join condition "+c.String()+" as filter on "+lf.alias)
				}
			}
		} else {
			joinConds = append(joinConds, c)
		}
	}

	// Greedy ordering: start from the smallest leaf; extend with the
	// cheapest connected leaf; fall back to the smallest remaining leaf
	// (cross product) only when nothing connects.
	remaining := map[string]*leaf{}
	for _, lf := range leaves {
		remaining[lf.alias] = lf
	}
	pick := func(cands []*leaf) *leaf {
		sort.Slice(cands, func(i, j int) bool {
			if cands[i].rows != cands[j].rows {
				return cands[i].rows < cands[j].rows
			}
			return cands[i].alias < cands[j].alias
		})
		return cands[0]
	}
	all := []*leaf{}
	for _, lf := range remaining {
		all = append(all, lf)
	}
	first := pick(all)
	joined := []string{first.alias}
	delete(remaining, first.alias)
	var out plan.Logical = first.node
	joinedRows := first.rows

	for len(remaining) > 0 {
		// Find conditions that connect a remaining leaf to the joined set.
		connected := map[string]bool{}
		for _, c := range joinConds {
			tabs := leafSetOf(c, leaves)
			if len(tabs) != 2 {
				continue
			}
			a, b := tabs[0], tabs[1]
			aIn, bIn := contains(joined, a), contains(joined, b)
			_, aRem := remaining[a]
			_, bRem := remaining[b]
			if aIn && bRem {
				connected[b] = true
			}
			if bIn && aRem {
				connected[a] = true
			}
		}
		var cands []*leaf
		for alias := range remaining {
			if connected[alias] {
				cands = append(cands, remaining[alias])
			}
		}
		if len(cands) == 0 {
			for _, lf := range remaining {
				cands = append(cands, lf)
			}
			next := pick(cands)
			log.Add("join-reorder", fmt.Sprintf("no join condition connects %s to {%v}; keeping a cross product (this is the only semantically correct option)", next.alias, joined))
			out = &plan.Join{How: plan.InnerJoin, Cond: nil, Left: out, Right: next.node}
			joined = append(joined, next.alias)
			joinedRows = joinedRows * max64(next.rows, 1)
			delete(remaining, next.alias)
			continue
		}
		next := pick(cands)
		// Consume every condition whose both endpoints are now inside the
		// joined set; this attaches each condition at the earliest join
		// where it becomes applicable.
		inSet := func(a string) bool { return a == next.alias || contains(joined, a) }
		var use, rest []expr.Expr
		for _, c := range joinConds {
			tabs := leafSetOf(c, leaves)
			if len(tabs) == 2 && inSet(tabs[0]) && inSet(tabs[1]) {
				use = append(use, c)
			} else {
				rest = append(rest, c)
			}
		}
		joinConds = rest
		out = &plan.Join{How: plan.InnerJoin, Cond: expr.AndAll(use), Left: out, Right: next.node}
		joined = append(joined, next.alias)
		joinedRows = joinedRows * max64(next.rows, 1)
		delete(remaining, next.alias)
	}
	// Any leftover conditions (e.g. between two leaves that were joined
	// across a cross product) become a residual filter on top.
	if len(joinConds) > 0 {
		log.Add("join-reorder", fmt.Sprintf("%d join condition(s) could not be assigned to a single join and stay as residual filters", len(joinConds)))
		out = &plan.Filter{Pred: expr.AndAll(joinConds), Input: out}
	}
	log.Add("join-reorder", fmt.Sprintf("join order: %v", joined))
	return out
}

func scanAlias(n plan.Logical) string {
	switch t := n.(type) {
	case *plan.Scan:
		return t.Alias
	case *plan.Filter:
		return scanAlias(t.Input)
	case *plan.Project:
		return scanAlias(t.Input)
	}
	return ""
}

// leafSetOf returns the sorted distinct aliases referenced by e.
func leafSetOf(e expr.Expr, leaves []*leaf) []string {
	set := map[string]bool{}
	for _, c := range e.Columns() {
		set[c.Table] = true
	}
	out := make([]string, 0, len(set))
	for a := range set {
		out = append(out, a)
	}
	sort.Strings(out)
	return out
}

func contains(s []string, v string) bool {
	for _, x := range s {
		if x == v {
			return true
		}
	}
	return false
}

func max64(a, b int64) int64 {
	if a > b {
		return a
	}
	return b
}
