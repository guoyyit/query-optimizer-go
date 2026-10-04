package exec

import (
	"fmt"
	"regexp"
	"strings"

	"github.com/guoyyit/query-optimizer-go/rows"
	"github.com/guoyyit/query-optimizer-go/sql"
)

// EvalContext resolves column references during execution.
type EvalContext struct {
	Schema      rows.Schema
	Row         rows.Row
	AggResolver func(*sql.Aggregate) (rows.Value, error)
}

// Eval evaluates an expression in the given context.
func Eval(e sql.Expr, ctx *EvalContext) (rows.Value, error) {
	switch x := e.(type) {
	case *sql.Literal:
		return literalValue(x), nil
	case *sql.ColumnRef:
		idx, err := resolveColumnIndex(ctx.Schema, x)
		if err != nil {
			return rows.NullValue(), err
		}
		if idx >= len(ctx.Row.Vals) {
			return rows.NullValue(), nil
		}
		return ctx.Row.Vals[idx], nil
	case *sql.BinOp:
		return evalBinOp(x, ctx)
	case *sql.UnaryOp:
		v, err := Eval(x.Expr, ctx)
		if err != nil {
			return rows.NullValue(), err
		}
		switch x.Op {
		case "NOT":
			tv := evalTruth(v)
			switch rows.Not(tv) {
			case rows.TVTrue:
				return rows.BoolValue(true), nil
			case rows.TVFalse:
				return rows.BoolValue(false), nil
			}
			return rows.NullValue(), nil
		case "-":
			if v.Null {
				return rows.NullValue(), nil
			}
			if v.Type == rows.TypeInt {
				return rows.IntValue(-v.Int), nil
			}
			if v.Type == rows.TypeFloat {
				return rows.FloatValue(-v.Float), nil
			}
			return rows.NullValue(), fmt.Errorf("unary minus on non-numeric value")
		}
	case *sql.InList:
		tv, err := evalInList(x, ctx)
		if err != nil {
			return rows.NullValue(), err
		}
		return tvToValue(tv), nil
	case *sql.Between:
		tv, err := evalBetween(x, ctx)
		if err != nil {
			return rows.NullValue(), err
		}
		return tvToValue(tv), nil
	case *sql.IsNull:
		v, err := Eval(x.Expr, ctx)
		if err != nil {
			return rows.NullValue(), err
		}
		isNull := v.Null
		if x.Not {
			isNull = !isNull
		}
		return rows.BoolValue(isNull), nil
	case *sql.Like:
		v, err := Eval(x.Expr, ctx)
		if err != nil {
			return rows.NullValue(), err
		}
		if v.Null {
			return rows.NullValue(), nil
		}
		matched, err := likeMatch(v.Str, x.Pattern)
		if err != nil {
			return rows.NullValue(), err
		}
		if x.Not {
			matched = !matched
		}
		return rows.BoolValue(matched), nil
	case *sql.Aggregate:
		if ctx.AggResolver != nil {
			return ctx.AggResolver(x)
		}
		return rows.NullValue(), fmt.Errorf("aggregate expression cannot be evaluated row-wise")
	}
	return rows.NullValue(), fmt.Errorf("unsupported expression %T", e)
}

// EvalBool evaluates a predicate with SQL three-valued logic; only TRUE
// satisfies a filter.
func EvalBool(e sql.Expr, ctx *EvalContext) (bool, error) {
	v, err := Eval(e, ctx)
	if err != nil {
		return false, err
	}
	return !v.Null && v.Type == rows.TypeBool && v.Bool, nil
}

func literalValue(l *sql.Literal) rows.Value {
	switch l.Kind {
	case sql.LitNull:
		return rows.NullValue()
	case sql.LitInt:
		return rows.IntValue(l.Int)
	case sql.LitFloat:
		return rows.FloatValue(l.Float)
	case sql.LitString:
		return rows.StringValue(l.Str)
	case sql.LitBool:
		return rows.BoolValue(l.Bool)
	}
	return rows.NullValue()
}

func resolveColumnIndex(schema rows.Schema, ref *sql.ColumnRef) (int, error) {
	if ref.Table != "" {
		if idx, ok := schema.IndexQualified(ref.Table, ref.Name); ok {
			return idx, nil
		}
		return -1, fmt.Errorf("column %s.%s not found", ref.Table, ref.Name)
	}
	if schema.HasAmbiguous(ref.Name) {
		return -1, fmt.Errorf("ambiguous column %q; qualify with a table name", ref.Name)
	}
	if idx, ok := schema.Index(ref.Name); ok {
		return idx, nil
	}
	return -1, fmt.Errorf("column %q not found", ref.Name)
}

func evalBinOp(x *sql.BinOp, ctx *EvalContext) (rows.Value, error) {
	op := strings.ToUpper(x.Op)
	switch op {
	case "AND":
		l, err := Eval(x.Left, ctx)
		if err != nil {
			return rows.NullValue(), err
		}
		r, err := Eval(x.Right, ctx)
		if err != nil {
			return rows.NullValue(), err
		}
		return tvToValue(rows.And(evalTruth(l), evalTruth(r))), nil
	case "OR":
		l, err := Eval(x.Left, ctx)
		if err != nil {
			return rows.NullValue(), err
		}
		r, err := Eval(x.Right, ctx)
		if err != nil {
			return rows.NullValue(), err
		}
		return tvToValue(rows.Or(evalTruth(l), evalTruth(r))), nil
	case "+", "-", "*", "/":
		l, err := Eval(x.Left, ctx)
		if err != nil {
			return rows.NullValue(), err
		}
		r, err := Eval(x.Right, ctx)
		if err != nil {
			return rows.NullValue(), err
		}
		return rows.Arith(x.Op, l, r)
	}
	// comparison
	l, err := Eval(x.Left, ctx)
	if err != nil {
		return rows.NullValue(), err
	}
	r, err := Eval(x.Right, ctx)
	if err != nil {
		return rows.NullValue(), err
	}
	if l.Null || r.Null {
		return rows.NullValue(), nil
	}
	cmp := rows.Compare(l, r)
	var b bool
	switch x.Op {
	case "=":
		b = cmp == 0
	case "<>", "!=":
		b = cmp != 0
	case "<":
		b = cmp < 0
	case ">":
		b = cmp > 0
	case "<=":
		b = cmp <= 0
	case ">=":
		b = cmp >= 0
	default:
		return rows.NullValue(), fmt.Errorf("unsupported comparison %q", x.Op)
	}
	return rows.BoolValue(b), nil
}

func evalTruth(v rows.Value) rows.TV {
	if v.Null {
		return rows.TVUnknown
	}
	if v.Type == rows.TypeBool {
		return rows.BoolToTV(v.Bool)
	}
	return rows.TVUnknown
}

func tvToValue(tv rows.TV) rows.Value {
	switch tv {
	case rows.TVTrue:
		return rows.BoolValue(true)
	case rows.TVFalse:
		return rows.BoolValue(false)
	}
	return rows.NullValue()
}

func evalInList(x *sql.InList, ctx *EvalContext) (rows.TV, error) {
	v, err := Eval(x.Expr, ctx)
	if err != nil {
		return rows.TVUnknown, err
	}
	if v.Null {
		return rows.TVUnknown, nil
	}
	hasUnknown := false
	for _, item := range x.List {
		iv, err := Eval(item, ctx)
		if err != nil {
			return rows.TVUnknown, err
		}
		if iv.Null {
			hasUnknown = true
			continue
		}
		if rows.Equal(v, iv) {
			return rows.TVTrue, nil
		}
	}
	if hasUnknown {
		return rows.TVUnknown, nil
	}
	return rows.TVFalse, nil
}

func evalBetween(x *sql.Between, ctx *EvalContext) (rows.TV, error) {
	v, err := Eval(x.Expr, ctx)
	if err != nil {
		return rows.TVUnknown, err
	}
	lo, err := Eval(x.Low, ctx)
	if err != nil {
		return rows.TVUnknown, err
	}
	hi, err := Eval(x.High, ctx)
	if err != nil {
		return rows.TVUnknown, err
	}
	if v.Null || lo.Null || hi.Null {
		return rows.TVUnknown, nil
	}
	if rows.Compare(v, lo) >= 0 && rows.Compare(v, hi) <= 0 {
		return rows.TVTrue, nil
	}
	return rows.TVFalse, nil
}

var likeCache = map[string]*regexp.Regexp{}

func likeMatch(s, pattern string) (bool, error) {
	re, ok := likeCache[pattern]
	if !ok {
		var b strings.Builder
		b.WriteString("^")
		for i := 0; i < len(pattern); i++ {
			c := pattern[i]
			switch c {
			case '%':
				b.WriteString(".*")
			case '_':
				b.WriteString(".")
			default:
				b.WriteString(regexp.QuoteMeta(string(c)))
			}
		}
		b.WriteString("$")
		var err error
		re, err = regexp.Compile(b.String())
		if err != nil {
			return false, err
		}
		likeCache[pattern] = re
	}
	return re.MatchString(s), nil
}
