// Package config defines planner configuration: rule toggles, cost weights
// and conservative defaults for missing statistics.
package config

import (
	"encoding/json"
	"fmt"
	"math"
	"os"
	"sort"
	"strings"
)

// Rules groups the on/off switches for every rewrite rule.
type Rules struct {
	PredicatePushdown bool `json:"predicate_pushdown"`
	ProjectionPruning bool `json:"projection_pruning"`
	JoinReorder       bool `json:"join_reorder"`
	ConstantFolding   bool `json:"constant_folding"`
	IndexScan         bool `json:"index_scan"`
}

// Weights groups cost-model weights. All weights must be non-negative.
type Weights struct {
	SeqScanPerRow       float64 `json:"seq_scan_per_row"`
	IndexSetup          float64 `json:"index_setup"`
	IndexPerRow         float64 `json:"index_per_row"`
	HashJoinBuildPerRow float64 `json:"hash_join_build_per_row"`
	HashJoinProbePerRow float64 `json:"hash_join_probe_per_row"`
	NLJPerPair          float64 `json:"nlj_per_pair"`
	SortPerRow          float64 `json:"sort_per_row"`
	AggPerRow           float64 `json:"agg_per_row"`
	ProjectPerRow       float64 `json:"project_per_row"`
}

// Defaults holds conservative assumptions used when statistics are missing.
type Defaults struct {
	EqSelectivity    float64 `json:"eq_selectivity"`
	RangeSelectivity float64 `json:"range_selectivity"`
	NullSelectivity  float64 `json:"null_selectivity"`
	DefaultNDV       int64   `json:"default_ndv"`
}

// Config is the full planner configuration.
type Config struct {
	Rules       Rules   `json:"rules"`
	Weights     Weights `json:"weights"`
	Defaults    Defaults `json:"defaults"`
	PlanCache   bool    `json:"plan_cache"`
	CostEpsilon float64 `json:"cost_epsilon"` // tie tolerance
}

// Default returns the default configuration with all rules enabled.
func Default() *Config {
	return &Config{
		Rules: Rules{
			PredicatePushdown: true,
			ProjectionPruning: true,
			JoinReorder:       true,
			ConstantFolding:   true,
			IndexScan:         true,
		},
		Weights: Weights{
			SeqScanPerRow:       1.0,
			IndexSetup:          5.0,
			IndexPerRow:         2.0,
			HashJoinBuildPerRow: 1.0,
			HashJoinProbePerRow: 0.8,
			NLJPerPair:          1.0,
			SortPerRow:          1.5,
			AggPerRow:           1.0,
		},
		Defaults: Defaults{
			EqSelectivity:    0.1,
			RangeSelectivity: 0.33,
			NullSelectivity:  0.05,
			DefaultNDV:       100,
		},
		PlanCache:   true,
		CostEpsilon: 1e-9,
	}
}

// ConfigErrorKind distinguishes configuration failure reasons.
type ConfigErrorKind int

const (
	// ErrKindInvalid is a value out of range (negative weight, bad selectivity...).
	ErrKindInvalid ConfigErrorKind = iota
	// ErrKindUnknownRule is a rule name that does not exist.
	ErrKindUnknownRule
	// ErrKindParse is a malformed configuration file.
	ErrKindParse
)

// ConfigError describes a configuration failure with a distinguishable kind.
type ConfigError struct {
	Kind   ConfigErrorKind
	Field  string
	Reason string
}

func (e *ConfigError) Error() string {
	return fmt.Sprintf("config: %s: %s (%s)", e.Field, e.Reason, kindName(e.Kind))
}

func kindName(k ConfigErrorKind) string {
	switch k {
	case ErrKindInvalid:
		return "invalid value"
	case ErrKindUnknownRule:
		return "unknown rule"
	case ErrKindParse:
		return "parse error"
	}
	return "unknown"
}

// Is allows errors.Is to match the kind.
func (e *ConfigError) Is(target error) bool {
	t, ok := target.(*ConfigError)
	if !ok {
		return false
	}
	return e.Kind == t.Kind
}

func invalid(field, reason string) error {
	return &ConfigError{Kind: ErrKindInvalid, Field: field, Reason: reason}
}

// Validate checks the configuration and returns a *ConfigError on failure.
func (c *Config) Validate() error {
	w := c.Weights
	checks := []struct {
		name string
		v    float64
	}{
		{"weights.seq_scan_per_row", w.SeqScanPerRow},
		{"weights.index_setup", w.IndexSetup},
		{"weights.index_per_row", w.IndexPerRow},
		{"weights.hash_join_build_per_row", w.HashJoinBuildPerRow},
		{"weights.hash_join_probe_per_row", w.HashJoinProbePerRow},
		{"weights.nlj_per_pair", w.NLJPerPair},
		{"weights.sort_per_row", w.SortPerRow},
		{"weights.agg_per_row", w.AggPerRow},
		{"weights.project_per_row", w.ProjectPerRow},
		{"cost_epsilon", c.CostEpsilon},
	}
	for _, ch := range checks {
		if math.IsNaN(ch.v) || math.IsInf(ch.v, 0) || ch.v < 0 {
			return invalid(ch.name, "must be a finite non-negative number")
		}
	}
	d := c.Defaults
	if d.EqSelectivity <= 0 || d.EqSelectivity > 1 {
		return invalid("defaults.eq_selectivity", "must be in (0, 1]")
	}
	if d.RangeSelectivity <= 0 || d.RangeSelectivity > 1 {
		return invalid("defaults.range_selectivity", "must be in (0, 1]")
	}
	if d.NullSelectivity < 0 || d.NullSelectivity >= 1 {
		return invalid("defaults.null_selectivity", "must be in [0, 1)")
	}
	if d.DefaultNDV <= 0 {
		return invalid("defaults.default_ndv", "must be positive")
	}
	return nil
}

// ruleNames returns the known rule names in deterministic order.
func ruleNames() []string {
	return []string{
		"predicate_pushdown",
		"projection_pruning",
		"join_reorder",
		"constant_folding",
		"index_scan",
	}
}

// SetRule toggles a rule by name. Unknown names return ErrKindUnknownRule.
func (c *Config) SetRule(name string, enabled bool) error {
	switch name {
	case "predicate_pushdown":
		c.Rules.PredicatePushdown = enabled
	case "projection_pruning":
		c.Rules.ProjectionPruning = enabled
	case "join_reorder":
		c.Rules.JoinReorder = enabled
	case "constant_folding":
		c.Rules.ConstantFolding = enabled
	case "index_scan":
		c.Rules.IndexScan = enabled
	default:
		return &ConfigError{Kind: ErrKindUnknownRule, Field: "rules." + name, Reason: "no such rule; known rules: " + strings.Join(ruleNames(), ", ")}
	}
	return nil
}

// RuleEnabled reports whether the named rule is enabled.
func (c *Config) RuleEnabled(name string) (bool, error) {
	switch name {
	case "predicate_pushdown":
		return c.Rules.PredicatePushdown, nil
	case "projection_pruning":
		return c.Rules.ProjectionPruning, nil
	case "join_reorder":
		return c.Rules.JoinReorder, nil
	case "constant_folding":
		return c.Rules.ConstantFolding, nil
	case "index_scan":
		return c.Rules.IndexScan, nil
	}
	return false, &ConfigError{Kind: ErrKindUnknownRule, Field: "rules." + name, Reason: "no such rule; known rules: " + strings.Join(ruleNames(), ", ")}
}

// Hash returns a deterministic hash of the configuration for cache keys.
func (c *Config) Hash() string {
	b, err := json.Marshal(c)
	if err != nil {
		return "default"
	}
	return fmt.Sprintf("%x", b)
}

// LoadJSON reads a JSON configuration file. Missing fields fall back to
// defaults.
func LoadJSON(path string) (*Config, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, &ConfigError{Kind: ErrKindParse, Field: path, Reason: err.Error()}
	}
	cfg := Default()
	if err := json.Unmarshal(data, cfg); err != nil {
		return nil, &ConfigError{Kind: ErrKindParse, Field: path, Reason: err.Error()}
	}
	if err := cfg.Validate(); err != nil {
		return nil, err
	}
	return cfg, nil
}

// LoadJSONBytes parses JSON configuration bytes.
func LoadJSONBytes(data []byte) (*Config, error) {
	cfg := Default()
	if err := json.Unmarshal(data, cfg); err != nil {
		return nil, &ConfigError{Kind: ErrKindParse, Field: "<bytes>", Reason: err.Error()}
	}
	if err := cfg.Validate(); err != nil {
		return nil, err
	}
	return cfg, nil
}

// SortedRuleNames returns known rule names sorted.
func SortedRuleNames() []string {
	r := ruleNames()
	sort.Strings(r)
	return r
}
