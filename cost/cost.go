// Package cost implements the deterministic cost model.
//
// Determinism contract (documented in docs/OPTIMIZER.md): plans are
// compared by (cost, fingerprint); equal costs are always resolved by the
// lexicographically smaller plan fingerprint, so the same query under the
// same statistics picks the same plan regardless of execution order or
// concurrency.
package cost

import (
	"fmt"
	"math"
	"strings"
)

// Weights are the configurable cost coefficients.
type Weights struct {
	SeqScanRow   float64 // per-row cost of a sequential scan
	IndexProbe   float64 // fixed cost of one hash-index probe
	IndexRow     float64 // per-row cost of rows fetched via index
	FilterRow    float64 // per-row predicate evaluation
	HashBuildRow float64 // per-row cost of building a hash table
	HashProbeRow float64 // per-row cost of probing
	NestedLoop   float64 // per row-pair cost of a nested-loop join
	OutputRow    float64 // per output row
}

// DefaultWeights returns the documented default coefficients.
func DefaultWeights() Weights {
	return Weights{
		SeqScanRow:   1.0,
		IndexProbe:   8.0,
		IndexRow:     0.5,
		FilterRow:    0.1,
		HashBuildRow: 1.5,
		HashProbeRow: 0.5,
		NestedLoop:   1.0,
		OutputRow:    0.05,
	}
}

// Validate rejects negative or NaN weights, naming the offending field.
func (w Weights) Validate() error {
	fields := []struct {
		name string
		v    float64
	}{
		{"SeqScanRow", w.SeqScanRow},
		{"IndexProbe", w.IndexProbe},
		{"IndexRow", w.IndexRow},
		{"FilterRow", w.FilterRow},
		{"HashBuildRow", w.HashBuildRow},
		{"HashProbeRow", w.HashProbeRow},
		{"NestedLoop", w.NestedLoop},
		{"OutputRow", w.OutputRow},
	}
	for _, f := range fields {
		if math.IsNaN(f.v) || math.IsInf(f.v, 0) {
			return fmt.Errorf("cost weight %s must be finite, got %v", f.name, f.v)
		}
		if f.v < 0 {
			return fmt.Errorf("cost weight %s must be >= 0, got %v", f.name, f.v)
		}
	}
	return nil
}

// Compare returns -1/0/+1 comparing (cost, fingerprint) pairs. The
// fingerprint comparison makes cost ties deterministic.
func Compare(costA float64, fpA string, costB float64, fpB string) int {
	if costA < costB {
		return -1
	}
	if costA > costB {
		return 1
	}
	return strings.Compare(fpA, fpB)
}
