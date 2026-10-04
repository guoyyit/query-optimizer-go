// Package exec executes physical plans with a volcano-style iterator model.
package exec

import (
	"context"
	"fmt"
	"sort"
	"strings"

	"github.com/guoyyit/query-optimizer-go/catalog"
	"github.com/guoyyit/query-optimizer-go/plan/physical"
	"github.com/guoyyit/query-optimizer-go/rows"
	"github.com/guoyyit/query-optimizer-go/sql"
)

// Operator is a physical operator iterator.
type Operator interface {
	Open(ctx context.Context) error
	Next() (rows.Row, error) // nil row signals EOF
	Close() error
	OutSchema() rows.Schema
}

// NewOperator builds the operator tree for a physical plan.
func NewOperator(p physical.PhysicalNode, cat *catalog.Catalog) (Operator, error) {
	return buildOperator(p, cat)
}

func buildOperator(p physical.PhysicalNode, cat *catalog.Catalog) (Operator, error) {
	switch x := p.(type) {
	case *physical.SeqScan:
		return &seqScanOp{node: x, cat: cat}, nil
	case *physical.IndexScan:
		return &indexScanOp{node: x, cat: cat}, nil
	case *physical.Filter:
		child, err := buildOperator(x.Child, cat)
		if err != nil {
			return nil, err
		}
		return &filterOp{node: x, child: child}, nil
	case *physical.Project:
		child, err := buildOperator(x.Child, cat)
		if err != nil {
			return nil, err
		}
		return &projectOp{node: x, child: child}, nil
	case *physical.HashJoin:
		left, err := buildOperator(x.Left, cat)
		if err != nil {
			return nil, err
		}
		right, err := buildOperator(x.Right, cat)
		if err != nil {
			return nil, err
		}
		return &hashJoinOp{node: x, left: left, right: right}, nil
	case *physical.NLJ:
		left, err := buildOperator(x.Left, cat)
		if err != nil {
			return nil, err
		}
		right, err := buildOperator(x.Right, cat)
		if err != nil {
			return nil, err
		}
		return &nljOp{node: x, left: left, right: right}, nil
	case *physical.HashAggregate:
		child, err := buildOperator(x.Child, cat)
		if err != nil {
			return nil, err
		}
		return &hashAggOp{node: x, child: child}, nil
	case *physical.Sort:
		child, err := buildOperator(x.Child, cat)
		if err != nil {
			return nil, err
		}
		return &sortOp{node: x, child: child}, nil
	case *physical.Limit:
		child, err := buildOperator(x.Child, cat)
		if err != nil {
			return nil, err
		}
		return &limitOp{node: x, child: child}, nil
	}
	return nil, fmt.Errorf("exec: unknown physical node %T", p)
}

// Execute runs a physical plan to completion and collects all rows.
func Execute(ctx context.Context, p physical.PhysicalNode, cat *catalog.Catalog) ([]rows.Row, error) {
	op, err := NewOperator(p, cat)
	if err != nil {
		return nil, err
	}
	if err := op.Open(ctx); err != nil {
		return nil, err
	}
	defer op.Close()
	var out []rows.Row
	for {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		r, err := op.Next()
		if err != nil {
			return nil, err
		}
		if r.Vals == nil {
			break
		}
		out = append(out, r)
	}
	return out, nil
}

func checkCtx(ctx context.Context, n int) error {
	if n%1000 == 0 {
		return ctx.Err()
	}
	return nil
}

// ---------- sequential scan ----------

type seqScanOp struct {
	node *physical.SeqScan
	cat  *catalog.Catalog
	rows []rows.Row
	pos  int
}

func (o *seqScanOp) Open(ctx context.Context) error {
	t, ok := o.cat.Get(o.node.Table)
	if !ok {
		return fmt.Errorf("exec: unknown table %q", o.node.Table)
	}
	o.rows = t.Rows
	o.pos = 0
	return nil
}

func (o *seqScanOp) Next() (rows.Row, error) {
	for o.pos < len(o.rows) {
		r := o.rows[o.pos]
		o.pos++
		out := rows.Row{Vals: o.project(r.Vals)}
		return out, nil
	}
	return rows.Row{}, nil
}

func (o *seqScanOp) project(vals []rows.Value) []rows.Value {
	if o.node.Cols == nil {
		return vals
	}
	out := make([]rows.Value, len(o.node.Cols))
	for i, c := range o.node.Cols {
		if c < len(vals) {
			out[i] = vals[c]
		} else {
			out[i] = rows.NullValue()
		}
	}
	return out
}

func (o *seqScanOp) Close() error         { return nil }
func (o *seqScanOp) OutSchema() rows.Schema { return o.node.Schema }

// ---------- index scan ----------

type indexScanOp struct {
	node *physical.IndexScan
	cat  *catalog.Catalog
	rows []rows.Row
	pos  int
}

func (o *indexScanOp) Open(ctx context.Context) error {
	t, ok := o.cat.Get(o.node.Table)
	if !ok {
		return fmt.Errorf("exec: unknown table %q", o.node.Table)
	}
	idx := o.cat.EnsureIndex(o.node.Table, o.node.Index)
	if idx == nil {
		return fmt.Errorf("exec: index %s not found on table %s", o.node.Index, o.node.Table)
	}
	lit, ok := o.node.KeyExpr.(*sql.Literal)
	if !ok {
		return fmt.Errorf("exec: index key must be a literal")
	}
	key := literalValue(lit).String()
	rowIdxs := idx.Keys[key]
	o.rows = make([]rows.Row, 0, len(rowIdxs))
	for _, ri := range rowIdxs {
		if ri < len(t.Rows) {
			o.rows = append(o.rows, t.Rows[ri])
		}
	}
	o.pos = 0
	return nil
}

func (o *indexScanOp) Next() (rows.Row, error) {
	if o.pos >= len(o.rows) {
		return rows.Row{}, nil
	}
	r := o.rows[o.pos]
	o.pos++
	out := rows.Row{Vals: o.project(r.Vals)}
	return out, nil
}

func (o *indexScanOp) project(vals []rows.Value) []rows.Value {
	if o.node.Cols == nil {
		return vals
	}
	out := make([]rows.Value, len(o.node.Cols))
	for i, c := range o.node.Cols {
		if c < len(vals) {
			out[i] = vals[c]
		} else {
			out[i] = rows.NullValue()
		}
	}
	return out
}

func (o *indexScanOp) Close() error         { return nil }
func (o *indexScanOp) OutSchema() rows.Schema { return o.node.Schema }

// ---------- filter ----------

type filterOp struct {
	node   *physical.Filter
	child  Operator
	aggOp  *hashAggOp
}

func (o *filterOp) Open(ctx context.Context) error {
	if err := o.child.Open(ctx); err != nil {
		return err
	}
	if a, ok := o.child.(*hashAggOp); ok {
		o.aggOp = a
	}
	return nil
}

func (o *filterOp) Next() (rows.Row, error) {
	for {
		r, err := o.child.Next()
		if err != nil {
			return rows.Row{}, err
		}
		if r.Vals == nil {
			return rows.Row{}, nil
		}
		ctx := &EvalContext{Schema: o.child.OutSchema(), Row: r}
		if o.aggOp != nil {
			ctx.AggResolver = o.aggOp.resolveAgg
		}
		ok, err := EvalBool(o.node.Pred, ctx)
		if err != nil {
			return rows.Row{}, err
		}
		if ok {
			return r, nil
		}
	}
}

func (o *filterOp) Close() error         { return o.child.Close() }
func (o *filterOp) OutSchema() rows.Schema { return o.child.OutSchema() }

// ---------- project ----------

type projectOp struct {
	node     *physical.Project
	child    Operator
	aggOp    *hashAggOp
}

func (o *projectOp) Open(ctx context.Context) error {
	if err := o.child.Open(ctx); err != nil {
		return err
	}
	o.aggOp = findAgg(o.child)
	return nil
}

// findAgg searches for a hashAggOp below (possibly through filters).
func findAgg(op Operator) *hashAggOp {
	switch x := op.(type) {
	case *hashAggOp:
		return x
	case *filterOp:
		return findAgg(x.child)
	}
	return nil
}

func (o *projectOp) Next() (rows.Row, error) {
	r, err := o.child.Next()
	if err != nil {
		return rows.Row{}, err
	}
	if r.Vals == nil {
		return rows.Row{}, nil
	}
	out := make([]rows.Value, len(o.node.Exprs))
	ctx := &EvalContext{Schema: o.child.OutSchema(), Row: r}
	if o.aggOp != nil {
		ctx.AggResolver = o.aggOp.resolveAgg
	}
	for i, e := range o.node.Exprs {
		v, err := Eval(e, ctx)
		if err != nil {
			return rows.Row{}, err
		}
		out[i] = v
	}
	return rows.Row{Vals: out}, nil
}

func (o *projectOp) Close() error         { return o.child.Close() }
func (o *projectOp) OutSchema() rows.Schema { return o.node.Schema }

// ---------- hash join ----------

type hashJoinOp struct {
	node      *physical.HashJoin
	left      Operator
	right     Operator
	built     bool
	probeRows []rows.Row
	probePos  int
	matchPos  int
	matches   []rows.Row
	buildCols []int
	probeCols []int
	hashTable map[string][]rows.Row
	residual  sql.Expr
}

func (o *hashJoinOp) Open(ctx context.Context) error {
	if err := o.left.Open(ctx); err != nil {
		return err
	}
	if err := o.right.Open(ctx); err != nil {
		return err
	}
	return nil
}

// equiKeys extracts column index pairs from an equi-join predicate.
func equiKeys(cond sql.Expr, leftSchema, rightSchema rows.Schema) (leftCols, rightCols []int, residual sql.Expr, err error) {
	var residualConds []sql.Expr
	for _, c := range sql.SplitConjuncts(cond) {
		b, ok := c.(*sql.BinOp)
		if !ok || b.Op != "=" {
			residualConds = append(residualConds, c)
			continue
		}
		lref, lok := b.Left.(*sql.ColumnRef)
		rref, rok := b.Right.(*sql.ColumnRef)
		if !lok || !rok {
			residualConds = append(residualConds, c)
			continue
		}
		// determine which side each ref belongs to
		li, lerr := resolveColumnIndex(leftSchema, lref)
		ri, rerr := resolveColumnIndex(rightSchema, rref)
		if lerr == nil && rerr == nil {
			// ambiguous: both sides have the column; use alias to disambiguate
			if lref.Table != "" && rref.Table != "" {
				leftCols = append(leftCols, li)
				rightCols = append(rightCols, ri)
				continue
			}
			residualConds = append(residualConds, c)
			continue
		}
		if lerr == nil {
			// left ref is on left; right ref must be on right
			ri2, rerr2 := resolveColumnIndex(rightSchema, rref)
			if rerr2 != nil {
				return nil, nil, nil, fmt.Errorf("join column %s not found on right side", rref.Name)
			}
			leftCols = append(leftCols, li)
			rightCols = append(rightCols, ri2)
			continue
		}
		// lref on right, rref on left
		ri2, rerr2 := resolveColumnIndex(rightSchema, lref)
		li2, lerr2 := resolveColumnIndex(leftSchema, rref)
		if rerr2 != nil || lerr2 != nil {
			return nil, nil, nil, fmt.Errorf("join columns not found: %s / %s", lref.Name, rref.Name)
		}
		leftCols = append(leftCols, li2)
		rightCols = append(rightCols, ri2)
	}
	if len(residualConds) > 0 {
		residual = sql.Conjoin(residualConds)
	}
	return leftCols, rightCols, residual, nil
}

func joinKey(vals []rows.Value, cols []int) (string, bool) {
	var b strings.Builder
	for _, c := range cols {
		if c >= len(vals) {
			return "", false
		}
		v := vals[c]
		if v.Null {
			return "", false
		}
		b.WriteString(v.String())
		b.WriteByte(0)
	}
	return b.String(), true
}

func (o *hashJoinOp) Next() (rows.Row, error) {
	if !o.built {
		if err := o.build(); err != nil {
			return rows.Row{}, err
		}
		o.built = true
	}
	for {
		// emit remaining matches for the current probe row
		for o.matchPos < len(o.matches) {
			m := o.matches[o.matchPos]
			o.matchPos++
			joined := o.combine(o.probeRows[o.probePos-1], m)
			if o.residual != nil {
				ctx := &EvalContext{Schema: o.OutSchema(), Row: joined}
				ok, err := EvalBool(o.residual, ctx)
				if err != nil {
					return rows.Row{}, err
				}
				if !ok {
					continue
				}
			}
			return joined, nil
		}
		if o.probePos >= len(o.probeRows) {
			return rows.Row{}, nil
		}
		probe := o.probeRows[o.probePos]
		o.probePos++
		o.matchPos = 0
		key, ok := joinKey(probe.Vals, o.probeCols)
		if !ok {
			if o.node.JoinType == sql.JoinLeft {
				return o.nullPadded(probe), nil
			}
			o.matches = nil
			continue
		}
		o.matches = o.hashTable[key]
		if len(o.matches) == 0 {
			if o.node.JoinType == sql.JoinLeft {
				return o.nullPadded(probe), nil
			}
			continue
		}
	}
}

func (o *hashJoinOp) build() error {
	buildOp := o.right
	probeOp := o.left
	if o.node.BuildLeft {
		buildOp = o.left
		probeOp = o.right
	}
	buildSchema := buildOp.OutSchema()
	probeSchema := probeOp.OutSchema()
	leftSchema := o.left.OutSchema()
	rightSchema := o.right.OutSchema()
	leftCols, rightCols, residual, err := equiKeys(o.node.Cond, leftSchema, rightSchema)
	if err != nil {
		return err
	}
	o.residual = residual
	if o.node.BuildLeft {
		o.buildCols = leftCols
		o.probeCols = rightCols
	} else {
		o.buildCols = rightCols
		o.probeCols = leftCols
	}
	o.hashTable = map[string][]rows.Row{}
	for {
		r, err := buildOp.Next()
		if err != nil {
			return err
		}
		if r.Vals == nil {
			break
		}
		key, ok := joinKey(r.Vals, o.buildCols)
		if !ok {
			continue // NULL never matches
		}
		o.hashTable[key] = append(o.hashTable[key], r)
	}
	// collect probe side rows
	for {
		r, err := probeOp.Next()
		if err != nil {
			return err
		}
		if r.Vals == nil {
			break
		}
		o.probeRows = append(o.probeRows, r)
	}
	_ = buildSchema
	_ = probeSchema
	return nil
}

func (o *hashJoinOp) combine(probe, build rows.Row) rows.Row {
	if o.node.BuildLeft {
		// build is left, probe is right
		return rows.Row{Vals: append(append([]rows.Value{}, build.Vals...), probe.Vals...)}
	}
	// build is right, probe is left
	return rows.Row{Vals: append(append([]rows.Value{}, probe.Vals...), build.Vals...)}
}

func (o *hashJoinOp) nullPadded(probe rows.Row) rows.Row {
	nulls := make([]rows.Value, len(o.right.OutSchema().Columns))
	for i := range nulls {
		nulls[i] = rows.NullValue()
	}
	return rows.Row{Vals: append(append([]rows.Value{}, probe.Vals...), nulls...)}
}

func (o *hashJoinOp) Close() error {
	if err := o.left.Close(); err != nil {
		return err
	}
	return o.right.Close()
}

func (o *hashJoinOp) OutSchema() rows.Schema {
	return concatSchema(o.left.OutSchema(), o.right.OutSchema())
}

// ---------- nested loop join ----------

type nljOp struct {
	node  *physical.NLJ
	left  Operator
	right Operator
	rightRows []rows.Row
	rightPos  int
	leftRow   rows.Row
	started   bool
}

func (o *nljOp) Open(ctx context.Context) error {
	if err := o.left.Open(ctx); err != nil {
		return err
	}
	if err := o.right.Open(ctx); err != nil {
		return err
	}
	for {
		r, err := o.right.Next()
		if err != nil {
			return err
		}
		if r.Vals == nil {
			break
		}
		o.rightRows = append(o.rightRows, r)
	}
	return nil
}

func (o *nljOp) Next() (rows.Row, error) {
	for {
		if !o.started {
			r, err := o.left.Next()
			if err != nil {
				return rows.Row{}, err
			}
			if r.Vals == nil {
				return rows.Row{}, nil
			}
			o.leftRow = r
			o.rightPos = 0
			o.started = true
		}
		if o.rightPos >= len(o.rightRows) {
			o.started = false
			continue
		}
		right := o.rightRows[o.rightPos]
		o.rightPos++
		joined := rows.Row{Vals: append(append([]rows.Value{}, o.leftRow.Vals...), right.Vals...)}
		if o.node.Cond != nil {
			ctx := &EvalContext{Schema: o.OutSchema(), Row: joined}
			ok, err := EvalBool(o.node.Cond, ctx)
			if err != nil {
				return rows.Row{}, err
			}
			if !ok {
				continue
			}
		}
		return joined, nil
	}
}

func (o *nljOp) Close() error {
	if err := o.left.Close(); err != nil {
		return err
	}
	return o.right.Close()
}

func (o *nljOp) OutSchema() rows.Schema {
	return concatSchema(o.left.OutSchema(), o.right.OutSchema())
}

// ---------- hash aggregate ----------

type aggAcc struct {
	count   int64
	sum     float64
	sumInt  int64
	hasInt  bool
	min     rows.Value
	max     rows.Value
	distinct map[string]bool
}

type hashAggOp struct {
	node    *physical.HashAggregate
	child   Operator
	groups  map[string]*groupRow
	keys    []string
	pos     int
}

type groupRow struct {
	key    string
	values []rows.Value
	aggs   []*aggAcc
}

func (o *hashAggOp) Open(ctx context.Context) error {
	if err := o.child.Open(ctx); err != nil {
		return err
	}
	o.groups = map[string]*groupRow{}
	for {
		if err := checkCtx(ctx, o.pos); err != nil {
			return err
		}
		r, err := o.child.Next()
		if err != nil {
			return err
		}
		if r.Vals == nil {
			break
		}
		if err := o.accumulate(r); err != nil {
			return err
		}
	}
	return nil
}

func (o *hashAggOp) accumulate(r rows.Row) error {
	ctx := &EvalContext{Schema: o.child.OutSchema(), Row: r}
	var keyParts []string
	var groupVals []rows.Value
	for _, g := range o.node.Group {
		v, err := Eval(g, ctx)
		if err != nil {
			return err
		}
		groupVals = append(groupVals, v)
		keyParts = append(keyParts, v.String())
	}
	key := strings.Join(keyParts, "\x00")
	gr, ok := o.groups[key]
	if !ok {
		gr = &groupRow{key: key, values: groupVals, aggs: make([]*aggAcc, len(o.node.Aggs))}
		for i := range gr.aggs {
			gr.aggs[i] = &aggAcc{distinct: map[string]bool{}}
		}
		o.groups[key] = gr
		o.keys = append(o.keys, key)
	}
	for i, a := range o.node.Aggs {
		agg := a.(*sql.Aggregate)
		acc := gr.aggs[i]
		switch strings.ToUpper(agg.Name) {
		case "COUNT":
			if agg.Expr == nil {
				acc.count++
			} else {
				v, err := Eval(agg.Expr, ctx)
				if err != nil {
					return err
				}
				if v.Null {
					continue
				}
				if agg.Distinct {
					k := v.String()
					if acc.distinct[k] {
						continue
					}
					acc.distinct[k] = true
				}
				acc.count++
			}
		case "SUM":
			v, err := Eval(agg.Expr, ctx)
			if err != nil {
				return err
			}
			if v.Null {
				continue
			}
			if agg.Distinct {
				k := v.String()
				if acc.distinct[k] {
					continue
				}
				acc.distinct[k] = true
			}
			if v.Type == rows.TypeInt {
				acc.sumInt += v.Int
				acc.hasInt = true
			} else {
				f, _ := v.AsFloat()
				acc.sum += f
			}
			acc.count++
		case "AVG":
			v, err := Eval(agg.Expr, ctx)
			if err != nil {
				return err
			}
			if v.Null {
				continue
			}
			if agg.Distinct {
				k := v.String()
				if acc.distinct[k] {
					continue
				}
				acc.distinct[k] = true
			}
			f, _ := v.AsFloat()
			acc.sum += f
			acc.count++
		case "MIN":
			v, err := Eval(agg.Expr, ctx)
			if err != nil {
				return err
			}
			if v.Null {
				continue
			}
			if acc.count == 0 || rows.Compare(v, acc.min) < 0 {
				acc.min = v
			}
			acc.count++
		case "MAX":
			v, err := Eval(agg.Expr, ctx)
			if err != nil {
				return err
			}
			if v.Null {
				continue
			}
			if acc.count == 0 || rows.Compare(v, acc.max) > 0 {
				acc.max = v
			}
			acc.count++
		}
	}
	return nil
}

func (o *hashAggOp) Next() (rows.Row, error) {
	if o.pos >= len(o.keys) {
		return rows.Row{}, nil
	}
	gr := o.groups[o.keys[o.pos]]
	o.pos++
	out := make([]rows.Value, 0, len(gr.values)+len(o.node.Aggs))
	out = append(out, gr.values...)
	for i, a := range o.node.Aggs {
		out = append(out, o.aggValue(a.(*sql.Aggregate), gr.aggs[i]))
	}
	return rows.Row{Vals: out}, nil
}

func (o *hashAggOp) aggValue(agg *sql.Aggregate, acc *aggAcc) rows.Value {
	switch strings.ToUpper(agg.Name) {
	case "COUNT":
		return rows.IntValue(acc.count)
	case "SUM":
		if acc.count == 0 {
			return rows.NullValue()
		}
		if acc.hasInt {
			return rows.IntValue(acc.sumInt)
		}
		return rows.FloatValue(acc.sum)
	case "AVG":
		if acc.count == 0 {
			return rows.NullValue()
		}
		return rows.FloatValue(acc.sum / float64(acc.count))
	case "MIN":
		if acc.count == 0 {
			return rows.NullValue()
		}
		return acc.min
	case "MAX":
		if acc.count == 0 {
			return rows.NullValue()
		}
		return acc.max
	}
	return rows.NullValue()
}

// resolveAgg resolves an aggregate expression against the group currently
// being emitted. The project operator reads rows sequentially after the
// aggregate's Next has advanced its position, so pos-1 is the current group.
func (o *hashAggOp) resolveAgg(agg *sql.Aggregate) (rows.Value, error) {
	if o.pos == 0 || o.pos > len(o.keys) {
		return rows.NullValue(), fmt.Errorf("aggregate %s resolved outside group context", sql.ExprString(agg))
	}
	gr := o.groups[o.keys[o.pos-1]]
	for i, a := range o.node.Aggs {
		if sql.ExprString(a) == sql.ExprString(agg) {
			return o.aggValue(a.(*sql.Aggregate), gr.aggs[i]), nil
		}
	}
	return rows.NullValue(), fmt.Errorf("aggregate %s not found", sql.ExprString(agg))
}

func (o *hashAggOp) Close() error         { return o.child.Close() }
func (o *hashAggOp) OutSchema() rows.Schema { return o.node.Schema }

// ---------- sort ----------

type sortOp struct {
	node   *physical.Sort
	child  Operator
	rows   []rows.Row
	pos    int
}

func (o *sortOp) Open(ctx context.Context) error {
	if err := o.child.Open(ctx); err != nil {
		return err
	}
	for {
		if err := checkCtx(ctx, o.pos); err != nil {
			return err
		}
		r, err := o.child.Next()
		if err != nil {
			return err
		}
		if r.Vals == nil {
			break
		}
		o.rows = append(o.rows, r)
	}
	sort.SliceStable(o.rows, func(i, j int) bool {
		ri := o.rows[i]
		rj := o.rows[j]
		for _, k := range o.node.Keys {
			ci := &EvalContext{Schema: o.child.OutSchema(), Row: ri}
			cj := &EvalContext{Schema: o.child.OutSchema(), Row: rj}
			vi, err := Eval(k.Expr, ci)
			if err != nil {
				return false
			}
			vj, err := Eval(k.Expr, cj)
			if err != nil {
				return false
			}
			cmp := rows.Compare(vi, vj)
			if cmp == 0 {
				continue
			}
			if k.Desc {
				return cmp > 0
			}
			return cmp < 0
		}
		return false
	})
	return nil
}

func (o *sortOp) Next() (rows.Row, error) {
	if o.pos >= len(o.rows) {
		return rows.Row{}, nil
	}
	r := o.rows[o.pos]
	o.pos++
	return r, nil
}

func (o *sortOp) Close() error         { return o.child.Close() }
func (o *sortOp) OutSchema() rows.Schema { return o.child.OutSchema() }

// ---------- limit ----------

type limitOp struct {
	node    *physical.Limit
	child   Operator
	skipped int64
	emitted int64
}

func (o *limitOp) Open(ctx context.Context) error { return o.child.Open(ctx) }

func (o *limitOp) Next() (rows.Row, error) {
	for {
		r, err := o.child.Next()
		if err != nil {
			return rows.Row{}, err
		}
		if r.Vals == nil {
			return rows.Row{}, nil
		}
		if o.skipped < o.node.Offset {
			o.skipped++
			continue
		}
		if o.node.Limit >= 0 && o.emitted >= o.node.Limit {
			return rows.Row{}, nil
		}
		o.emitted++
		return r, nil
	}
}

func (o *limitOp) Close() error         { return o.child.Close() }
func (o *limitOp) OutSchema() rows.Schema { return o.child.OutSchema() }

// ---------- schema concat ----------

func concatSchema(a, b rows.Schema) rows.Schema {
	cols := make([]rows.Column, 0, len(a.Columns)+len(b.Columns))
	cols = append(cols, a.Columns...)
	cols = append(cols, b.Columns...)
	return rows.Schema{Columns: cols}
}
