package optimizer

import (
	"context"
	"testing"

	"github.com/guoyyit/query-optimizer-go/catalog"
	"github.com/guoyyit/query-optimizer-go/value"
)

func buildTestCatalog(t *testing.T) *catalog.Catalog {
	t.Helper()
	cat := catalog.New()
	mk := func(name string, cols ...catalog.Column) {
		if err := cat.CreateTable(name, cols); err != nil {
			t.Fatal(err)
		}
	}
	mk("users",
		catalog.Column{Name: "id", Type: value.Int},
		catalog.Column{Name: "age", Type: value.Int},
		catalog.Column{Name: "name", Type: value.String},
	)
	mk("orders",
		catalog.Column{Name: "oid", Type: value.Int},
		catalog.Column{Name: "uid", Type: value.Int},
		catalog.Column{Name: "total", Type: value.Float},
	)
	u := cat.Table("users")
	for _, row := range [][]value.Value{
		{value.FromInt(1), value.FromInt(30), value.FromString("ann")},
		{value.FromInt(2), value.FromInt(17), value.FromString("bob")},
		{value.FromInt(3), value.FromInt(45), value.FromString("cat")},
		{value.FromInt(4), value.NullValue(), value.FromString("dan")},
	} {
		if err := u.Insert(row); err != nil {
			t.Fatal(err)
		}
	}
	o := cat.Table("orders")
	for _, row := range [][]value.Value{
		{value.FromInt(10), value.FromInt(1), value.FromFloat(99.5)},
		{value.FromInt(11), value.FromInt(1), value.FromFloat(10.0)},
		{value.FromInt(12), value.FromInt(3), value.FromFloat(250.0)},
		{value.FromInt(13), value.FromInt(99), value.FromFloat(5.0)},
	} {
		if err := o.Insert(row); err != nil {
			t.Fatal(err)
		}
	}
	u.SetStats(&catalog.Stats{RowCount: 4, NDV: map[string]int64{"id": 4, "age": 3, "name": 4}})
	o.SetStats(&catalog.Stats{RowCount: 4, NDV: map[string]int64{"oid": 4, "uid": 3, "total": 4}})
	return cat
}

func TestSmokeEndToEnd(t *testing.T) {
	cat := buildTestCatalog(t)
	eng, err := NewEngine(cat, DefaultConfig())
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	q := `SELECT u.name, o.total FROM users u JOIN orders o ON u.id = o.uid WHERE u.age >= 18 AND o.total < 200`
	pq, err := eng.Plan(ctx, q)
	if err != nil {
		t.Fatal(err)
	}
	t.Log("\n" + pq.Explain)
	got, err := eng.Execute(ctx, q)
	if err != nil {
		t.Fatal(err)
	}
	want, err := eng.ExecuteNaive(ctx, q)
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Rows) != len(want.Rows) {
		t.Fatalf("row count mismatch: got %d want %d", len(got.Rows), len(want.Rows))
	}
	for i := range got.Rows {
		for j := range got.Rows[i] {
			if !value.Equal(got.Rows[i][j], want.Rows[i][j]) && !(got.Rows[i][j].IsNull() && want.Rows[i][j].IsNull()) {
				t.Fatalf("row %d col %d: got %v want %v", i, j, got.Rows[i][j], want.Rows[i][j])
			}
		}
	}
	t.Logf("rows: %v", got.Rows)
}
