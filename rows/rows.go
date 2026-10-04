// Package rows defines the in-process value, row and schema types used by
// both the planner and the execution engine.
package rows

import (
	"fmt"
	"math"
	"strconv"
	"strings"
)

// Type is the runtime type of a scalar value.
type Type int

const (
	TypeNull Type = iota
	TypeInt
	TypeFloat
	TypeString
	TypeBool
)

// Value is a nullable scalar. Only the field matching Type is meaningful.
type Value struct {
	Type  Type
	Int   int64
	Float float64
	Str   string
	Bool  bool
	Null  bool
}

// Column describes one column of a table or an intermediate row.
type Column struct {
	Name  string
	Type  Type
	Alias string // table alias that produced this column (empty for output columns)
}

// Schema is an ordered list of columns.
type Schema struct {
	Columns []Column
}

// Row is an ordered list of nullable values.
type Row struct {
	Vals []Value
}

// NullValue returns a NULL value of unknown type.
func NullValue() Value { return Value{Null: true, Type: TypeNull} }

// IntValue returns a non-null integer value.
func IntValue(v int64) Value { return Value{Type: TypeInt, Int: v} }

// FloatValue returns a non-null float value.
func FloatValue(v float64) Value { return Value{Type: TypeFloat, Float: v} }

// StringValue returns a non-null string value.
func StringValue(v string) Value { return Value{Type: TypeString, Str: v} }

// BoolValue returns a non-null boolean value.
func BoolValue(v bool) Value { return Value{Type: TypeBool, Bool: v} }

// Clone returns a deep copy of the row.
func (r Row) Clone() Row {
	vals := make([]Value, len(r.Vals))
	copy(vals, r.Vals)
	return Row{Vals: vals}
}

// Index returns the index of the named column (case-insensitive).
func (s Schema) Index(name string) (int, bool) {
	name = strings.ToLower(name)
	for i, c := range s.Columns {
		if strings.ToLower(c.Name) == name {
			return i, true
		}
	}
	return 0, false
}

// IndexQualified returns the index of the column with the given table alias
// and name (case-insensitive).
func (s Schema) IndexQualified(alias, name string) (int, bool) {
	name = strings.ToLower(name)
	alias = strings.ToLower(alias)
	for i, c := range s.Columns {
		if strings.ToLower(c.Name) == name && (alias == "" || strings.ToLower(c.Alias) == alias) {
			return i, true
		}
	}
	return 0, false
}

// HasAmbiguous reports whether an unqualified column name matches more than
// one column.
func (s Schema) HasAmbiguous(name string) bool {
	name = strings.ToLower(name)
	n := 0
	for _, c := range s.Columns {
		if strings.ToLower(c.Name) == name {
			n++
		}
	}
	return n > 1
}

// Numeric reports whether the value is an integer or float.
func (v Value) Numeric() bool {
	return !v.Null && (v.Type == TypeInt || v.Type == TypeFloat)
}

// AsFloat returns the numeric value as float64.
func (v Value) AsFloat() (float64, bool) {
	switch v.Type {
	case TypeInt:
		return float64(v.Int), true
	case TypeFloat:
		return v.Float, true
	}
	return 0, false
}

// Compare compares two values. Numeric types (int/float) are compared
// numerically; values of incompatible types are ordered by a fixed type rank
// so that ORDER BY stays deterministic. NULL compares as smaller than any
// non-null value (SQL NULLS FIRST semantics).
func Compare(a, b Value) int {
	if a.Null && b.Null {
		return 0
	}
	if a.Null {
		return -1
	}
	if b.Null {
		return 1
	}
	if a.Numeric() && b.Numeric() {
		af, _ := a.AsFloat()
		bf, _ := b.AsFloat()
		if af < bf {
			return -1
		}
		if af > bf {
			return 1
		}
		return 0
	}
	if a.Type != b.Type {
		if a.Type < b.Type {
			return -1
		}
		return 1
	}
	switch a.Type {
	case TypeString:
		return strings.Compare(a.Str, b.Str)
	case TypeBool:
		if a.Bool == b.Bool {
			return 0
		}
		if !a.Bool {
			return -1
		}
		return 1
	}
	return 0
}

// Equal reports whether two values are equal under SQL semantics: NULL
// compared to anything yields unknown (false here; use ThreeValued for
// predicates).
func Equal(a, b Value) bool {
	if a.Null || b.Null {
		return false
	}
	return Compare(a, b) == 0
}

// TV is a SQL three-valued logic result.
type TV int

const (
	TVFalse TV = iota
	TVTrue
	TVUnknown
)

// Not applies SQL NOT to a three-valued result.
func Not(v TV) TV {
	switch v {
	case TVTrue:
		return TVFalse
	case TVFalse:
		return TVTrue
	}
	return TVUnknown
}

// And applies SQL AND: unknown is absorbing with true.
func And(a, b TV) TV {
	if a == TVFalse || b == TVFalse {
		return TVFalse
	}
	if a == TVUnknown || b == TVUnknown {
		return TVUnknown
	}
	return TVTrue
}

// Or applies SQL OR: unknown is absorbing with false.
func Or(a, b TV) TV {
	if a == TVTrue || b == TVTrue {
		return TVTrue
	}
	if a == TVUnknown || b == TVUnknown {
		return TVUnknown
	}
	return TVFalse
}

// BoolToTV converts a Go bool to TV.
func BoolToTV(b bool) TV {
	if b {
		return TVTrue
	}
	return TVFalse
}

// Arith performs a binary arithmetic operation. NULL propagates; numeric
// inputs are coerced to float when one side is a float.
func Arith(op string, a, b Value) (Value, error) {
	if a.Null || b.Null {
		return NullValue(), nil
	}
	if a.Numeric() && b.Numeric() {
		af, _ := a.AsFloat()
		bf, _ := b.AsFloat()
		var rf float64
		switch op {
		case "+":
			rf = af + bf
		case "-":
			rf = af - bf
		case "*":
			rf = af * bf
		case "/":
			if bf == 0 {
				return NullValue(), nil // SQL: division by zero yields NULL
			}
			rf = af / bf
		default:
			return NullValue(), fmt.Errorf("unsupported arithmetic operator %q", op)
		}
		if a.Type == TypeInt && b.Type == TypeInt && op != "/" {
			return IntValue(int64(rf)), nil
		}
		if a.Type == TypeInt && b.Type == TypeInt && op == "/" {
			// integer division keeps integer semantics when divisible
			if math.Mod(af, bf) == 0 {
				return IntValue(int64(rf)), nil
			}
			return FloatValue(rf), nil
		}
		return FloatValue(rf), nil
	}
	if op == "+" && a.Type == TypeString && b.Type == TypeString {
		return StringValue(a.Str + b.Str), nil
	}
	return NullValue(), fmt.Errorf("type mismatch for operator %q: %s vs %s", op, TypeName(a.Type), TypeName(b.Type))
}

// TypeName returns a readable type name.
func TypeName(t Type) string {
	switch t {
	case TypeInt:
		return "int"
	case TypeFloat:
		return "float"
	case TypeString:
		return "string"
	case TypeBool:
		return "bool"
	}
	return "null"
}

// String renders a value for display.
func (v Value) String() string {
	if v.Null {
		return "NULL"
	}
	switch v.Type {
	case TypeInt:
		return strconv.FormatInt(v.Int, 10)
	case TypeFloat:
		return strconv.FormatFloat(v.Float, 'g', -1, 64)
	case TypeString:
		return v.Str
	case TypeBool:
		return strconv.FormatBool(v.Bool)
	}
	return "?"
}
