package config_test

import (
	"errors"
	"testing"

	"github.com/guoyyit/query-optimizer-go/config"
)

func TestValidateNegativeWeight(t *testing.T) {
	cfg := config.Default()
	cfg.Weights.NLJPerPair = -1
	err := cfg.Validate()
	var ce *config.ConfigError
	if !errors.As(err, &ce) {
		t.Fatalf("expected ConfigError, got %v", err)
	}
	if ce.Kind != config.ErrKindInvalid {
		t.Errorf("expected ErrKindInvalid, got %v", ce.Kind)
	}
	if ce.Field != "weights.nlj_per_pair" {
		t.Errorf("expected field weights.nlj_per_pair, got %s", ce.Field)
	}
}

func TestValidateBadSelectivity(t *testing.T) {
	cfg := config.Default()
	cfg.Defaults.EqSelectivity = 1.5
	err := cfg.Validate()
	var ce *config.ConfigError
	if !errors.As(err, &ce) {
		t.Fatalf("expected ConfigError, got %v", err)
	}
	if ce.Kind != config.ErrKindInvalid {
		t.Errorf("expected ErrKindInvalid, got %v", ce.Kind)
	}
}

func TestValidateNegativeNDV(t *testing.T) {
	cfg := config.Default()
	cfg.Defaults.DefaultNDV = 0
	err := cfg.Validate()
	if err == nil {
		t.Error("expected error for zero default NDV")
	}
}

func TestSetRuleUnknown(t *testing.T) {
	cfg := config.Default()
	err := cfg.SetRule("nonexistent_rule", true)
	var ce *config.ConfigError
	if !errors.As(err, &ce) {
		t.Fatalf("expected ConfigError, got %v", err)
	}
	if ce.Kind != config.ErrKindUnknownRule {
		t.Errorf("expected ErrKindUnknownRule, got %v", ce.Kind)
	}
}

func TestSetRuleValid(t *testing.T) {
	cfg := config.Default()
	if err := cfg.SetRule("join_reorder", false); err != nil {
		t.Fatal(err)
	}
	if cfg.Rules.JoinReorder {
		t.Error("join_reorder should be disabled")
	}
	if err := cfg.SetRule("join_reorder", true); err != nil {
		t.Fatal(err)
	}
	if !cfg.Rules.JoinReorder {
		t.Error("join_reorder should be enabled")
	}
}

func TestRuleEnabled(t *testing.T) {
	cfg := config.Default()
	enabled, err := cfg.RuleEnabled("predicate_pushdown")
	if err != nil {
		t.Fatal(err)
	}
	if !enabled {
		t.Error("predicate_pushdown should be enabled by default")
	}
	_, err = cfg.RuleEnabled("bogus")
	if err == nil {
		t.Error("expected error for unknown rule")
	}
}

func TestConfigHashDeterministic(t *testing.T) {
	cfg1 := config.Default()
	cfg2 := config.Default()
	if cfg1.Hash() != cfg2.Hash() {
		t.Error("identical configs should have identical hashes")
	}
	cfg2.Weights.SeqScanPerRow = 999.0
	if cfg1.Hash() == cfg2.Hash() {
		t.Error("different configs should have different hashes")
	}
}

func TestConfigHashChangesOnRuleToggle(t *testing.T) {
	cfg := config.Default()
	h1 := cfg.Hash()
	_ = cfg.SetRule("join_reorder", false)
	h2 := cfg.Hash()
	if h1 == h2 {
		t.Error("hash should change when rule toggles")
	}
}

func TestLoadJSONBytes(t *testing.T) {
	data := []byte(`{
		"rules": {"join_reorder": false},
		"weights": {"seq_scan_per_row": 2.0}
	}`)
	cfg, err := config.LoadJSONBytes(data)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Rules.JoinReorder {
		t.Error("join_reorder should be disabled from JSON")
	}
	if cfg.Weights.SeqScanPerRow != 2.0 {
		t.Errorf("expected seq_scan_per_row=2.0, got %f", cfg.Weights.SeqScanPerRow)
	}
	// defaults should still be present
	if cfg.Defaults.EqSelectivity != 0.1 {
		t.Error("defaults should be preserved from default config")
	}
}

func TestLoadJSONBytesInvalid(t *testing.T) {
	data := []byte(`{"weights": {"nlj_per_pair": -5}}`)
	_, err := config.LoadJSONBytes(data)
	if err == nil {
		t.Error("expected error for negative weight in JSON")
	}
}

func TestLoadJSONBytesMalformed(t *testing.T) {
	data := []byte(`{not valid json`)
	_, err := config.LoadJSONBytes(data)
	var ce *config.ConfigError
	if !errors.As(err, &ce) {
		t.Fatalf("expected ConfigError, got %v", err)
	}
	if ce.Kind != config.ErrKindParse {
		t.Errorf("expected ErrKindParse, got %v", ce.Kind)
	}
}
