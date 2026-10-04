package plan

import (
	"strings"

	"github.com/guoyyit/query-optimizer-go/rows"
	"github.com/guoyyit/query-optimizer-go/sql"
)

// resolveColumn finds a column in a schema, optionally qualified by alias.
func resolveColumn(schema rows.Schema, ref *sql.ColumnRef) (int, bool) {
	if ref.Table != "" {
		return schema.IndexQualified(ref.Table, ref.Name)
	}
	if schema.HasAmbiguous(ref.Name) {
		return -1, false
	}
	return schema.Index(ref.Name)
}

// InferType infers the output type of an expression against a child schema.
func InferType(e sql.Expr, schema rows.Schema) rows.Type {
	switch x := e.(type) {
	case *sql.Literal:
		return literalType(x)
	case *sql.ColumnRef:
		if idx, ok := resolveColumn(schema, x); ok && idx >= 0 {
			return schema.Columns[idx].Type
		}
		return rows.TypeString
	case *sql.BinOp:
		switch strings.ToUpper(x.Op) {
		case "=", "<>", "!=", "<", ">", "<=", ">=", "AND", "OR":
			return rows.TypeBool
		}
		lt := InferType(x.Left, schema)
		rt := InferType(x.Right, schema)
		if lt == rows.TypeFloat || rt == rows.TypeFloat {
			return rows.TypeFloat
		}
		return rows.TypeInt
	case *sql.UnaryOp:
		if x.Op == "NOT" {
			return rows.TypeBool
		}
		return InferType(x.Expr, schema)
	case *sql.Aggregate:
		return AggType(x, schema)
	case *sql.Alias:
		return InferType(x.Expr, schema)
	case *sql.InList, *sql.Between, *sql.IsNull, *sql.Like:
		return rows.TypeBool
	}
	return rows.TypeString
}

func literalType(l *sql.Literal) rows.Type {
	switch l.Kind {
	case sql.LitInt:
		return rows.TypeInt
	case sql.LitFloat:
		return rows.TypeFloat
	case sql.LitString:
		return rows.TypeString
	case sql.LitBool:
		return rows.TypeBool
	}
	return rows.TypeNull
}

// AggType infers the output type of an aggregate.
func AggType(a *sql.Aggregate, schema rows.Schema) rows.Type {
	switch strings.ToUpper(a.Name) {
	case "COUNT":
		return rows.TypeInt
	case "SUM":
		if a.Expr != nil {
			t := InferType(a.Expr, schema)
			if t == rows.TypeFloat {
				return rows.TypeFloat
			}
		}
		return rows.TypeInt
	case "AVG":
		return rows.TypeFloat
	case "MIN", "MAX":
		if a.Expr != nil {
			return InferType(a.Expr, schema)
		}
	}
	return rows.TypeString
}
