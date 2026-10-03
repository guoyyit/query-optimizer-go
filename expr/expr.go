// Package expr defines scalar/predicate expressions evaluated against a row
// with a known schema. Expressions reference columns by (table, column)
// name; evaluation resolves names against the row schema each time, so
// rewrites never invalidate expression trees.
//
// Boolean logic is three-valued: TRUE / FALSE / UNKNOWN (NULL). A WHERE or
// join condition keeps a row only when it evaluates to TRUE.
package expr

import (
	"sort"
	"strings"

	"github.com/guoyyit/query-optimizer-go/value"
)

// ColRef identifies one output column of a plan node.
type ColRef struct {
	Table  string // table name or alias; "" for computed columns
	Column string
}

func (c ColRef) String() string {
	if c.Table == "" {
		return c.Column
	}
	return c.Table + "." + c.Column
}

// Op is a binary operator.
type Op int

const (
	Eq Op = iota
	Ne
	Lt
	Le
	Gt
	Ge
	And
	Or
)

func (o Op) String() string {
	switch o {
	case Eq:
		return "="
	case Ne:
		return "!="
	case Lt:
		return "<"
	case Le:
		return "<="
	case Gt:
		return ">"
	case Ge:
		return ">="
	case And:
		return "AND"
	case Or:
		return "OR"
	}
	return "?"
}

// Expr is a scalar or boolean expression tree.
type Expr interface {
	// Eval computes the expression against row, whose layout is schema.
	Eval(schema []ColRef, row []value.Value) value.Value
	// Columns returns the column references appearing in the tree
	// (deduplicated, sorted for determinism).
	Columns() []ColRef
	String() string
}

// Literal is a constant.
type Literal struct{ V value.Value }

func (l *Literal) Eval(_ []ColRef, _ []value.Value) value.Value { return l.V }
func (l *Literal) Columns() []ColRef                            { return nil }
func (l *Literal) String() string                               { return l.V.String() }

// ColumnRef references a column by (table, column) name.
type ColumnRef struct {
	Table  string
	Column string
}

func (c *ColumnRef) Eval(schema []ColRef, row []value.Value) value.Value {
	for i, s := range schema {
		if s.Table == c.Table && s.Column == c.Column {
			return row[i]
		}
	}
	// Unresolved column: treat as NULL. Resolution happens at plan build
	// time, so reaching here indicates a planner bug, not a user error.
	return value.NullValue()
}
func (c *ColumnRef) Columns() []ColRef { return []ColRef{{c.Table, c.Column}} }
func (c *ColumnRef) String() string    { return ColRef{c.Table, c.Column}.String() }

// Binary is a binary operation (comparison or boolean connective).
type Binary struct {
	Op   Op
	L, R Expr
}

func (b *Binary) Eval(schema []ColRef, row []value.Value) value.Value {
	if b.Op == And || b.Op == Or {
		return evalBool(b.Op, b.L.Eval(schema, row), b.R.Eval(schema, row))
	}
	l, r := b.L.Eval(schema, row), b.R.Eval(schema, row)
	cmp, ok := value.Compare(l, r)
	if !ok {
		return value.NullValue() // UNKNOWN
	}
	var res bool
	switch b.Op {
	case Eq:
		res = cmp == 0
	case Ne:
		res = cmp != 0
	case Lt:
		res = cmp < 0
	case Le:
		res = cmp <= 0
	case Gt:
		res = cmp > 0
	case Ge:
		res = cmp >= 0
	}
	return value.FromBool(res)
}

// evalBool implements three-valued AND/OR.
func evalBool(op Op, l, r value.Value) value.Value {
	lb, lok := boolOf(l)
	rb, rok := boolOf(r)
	switch op {
	case And:
		if (lok && !lb) || (rok && !rb) {
			return value.FromBool(false)
		}
		if lok && rok {
			return value.FromBool(lb && rb)
		}
	case Or:
		if (lok && lb) || (rok && rb) {
			return value.FromBool(true)
		}
		if lok && rok {
			return value.FromBool(lb || rb)
		}
	}
	return value.NullValue()
}

func boolOf(v value.Value) (bool, bool) {
	if v.T == value.Bool {
		return v.B, true
	}
	return false, false
}

func (b *Binary) Columns() []ColRef { return mergeCols(b.L.Columns(), b.R.Columns()) }
func (b *Binary) String() string {
	return "(" + b.L.String() + " " + b.Op.String() + " " + b.R.String() + ")"
}

// Not negates a boolean expression (three-valued).
type Not struct{ E Expr }

func (n *Not) Eval(schema []ColRef, row []value.Value) value.Value {
	v := n.E.Eval(schema, row)
	if b, ok := boolOf(v); ok {
		return value.FromBool(!b)
	}
	return value.NullValue()
}
func (n *Not) Columns() []ColRef { return n.E.Columns() }
func (n *Not) String() string    { return "(NOT " + n.E.String() + ")" }

func mergeCols(a, b []ColRef) []ColRef {
	set := map[ColRef]struct{}{}
	for _, c := range a {
		set[c] = struct{}{}
	}
	for _, c := range b {
		set[c] = struct{}{}
	}
	out := make([]ColRef, 0, len(set))
	for c := range set {
		out = append(out, c)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].String() < out[j].String() })
	return out
}

// SplitConjuncts flattens an AND tree into a list of conjuncts.
func SplitConjuncts(e Expr) []Expr {
	if e == nil {
		return nil
	}
	if b, ok := e.(*Binary); ok && b.Op == And {
		return append(SplitConjuncts(b.L), SplitConjuncts(b.R)...)
	}
	return []Expr{e}
}

// AndAll combines expressions with AND; nil/empty yields nil. The result is
// built in a canonical left-deep order so fingerprints are deterministic.
func AndAll(es []Expr) Expr {
	es = compact(es)
	if len(es) == 0 {
		return nil
	}
	acc := es[0]
	for _, e := range es[1:] {
		acc = &Binary{Op: And, L: acc, R: e}
	}
	return acc
}

func compact(es []Expr) []Expr {
	out := es[:0]
	for _, e := range es {
		if e != nil {
			out = append(out, e)
		}
	}
	return out
}

// Truthy reports whether a WHERE/ON evaluation keeps the row.
func Truthy(v value.Value) bool { return v.T == value.Bool && v.B }

// Canonical returns a deterministic string form of e: AND/OR children are
// sorted so semantically equal trees fingerprint identically.
func Canonical(e Expr) string {
	if e == nil {
		return "<none>"
	}
	switch t := e.(type) {
	case *Binary:
		if t.Op == And || t.Op == Or {
			parts := []string{Canonical(t.L), Canonical(t.R)}
			sort.Strings(parts)
			return "(" + strings.Join(parts, " "+t.Op.String()+" ") + ")"
		}
		return "(" + Canonical(t.L) + " " + t.Op.String() + " " + Canonical(t.R) + ")"
	case *Not:
		return "(NOT " + Canonical(t.E) + ")"
	default:
		return e.String()
	}
}
