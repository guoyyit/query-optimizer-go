package plan

import (
	"fmt"
	"math"
	"sort"
	"strings"

	"github.com/guoyyit/query-optimizer-go/catalog"
	"github.com/guoyyit/query-optimizer-go/config"
	"github.com/guoyyit/query-optimizer-go/plan/physical"
	"github.com/guoyyit/query-optimizer-go/rows"
	"github.com/guoyyit/query-optimizer-go/sql"
	"github.com/guoyyit/query-optimizer-go/stats"
)

type physicalPlanner struct {
	cat      *catalog.Catalog
	cfg      *config.Config
	degraded []string
	notes    []RewriteNote
}

// plan converts a rewritten logical plan into a cost-based physical plan.
func (pp *physicalPlanner) plan(root LogicalNode) (physical.PhysicalNode, error) {
	node, err := pp.planNode(root)
	if err != nil {
		return nil, err
	}
	if pp.cfg.Rules.ProjectionPruning {
		pp.pruneColumns(node)
	}
	costPlan(node, pp.cfg.Weights)
	return node, nil
}

func (pp *physicalPlanner) planNode(n LogicalNode) (physical.PhysicalNode, error) {
	switch x := n.(type) {
	case *Scan:
		return pp.planScan(x)
	case *Filter:
		return pp.planFilter(x)
	case *Project:
		return pp.planProject(x)
	case *Join:
		return pp.planJoin(x)
	case *Aggregate:
		return pp.planAggregate(x)
	case *Sort:
		return pp.planSort(x)
	case *Limit:
		return pp.planLimit(x)
	case *Distinct:
		return pp.planDistinct(x)
	}
	return nil, fmt.Errorf("physical: unknown logical node %T", n)
}

func (pp *physicalPlanner) tableFor(alias string) (*catalog.Table, error) {
	t, ok := pp.cat.Get(alias)
	if !ok {
		return nil, fmt.Errorf("physical: unknown table or alias %q", alias)
	}
	return t, nil
}

func (pp *physicalPlanner) statsFor(alias string) (*stats.TableStats, *catalog.Table) {
	t, err := pp.tableFor(alias)
	if err != nil {
		return nil, nil
	}
	if t.HasStats && t.Stats != nil {
		return t.Stats, t
	}
	pp.degraded = append(pp.degraded, fmt.Sprintf("missing statistics for table %q; using conservative defaults", alias))
	return nil, t
}

func scanSchema(t *catalog.Table, alias string) rows.Schema {
	cols := make([]rows.Column, len(t.Schema.Columns))
	for i, c := range t.Schema.Columns {
		cols[i] = rows.Column{Name: c.Name, Type: c.Type, Alias: alias}
	}
	return rows.Schema{Columns: cols}
}

func (pp *physicalPlanner) planScan(x *Scan) (physical.PhysicalNode, error) {
	t, err := pp.tableFor(x.Table)
	if err != nil {
		return nil, err
	}
	st, _ := pp.statsFor(x.Table)
	nrows := int64(len(t.Rows))
	if st != nil {
		nrows = st.Rows
	}
	schema := scanSchema(t, x.Alias)
	node := &physical.SeqScan{
		Table:  x.Table,
		Alias:  x.Alias,
		Cols:   nil, // pruned later
		Schema: schema,
		Rows:   nrows,
	}
	return node, nil
}

func (pp *physicalPlanner) planFilter(x *Filter) (physical.PhysicalNode, error) {
	child, err := pp.planNode(x.Child)
	if err != nil {
		return nil, err
	}
	// try index scan when the child is a plain sequential scan
	if seq, ok := child.(*physical.SeqScan); ok && pp.cfg.Rules.IndexScan {
		if idx, residual, ok := pp.tryIndexScan(seq, x.Pred); ok {
			if residual != nil {
				st, _ := pp.statsFor(seq.Alias)
				sel, _ := pp.estimateSel(residual, st)
				return &physical.Filter{Child: idx, Pred: residual, Schema: idx.Schema, Sel: sel}, nil
			}
			return idx, nil
		}
	}
	st := pp.statsOf(child)
	sel, _ := pp.estimateSel(x.Pred, st)
	return &physical.Filter{Child: child, Pred: x.Pred, Schema: child.OutSchema(), Sel: sel}, nil
}

// statsOf returns table stats if the subtree is a single table scan.
func (pp *physicalPlanner) statsOf(n physical.PhysicalNode) *stats.TableStats {
	switch x := n.(type) {
	case *physical.SeqScan:
		st, _ := pp.statsFor(x.Table)
		return st
	case *physical.IndexScan:
		st, _ := pp.statsFor(x.Table)
		return st
	case *physical.Filter:
		return pp.statsOf(x.Child)
	}
	return nil
}

func (pp *physicalPlanner) estimateSel(pred sql.Expr, st *stats.TableStats) (float64, []string) {
	if st == nil {
		pp.degraded = append(pp.degraded, "cannot estimate selectivity without table statistics; using conservative defaults")
		return pp.cfg.Defaults.EqSelectivity, nil
	}
	sel, degraded, reasons := stats.EstimateSelectivity(pred, st, pp.cfg.Defaults)
	if degraded {
		for _, r := range reasons {
			pp.degraded = append(pp.degraded, r)
		}
	}
	return sel, reasons
}

// tryIndexScan attempts to replace a seq scan with an index scan for an
// equality predicate on an indexed column.
func (pp *physicalPlanner) tryIndexScan(seq *physical.SeqScan, pred sql.Expr) (*physical.IndexScan, sql.Expr, bool) {
	conjuncts := sql.SplitConjuncts(pred)
	var residual []sql.Expr
	for _, c := range conjuncts {
		if ref, lit, ok := eqColLit(c); ok && (ref.Table == "" || ref.Table == seq.Alias) {
			t, err := pp.tableFor(seq.Table)
			if err != nil {
				residual = append(residual, c)
				continue
			}
			colIdx, found := t.Schema.Index(ref.Name)
			if !found {
				residual = append(residual, c)
				continue
			}
			idx := pp.cat.EnsureIndex(seq.Table, ref.Name)
			if idx == nil {
				residual = append(residual, c)
				continue
			}
			st, _ := pp.statsFor(seq.Table)
			sel, _ := pp.estimateSel(c, st)
			idxRows := int64(float64(seq.Rows) * sel)
			if idxRows < 1 {
				idxRows = 1
			}
			seqCost := float64(seq.Rows) * pp.cfg.Weights.SeqScanPerRow
			idxCost := pp.cfg.Weights.IndexSetup + float64(idxRows)*pp.cfg.Weights.IndexPerRow
			if idxCost < seqCost-pp.cfg.CostEpsilon {
				schema := scanSchema(t, seq.Alias)
				node := &physical.IndexScan{
					Table:   seq.Table,
					Alias:   seq.Alias,
					Index:   ref.Name,
					KeyCols: []int{colIdx},
					KeyExpr: lit,
					Schema:  schema,
					Rows:    idxRows,
					Sel:     sel,
				}
				return node, nil, true
			}
		}
		residual = append(residual, c)
	}
	return nil, nil, false
}

// eqColLit matches `alias.col = literal` or `literal = alias.col`.
func eqColLit(e sql.Expr) (*sql.ColumnRef, sql.Expr, bool) {
	b, ok := e.(*sql.BinOp)
	if !ok {
		return nil, nil, false
	}
	if b.Op != "=" {
		return nil, nil, false
	}
	if ref, ok := b.Left.(*sql.ColumnRef); ok {
		if _, ok := b.Right.(*sql.Literal); ok {
			return ref, b.Right, true
		}
	}
	if ref, ok := b.Right.(*sql.ColumnRef); ok {
		if _, ok := b.Left.(*sql.Literal); ok {
			return ref, b.Left, true
		}
	}
	return nil, nil, false
}

func (pp *physicalPlanner) planProject(x *Project) (physical.PhysicalNode, error) {
	child, err := pp.planNode(x.Child)
	if err != nil {
		return nil, err
	}
	schema := child.OutSchema()
	outCols := make([]rows.Column, 0, len(x.Exprs))
	outExprs := make([]sql.Expr, 0, len(x.Exprs))
	outNames := make([]string, 0, len(x.Exprs))
	for i, e := range x.Exprs {
		name := x.Names[i]
		if star, ok := e.(*sql.Star); ok {
			for _, c := range schema.Columns {
				if star.Table != "" && c.Alias != star.Table {
					continue
				}
				outCols = append(outCols, rows.Column{Name: c.Name, Type: c.Type})
				outExprs = append(outExprs, &sql.ColumnRef{Name: c.Name, Table: c.Alias})
				outNames = append(outNames, c.Name)
			}
			continue
		}
		outCols = append(outCols, rows.Column{Name: name, Type: InferType(e, schema)})
		outExprs = append(outExprs, e)
		outNames = append(outNames, name)
	}
	return &physical.Project{
		Child:  child,
		Exprs:  outExprs,
		Names:  outNames,
		Schema: rows.Schema{Columns: outCols},
	}, nil
}

func (pp *physicalPlanner) planAggregate(x *Aggregate) (physical.PhysicalNode, error) {
	child, err := pp.planNode(x.Child)
	if err != nil {
		return nil, err
	}
	schema := child.OutSchema()
	outCols := make([]rows.Column, 0, len(x.Group)+len(x.Aggs))
	for _, g := range x.Group {
		name := "group"
		alias := ""
		if ref, ok := g.(*sql.ColumnRef); ok {
			name = ref.Name
			alias = ref.Table
		}
		outCols = append(outCols, rows.Column{Name: name, Type: InferType(g, schema), Alias: alias})
	}
	for _, a := range x.Aggs {
		agg := a.(*sql.Aggregate)
		outCols = append(outCols, rows.Column{Name: defaultColumnName(agg), Type: AggType(agg, schema)})
	}
	// estimate output rows
	inRows := child.OutSchema()
	_ = inRows
	outRows := int64(1)
	if len(x.Group) > 0 {
		outRows = childRows(child) / 10
		if outRows < 1 {
			outRows = 1
		}
	}
	return &physical.HashAggregate{
		Child:  child,
		Group:  x.Group,
		Aggs:   x.Aggs,
		Schema: rows.Schema{Columns: outCols},
		Rows:   outRows,
	}, nil
}

func childRows(n physical.PhysicalNode) int64 {
	switch x := n.(type) {
	case *physical.SeqScan:
		return x.Rows
	case *physical.IndexScan:
		return x.Rows
	case *physical.Filter:
		return x.Rows
	case *physical.Project:
		return childRows(x.Child)
	case *physical.HashJoin:
		return x.Rows
	case *physical.NLJ:
		return x.Rows
	case *physical.HashAggregate:
		return x.Rows
	case *physical.Sort:
		return childRows(x.Child)
	case *physical.Limit:
		return x.Rows
	}
	return 0
}

func (pp *physicalPlanner) planSort(x *Sort) (physical.PhysicalNode, error) {
	child, err := pp.planNode(x.Child)
	if err != nil {
		return nil, err
	}
	return &physical.Sort{Child: child, Keys: x.Keys, Schema: child.OutSchema()}, nil
}

func (pp *physicalPlanner) planLimit(x *Limit) (physical.PhysicalNode, error) {
	child, err := pp.planNode(x.Child)
	if err != nil {
		return nil, err
	}
	outRows := childRows(child)
	lim := x.Limit
	if lim < 0 {
		lim = outRows
	}
	if x.Offset > 0 && lim > x.Offset {
		lim = x.Limit
	}
	return &physical.Limit{Child: child, Limit: x.Limit, Offset: x.Offset, Rows: lim}, nil
}

func (pp *physicalPlanner) planDistinct(x *Distinct) (physical.PhysicalNode, error) {
	child, err := pp.planNode(x.Child)
	if err != nil {
		return nil, err
	}
	schema := child.OutSchema()
	groups := make([]sql.Expr, 0, len(schema.Columns))
	for _, c := range schema.Columns {
		groups = append(groups, &sql.ColumnRef{Name: c.Name, Table: c.Alias})
	}
	outRows := childRows(child) / 10
	if outRows < 1 {
		outRows = 1
	}
	return &physical.HashAggregate{
		Child:  child,
		Group:  groups,
		Aggs:   nil,
		Schema: schema,
		Rows:   outRows,
	}, nil
}

// ---------- joins ----------

func (pp *physicalPlanner) planJoin(x *Join) (physical.PhysicalNode, error) {
	if x.Type == sql.JoinInner && pp.cfg.Rules.JoinReorder && isInnerTree(x) {
		return pp.planJoinGraph(x)
	}
	left, err := pp.planNode(x.Left)
	if err != nil {
		return nil, err
	}
	right, err := pp.planNode(x.Right)
	if err != nil {
		return nil, err
	}
	return pp.buildJoin(left, right, x.Cond, x.Type), nil
}

// isInnerTree reports whether a logical tree consists only of inner joins
// (and scans/filters), so it can be reordered.
func isInnerTree(n LogicalNode) bool {
	switch x := n.(type) {
	case *Scan:
		return true
	case *Filter:
		return isInnerTree(x.Child)
	case *Join:
		return x.Type == sql.JoinInner && isInnerTree(x.Left) && isInnerTree(x.Right)
	}
	return false
}

func (pp *physicalPlanner) buildJoin(left, right physical.PhysicalNode, cond sql.Expr, jt sql.JoinType) physical.PhysicalNode {
	schema := concatSchema(left.OutSchema(), right.OutSchema())
	leftRows := childRows(left)
	rightRows := childRows(right)
	if jt == sql.JoinCross || cond == nil {
		pp.notes = append(pp.notes, note(RuleJoinReorder, "disconnected join graph; Cartesian product is unavoidable"))
		return &physical.NLJ{
			Left: left, Right: right, Cond: nil, JoinType: sql.JoinCross,
			Schema: schema, Rows: leftRows * rightRows, Sel: 1.0,
		}
	}
	equi := isEquiCond(cond)
	if jt == sql.JoinLeft {
		// left join: build side must be the right (null-supplying) relation
		sel := pp.joinSel(cond, left, right)
		return &physical.HashJoin{
			Left: left, Right: right, Cond: cond, BuildLeft: false, JoinType: sql.JoinLeft,
			Schema: schema, Rows: int64(float64(leftRows) * float64(rightRows) * sel), Sel: sel,
		}
	}
	if equi {
		sel := pp.joinSel(cond, left, right)
		buildLeft := leftRows <= rightRows
		return &physical.HashJoin{
			Left: left, Right: right, Cond: cond, BuildLeft: buildLeft, JoinType: sql.JoinInner,
			Schema: schema, Rows: int64(float64(leftRows) * float64(rightRows) * sel), Sel: sel,
		}
	}
	sel := pp.nonEquiSel(cond)
	return &physical.NLJ{
		Left: left, Right: right, Cond: cond, JoinType: sql.JoinInner,
		Schema: schema, Rows: int64(float64(leftRows) * float64(rightRows) * sel), Sel: sel,
	}
}

func isEquiCond(cond sql.Expr) bool {
	for _, c := range sql.SplitConjuncts(cond) {
		b, ok := c.(*sql.BinOp)
		if !ok || b.Op != "=" {
			return false
		}
		_, lok := b.Left.(*sql.ColumnRef)
		_, rok := b.Right.(*sql.ColumnRef)
		if !lok || !rok {
			return false
		}
	}
	return cond != nil
}

func (pp *physicalPlanner) joinSel(cond sql.Expr, left, right physical.PhysicalNode) float64 {
	// use NDV of equi-join columns
	for _, c := range sql.SplitConjuncts(cond) {
		b := c.(*sql.BinOp)
		lref := b.Left.(*sql.ColumnRef)
		rref := b.Right.(*sql.ColumnRef)
		lst := pp.statsOf(left)
		rst := pp.statsOf(right)
		var lcol, rcol *stats.ColumnStats
		if lst != nil {
			lcol = lst.LookupColumn(lref.Name)
		}
		if rst != nil {
			rcol = rst.LookupColumn(rref.Name)
		}
		if lcol != nil && rcol != nil {
			sel, _, _ := stats.JoinSelectivity(lcol, rcol, pp.cfg.Defaults)
			return sel
		}
		pp.degraded = append(pp.degraded, "missing NDV for join column; using conservative join selectivity")
	}
	return pp.cfg.Defaults.EqSelectivity
}

func (pp *physicalPlanner) nonEquiSel(cond sql.Expr) float64 {
	pp.degraded = append(pp.degraded, "non-equi join; using conservative range selectivity")
	return pp.cfg.Defaults.RangeSelectivity
}

func concatSchema(a, b rows.Schema) rows.Schema {
	cols := make([]rows.Column, 0, len(a.Columns)+len(b.Columns))
	cols = append(cols, a.Columns...)
	cols = append(cols, b.Columns...)
	return rows.Schema{Columns: cols}
}

// ---------- join reorder DP ----------

type relation struct {
	alias   string
	table   string
	filters []sql.Expr
	scan    *Scan
}

type joinGraph struct {
	rels     []*relation
	edges    []*joinEdge
	residual []sql.Expr // predicates on 3+ relations that stay above the join
}

type joinEdge struct {
	left  string
	right string
	cond  sql.Expr
	equi  bool
}

type dpState struct {
	node physical.PhysicalNode
	cost float64
	rows int64
	sig  string
}

func (pp *physicalPlanner) planJoinGraph(root LogicalNode) (physical.PhysicalNode, error) {
	g := collectJoinGraph(root)
	rels, edges := g.rels, g.edges
	if len(rels) < 2 {
		return pp.planNode(root)
	}
	relIndex := map[string]int{}
	for i, r := range rels {
		relIndex[r.alias] = i
	}

	// build adjacency
	n := len(rels)
	adj := make([][]bool, n)
	for i := range adj {
		adj[i] = make([]bool, n)
	}
	edgeBetween := map[string]*joinEdge{}
	for _, e := range edges {
		i := relIndex[e.left]
		j := relIndex[e.right]
		adj[i][j] = true
		adj[j][i] = true
		key := pairKey(i, j)
		if ex, ok := edgeBetween[key]; ok {
			ex.cond = sql.Conjoin([]sql.Expr{ex.cond, e.cond})
			ex.equi = ex.equi && e.equi
		} else {
			edgeBetween[key] = &joinEdge{left: e.left, right: e.right, cond: e.cond, equi: e.equi}
		}
	}

	// connected components
	comps := connectedComponents(n, adj)
	var notes []string
	if len(comps) > 1 {
		names := make([]string, 0, len(comps))
		for _, comp := range comps {
			cn := rels[comp[0]].alias
			for _, idx := range comp[1:] {
				cn += "," + rels[idx].alias
			}
			names = append(names, "{"+cn+"}")
		}
		sort.Strings(names)
		notes = append(notes, "join graph is disconnected; Cartesian product between "+strings.Join(names, " and ")+" is unavoidable")
	}

	var compPlans []*dpState
	for _, comp := range comps {
		st, err := pp.dpComponent(comp, rels, adj, edgeBetween)
		if err != nil {
			return nil, err
		}
		compPlans = append(compPlans, st)
	}
	if len(comps) > 1 {
		names := make([]string, 0, len(comps))
		for _, comp := range comps {
			cn := rels[comp[0]].alias
			for _, idx := range comp[1:] {
				cn += "," + rels[idx].alias
			}
			names = append(names, "{"+cn+"}")
		}
		sort.Strings(names)
		pp.notes = append(pp.notes, note(RuleJoinReorder, "join graph is disconnected; Cartesian product between "+strings.Join(names, " and ")+" is unavoidable"))
	}
	// cross join components in deterministic order (sorted by min alias)
	sort.Slice(compPlans, func(i, j int) bool {
		return compPlans[i].sig < compPlans[j].sig
	})
	rootState := compPlans[0]
	for i := 1; i < len(compPlans); i++ {
		rootState = pp.crossJoinStates(rootState, compPlans[i])
	}
	node := rootState.node
	if len(g.residual) > 0 {
		pred := sql.Conjoin(g.residual)
		st := pp.statsOf(node)
		sel, _ := pp.estimateSel(pred, st)
		node = &physical.Filter{Child: node, Pred: pred, Schema: node.OutSchema(), Sel: sel}
	}
	return node, nil
}

func pairKey(i, j int) string {
	if i > j {
		i, j = j, i
	}
	return fmt.Sprintf("%d|%d", i, j)
}

func connectedComponents(n int, adj [][]bool) [][]int {
	visited := make([]bool, n)
	var comps [][]int
	for i := 0; i < n; i++ {
		if visited[i] {
			continue
		}
		var comp []int
		stack := []int{i}
		for len(stack) > 0 {
			v := stack[len(stack)-1]
			stack = stack[:len(stack)-1]
			if visited[v] {
				continue
			}
			visited[v] = true
			comp = append(comp, v)
			for w := 0; w < n; w++ {
				if adj[v][w] && !visited[w] {
					stack = append(stack, w)
				}
			}
		}
		sort.Ints(comp)
		comps = append(comps, comp)
	}
	return comps
}

func collectJoinGraph(root LogicalNode) *joinGraph {
	g := &joinGraph{}
	var walk func(n LogicalNode)
	walk = func(n LogicalNode) {
		switch x := n.(type) {
		case *Scan:
			g.rels = append(g.rels, &relation{alias: x.Alias, table: x.Table, scan: x})
		case *Filter:
			if s, ok := x.Child.(*Scan); ok {
				g.rels = append(g.rels, &relation{alias: s.Alias, table: s.Table, scan: s, filters: sql.SplitConjuncts(x.Pred)})
			} else {
				g.residual = append(g.residual, sql.SplitConjuncts(x.Pred)...)
				walk(x.Child)
			}
		case *Join:
			walk(x.Left)
			walk(x.Right)
			if x.Cond != nil {
				for _, c := range sql.SplitConjuncts(x.Cond) {
					refs := aliasesOf(c)
					if len(refs) == 2 {
						g.edges = append(g.edges, &joinEdge{left: refs[0], right: refs[1], cond: c, equi: isEquiCond(c)})
					}
				}
			}
		}
	}
	walk(root)
	return g
}

func (pp *physicalPlanner) dpComponent(comp []int, rels []*relation, adj [][]bool, edgeBetween map[string]*joinEdge) (*dpState, error) {
	m := len(comp)
	local := map[int]int{}
	for i, g := range comp {
		local[g] = i
	}
	size := 1 << m
	best := make([]*dpState, size)

	// base: single relations
	for i := 0; i < m; i++ {
		g := comp[i]
		rel := rels[g]
		var node physical.PhysicalNode
		var err error
		if len(rel.filters) > 0 {
			node, err = pp.planFilter(&Filter{Child: rel.scan, Pred: sql.Conjoin(rel.filters)})
		} else {
			node, err = pp.planScan(rel.scan)
		}
		if err != nil {
			return nil, err
		}
		best[1<<i] = &dpState{
			node: node,
			cost: pp.nodeCost(node),
			rows: childRows(node),
			sig:  pp.nodeSig(node),
		}
	}

	for sz := 2; sz <= m; sz++ {
		for mask := 1; mask < size; mask++ {
			if bits(mask) != sz {
				continue
			}
			for s1 := (mask - 1) & mask; s1 > 0; s1 = (s1 - 1) & mask {
				s2 := mask ^ s1
				if s1 > s2 {
					continue
				}
				if best[s1] == nil || best[s2] == nil {
					continue
				}
				if !dpConnected(s1, s2, comp, adj) {
					continue
				}
				cand := pp.combineStates(best[s1], best[s2], s1, s2, comp, edgeBetween)
				if cand == nil {
					continue
				}
				if best[mask] == nil || cand.cost < best[mask].cost-pp.cfg.CostEpsilon*math.Max(1, best[mask].cost) ||
					(math.Abs(cand.cost-best[mask].cost) <= pp.cfg.CostEpsilon*math.Max(1, best[mask].cost) && cand.sig < best[mask].sig) {
					best[mask] = cand
				}
			}
		}
	}
	return best[size-1], nil
}

func bits(m int) int {
	n := 0
	for m > 0 {
		n += m & 1
		m >>= 1
	}
	return n
}

func dpConnected(s1, s2 int, comp []int, adj [][]bool) bool {
	for i := 0; i < len(comp); i++ {
		for j := 0; j < len(comp); j++ {
			if i == j {
				continue
			}
			in1 := s1>>uint(i)&1 == 1
			in2 := s2>>uint(j)&1 == 1
			if in1 && in2 && adj[comp[i]][comp[j]] {
				return true
			}
		}
	}
	return false
}

// combineStates joins two DP states using the edges between them.
func (pp *physicalPlanner) combineStates(a, b *dpState, maskA, maskB int, comp []int, edgeBetween map[string]*joinEdge) *dpState {
	var conds []sql.Expr
	equi := true
	found := false
	for i := 0; i < len(comp); i++ {
		for j := 0; j < len(comp); j++ {
			if i == j {
				continue
			}
			inA1 := maskA>>uint(i)&1 == 1
			inB1 := maskB>>uint(j)&1 == 1
			if !(inA1 && inB1) {
				continue
			}
			key := pairKey(comp[i], comp[j])
			if e, ok := edgeBetween[key]; ok {
				conds = append(conds, e.cond)
				if !e.equi {
					equi = false
				}
				found = true
			}
		}
	}
	if !found {
		return nil
	}
	cond := sql.Conjoin(conds)
	schema := concatSchema(a.node.OutSchema(), b.node.OutSchema())
	weights := pp.cfg.Weights

	if equi {
		sel := pp.joinSel(cond, a.node, b.node)
		// orientation 1: a build
		costA := a.cost + b.cost + float64(a.rows)*weights.HashJoinBuildPerRow + float64(b.rows)*weights.HashJoinProbePerRow
		rows := int64(float64(a.rows) * float64(b.rows) * sel)
		sigA := "hj(" + a.sig + "|" + b.sig + "," + sql.CanonicalExpr(cond) + ")"
		// orientation 2: b build
		costB := a.cost + b.cost + float64(b.rows)*weights.HashJoinBuildPerRow + float64(a.rows)*weights.HashJoinProbePerRow
		sigB := "hj(" + b.sig + "|" + a.sig + "," + sql.CanonicalExpr(cond) + ")"
		nodeA := &physical.HashJoin{
			Left: a.node, Right: b.node, Cond: cond, BuildLeft: true, JoinType: sql.JoinInner,
			Schema: schema, Rows: rows, Sel: sel,
		}
		nodeB := &physical.HashJoin{
			Left: b.node, Right: a.node, Cond: cond, BuildLeft: true, JoinType: sql.JoinInner,
			Schema: schema, Rows: rows, Sel: sel,
		}
		if costB < costA-pp.cfg.CostEpsilon*math.Max(1, costA) ||
			(math.Abs(costB-costA) <= pp.cfg.CostEpsilon*math.Max(1, costA) && sigB < sigA) {
			return &dpState{node: nodeB, cost: costB, rows: rows, sig: sigB}
		}
		return &dpState{node: nodeA, cost: costA, rows: rows, sig: sigA}
	}

	// non-equi: nested loop
	sel := pp.nonEquiSel(cond)
	cost := a.cost + b.cost + float64(a.rows*b.rows)*weights.NLJPerPair
	rows := int64(float64(a.rows) * float64(b.rows) * sel)
	sig := "nlj(" + a.sig + "," + b.sig + "," + sql.CanonicalExpr(cond) + ")"
	node := &physical.NLJ{
		Left: a.node, Right: b.node, Cond: cond, JoinType: sql.JoinInner,
		Schema: schema, Rows: rows, Sel: sel,
	}
	return &dpState{node: node, cost: cost, rows: rows, sig: sig}
}

func (pp *physicalPlanner) crossJoinStates(a, b *dpState) *dpState {
	schema := concatSchema(a.node.OutSchema(), b.node.OutSchema())
	cost := a.cost + b.cost + float64(a.rows*b.rows)*pp.cfg.Weights.NLJPerPair
	rows := a.rows * b.rows
	sig := "nlj(" + a.sig + "," + b.sig + ")"
	node := &physical.NLJ{
		Left: a.node, Right: b.node, Cond: nil, JoinType: sql.JoinCross,
		Schema: schema, Rows: rows, Sel: 1.0,
	}
	return &dpState{node: node, cost: cost, rows: rows, sig: sig}
}

// ---------- signatures & costs ----------

func (pp *physicalPlanner) nodeCost(n physical.PhysicalNode) float64 {
	w := pp.cfg.Weights
	switch x := n.(type) {
	case *physical.SeqScan:
		return float64(x.Rows) * w.SeqScanPerRow
	case *physical.IndexScan:
		return w.IndexSetup + float64(x.Rows)*w.IndexPerRow
	case *physical.Filter:
		return pp.nodeCost(x.Child) + float64(childRows(x.Child))*w.SeqScanPerRow
	case *physical.Project:
		return pp.nodeCost(x.Child) + float64(childRows(x.Child))*w.ProjectPerRow
	case *physical.HashJoin:
		return pp.nodeCost(x.Left) + pp.nodeCost(x.Right) + float64(childRows(x.Left))*w.HashJoinBuildPerRow + float64(childRows(x.Right))*w.HashJoinProbePerRow
	case *physical.NLJ:
		return pp.nodeCost(x.Left) + pp.nodeCost(x.Right) + float64(childRows(x.Left)*childRows(x.Right))*w.NLJPerPair
	case *physical.HashAggregate:
		return pp.nodeCost(x.Child) + float64(childRows(x.Child))*w.AggPerRow
	case *physical.Sort:
		return pp.nodeCost(x.Child) + float64(childRows(x.Child))*w.SortPerRow
	case *physical.Limit:
		return pp.nodeCost(x.Child)
	}
	return 0
}

func (pp *physicalPlanner) nodeSig(n physical.PhysicalNode) string {
	switch x := n.(type) {
	case *physical.SeqScan:
		return "seqscan(" + x.Alias + ")"
	case *physical.IndexScan:
		return "indexscan(" + x.Alias + "," + x.Index + ")"
	case *physical.Filter:
		return "filter(" + sql.CanonicalExpr(x.Pred) + "," + pp.nodeSig(x.Child) + ")"
	case *physical.Project:
		return "project(" + pp.nodeSig(x.Child) + ")"
	case *physical.HashJoin:
		cond := ""
		if x.Cond != nil {
			cond = sql.CanonicalExpr(x.Cond)
		}
		return "hj(" + pp.nodeSig(x.Left) + "|" + pp.nodeSig(x.Right) + "," + cond + ")"
	case *physical.NLJ:
		cond := ""
		if x.Cond != nil {
			cond = sql.CanonicalExpr(x.Cond)
		}
		return "nlj(" + pp.nodeSig(x.Left) + "," + pp.nodeSig(x.Right) + "," + cond + ")"
	case *physical.HashAggregate:
		return "agg(" + pp.nodeSig(x.Child) + ")"
	case *physical.Sort:
		return "sort(" + pp.nodeSig(x.Child) + ")"
	case *physical.Limit:
		return "limit(" + pp.nodeSig(x.Child) + ")"
	}
	return "?"
}

// ---------- projection pruning ----------

func (pp *physicalPlanner) pruneColumns(root physical.PhysicalNode) {
	req := map[string]map[string]bool{}
	// seed from top project
	if proj, ok := root.(*physical.Project); ok {
		seedFromExprs(proj.Exprs, proj.Child.OutSchema(), req)
	}
	pp.applyPruning(root, req)
}

func seedFromExprs(exprs []sql.Expr, schema rows.Schema, req map[string]map[string]bool) {
	for _, e := range exprs {
		if star, ok := e.(*sql.Star); ok {
			for _, c := range schema.Columns {
				if star.Table == "" || c.Alias == star.Table {
					addReq(req, c.Alias, c.Name)
				}
			}
			continue
		}
		for _, ref := range sql.CollectColumns(e) {
			if ref.Table != "" {
				addReq(req, ref.Table, ref.Name)
			} else if idx, ok := schema.Index(ref.Name); ok {
				addReq(req, schema.Columns[idx].Alias, ref.Name)
			}
		}
	}
}

func addReq(req map[string]map[string]bool, alias, col string) {
	if alias == "" {
		alias = "_"
	}
	if req[alias] == nil {
		req[alias] = map[string]bool{}
	}
	req[alias][col] = true
}

func propagateReq(n physical.PhysicalNode, req map[string]map[string]bool) {
	switch x := n.(type) {
	case *physical.Filter:
		addPredReq(x.Pred, x.Child.OutSchema(), req)
		propagateReq(x.Child, req)
	case *physical.Project:
		childReq := map[string]map[string]bool{}
		seedFromExprs(x.Exprs, x.Child.OutSchema(), childReq)
		// merge parent req into child req
		for a, cols := range req {
			for c := range cols {
				addReq(childReq, a, c)
			}
		}
		propagateReq(x.Child, childReq)
	case *physical.HashJoin:
		childReq := map[string]map[string]bool{}
		for a, cols := range req {
			for c := range cols {
				addReq(childReq, a, c)
			}
		}
		addPredReq(x.Cond, concatSchema(x.Left.OutSchema(), x.Right.OutSchema()), childReq)
		propagateReq(x.Left, childReq)
		propagateReq(x.Right, childReq)
	case *physical.NLJ:
		childReq := map[string]map[string]bool{}
		for a, cols := range req {
			for c := range cols {
				addReq(childReq, a, c)
			}
		}
		if x.Cond != nil {
			addPredReq(x.Cond, concatSchema(x.Left.OutSchema(), x.Right.OutSchema()), childReq)
		}
		propagateReq(x.Left, childReq)
		propagateReq(x.Right, childReq)
	case *physical.HashAggregate:
		childReq := map[string]map[string]bool{}
		for _, g := range x.Group {
			addPredReq(g, x.Child.OutSchema(), childReq)
		}
		for _, a := range x.Aggs {
			addPredReq(a, x.Child.OutSchema(), childReq)
		}
		propagateReq(x.Child, childReq)
	case *physical.Sort:
		childReq := map[string]map[string]bool{}
		for a, cols := range req {
			for c := range cols {
				addReq(childReq, a, c)
			}
		}
		for _, k := range x.Keys {
			addPredReq(k.Expr, x.Child.OutSchema(), childReq)
		}
		propagateReq(x.Child, childReq)
	case *physical.Limit:
		propagateReq(x.Child, req)
	}
}

func addPredReq(pred sql.Expr, schema rows.Schema, req map[string]map[string]bool) {
	if pred == nil {
		return
	}
	for _, ref := range sql.CollectColumns(pred) {
		if ref.Table != "" {
			addReq(req, ref.Table, ref.Name)
		} else if idx, ok := schema.Index(ref.Name); ok {
			addReq(req, schema.Columns[idx].Alias, ref.Name)
		}
	}
}

// mergeReq merges parent requirements into child requirements.
func mergeReq(parent, child map[string]map[string]bool) {
	for a, cols := range parent {
		for c := range cols {
			addReq(child, a, c)
		}
	}
}

func (pp *physicalPlanner) applyPruning(n physical.PhysicalNode, req map[string]map[string]bool) {
	switch x := n.(type) {
	case *physical.SeqScan:
		pp.applyScanPruning(x, req)
	case *physical.IndexScan:
		pp.applyIndexPruning(x, req)
	case *physical.Filter:
		childReq := map[string]map[string]bool{}
		mergeReq(req, childReq)
		addPredReq(x.Pred, x.Child.OutSchema(), childReq)
		pp.applyPruning(x.Child, childReq)
	case *physical.Project:
		childReq := map[string]map[string]bool{}
		mergeReq(req, childReq)
		seedFromExprs(x.Exprs, x.Child.OutSchema(), childReq)
		pp.applyPruning(x.Child, childReq)
	case *physical.HashJoin:
		childReq := map[string]map[string]bool{}
		mergeReq(req, childReq)
		addPredReq(x.Cond, concatSchema(x.Left.OutSchema(), x.Right.OutSchema()), childReq)
		pp.applyPruning(x.Left, childReq)
		pp.applyPruning(x.Right, childReq)
	case *physical.NLJ:
		childReq := map[string]map[string]bool{}
		mergeReq(req, childReq)
		if x.Cond != nil {
			addPredReq(x.Cond, concatSchema(x.Left.OutSchema(), x.Right.OutSchema()), childReq)
		}
		pp.applyPruning(x.Left, childReq)
		pp.applyPruning(x.Right, childReq)
	case *physical.HashAggregate:
		childReq := map[string]map[string]bool{}
		mergeReq(req, childReq)
		for _, g := range x.Group {
			addPredReq(g, x.Child.OutSchema(), childReq)
		}
		for _, a := range x.Aggs {
			addPredReq(a, x.Child.OutSchema(), childReq)
		}
		pp.applyPruning(x.Child, childReq)
	case *physical.Sort:
		childReq := map[string]map[string]bool{}
		mergeReq(req, childReq)
		for _, k := range x.Keys {
			addPredReq(k.Expr, x.Child.OutSchema(), childReq)
		}
		pp.applyPruning(x.Child, childReq)
	case *physical.Limit:
		pp.applyPruning(x.Child, req)
	}
}

func (pp *physicalPlanner) applyScanPruning(scan *physical.SeqScan, req map[string]map[string]bool) {
	cols := req[scan.Alias]
	t, err := pp.tableFor(scan.Table)
	if err != nil {
		return
	}
	var idxs []int
	for i, c := range t.Schema.Columns {
		if cols[c.Name] {
			idxs = append(idxs, i)
		}
	}
	sort.Ints(idxs)
	scan.Cols = idxs
	newCols := make([]rows.Column, 0, len(idxs))
	for _, i := range idxs {
		newCols = append(newCols, rows.Column{Name: t.Schema.Columns[i].Name, Type: t.Schema.Columns[i].Type, Alias: scan.Alias})
	}
	scan.Schema = rows.Schema{Columns: newCols}
}

func (pp *physicalPlanner) applyIndexPruning(scan *physical.IndexScan, req map[string]map[string]bool) {
	cols := req[scan.Alias]
	t, err := pp.tableFor(scan.Table)
	if err != nil {
		return
	}
	var idxs []int
	for i, c := range t.Schema.Columns {
		if cols[c.Name] {
			idxs = append(idxs, i)
		}
	}
	sort.Ints(idxs)
	scan.Cols = idxs
	newCols := make([]rows.Column, 0, len(idxs))
	for _, i := range idxs {
		newCols = append(newCols, rows.Column{Name: t.Schema.Columns[i].Name, Type: t.Schema.Columns[i].Type, Alias: scan.Alias})
	}
	scan.Schema = rows.Schema{Columns: newCols}
}
