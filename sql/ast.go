// Package sql defines the declarative query AST and a parser for a small
// SQL subset:
//
//	SELECT * | col [, col ...] FROM t [alias] [JOIN t [alias] ON cond] ...
//	[WHERE cond]
//
// cond := orExpr; orExpr := andExpr (OR andExpr)*; andExpr := notExpr
// (AND notExpr)*; notExpr := [NOT] primary; primary := '(' cond ')' |
// operand (cmp operand)?; operand := literal | [table '.'] column.
// Literals: integers, floats, 'strings', TRUE, FALSE, NULL.
package sql

import (
	"fmt"
	"strings"

	"github.com/guoyyit/query-optimizer-go/expr"
)

// SelectStmt is a parsed declarative query.
type SelectStmt struct {
	// Star is true for SELECT *.
	Star        bool
	Projections []expr.ColumnRef
	// Tables in FROM order. Joins[i] describes how Tables[i+1] joins.
	Tables []TableRef
	Joins  []JoinClause
	Where  expr.Expr
}

// TableRef names a table and its alias (alias defaults to table name).
type TableRef struct {
	Table string
	Alias string
}

// JoinClause is an INNER JOIN ... ON condition.
type JoinClause struct {
	On expr.Expr
}

// Fingerprint returns a canonical serialization of the query. Two
// structurally identical queries (modulo AND/OR child order) share a
// fingerprint, so the plan cache treats them as one.
func (s *SelectStmt) Fingerprint() string {
	var b strings.Builder
	b.WriteString("SELECT ")
	if s.Star {
		b.WriteString("*")
	} else {
		parts := make([]string, len(s.Projections))
		for i, p := range s.Projections {
			parts[i] = p.String()
		}
		b.WriteString(strings.Join(parts, ","))
	}
	b.WriteString(" FROM ")
	for i, t := range s.Tables {
		if i > 0 {
			fmt.Fprintf(&b, " JOIN ")
		}
		b.WriteString(t.Table)
		if t.Alias != "" && t.Alias != t.Table {
			b.WriteString(" AS " + t.Alias)
		}
		if i > 0 {
			b.WriteString(" ON " + expr.Canonical(s.Joins[i-1].On))
		}
	}
	if s.Where != nil {
		b.WriteString(" WHERE " + expr.Canonical(s.Where))
	}
	return b.String()
}
