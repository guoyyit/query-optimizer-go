// Package value defines the typed value model shared by the naive and
// optimized execution paths so both observe identical semantics.
//
// Coercion rules (documented in docs/OPTIMIZER.md):
//   - NULL compared to anything is UNKNOWN (ok == false).
//   - Int vs Float promotes the Int to Float.
//   - Same types compare naturally.
//   - Any other type pair is incomparable (ok == false), so equality is
//     false and range predicates are UNKNOWN: the row is filtered out.
package value

import (
	"math"
	"strconv"
)

// Type is the runtime type tag of a Value.
type Type int

const (
	Null Type = iota
	Int
	Float
	String
	Bool
)

func (t Type) String() string {
	switch t {
	case Null:
		return "NULL"
	case Int:
		return "INT"
	case Float:
		return "FLOAT"
	case String:
		return "STRING"
	case Bool:
		return "BOOL"
	}
	return "?"
}

// Value is a typed scalar. Only the field matching T is meaningful.
type Value struct {
	T Type
	I int64
	F float64
	S string
	B bool
}

func NullValue() Value          { return Value{T: Null} }
func FromInt(v int64) Value     { return Value{T: Int, I: v} }
func FromFloat(v float64) Value { return Value{T: Float, F: v} }
func FromString(v string) Value { return Value{T: String, S: v} }
func FromBool(v bool) Value     { return Value{T: Bool, B: v} }

// IsNull reports whether the value is SQL NULL.
func (v Value) IsNull() bool { return v.T == Null }

// Compare compares two values using the coercion rules above. ok is false
// when the comparison is SQL UNKNOWN.
func Compare(a, b Value) (cmp int, ok bool) {
	if a.T == Null || b.T == Null {
		return 0, false
	}
	// Numeric promotion: Int vs Float.
	if (a.T == Int || a.T == Float) && (b.T == Int || b.T == Float) {
		fa, fb := numeric(a), numeric(b)
		switch {
		case fa < fb:
			return -1, true
		case fa > fb:
			return 1, true
		}
		return 0, true
	}
	if a.T != b.T {
		return 0, false
	}
	switch a.T {
	case Int:
		switch {
		case a.I < b.I:
			return -1, true
		case a.I > b.I:
			return 1, true
		}
		return 0, true
	case String:
		switch {
		case a.S < b.S:
			return -1, true
		case a.S > b.S:
			return 1, true
		}
		return 0, true
	case Bool:
		switch {
		case a.B == b.B:
			return 0, true
		case !a.B:
			return -1, true
		}
		return 1, true
	}
	return 0, false
}

func numeric(v Value) float64 {
	if v.T == Int {
		return float64(v.I)
	}
	return v.F
}

// Equal reports SQL equality (true only when comparable and equal).
func Equal(a, b Value) bool {
	cmp, ok := Compare(a, b)
	return ok && cmp == 0
}

// HashKey encodes a value for hash-map keys such that Equal values share a
// key (Int/Float promotion included). NULL and incomparable mixes never
// collide with real values.
func (v Value) HashKey() uint64 {
	const (
		hNull   = 0x9e3779b97f4a7c15
		hIntBas = 0x100000001b3
	)
	h := uint64(1469598103934665603)
	mix := func(x uint64) { h ^= x; h *= 1099511628211 }
	switch v.T {
	case Null:
		return hNull
	case Int, Float:
		// Normalize numerics to float64 bits so Int(1) and Float(1) match.
		f := numeric(v)
		bits := uint64(0)
		if f == f { // not NaN
			bits = math.Float64bits(f)
		}
		mix(hIntBas)
		mix(bits)
	case String:
		mix(2)
		for i := 0; i < len(v.S); i++ {
			mix(uint64(v.S[i]))
		}
	case Bool:
		mix(3)
		if v.B {
			mix(1)
		} else {
			mix(0)
		}
	}
	return h
}

func (v Value) String() string {
	switch v.T {
	case Null:
		return "NULL"
	case Int:
		return strconv.FormatInt(v.I, 10)
	case Float:
		return strconv.FormatFloat(v.F, 'g', -1, 64)
	case String:
		return strconv.Quote(v.S)
	case Bool:
		return strconv.FormatBool(v.B)
	}
	return "?"
}
