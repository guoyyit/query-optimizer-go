package main

import (
	"context"
	"fmt"
	"os"
	"path/filepath"

	"github.com/guoyyit/query-optimizer-go/catalog"
	"github.com/guoyyit/query-optimizer-go/config"
	"github.com/guoyyit/query-optimizer-go/exec"
	"github.com/guoyyit/query-optimizer-go/plancache"
	"github.com/guoyyit/query-optimizer-go/plan"
	"github.com/guoyyit/query-optimizer-go/rows"
	"github.com/guoyyit/query-optimizer-go/sql"
	"github.com/guoyyit/query-optimizer-go/stats"
)

func main() {
	cat := catalog.New()

	// --- in-process generated tables ---
	customers := catalog.NewTable("customers",
		rows.Schema{Columns: []rows.Column{
			{Name: "id", Type: rows.TypeInt},
			{Name: "name", Type: rows.TypeString},
			{Name: "region", Type: rows.TypeString},
		}},
		[]rows.Row{
			{Vals: []rows.Value{rows.IntValue(1), rows.StringValue("alice"), rows.StringValue("east")}},
			{Vals: []rows.Value{rows.IntValue(2), rows.StringValue("bob"), rows.StringValue("west")}},
			{Vals: []rows.Value{rows.IntValue(3), rows.StringValue("carol"), rows.StringValue("east")}},
			{Vals: []rows.Value{rows.IntValue(4), rows.StringValue("dave"), rows.NullValue()}},
		})
	orders := catalog.NewTable("orders",
		rows.Schema{Columns: []rows.Column{
			{Name: "id", Type: rows.TypeInt},
			{Name: "cust_id", Type: rows.TypeInt},
			{Name: "amount", Type: rows.TypeFloat},
		}},
		[]rows.Row{
			{Vals: []rows.Value{rows.IntValue(100), rows.IntValue(1), rows.FloatValue(120.5)}},
			{Vals: []rows.Value{rows.IntValue(101), rows.IntValue(1), rows.FloatValue(80.0)}},
			{Vals: []rows.Value{rows.IntValue(102), rows.IntValue(2), rows.FloatValue(200.0)}},
			{Vals: []rows.Value{rows.IntValue(103), rows.IntValue(3), rows.FloatValue(50.0)}},
			{Vals: []rows.Value{rows.IntValue(104), rows.IntValue(99), rows.FloatValue(10.0)}},
		})
	cat.AddTable(customers)
	cat.AddTable(orders)
	_ = cat.SetStats("customers", stats.Collect("customers", customers.Schema, customers.Rows))
	_ = cat.SetStats("orders", stats.Collect("orders", orders.Schema, orders.Rows))

	// --- CSV-loaded table ---
	dataDir := filepath.Join(".", "data")
	if _, err := os.Stat(filepath.Join(dataDir, "products.csv")); err == nil {
		if err := catalog.LoadCSV(cat, "products", filepath.Join(dataDir, "products.csv")); err != nil {
			fmt.Println("load csv:", err)
		} else {
			t, _ := cat.Get("products")
			_ = cat.SetStats("products", stats.Collect("products", t.Schema, t.Rows))
		}
	}

	cfg := config.Default()
	p := plan.NewPlanner(cat, cfg)
	cache := plancache.New()
	p.SetCache(cache)

	queries := []string{
		"SELECT c.name, o.amount FROM customers c JOIN orders o ON c.id = o.cust_id WHERE o.amount > 100 ORDER BY o.amount DESC",
		"SELECT c.region, COUNT(*), SUM(o.amount) FROM customers c JOIN orders o ON c.id = o.cust_id GROUP BY c.region",
		"SELECT c.name FROM customers c WHERE c.id = 2",
		"SELECT c.name, o.id FROM customers c LEFT JOIN orders o ON c.id = o.cust_id",
	}

	ctx := context.Background()
	for _, q := range queries {
		fmt.Println("==================================================")
		fmt.Println("QUERY:", q)
		res, err := p.Plan(ctx, q)
		if err != nil {
			fmt.Println("plan error:", err)
			continue
		}
		fmt.Print(res.Explain)
		sel, _ := sql.Parse(q)
		out, err := exec.Execute(ctx, res.Physical, cat)
		if err != nil {
			fmt.Println("exec error:", err)
			continue
		}
		fmt.Println("Results:")
		for _, r := range out {
			fmt.Println("  ", r.Vals)
		}
		_ = sel
	}
	fmt.Println("==================================================")
	hits, misses := cache.Stats()
	fmt.Printf("Cache stats: hits=%d misses=%d\n", hits, misses)
}
