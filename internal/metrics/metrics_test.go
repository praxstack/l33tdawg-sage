package metrics

import (
	"testing"

	"github.com/prometheus/client_golang/prometheus/testutil"
)

// SetPoEWeights must publish the gauge for each validator and, via Reset(),
// prune series for validators absent from the new set — so a governance-removed
// validator does not leave a stale, misleading sage_poe_weight series behind.
func TestSetPoEWeights_PublishesAndPrunes(t *testing.T) {
	t.Cleanup(PoEWeight.Reset)

	SetPoEWeights(map[string]float64{"valA": 0.4, "valB": 0.6})

	if got := testutil.ToFloat64(PoEWeight.WithLabelValues("valA")); got != 0.4 {
		t.Fatalf("valA gauge = %v, want 0.4", got)
	}
	if got := testutil.ToFloat64(PoEWeight.WithLabelValues("valB")); got != 0.6 {
		t.Fatalf("valB gauge = %v, want 0.6", got)
	}
	if n := testutil.CollectAndCount(PoEWeight); n != 2 {
		t.Fatalf("series count = %d, want 2", n)
	}

	// Next epoch drops valB (e.g. removed via governance) and reweights valA.
	SetPoEWeights(map[string]float64{"valA": 0.5})

	if got := testutil.ToFloat64(PoEWeight.WithLabelValues("valA")); got != 0.5 {
		t.Fatalf("valA gauge after reweight = %v, want 0.5", got)
	}
	if n := testutil.CollectAndCount(PoEWeight); n != 1 {
		t.Fatalf("series count after prune = %d, want 1 (valB must be gone)", n)
	}
}
