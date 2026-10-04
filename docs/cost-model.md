# Cost Model & Determinism

## Cost model

All costs are relative, unit-less numbers used only to compare plans. The cost
weights are configurable (see [configuration](config.md)).

### Sequential scan

```
cost = estimatedRows × SeqScanPerRow
```

`estimatedRows` is the number of rows after pushed filters (for a plain scan,
the table row count).

### Index scan

```
cost = IndexSetup + selectedRows × IndexPerRow
```

`selectedRows = tableRows × selectivity`. The index is a hash index; one
equality lookup.

### Filter

```
cost = childCost + inputRows × SeqScanPerRow
outputRows = inputRows × selectivity
```

### Project

```
cost = childCost + inputRows × ProjectPerRow
```

### Hash join

```
cost = leftCost + rightCost
       + buildRows × HashJoinBuildPerRow
       + probeRows × HashJoinProbePerRow
outputRows = leftRows × rightRows × joinSelectivity
```

The build side is the smaller input (by estimated row count). For LEFT JOINs
the build side is always the right (null-supplying) input.

### Nested-loop join

```
cost = leftCost + rightCost + leftRows × rightRows × NLJPerPair
outputRows = leftRows × rightRows × joinSelectivity
```

Used for non-equi join conditions and cross joins.

### Hash aggregate

```
cost = childCost + inputRows × AggPerRow
outputRows = 1 (no GROUP BY) or max(1, inputRows / 10) (with GROUP BY)
```

### Sort

```
cost = childCost + inputRows × log2(inputRows) × SortPerRow
```

### Limit

```
cost = childCost
```

## Selectivity estimation

Selectivity is the fraction of input rows expected to satisfy a predicate.

| Predicate | Selectivity | Source |
|---|---|---|
| `col = const` | `1 / NDV` (or histogram frequency) | column stats |
| `col != const` | `1 - eqSelectivity` | derived |
| `col > const` | `(max - const) / (max - min)` clamped | min/max stats |
| `col BETWEEN a AND b` | range overlap fraction | min/max stats |
| `col IS NULL` | `nulls / rows` | null count |
| `col IN (...)` | sum of value selectivities (capped at 1) | NDV / histogram |
| `col LIKE 'prefix%'` | 0.1 (conservative) | default |
| `col LIKE '%x%'` | 0.05 (conservative) | default |
| `p AND q` | `sel(p) × sel(q)` | independence assumption |
| `p OR q` | `sel(p) + sel(q) - sel(p)×sel(q)` | derived |
| `NOT p` | `1 - sel(p)` | derived |
| equi-join `a.x = b.y` | `1 / max(NDV(a.x), NDV(b.y))` | join column stats |
| non-equi join | `RangeSelectivity` (0.33) | default |

### Missing statistics: degraded mode

When a table or column has no statistics, the planner uses conservative
defaults and records a degraded warning:

| Default | Value |
|---|---|
| `EqSelectivity` | 0.1 |
| `RangeSelectivity` | 0.33 |
| `NullSelectivity` | 0.05 |
| `DefaultNDV` | 100 |

Every use of a default is reported in the plan's `Degraded` list and in the
EXPLAIN output under "Degraded Statistics Warnings". The planner never silently
applies a wrong cost: the warning is always observable.

## Deterministic tie-breaking

The planner guarantees that **the same query with the same statistics always
produces the same plan**, regardless of execution order or concurrency.

### Sources of nondeterminism and how they are eliminated

1. **Map iteration order.** The planner never iterates a Go map to make a
   planning decision. All maps are lookups only. Subsets are enumerated by
   increasing bitmask; relations are indexed by sorted alias.

2. **Join orientation.** For each join, both orientations are costed. The
   cheaper orientation wins; on a tie, the lexicographically smaller canonical
   signature wins.

3. **DP subset enumeration.** Subsets are enumerated in increasing bitmask
   order. Split points are enumerated deterministically. The best plan per
   subset is kept; ties are broken by canonical signature.

4. **Predicate canonicalization.** Join predicates are canonicalized by
   sorting commutative operands (`a.x = b.y` and `b.y = a.x` compare equal).

### Canonical plan signature

Each plan has a recursive signature:

```
seqscan(alias)
indexscan(alias,column)
filter(canonicalPred, childSig)
project(childSig)
hj(buildSig|probeSig, canonicalCond)
nlj(leftSig,rightSig,canonicalCond)
agg(childSig)
sort(childSig)
limit(childSig)
```

When two plans have costs within `CostEpsilon` (default 1e-9 relative), the
plan with the lexicographically smaller signature is chosen. This rule is
fixed and documented; it does not depend on table creation order, statistics
collection order, or goroutine scheduling.

### Verifying determinism

The test suite plans the same query 30 times with fresh planners and asserts
identical EXPLAIN output. It also re-collects statistics in reverse table order
and asserts the plan is unchanged.
