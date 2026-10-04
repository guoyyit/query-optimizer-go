// Package stats holds table/column statistics and provides selectivity
// estimation for the cost model.
package stats

import (
	"math"
	"sort"
	"strings"

	"github.com/guoyyit/query-optimizer-go/config"
	"github.com/guoyyit/query-optimizer-go/rows"
	"github.com/guoyyit/query-optimizer-go/sql"
)

// ColumnStats describes statistics for a single column.
type ColumnStats struct {
	Name      string
	NDV       int64 // number of distinct values
	Nulls     int64 // number of NULL rows
	Min       rows.Value
	Max       rows.Value
	HasMinMax bool
	// Freq stores the most frequent values and their counts (top-N).
	Freq []FreqValue
}

// FreqValue is a frequent value with its occurrence count.
type FreqValue struct {
	Value rows.Value
	Count int64
}

// TableStats describes statistics for one table.
type TableStats struct {
	Rows    int64
	Pages   int64 // estimated number of pages (cost units)
	Columns map[string]*ColumnStats
}

const pageSize = 8192.0

// Collect computes statistics for a table.
func Collect(name string, schema rows.Schema, tableRows []rows.Row) *TableStats {
	ncols := len(schema.Columns)
	ndvs := make([]map[string]int64, ncols)
	nulls := make([]int64, ncols)
	for i := range ndvs {
		ndvs[i] = map[string]int64{}
	}
	minVals := make([]rows.Value, ncols)
	maxVals := make([]rows.Value, ncols)
	hasMinMax := make([]bool, ncols)

	for _, r := range tableRows {
		for ci := 0; ci < ncols; ci++ {
			if ci >= len(r.Vals) {
				continue
			}
			v := r.Vals[ci]
			if v.Null {
				nulls[ci]++
				continue
			}
			key := v.String()
			ndvs[ci][key]++
			if !hasMinMax[ci] || rows.Compare(v, minVals[ci]) < 0 {
				minVals[ci] = v
			}
			if !hasMinMax[ci] || rows.Compare(v, maxVals[ci]) > 0 {
				maxVals[ci] = v
			}
			hasMinMax[ci] = true
		}
	}

	cs := &TableStats{
		Rows:    int64(len(tableRows)),
		Pages:   0,
		Columns: map[string]*ColumnStats{},
	}
	rowBytes := 16 + 8*ncols
	cs.Pages = int64(math.Ceil(float64(len(tableRows)*rowBytes) / pageSize))
	if cs.Pages < 1 && len(tableRows) > 0 {
		cs.Pages = 1
	}

	for ci := 0; ci < ncols; ci++ {
		col := schema.Columns[ci]
		// build frequent-value histogram (top-N)
		type fv struct {
			v     rows.Value
			count int64
		}
		counts := map[string]*fv{}
		for _, r := range tableRows {
			if ci >= len(r.Vals) || r.Vals[ci].Null {
				continue
			}
			key := r.Vals[ci].String()
			if c, ok := counts[key]; ok {
				c.count++
			} else {
				counts[key] = &fv{v: r.Vals[ci], count: 1}
			}
		}
		freqList := make([]FreqValue, 0, len(counts))
		for _, c := range counts {
			freqList = append(freqList, FreqValue{Value: c.v, Count: c.count})
		}
		sort.Slice(freqList, func(i, j int) bool {
			if freqList[i].Count != freqList[j].Count {
				return freqList[i].Count > freqList[j].Count
			}
			return freqList[i].Value.String() < freqList[j].Value.String()
		})
		if len(freqList) > 10 {
			freqList = freqList[:10]
		}

		cs.Columns[strings.ToLower(col.Name)] = &ColumnStats{
			Name:      strings.ToLower(col.Name),
			NDV:       int64(len(ndvs[ci])),
			Nulls:     nulls[ci],
			Min:       minVals[ci],
			Max:       maxVals[ci],
			HasMinMax: hasMinMax[ci],
			Freq:      freqList,
		}
	}
	return cs
}

// LookupColumn returns column statistics by name (case-insensitive).
func (t *TableStats) LookupColumn(name string) *ColumnStats {
	if t == nil || t.Columns == nil {
		return nil
	}
	return t.Columns[strings.ToLower(name)]
}

func (t *TableStats) rowCount() int64 {
	if t == nil {
		return 0
	}
	return t.Rows
}

// freqCount returns the occurrence count of a literal value from the
// frequent-value histogram, or -1 if not present.
func (c *ColumnStats) freqCount(v rows.Value) int64 {
	for _, f := range c.Freq {
		if rows.Equal(f.Value, v) {
			return f.Count
		}
	}
	return -1
}

// EstimateSelectivity estimates the fraction of rows (0..1) of a single-table
// predicate. When statistics are missing it returns a conservative default
// and reports degraded reasons so the caller can mark the plan.
func EstimateSelectivity(pred sql.Expr, t *TableStats, d config.Defaults) (float64, bool, []string) {
	if pred == nil {
		return 1.0, false, nil
	}
	e := &estimator{t: t, d: d}
	sel := e.estimate(pred)
	if sel < 0 {
		sel = 0
	}
	if sel > 1 {
		sel = 1
	}
	return sel, e.degraded, e.reasons
}

type estimator struct {
	t        *TableStats
	d        config.Defaults
	degraded bool
	reasons  []string
}

func (e *estimator) degrade(reason string) {
	e.degraded = true
	e.reasons = append(e.reasons, reason)
}

func (e *estimator) colStats(ref *sql.ColumnRef) *ColumnStats {
	if e.t == nil {
		e.degrade("no statistics available for table")
		return nil
	}
	c := e.t.LookupColumn(ref.Name)
	if c == nil {
		e.degrade("no column statistics for column " + ref.Name)
	}
	return c
}

func (e *estimator) estimate(expr sql.Expr) float64 {
	switch x := expr.(type) {
	case *sql.BinOp:
		switch strings.ToUpper(x.Op) {
		case "AND":
			return e.estimate(x.Left) * e.estimate(x.Right)
		case "OR":
			l := e.estimate(x.Left)
			r := e.estimate(x.Right)
			return l + r - l*r
		case "=":
			if ref, lit, ok := extractColLit(x); ok {
				return e.eqSel(ref, lit)
			}
			if ref, lit, ok := extractLitCol(x); ok {
				return e.eqSel(ref, lit)
			}
			return e.d.EqSelectivity
		case "<>", "!=":
			if ref, lit, ok := extractColLit(x); ok {
				return 1 - e.eqSel(ref, lit)
			}
			if ref, lit, ok := extractLitCol(x); ok {
				return 1 - e.eqSel(ref, lit)
			}
			return 1 - e.d.EqSelectivity
		case "<", "<=", ">", ">=":
			if ref, lit, ok := extractColLit(x); ok {
				return e.rangeSel(ref, lit, x.Op)
			}
			if ref, lit, ok := extractLitCol(x); ok {
				// lit OP col  => col OP' lit with reversed operator
				return e.rangeSel(ref, lit, reverseOp(x.Op))
			}
			return e.d.RangeSelectivity
		}
	case *sql.UnaryOp:
		if x.Op == "NOT" {
			return 1 - e.estimate(x.Expr)
		}
		if x.Op == "-" {
			return e.estimate(x.Expr)
		}
	case *sql.IsNull:
		if ref, ok := x.Expr.(*sql.ColumnRef); ok {
			c := e.colStats(ref)
			if c == nil || e.t.Rows == 0 {
				return e.d.NullSelectivity
			}
			frac := float64(c.Nulls) / float64(e.t.Rows)
			if x.Not {
				return 1 - frac
			}
			return frac
		}
		return e.d.NullSelectivity
	case *sql.InList:
		if ref, ok := x.Expr.(*sql.ColumnRef); ok {
			c := e.colStats(ref)
			if c == nil {
				return e.d.EqSelectivity
			}
			sel := 0.0
			for _, v := range x.List {
				if lit, ok := v.(*sql.Literal); ok {
					val := literalToValue(lit)
					if cnt := c.freqCount(val); cnt >= 0 {
						sel += float64(cnt) / float64(e.t.Rows)
					} else {
						sel += 1.0 / float64(c.NDV)
					}
				}
			}
			if sel > 1 {
				sel = 1
			}
			return sel
		}
		return e.d.EqSelectivity
	case *sql.Between:
		if ref, ok := x.Expr.(*sql.ColumnRef); ok {
			c := e.colStats(ref)
			if c == nil || !c.HasMinMax {
				if c == nil {
					return e.d.RangeSelectivity
				}
				e.degrade("no min/max statistics for column " + ref.Name)
				return e.d.RangeSelectivity
			}
			low, lok := literalFrom(x.Low)
			high, hik := literalFrom(x.High)
			if !lok || !hik {
				return e.d.RangeSelectivity
			}
			lo := rows.Compare(low, c.Min)
			hi := rows.Compare(high, c.Max)
			_ = lo
			_ = hi
			// fraction of [min,max] covered by [low,high]
			if c.Min.Numeric() && c.Max.Numeric() && low.Numeric() && high.Numeric() {
				minF, _ := c.Min.AsFloat()
				maxF, _ := c.Max.AsFloat()
				loF, _ := low.AsFloat()
				hiF, _ := high.AsFloat()
				if maxF > minF {
					clampedLo := math.Max(loF, minF)
					clampedHi := math.Min(hiF, maxF)
					frac := (clampedHi - clampedLo) / (maxF - minF)
					if frac < 0 {
						frac = 0
					}
					if frac > 1 {
						frac = 1
					}
					return frac
				}
			}
			return e.d.RangeSelectivity
		}
		return e.d.RangeSelectivity
	case *sql.Like:
		if ref, ok := x.Expr.(*sql.ColumnRef); ok {
			c := e.colStats(ref)
			if c == nil {
				return e.d.RangeSelectivity
			}
			pat := x.Pattern
			if strings.HasSuffix(pat, "%") && !strings.Contains(pat[:len(pat)-1], "%") && !strings.Contains(pat, "_") {
				return 0.1 // prefix match: conservative
			}
			if !strings.ContainsAny(pat, "%_") {
				val := rows.StringValue(pat)
				if cnt := c.freqCount(val); cnt >= 0 {
					return float64(cnt) / float64(e.t.Rows)
				}
				return 1.0 / float64(c.NDV)
			}
			return 0.05
		}
		return e.d.RangeSelectivity
	}
	return e.d.EqSelectivity
}

func (e *estimator) eqSel(ref *sql.ColumnRef, lit rows.Value) float64 {
	c := e.colStats(ref)
	if c == nil {
		return e.d.EqSelectivity
	}
	if cnt := c.freqCount(lit); cnt >= 0 {
		return float64(cnt) / float64(e.t.Rows)
	}
	if c.NDV > 0 {
		return 1.0 / float64(c.NDV)
	}
	return e.d.EqSelectivity
}

func (e *estimator) rangeSel(ref *sql.ColumnRef, lit rows.Value, op string) float64 {
	c := e.colStats(ref)
	if c == nil || !c.HasMinMax {
		if c != nil {
			e.degrade("no min/max statistics for column " + ref.Name)
		}
		return e.d.RangeSelectivity
	}
	if !c.Min.Numeric() || !c.Max.Numeric() || !lit.Numeric() {
		return e.d.RangeSelectivity
	}
	minF, _ := c.Min.AsFloat()
	maxF, _ := c.Max.AsFloat()
	v, _ := lit.AsFloat()
	if maxF <= minF {
		return e.d.RangeSelectivity
	}
	var frac float64
	switch op {
	case ">":
		frac = (maxF - v) / (maxF - minF)
	case ">=":
		frac = (maxF - v) / (maxF - minF)
	case "<":
		frac = (v - minF) / (maxF - minF)
	case "<=":
		frac = (v - minF) / (maxF - minF)
	}
	if frac < 0 {
		frac = 0
	}
	if frac > 1 {
		frac = 1
	}
	return frac
}

func reverseOp(op string) string {
	switch op {
	case "<":
		return ">"
	case ">":
		return "<"
	case "<=":
		return ">="
	case ">=":
		return "<="
	}
	return op
}

// extractColLit matches `column OP literal`.
func extractColLit(x *sql.BinOp) (*sql.ColumnRef, rows.Value, bool) {
	ref, ok := x.Left.(*sql.ColumnRef)
	if !ok {
		return nil, rows.Value{}, false
	}
	lit, ok := x.Right.(*sql.Literal)
	if !ok {
		return nil, rows.Value{}, false
	}
	return ref, literalToValue(lit), true
}

// extractLitCol matches `literal OP column`.
func extractLitCol(x *sql.BinOp) (*sql.ColumnRef, rows.Value, bool) {
	lit, ok := x.Left.(*sql.Literal)
	if !ok {
		return nil, rows.Value{}, false
	}
	ref, ok := x.Right.(*sql.ColumnRef)
	if !ok {
		return nil, rows.Value{}, false
	}
	return ref, literalToValue(lit), true
}

func literalFrom(e sql.Expr) (rows.Value, bool) {
	lit, ok := e.(*sql.Literal)
	if !ok {
		return rows.Value{}, false
	}
	return literalToValue(lit), true
}

func literalToValue(lit *sql.Literal) rows.Value {
	switch lit.Kind {
	case sql.LitNull:
		return rows.NullValue()
	case sql.LitInt:
		return rows.IntValue(lit.Int)
	case sql.LitFloat:
		return rows.FloatValue(lit.Float)
	case sql.LitString:
		return rows.StringValue(lit.Str)
	case sql.LitBool:
		return rows.BoolValue(lit.Bool)
	}
	return rows.NullValue()
}

// JoinSelectivity estimates the fraction of cross-product rows that satisfy
// an equality join between two columns.
func JoinSelectivity(leftCol, rightCol *ColumnStats, d config.Defaults) (float64, bool, []string) {
	degraded := false
	var reasons []string
	ndv := int64(0)
	if leftCol != nil && rightCol != nil {
		ndv = leftCol.NDV
		if rightCol.NDV > ndv {
			ndv = rightCol.NDV
		}
	}
	if ndv <= 0 {
		degraded = true
		reasons = append(reasons, "missing NDV for join column; using conservative join selectivity")
		return d.EqSelectivity, degraded, reasons
	}
	return 1.0 / float64(ndv), degraded, reasons
}
