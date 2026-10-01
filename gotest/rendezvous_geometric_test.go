// Mirrors rendezvous_harmonic_test.go exactly, for hash-decay geometric:
// proves t_i = threshold * rate^i (rdvz_pick_priority in src/lb_rdvz.c) by
// statistically validating the exact per-rank acceptance formula, and
// confirming the result is NOT also consistent with what harmonic decay
// would have predicted with the same threshold/rate.
package gotest

import (
	"math"
	"net/http"
	"testing"
	"time"

	"gotest/internal/haharness"
)

// TestRendezvousSubsetPriority_GeometricDecayMatchesFormula is the
// geometric counterpart of TestRendezvousSubsetPriority_HarmonicDecayMatchesFormula:
// same threshold/rate (50, 0.5), same rank-2/rank-3 isolation technique (see
// rankProbabilities's doc comment), same statistical method - just checked
// against the geometric formula instead, and cross-checked against harmonic
// for contrast.
func TestRendezvousSubsetPriority_GeometricDecayMatchesFormula(t *testing.T) {
	const threshold = 50.0
	const rate = 0.5
	const trials = 4000

	wantRank2, wantRank3 := rankProbabilities(func(i int) float64 { return geometricThreshold(threshold, rate, i) })
	harmRank2, harmRank3 := rankProbabilities(func(i int) float64 { return harmonicThreshold(threshold, rate, i) })
	t.Logf("theoretical P(rank2)=%.4f P(rank3)=%.4f (geometric) vs %.4f/%.4f (harmonic, for contrast)",
		wantRank2, wantRank3, harmRank2, harmRank3)

	backends := haharness.StartBackends(t, numServers, 0)
	cfg := haharness.Config{
		Backends:       backends,
		HashCandidates: priorityY,
		HashSubsetMode: "priority",
		HashThreshold:  int(threshold),
		HashDecayKind:  "geometric",
		HashDecayRate:  "0.5",
		MaxConnPerSrv:  1000, // high enough that saturation never biases the roll
		MaxQueue:       1000,
		ServerTimeout:  "5s",
		QueueTimeout:   "5s",
	}
	cfgPath, frontendAddr, _ := haharness.RenderConfig(t, cfg)

	bin := haharness.BuildHAProxy(t)
	haharness.ValidateConfig(t, bin, cfgPath)
	haharness.Start(t, bin, cfgPath, frontendAddr)

	// Sequential, not concurrent: see harmonic test's identical reasoning.
	client := &http.Client{Timeout: 5 * time.Second}
	counts := map[string]int{}
	for range trials {
		status, id := getTenant(t, client, frontendAddr, priorityTenant)
		if status != http.StatusOK {
			t.Fatalf("request failed: status %d", status)
		}
		counts[id]++
	}

	gotRank2 := float64(counts[priorityRank2]) / trials
	gotRank3 := float64(counts[priorityRank3]) / trials
	t.Logf("observed over %d trials: %v", trials, counts)
	t.Logf("observed P(rank2)=%.4f P(rank3)=%.4f", gotRank2, gotRank3)

	// Same tolerance derivation as the harmonic test: ~4-sigma margin at
	// this sample size, still tighter than the geometric/harmonic gap.
	const tol = 0.025
	if math.Abs(gotRank2-wantRank2) > tol {
		t.Errorf("P(rank2)=%.4f, want %.4f +/- %.3f (geometric formula)", gotRank2, wantRank2, tol)
	}
	if math.Abs(gotRank3-wantRank3) > tol {
		t.Errorf("P(rank3)=%.4f, want %.4f +/- %.3f (geometric formula)", gotRank3, wantRank3, tol)
	}

	if math.Abs(gotRank2-harmRank2) < tol && math.Abs(gotRank3-harmRank3) < tol {
		t.Errorf("observed rates (%.4f, %.4f) are also consistent with harmonic decay (%.4f, %.4f) - test parameters no longer distinguish the two kinds",
			gotRank2, gotRank3, harmRank2, harmRank3)
	}
}
