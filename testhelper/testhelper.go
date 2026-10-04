// Package testhelper provides shared in-process table builders for tests.
package testhelper

import (
	"context"
	"sort"

	"github.com/guoyyit/query-optimizer-go/catalog"
	"github.com/guoyyit/query-optimizer-go/exec"
	"github.com/guoyyit/query-optimizer-go/plan"
	"github.com/guoyyit/query-optimizer-go/rows"
	"github.com/guoyyit/query-optimizer-go/sql"
	"github.com/guoyyit/query-optimizer-go/stats"
)

// Col is a shorthand for building columns.
func Col(name string, t rows.Type) rows.Column {
	return rows.Column{Name: name, Type: t}
}

// Cat builds a catalog from table definitions.
type TableDef struct {
	Name    string
	Columns []rows.Column
	Rows    []rows.Row
	Stats   bool
}

// BuildCatalog creates a catalog with the given tables and optionally stats.
func BuildCatalog(defs []TableDef) *catalog.Catalog {
	cat := catalog.New()
	for _, d := range defs {
		t := catalog.NewTable(d.Name, rows.Schema{Columns: d.Columns}, d.Rows)
		cat.AddTable(t)
		if d.Stats {
			_ = cat.SetStats(d.Name, stats.Collect(d.Name, rows.Schema{Columns: d.Columns}, d.Rows))
		}
	}
	return cat
}

// IntRow builds a row of int values.
func IntRow(vals ...int64) rows.Row {
	v := make([]rows.Value, len(vals))
	for i, x := range vals {
		v[i] = rows.IntValue(x)
	}
	return rows.Row{Vals: v}
}

// MixedRow builds a row from values.
func MixedRow(vals ...rows.Value) rows.Row {
	return rows.Row{Vals: vals}
}

// OptExecute plans and executes a query, returning rows.
func OptExecute(ctx context.Context, p *plan.Planner, cat *catalog.Catalog, q string) ([]rows.Row, *plan.Result, error) {
	res, err := p.Plan(ctx, q)
	if err != nil {
		return nil, nil, err
	}
	out, err := exec.Execute(ctx, res.Physical, cat)
	return out, res, err
}

// NaiveExecute parses and runs the naive reference implementation.
func NaiveExecute(ctx context.Context, cat *catalog.Catalog, q string) ([]rows.Row, rows.Schema, error) {
	sel, err := sql.Parse(q)
	if err != nil {
		return nil, rows.Schema{}, err
	}
	return exec.NaiveExecute(ctx, sel, cat)
}

// RowsEqual compares two row sets as multisets (order-independent).
func RowsEqual(a, b []rows.Row) bool {
	if len(a) != len(b) {
		return false
	}
	used := make([]bool, len(b))
	for _, ra := range a {
		found := false
		for j, rb := range b {
			if !used[j] && ValsEqual(ra.Vals, rb.Vals) {
				used[j] = true
				found = true
				break
			}
		}
		if !found {
			return false
		}
	}
	return true
}

// ValsEqual compares two value slices element by element. NULL values compare
// as equal here because we are comparing result sets, not evaluating SQL
// three-valued logic.
func ValsEqual(a, b []rows.Value) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		av, bv := a[i], b[i]
		if av.Null && bv.Null {
			continue
		}
		if !rows.Equal(av, bv) {
			return false
		}
	}
	return true
}

// SortedRows returns a sorted copy of rows for deterministic comparison.
func SortedRows(rs []rows.Row) []rows.Row {
	out := make([]rows.Row, len(rs))
	copy(out, rs)
	sort.Slice(out, func(i, j int) bool {
		for k := 0; k < len(out[i].Vals) && k < len(out[j].Vals); k++ {
			cmp := rows.Compare(out[i].Vals[k], out[j].Vals[k])
			if cmp != 0 {
				return cmp < 0
			}
		}
		return len(out[i].Vals) < len(out[j].Vals)
	})
	return out
}
