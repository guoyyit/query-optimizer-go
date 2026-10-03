package optimizer

import (
	"fmt"
	"math/rand"
	"testing"

	"github.com/guoyyit/query-optimizer-go/catalog"
	"github.com/guoyyit/query-optimizer-go/value"
)

// TestRandomizedEquivalence generates random table contents (with NULLs and
// mixed types) and random queries over them, asserting the optimized path
// matches the naive path row-by-row. Deterministic: fixed seed.
func TestRandomizedEquivalence(t *testing.T) {
	rng := rand.New(rand.NewSource(20240501))
	cat := catalog.New()
	must := func(err error) {
		if err != nil {
			t.Fatal(err)
		}
	}
	must(cat.CreateTable("r1", []catalog.Column{
		{Name: "id", Type: value.Int}, {Name: "n", Type: value.Int}, {Name: "s", Type: value.String},
	}))
	must(cat.CreateTable("r2", []catalog.Column{
		{Name: "id", Type: value.Int}, {Name: "f", Type: value.Float}, {Name: "n", Type: value.Int},
	}))
	randVal := func(t value.Type) value.Value {
		if rng.Intn(5) == 0 {
			return value.NullValue() // 20% NULLs
		}
		switch t {
		case value.Int:
			return value.FromInt(int64(rng.Intn(21) - 10))
		case value.Float:
			return value.FromFloat(float64(rng.Intn(41)-20) / 2)
		case value.String:
			return value.FromString([]string{"a", "b", "c", ""}[rng.Intn(4)])
		}
		return value.NullValue()
	}
	for i := 0; i < 40; i++ {
		must(cat.Table("r1").Insert([]value.Value{
			value.FromInt(int64(i)), randVal(value.Int), randVal(value.String),
		}))
	}
	for i := 0; i < 30; i++ {
		must(cat.Table("r2").Insert([]value.Value{
			value.FromInt(int64(i)), randVal(value.Float), randVal(value.Int),
		}))
	}
	cat.Table("r1").SetStats(&catalog.Stats{RowCount: 40, NDV: map[string]int64{"id": 40, "n": 15, "s": 4}})
	cat.Table("r2").SetStats(&catalog.Stats{RowCount: 30, NDV: map[string]int64{"id": 30, "f": 20, "n": 12}})

	eng, err := NewEngine(cat, DefaultConfig())
	if err != nil {
		t.Fatal(err)
	}
	cmpOps := []string{"=", "!=", "<", "<=", ">", ">="}
	consts := []string{"0", "3", "-5", "2.5", "'a'", "NULL"}
	cols := []string{"r1.n", "r2.f", "r2.n"}
	for iter := 0; iter < 60; iter++ {
		var where string
		nPreds := 1 + rng.Intn(3)
		for p := 0; p < nPreds; p++ {
			if p > 0 {
				if rng.Intn(2) == 0 {
					where += " AND "
				} else {
					where += " OR "
				}
			}
			col := cols[rng.Intn(len(cols))]
			op := cmpOps[rng.Intn(len(cmpOps))]
			if rng.Intn(4) == 0 {
				// column-to-column predicate
				other := cols[rng.Intn(len(cols))]
				where += fmt.Sprintf("%s %s %s", col, op, other)
			} else {
				where += fmt.Sprintf("%s %s %s", col, op, consts[rng.Intn(len(consts))])
			}
		}
		q := fmt.Sprintf(
			`SELECT r1.id, r2.id FROM r1 JOIN r2 ON r1.id = r2.id WHERE %s`, where)
		t.Run(fmt.Sprintf("iter%02d", iter), func(t *testing.T) {
			assertEquivalent(t, eng, q)
		})
	}
}
