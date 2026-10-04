package sql_test

import (
	"testing"

	"github.com/guoyyit/query-optimizer-go/sql"
)

func TestParserBasic(t *testing.T) {
	queries := []string{
		"SELECT 1",
		"SELECT id FROM t",
		"SELECT a, b FROM t WHERE a > 1",
		"SELECT * FROM t",
		"SELECT t.id FROM t WHERE t.id = 1",
		"SELECT a FROM t1 JOIN t2 ON t1.id = t2.t1_id",
		"SELECT a FROM t1 LEFT JOIN t2 ON t1.id = t2.t1_id",
		"SELECT a, COUNT(*) FROM t GROUP BY a",
		"SELECT a FROM t ORDER BY a DESC LIMIT 10",
		"SELECT DISTINCT a FROM t",
		"SELECT a FROM t WHERE a IN (1, 2, 3)",
		"SELECT a FROM t WHERE a BETWEEN 1 AND 10",
		"SELECT a FROM t WHERE a IS NULL",
		"SELECT a FROM t WHERE name LIKE 'x%'",
		"SELECT a FROM t WHERE NOT (a = 1)",
		"SELECT a + 1, a * 2 FROM t",
		"SELECT AVG(DISTINCT a) FROM t",
	}
	for _, q := range queries {
		sel, err := sql.Parse(q)
		if err != nil {
			t.Errorf("parse %q: %v", q, err)
			continue
		}
		if sel == nil {
			t.Errorf("parse %q: nil select", q)
		}
	}
}

func TestParserErrors(t *testing.T) {
	bad := []string{
		"SELECT FROM t",
		"SELECT * FROM",
		"SELECT a FROM t WHERE",
		"SELECT a FROM t JOIN",
		"SELECT a FROM t GROUP BY",
	}
	for _, q := range bad {
		_, err := sql.Parse(q)
		if err == nil {
			t.Errorf("expected error for %q", q)
		}
	}
}

func TestParserStarAndMultiply(t *testing.T) {
	// * in select list is a star; * in expression is multiplication
	sel, err := sql.Parse("SELECT a * 2 FROM t")
	if err != nil {
		t.Fatal(err)
	}
	if len(sel.Select) != 1 {
		t.Fatalf("expected 1 select item, got %d", len(sel.Select))
	}
	if _, ok := sel.Select[0].(*sql.BinOp); !ok {
		t.Errorf("expected BinOp for multiplication, got %T", sel.Select[0])
	}

	sel, err = sql.Parse("SELECT * FROM t")
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := sel.Select[0].(*sql.Star); !ok {
		t.Errorf("expected Star, got %T", sel.Select[0])
	}
}
