// Package exec executes physical plans with cancellation support, and
// provides a naive cartesian-product executor used as the semantic
// equivalence oracle. Both paths share the same expression evaluator, so
// NULL handling, type coercion and boundary behavior are identical by
// construction.
package exec

import (
	"context"
	"fmt"
	"sort"

	"github.com/guoyyit/query-optimizer-go/catalog"
	"github.com/guoyyit/query-optimizer-go/expr"
	"github.com/guoyyit/query-optimizer-go/plan"
	"github.com/guoyyit/query-optimizer-go/value"
)

// Result is a fully materialized query result.
type Result struct {
	Schema []expr.ColRef
	Rows   [][]value.Value
}

// checkCtx polls cancellation every batch of rows.
const cancelCheckInterval = 256

// Run executes a physical plan to completion. On cancellation or timeout
// it returns an error and no partial rows.
func Run(ctx context.Context, cat *catalog.Catalog, p plan.Physical) (*Result, error) {
	rows, err := execNode(ctx, cat, p)
	if err != nil {
		return nil, err
	}
	return &Result{Schema: p.Schema(), Rows: rows}, nil
}

func execNode(ctx context.Context, cat *catalog.Catalog, p plan.Physical) ([][]value.Value, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	switch n := p.(type) {
	case *plan.SeqScan:
		t := cat.Table(n.Table)
		if t == nil {
			return nil, fmt.Errorf("exec: unknown table %q", n.Table)
		}
		return projectScan(t, n.Columns), nil
	case *plan.IndexScan:
		t := cat.Table(n.Table)
		if t == nil {
			return nil, fmt.Errorf("exec: unknown table %q", n.Table)
		}
		idx := t.Index(n.Key.Column)
		if idx == nil {
			return nil, fmt.Errorf("exec: no index on %s.%s", n.Table, n.Key.Column)
		}
		colPos := t.ColumnPos(n.Key.Column)
		rows := t.Rows()
		var out [][]value.Value
		for _, pos := range idx.M[n.Value.HashKey()] {
			// Verify equality: hash keys may collide.
			if !value.Equal(rows[pos][colPos], n.Value) {
				continue
			}
			out = append(out, projectRow(t, rows[pos], n.Columns))
		}
		return out, nil
	case *plan.FilterOp:
		in, err := execNode(ctx, cat, n.Input)
		if err != nil {
			return nil, err
		}
		schema := n.Input.Schema()
		var out [][]value.Value
		for i, row := range in {
			if i%cancelCheckInterval == 0 {
				if err := ctx.Err(); err != nil {
					return nil, err
				}
			}
			if expr.Truthy(n.Pred.Eval(schema, row)) {
				out = append(out, row)
			}
		}
		return out, nil
	case *plan.ProjectOp:
		in, err := execNode(ctx, cat, n.Input)
		if err != nil {
			return nil, err
		}
		pos := positions(n.Input.Schema(), n.Cols)
		out := make([][]value.Value, len(in))
		for i, row := range in {
			r := make([]value.Value, len(pos))
			for j, p := range pos {
				r[j] = row[p]
			}
			out[i] = r
		}
		return out, nil
	case *plan.HashJoin:
		leftRows, err := execNode(ctx, cat, n.Left)
		if err != nil {
			return nil, err
		}
		rightRows, err := execNode(ctx, cat, n.Right)
		if err != nil {
			return nil, err
		}
		lSchema, rSchema := n.Left.Schema(), n.Right.Schema()
		rPos := positions(rSchema, n.RightKeys)
		lPos := positions(lSchema, n.LeftKeys)
		// Build on the right input.
		table := map[uint64][]int{}
		for i, row := range rightRows {
			if i%cancelCheckInterval == 0 {
				if err := ctx.Err(); err != nil {
					return nil, err
				}
			}
			h := hashRowKey(row, rPos)
			table[h] = append(table[h], i)
		}
		joinedSchema := n.Schema()
		var out [][]value.Value
		for i, lrow := range leftRows {
			if i%cancelCheckInterval == 0 {
				if err := ctx.Err(); err != nil {
					return nil, err
				}
			}
			h := hashRowKey(lrow, lPos)
			for _, ri := range table[h] {
				rrow := rightRows[ri]
				if !keysEqual(lrow, lPos, rrow, rPos) {
					continue // hash collision
				}
				joined := append(append([]value.Value{}, lrow...), rrow...)
				if n.Residual != nil && !expr.Truthy(n.Residual.Eval(joinedSchema, joined)) {
					continue
				}
				out = append(out, joined)
			}
		}
		return out, nil
	case *plan.NestedLoopJoin:
		leftRows, err := execNode(ctx, cat, n.Left)
		if err != nil {
			return nil, err
		}
		rightRows, err := execNode(ctx, cat, n.Right)
		if err != nil {
			return nil, err
		}
		joinedSchema := n.Schema()
		var out [][]value.Value
		for i, lrow := range leftRows {
			if i%cancelCheckInterval == 0 {
				if err := ctx.Err(); err != nil {
					return nil, err
				}
			}
			for _, rrow := range rightRows {
				joined := append(append([]value.Value{}, lrow...), rrow...)
				if n.Cond != nil && !expr.Truthy(n.Cond.Eval(joinedSchema, joined)) {
					continue
				}
				out = append(out, joined)
			}
		}
		return out, nil
	}
	return nil, fmt.Errorf("exec: unknown physical node %T", p)
}

// projectScan reads a table and projects the requested columns.
func projectScan(t *catalog.Table, cols []expr.ColRef) [][]value.Value {
	rows := t.Rows()
	out := make([][]value.Value, len(rows))
	for i, row := range rows {
		out[i] = projectRow(t, row, cols)
	}
	return out
}

func projectRow(t *catalog.Table, row []value.Value, cols []expr.ColRef) []value.Value {
	out := make([]value.Value, len(cols))
	for j, c := range cols {
		pos := t.ColumnPos(c.Column)
		if pos >= 0 {
			out[j] = row[pos]
		}
	}
	return out
}

func positions(schema []expr.ColRef, cols []expr.ColRef) []int {
	pos := make([]int, len(cols))
	for i, c := range cols {
		pos[i] = -1
		for j, s := range schema {
			if s == c {
				pos[i] = j
				break
			}
		}
	}
	return pos
}

func hashRowKey(row []value.Value, pos []int) uint64 {
	h := uint64(1469598103934665603)
	for _, p := range pos {
		k := row[p].HashKey()
		h ^= k
		h *= 1099511628211
	}
	return h
}

func keysEqual(lrow []value.Value, lPos []int, rrow []value.Value, rPos []int) bool {
	for i := range lPos {
		if !value.Equal(lrow[lPos[i]], rrow[rPos[i]]) {
			return false
		}
	}
	return true
}

// RunNaive executes a logical plan literally: joins are nested-loop
// cartesian products filtered by their conditions, filters stay where the
// parser put them, no indexes, no reordering. It is the equivalence oracle.
func RunNaive(ctx context.Context, cat *catalog.Catalog, root plan.Logical) (*Result, error) {
	rows, err := execLogical(ctx, cat, root)
	if err != nil {
		return nil, err
	}
	return &Result{Schema: root.Schema(), Rows: rows}, nil
}

func execLogical(ctx context.Context, cat *catalog.Catalog, node plan.Logical) ([][]value.Value, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	switch n := node.(type) {
	case *plan.Scan:
		t := cat.Table(n.Table)
		if t == nil {
			return nil, fmt.Errorf("exec: unknown table %q", n.Table)
		}
		return projectScan(t, n.Columns), nil
	case *plan.Filter:
		in, err := execLogical(ctx, cat, n.Input)
		if err != nil {
			return nil, err
		}
		schema := n.Input.Schema()
		var out [][]value.Value
		for i, row := range in {
			if i%cancelCheckInterval == 0 {
				if err := ctx.Err(); err != nil {
					return nil, err
				}
			}
			if expr.Truthy(n.Pred.Eval(schema, row)) {
				out = append(out, row)
			}
		}
		return out, nil
	case *plan.Project:
		in, err := execLogical(ctx, cat, n.Input)
		if err != nil {
			return nil, err
		}
		pos := positions(n.Input.Schema(), n.Cols)
		out := make([][]value.Value, len(in))
		for i, row := range in {
			r := make([]value.Value, len(pos))
			for j, p := range pos {
				r[j] = row[p]
			}
			out[i] = r
		}
		return out, nil
	case *plan.Join:
		leftRows, err := execLogical(ctx, cat, n.Left)
		if err != nil {
			return nil, err
		}
		rightRows, err := execLogical(ctx, cat, n.Right)
		if err != nil {
			return nil, err
		}
		joinedSchema := n.Schema()
		var out [][]value.Value
		for i, lrow := range leftRows {
			if i%cancelCheckInterval == 0 {
				if err := ctx.Err(); err != nil {
					return nil, err
				}
			}
			for _, rrow := range rightRows {
				joined := append(append([]value.Value{}, lrow...), rrow...)
				if n.Cond != nil && !expr.Truthy(n.Cond.Eval(joinedSchema, joined)) {
					continue
				}
				out = append(out, joined)
			}
		}
		return out, nil
	}
	return nil, fmt.Errorf("exec: unknown logical node %T", node)
}

// SortRows canonically orders result rows (by type rank, then value, NULL
// first) so that optimized and naive results are comparable row-by-row.
func SortRows(rows [][]value.Value) {
	sort.Slice(rows, func(i, j int) bool { return CompareRows(rows[i], rows[j]) < 0 })
}

// CompareRows lexicographically compares two rows.
func CompareRows(a, b []value.Value) int {
	for i := 0; i < len(a) && i < len(b); i++ {
		if c := compareValues(a[i], b[i]); c != 0 {
			return c
		}
	}
	switch {
	case len(a) < len(b):
		return -1
	case len(a) > len(b):
		return 1
	}
	return 0
}

// compareValues is a total order over values: NULL < INT/FLOAT (numeric
// order) < STRING < BOOL.
func compareValues(a, b value.Value) int {
	ra, rb := typeRank(a.T), typeRank(b.T)
	if ra != rb {
		return ra - rb
	}
	if cmp, ok := value.Compare(a, b); ok {
		return cmp
	}
	return 0
}

func typeRank(t value.Type) int {
	switch t {
	case value.Null:
		return 0
	case value.Int, value.Float:
		return 1
	case value.String:
		return 2
	case value.Bool:
		return 3
	}
	return 4
}
