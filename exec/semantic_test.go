package exec_test

import (
	"context"
	"testing"

	"github.com/guoyyit/query-optimizer-go/config"
	"github.com/guoyyit/query-optimizer-go/plan"
	"github.com/guoyyit/query-optimizer-go/rows"
	"github.com/guoyyit/query-optimizer-go/testhelper"
)

func TestSemanticEquivalence(t *testing.T) {
	ctx := context.Background()
	cat := testhelper.BuildCatalog([]testhelper.TableDef{
		{
			Name: "t1", Stats: true,
			Columns: []rows.Column{
				testhelper.Col("id", rows.TypeInt),
				testhelper.Col("name", rows.TypeString),
				testhelper.Col("score", rows.TypeFloat),
				testhelper.Col("flag", rows.TypeBool),
			},
			Rows: []rows.Row{
				testhelper.MixedRow(rows.IntValue(1), rows.StringValue("alice"), rows.FloatValue(90.5), rows.BoolValue(true)),
				testhelper.MixedRow(rows.IntValue(2), rows.StringValue("bob"), rows.FloatValue(80.0), rows.BoolValue(false)),
				testhelper.MixedRow(rows.IntValue(3), rows.NullValue(), rows.FloatValue(70.5), rows.NullValue()),
				testhelper.MixedRow(rows.IntValue(4), rows.StringValue("dave"), rows.NullValue(), rows.BoolValue(true)),
				testhelper.MixedRow(rows.IntValue(5), rows.StringValue("eve"), rows.FloatValue(100.0), rows.BoolValue(false)),
				testhelper.MixedRow(rows.IntValue(6), rows.StringValue("frank"), rows.FloatValue(0.0), rows.BoolValue(true)),
				testhelper.MixedRow(rows.IntValue(7), rows.StringValue("grace"), rows.FloatValue(-5.5), rows.BoolValue(false)),
			},
		},
		{
			Name: "t2", Stats: true,
			Columns: []rows.Column{
				testhelper.Col("id", rows.TypeInt),
				testhelper.Col("t1_id", rows.TypeInt),
				testhelper.Col("tag", rows.TypeString),
			},
			Rows: []rows.Row{
				testhelper.MixedRow(rows.IntValue(10), rows.IntValue(1), rows.StringValue("x")),
				testhelper.MixedRow(rows.IntValue(11), rows.IntValue(1), rows.StringValue("y")),
				testhelper.MixedRow(rows.IntValue(12), rows.IntValue(2), rows.StringValue("x")),
				testhelper.MixedRow(rows.IntValue(13), rows.IntValue(3), rows.NullValue()),
				testhelper.MixedRow(rows.IntValue(14), rows.IntValue(99), rows.StringValue("z")),
			},
		},
	})

	p := plan.NewPlanner(cat, config.Default())

	queries := []string{
		// basic filter
		"SELECT id, name FROM t1 WHERE score > 50",
		"SELECT id FROM t1 WHERE flag = true",
		"SELECT id FROM t1 WHERE name IS NULL",
		"SELECT id FROM t1 WHERE name IS NOT NULL",
		"SELECT id FROM t1 WHERE score IS NULL",
		// NULL semantics: NULL comparisons yield no rows
		"SELECT id FROM t1 WHERE score = NULL",
		"SELECT id FROM t1 WHERE score <> NULL",
		// three-valued logic
		"SELECT id FROM t1 WHERE NOT (score > 50)",
		"SELECT id FROM t1 WHERE score > 50 AND flag = true",
		"SELECT id FROM t1 WHERE score > 90 OR flag = false",
		// type coercion: int vs float
		"SELECT id FROM t1 WHERE score > 80",
		"SELECT id FROM t1 WHERE id = 2.0",
		// boundary values
		"SELECT id FROM t1 WHERE score >= 100.0",
		"SELECT id FROM t1 WHERE score <= -5.5",
		"SELECT id FROM t1 WHERE score BETWEEN 70.5 AND 90.5",
		// IN list
		"SELECT id FROM t1 WHERE name IN ('alice', 'bob', 'eve')",
		"SELECT id FROM t1 WHERE id IN (1, 3, 5)",
		// LIKE
		"SELECT id FROM t1 WHERE name LIKE 'a%'",
		"SELECT id FROM t1 WHERE name LIKE '_ob'",
		// arithmetic
		"SELECT id, score + 1, score * 2 FROM t1 WHERE score > 0",
		"SELECT id, score - score FROM t1",
		// aggregates
		"SELECT COUNT(*) FROM t1",
		"SELECT COUNT(name) FROM t1",
		"SELECT COUNT(DISTINCT name) FROM t1",
		"SELECT SUM(score) FROM t1",
		"SELECT AVG(score) FROM t1",
		"SELECT MIN(score), MAX(score) FROM t1",
		"SELECT flag, COUNT(*), AVG(score) FROM t1 GROUP BY flag",
		"SELECT flag, COUNT(*) FROM t1 WHERE score > 50 GROUP BY flag",
		// join + filter
		"SELECT t1.id, t2.tag FROM t1 JOIN t2 ON t1.id = t2.t1_id",
		"SELECT t1.name, t2.tag FROM t1 JOIN t2 ON t1.id = t2.t1_id WHERE t2.tag = 'x'",
		"SELECT t1.id, t2.tag FROM t1 LEFT JOIN t2 ON t1.id = t2.t1_id",
		// duplicate column names (both named id)
		"SELECT t1.id, t2.id FROM t1 JOIN t2 ON t1.id = t2.t1_id",
		// multi-join
		"SELECT t1.id, t2.tag FROM t1, t2 WHERE t1.id = t2.t1_id AND t1.score > 50",
		// order + limit
		"SELECT id, score FROM t1 ORDER BY score DESC",
		"SELECT id FROM t1 ORDER BY id LIMIT 3",
		"SELECT id FROM t1 ORDER BY id LIMIT 3 OFFSET 2",
		// distinct
		"SELECT DISTINCT flag FROM t1",
		"SELECT DISTINCT name FROM t1 WHERE score > 0",
		// having
		"SELECT flag, COUNT(*) FROM t1 GROUP BY flag HAVING COUNT(*) > 2",
		// constant predicates
		"SELECT id FROM t1 WHERE 1 = 1",
		"SELECT id FROM t1 WHERE 1 = 0",
		"SELECT id FROM t1 WHERE id = 1 AND 1 = 1",
	}

	for _, q := range queries {
		t.Run(q, func(t *testing.T) {
			opt, _, err := testhelper.OptExecute(ctx, p, cat, q)
			if err != nil {
				t.Fatalf("optimized execution error: %v", err)
			}
			naive, _, err := testhelper.NaiveExecute(ctx, cat, q)
			if err != nil {
				t.Fatalf("naive execution error: %v", err)
			}
			if !testhelper.RowsEqual(opt, naive) {
				t.Errorf("result mismatch\n  optimized: %v\n  naive:     %v", opt, naive)
			}
		})
	}
}
