package optimizer

import (
	"errors"
	"math"
	"strings"
	"testing"
)

func TestConfigValidationErrors(t *testing.T) {
	cat := chainCatalog(t, 10, 10, 10)
	cases := []struct {
		name  string
		mut   func(*Config)
		field string
	}{
		{"negative default rows", func(c *Config) { c.DefaultRows = -1 }, "DefaultRows"},
		{"zero default ndv", func(c *Config) { c.DefaultNDV = 0 }, "DefaultNDV"},
		{"negative cache", func(c *Config) { c.CacheSize = -5 }, "CacheSize"},
		{"negative stats age", func(c *Config) { c.StatsMaxAge = -1 }, "StatsMaxAge"},
		{"negative timeout", func(c *Config) { c.PlanTimeout = -1 }, "PlanTimeout"},
		{"nan weight", func(c *Config) { c.Weights.SeqScanRow = math.NaN() }, "Weights"},
		{"negative weight", func(c *Config) { c.Weights.HashProbeRow = -0.5 }, "Weights"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cfg := DefaultConfig()
			tc.mut(&cfg)
			_, err := NewEngine(cat, cfg)
			if err == nil {
				t.Fatal("expected config error")
			}
			var ce *ConfigError
			if !errors.As(err, &ce) {
				t.Fatalf("expected *ConfigError, got %T: %v", err, err)
			}
			if !strings.Contains(ce.Field, tc.field) {
				t.Fatalf("error names field %q, want %q: %v", ce.Field, tc.field, ce)
			}
		})
	}
}

func TestCostWeightsChangePlan(t *testing.T) {
	// With hash-build cost set extremely high, nested-loop must win.
	cat := chainCatalog(t, 200, 200, 10)
	cfg := DefaultConfig()
	cfg.Weights.HashBuildRow = 1e9
	cfg.Weights.HashProbeRow = 1e9
	eng, err := NewEngine(cat, cfg)
	if err != nil {
		t.Fatal(err)
	}
	pq, err := eng.Plan(testCtx(), `SELECT t1.id FROM t1 JOIN t2 ON t1.k2 = t2.id`)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(pq.Explain, "NestedLoopJoin") {
		t.Fatalf("expected nested-loop join under huge hash weights:\n%s", pq.Explain)
	}
	// And results stay correct.
	assertEquivalent(t, eng, `SELECT t1.id FROM t1 JOIN t2 ON t1.k2 = t2.id`)
}
