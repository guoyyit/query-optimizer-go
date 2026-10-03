package optimizer

import (
	"time"

	"github.com/guoyyit/query-optimizer-go/cost"
	"github.com/guoyyit/query-optimizer-go/rules"
)

// Config controls the optimizer. See docs/OPTIMIZER.md for semantics.
type Config struct {
	Rules       rules.Toggles
	Weights     cost.Weights
	StatsMaxAge time.Duration // older stats are treated as missing
	DefaultRows int64         // conservative row estimate when stats missing
	DefaultNDV  int64         // conservative NDV estimate when stats missing
	CacheSize   int           // max cached plans; 0 disables caching
	MaxPlans    int           // max concurrent planning passes; <=0 means 4
	PlanTimeout time.Duration // per-query planning timeout; 0 disables
}

// ConfigError identifies exactly which configuration field is invalid.
type ConfigError struct {
	Field  string
	Reason string
}

func (e *ConfigError) Error() string { return "config: " + e.Field + ": " + e.Reason }

// DefaultConfig returns a fully enabled, documented configuration.
func DefaultConfig() Config {
	return Config{
		Rules:       rules.DefaultToggles(),
		Weights:     cost.DefaultWeights(),
		StatsMaxAge: 5 * time.Minute,
		DefaultRows: 1000,
		DefaultNDV:  10,
		CacheSize:   128,
		MaxPlans:    4,
		PlanTimeout: 10 * time.Second,
	}
}

// Validate checks the config, returning a *ConfigError naming the first
// invalid field.
func (c Config) Validate() error {
	if err := c.Weights.Validate(); err != nil {
		return &ConfigError{Field: "Weights", Reason: err.Error()}
	}
	if c.StatsMaxAge < 0 {
		return &ConfigError{Field: "StatsMaxAge", Reason: "must be >= 0"}
	}
	if c.DefaultRows <= 0 {
		return &ConfigError{Field: "DefaultRows", Reason: "must be > 0"}
	}
	if c.DefaultNDV <= 0 {
		return &ConfigError{Field: "DefaultNDV", Reason: "must be > 0"}
	}
	if c.CacheSize < 0 {
		return &ConfigError{Field: "CacheSize", Reason: "must be >= 0"}
	}
	if c.PlanTimeout < 0 {
		return &ConfigError{Field: "PlanTimeout", Reason: "must be >= 0"}
	}
	return nil
}
