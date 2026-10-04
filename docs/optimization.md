# Optimization Semantics

This document describes the rewrite rules, what they guarantee, and the cases
where a rule cannot be applied (and how that is reported).

## Pipeline

```
SQL text
  │  sql.Parse
  ▼
Logical plan (tree of Scan/Filter/Project/Join/Aggregate/Sort/Limit)
  │  ApplyRules
  │    1. constant_folding
  │    2. predicate_pushdown
  ▼
Rewritten logical plan
  │  physicalPlanner.plan
  │    - join reorder (DPsub) for inner-join subtrees
  │    - access method selection (seq scan vs index scan)
  │    - projection pruning
  ▼
Physical plan with costs
```

## Rules

### 1. Constant folding (`constant_folding`)

Evaluates constant subexpressions at plan time:

- Arithmetic on literals (`1 + 2` → `3`).
- Comparisons of literals (`1 = 1` → `true`).
- Boolean simplification (`true AND x` → `x`, `false OR x` → `x`).
- `NOT true` → `false`, unary minus on literals.

If a filter folds to `true`, the filter is removed. If it folds to `false`,
the filter is kept (the subtree yields no rows) and a note is recorded.

**Cannot fold:** expressions containing column references, aggregates, or
non-deterministic functions.

### 2. Predicate pushdown (`predicate_pushdown`)

Splits each filter into conjuncts (AND-ed predicates) and pushes each conjunct
as far down the tree as possible:

- **Single-table predicates** are pushed to the scan of that table (through
  inner joins). This is always safe for inner joins.
- **Two-table predicates** on a cross join are converted to inner-join
  conditions (`CROSS JOIN` → `INNER JOIN`).
- **Two-table predicates** on an existing inner join are attached as join
  conditions.
- **Three-or-more-table predicates** stay above the join.

For **LEFT JOINs**, pushdown is restricted to preserve NULL semantics:

- `ON` predicates referencing only the left side are pushed to the left scan.
- `ON` predicates referencing only the right side are pushed to the right scan.
- `ON` predicates referencing both sides stay as the join condition.
- `WHERE` predicates are **not** pushed through a LEFT JOIN (they would change
  NULL-padded rows). A note is recorded.

**Cannot push down (reported in EXPLAIN):**

- Predicates referencing columns from both sides of a LEFT JOIN in WHERE.
- Predicates above aggregates (HAVING with aggregates stay above the
  aggregate; group-by predicates could be pushed but are kept for clarity).
- Constant predicates with no table references stay as filters.

### 3. Projection pruning (`projection_pruning`)

After physical planning, a top-down pass computes the set of columns required
by each operator (from SELECT expressions, WHERE predicates, join conditions,
GROUP BY keys, ORDER BY keys). Each scan then emits only the required columns.

- Columns needed only by a filter or join condition are included.
- `COUNT(*)` with no column references emits zero columns (rows still flow).
- `SELECT *` expands to all columns of the referenced tables.

Pruning is a physical-level optimization; the logical plan is unchanged.

### 4. Join reorder (`join_reorder`)

Inner-join subtrees are reordered using dynamic programming over subsets
(DPsub). For each subset of relations, the planner considers every split into
two connected halves and every join orientation (which side builds the hash
table). The cheapest plan is kept.

**Connectedness:** a subset is only joined if there is a join condition (edge)
between its two halves. This guarantees that connected join graphs never
produce Cartesian products.

**Disconnected graphs:** if the join graph has multiple connected components
(no join conditions between them), the components are planned independently and
then cross-joined in deterministic order. This is reported in EXPLAIN:

```
[join_reorder] disconnected join graph; Cartesian product between {a} and {c} is unavoidable
```

**Left joins** are never reordered (they preserve NULL semantics). Cross joins
from comma-separated tables are converted to inner joins when an equi-predicate
exists; otherwise they remain as nested-loop cross joins.

**Cannot reorder (reported in EXPLAIN):**

- LEFT JOINs (NULL-supplying side must be preserved).
- Subtrees containing aggregates or filters above joins (not part of the
  inner-join graph).

### 5. Access method selection

For each scan with a pushed equality predicate on an indexed column, the
planner compares:

- **Sequential scan:** `rows × SeqScanPerRow`
- **Index scan:** `IndexSetup + selectedRows × IndexPerRow`

The cheaper access method is chosen. Indexes are hash indexes built lazily from
the in-process table data. Index selection can be disabled with
`rules.index_scan = false`.

## Semantic guarantees

- **NULL semantics:** all predicate evaluation uses SQL three-valued logic
  (TRUE/FALSE/UNKNOWN). Only TRUE satisfies a filter. NULL propagates through
  arithmetic. LEFT JOINs NULL-pad unmatched rows.
- **Type coercion:** int and float are compared numerically. Values of
  incompatible types are ordered by a fixed type rank for ORDER BY (deterministic)
  and treated as unequal for equality predicates.
- **Duplicate column names:** qualified references (`alias.col`) resolve
  unambiguously. Unqualified references that match multiple columns produce an
  "ambiguous column" error.
- **Boundary values:** min/max int64, empty strings, and zero are handled
  without overflow (division by zero yields NULL).

## EXPLAIN output

`EXPLAIN` (via `Result.Explain`) contains:

1. **Physical plan tree** with operator type, key parameters, estimated rows,
   and cost.
2. **Total cost** of the plan.
3. **Rewrite rules** section: each rule that fired and what it did, including
   cases where a rule could not be applied.
4. **Degraded statistics warnings** section: every reason a conservative default
   was used instead of real statistics.
