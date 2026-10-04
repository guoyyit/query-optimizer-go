# Query Optimizer & Execution Planner

A self-contained, in-process query optimizer and execution planner for a small
SQL subset. The whole pipeline runs inside the process: queries are parsed into
logical plans, rewritten by rules (predicate pushdown, projection pruning,
constant folding, join reordering), converted to cost-based physical plans,
and executed with a volcano-style iterator engine. Input data comes from
in-process generated tables or local CSV files; no external service is used.

## Features

- **Declarative SQL parsing** → logical plan (`sql`, `plan/builder.go`)
- **Rule-based rewrite**: predicate pushdown, projection pruning, constant
  folding, cross-to-inner join conversion (`plan/rules.go`)
- **Cost-based join reordering** via dynamic programming (DPsub) with
  deterministic tie-breaking (`plan/physical_plan.go`)
- **Access method selection**: sequential scan vs. hash index scan based on
  cost (`plan/physical_plan.go`)
- **Hash join** (inner/left) and **nested-loop join** with automatic build-side
  selection (`exec/executor.go`)
- **Plan cache** with atomic invalidation on statistics, schema, or config
  changes (`plancache`, `plan/planner.go`)
- **Degraded mode** with observable markers when statistics are missing
  (`stats`, `plan/explain.go`)
- **Concurrent planning & execution** with context-based cancellation; canceled
  queries leave no partial results and do not pollute the plan cache
- **Configurable rule toggles and cost weights** with distinguishable
  configuration errors (`config`)
- **Readable EXPLAIN** output with rewrite notes and degraded warnings
- **Semantic equivalence**: optimized results are verified against a naive
  full-Cartesian-product reference implementation, including NULLs, type
  coercion, boundary values, and duplicate column names

## Quick start

```go
cat := catalog.New()
cat.AddTable(catalog.NewTable("t", schema, rows))
_ = cat.SetStats("t", stats.Collect("t", schema, rows))

cfg := config.Default()
p := plan.NewPlanner(cat, cfg)
p.SetCache(plancache.New())

res, err := p.Plan(ctx, "SELECT id FROM t WHERE id = 1")
fmt.Println(res.Explain)
out, err := exec.Execute(ctx, res.Physical, cat)
```

Run the demo:

```sh
go run .
```

## Project layout

| Package | Responsibility |
|---|---|
| `rows` | Scalar values, rows, schemas, SQL three-valued logic |
| `sql` | Lexer, parser, AST, expression utilities |
| `catalog` | In-process table registry, CSV loading, versioning |
| `stats` | Statistics collection, selectivity estimation |
| `config` | Rule toggles, cost weights, validation |
| `plan` | Logical/physical plans, rules, cost model, planner, EXPLAIN |
| `plan/physical` | Physical operator node types |
| `plancache` | Versioned plan cache |
| `exec` | Volcano iterator engine, naive reference executor |
| `testhelper` | Shared test builders |

## Documentation

- [Optimization semantics](docs/optimization.md)
- [Cost model & tie-breaking](docs/cost-model.md)
- [Configuration](docs/config.md)

## SQL subset

```sql
SELECT [DISTINCT] select_list
FROM table_ref [, table_ref ...]
[JOIN table_ref ON expr ...]
[WHERE predicate]
[GROUP BY expr [, ...]]
[HAVING predicate]
[ORDER BY expr [ASC|DESC] [, ...]]
[LIMIT n [OFFSET m]]
```

Supported expressions: column references (qualified or unqualified), literals,
arithmetic (`+ - * /`), comparisons (`= <> < > <= >=`), `AND/OR/NOT`,
`IS [NOT] NULL`, `[NOT] IN (...)`, `[NOT] BETWEEN ... AND ...`,
`[NOT] LIKE` (`%` and `_` wildcards), and aggregates
(`COUNT/SUM/AVG/MIN/MAX`, with `DISTINCT`).
