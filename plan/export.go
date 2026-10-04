package plan

import (
	"github.com/guoyyit/query-optimizer-go/rows"
	"github.com/guoyyit/query-optimizer-go/sql"
)

// ProjectSchema builds the output schema of a Project operator.
func ProjectSchema(childSchema rows.Schema, exprs []sql.Expr, names []string) rows.Schema {
	outCols := make([]rows.Column, 0, len(exprs))
	for i, e := range exprs {
		name := names[i]
		if star, ok := e.(*sql.Star); ok {
			for _, c := range childSchema.Columns {
				if star.Table != "" && c.Alias != star.Table {
					continue
				}
				outCols = append(outCols, rows.Column{Name: c.Name, Type: c.Type})
			}
			continue
		}
		outCols = append(outCols, rows.Column{Name: name, Type: InferType(e, childSchema)})
	}
	return rows.Schema{Columns: outCols}
}

// ResolveColumn finds a column in a schema, optionally qualified by alias.
func ResolveColumn(schema rows.Schema, ref *sql.ColumnRef) (int, bool) {
	return resolveColumn(schema, ref)
}

// DefaultColumnName returns the output column name for an expression.
func DefaultColumnName(e sql.Expr) string {
	return defaultColumnName(e)
}
