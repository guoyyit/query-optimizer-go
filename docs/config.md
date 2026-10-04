# Configuration

The planner is configured through a `config.Config` value. All rules can be
toggled and all cost weights can be tuned. Configuration errors are
distinguishable via `errors.As` against `*config.ConfigError`.

## Loading configuration

```go
// defaults
cfg := config.Default()

// from JSON file
cfg, err := config.LoadJSON("planner.json")

// from JSON bytes
cfg, err := config.LoadJSONBytes(data)

// runtime toggle
err := cfg.SetRule("join_reorder", false)
```

Pass the config to the planner:

```go
p := plan.NewPlanner(cat, cfg)
```

Or update at runtime (the config hash changes, so cached plans miss):

```go
err := p.UpdateConfig(cfg)
```

## Rule toggles

| Rule | Default | Effect when disabled |
|---|---|---|
| `predicate_pushdown` | `true` | Filters stay above joins; no cross-to-inner conversion |
| `projection_pruning` | `true` | Scans emit all columns |
| `join_reorder` | `true` | Inner joins keep literal (left-deep) order |
| `constant_folding` | `true` | Constant expressions are not evaluated at plan time |
| `index_scan` | `true` | Only sequential scans are considered |

Unknown rule names return `ErrKindUnknownRule` with the list of known rules.

## Cost weights

| Weight | Default | Meaning |
|---|---|---|
| `seq_scan_per_row` | 1.0 | Cost per row read by a sequential scan |
| `index_setup` | 5.0 | Fixed cost of an index lookup |
| `index_per_row` | 2.0 | Cost per row fetched through an index |
| `hash_join_build_per_row` | 1.0 | Cost per row inserted into the hash table |
| `hash_join_probe_per_row` | 0.8 | Cost per row probed |
| `nlj_per_pair` | 1.0 | Cost per nested-loop pair evaluated |
| `sort_per_row` | 1.5 | Cost per sort comparison |
| `agg_per_row` | 1.0 | Cost per aggregate input row |
| `project_per_row` | 0.2 | Cost per projected row |

All weights must be finite and non-negative. Negative or NaN weights return
`ErrKindInvalid`.

## Conservative defaults (missing statistics)

| Default | Value | Used when |
|---|---|---|
| `eq_selectivity` | 0.1 | Equality predicate with no NDV/histogram |
| `range_selectivity` | 0.33 | Range predicate with no min/max |
| `null_selectivity` | 0.05 | NULL predicate with no null count |
| `default_ndv` | 100 | Join column with no NDV |

Selectivities must be in `(0, 1]` (eq, range) or `[0, 1)` (null).
`default_ndv` must be positive. Out-of-range values return `ErrKindInvalid`.

## Other settings

| Setting | Default | Meaning |
|---|---|---|
| `plan_cache` | `true` | Whether to cache plans |
| `cost_epsilon` | 1e-9 | Relative tolerance for cost-tie detection |

## JSON configuration example

```json
{
  "rules": {
    "predicate_pushdown": true,
    "projection_pruning": true,
    "join_reorder": true,
    "constant_folding": true,
    "index_scan": true
  },
  "weights": {
    "seq_scan_per_row": 1.0,
    "index_setup": 5.0,
    "index_per_row": 2.0,
    "hash_join_build_per_row": 1.0,
    "hash_join_probe_per_row": 0.8,
    "nlj_per_pair": 1.0,
    "sort_per_row": 1.5,
    "agg_per_row": 1.0,
    "project_per_row": 0.2
  },
  "defaults": {
    "eq_selectivity": 0.1,
    "range_selectivity": 0.33,
    "null_selectivity": 0.05,
    "default_ndv": 100
  },
  "plan_cache": true,
  "cost_epsilon": 1e-9
}
```

## Error kinds

| Kind | Cause |
|---|---|
| `ErrKindInvalid` | Weight out of range, selectivity out of range, NDV not positive |
| `ErrKindUnknownRule` | Rule name not recognized |
| `ErrKindParse` | Malformed JSON configuration file |

`ConfigError` supports `errors.Is` for kind matching:

```go
var ce *config.ConfigError
if errors.As(err, &ce) {
    switch ce.Kind {
    case config.ErrKindInvalid:
        // handle invalid value
    case config.ErrKindUnknownRule:
        // handle unknown rule
    }
}
```

## Cache invalidation

The plan cache key includes:

1. **Query hash** — SHA-256 of the normalized SQL text.
2. **Catalog version** — bumped on every `AddTable` or `SetStats`.
3. **Config hash** — deterministic hash of the full configuration.

Any change to statistics, schema, or configuration produces a new key, so a
stale plan can never be reused. Entries are immutable; there is no half-updated
state.
