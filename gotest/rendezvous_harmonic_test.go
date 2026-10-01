// Proves hash-decay harmonic is actually computing
// t_i = threshold / (1 + rate*i), not just "some decay that looks
// plausible" - by statistically validating the exact per-rank acceptance
// formula against a large sample of real requests, and showing the result
// is measurably different from what geometric decay would predict with the
// same threshold/rate (so this isn't just two code paths computing the
// same thing).
package gotest

import (
	"math"
	"net/http"
	"testing"
	"time"

	"gotest/internal/haharness"
)

// harmonicThreshold and geometricThreshold mirror rdvz_pick_priority's two
// branches exactly (src/lb_rdvz.c): rank i is 0-indexed, i=0 is rank 1.
func harmonicThreshold(threshold, rate float64, i int) float64 {
	return threshold / (1.0 + rate*float64(i))
}

func geometricThreshold(threshold, rate float64, i int) float64 {
	return threshold * math.Pow(rate, float64(i))
}

// rankProbabilities returns, given a per-rank threshold function (percent,
// 0-100) over 3 ranks, the probability a fresh request lands on rank 2 and
// rank 3 specifically. These two are unambiguous measurements: unlike rank
// 1 (which can win either via its own roll or via the "none accepted ->
// fallback to rank 1" path), landing on rank 2 or rank 3 can only happen by
// that rank's own roll succeeding, so they isolate the formula cleanly.
func rankProbabilities(tFn func(i int) float64) (pRank2, pRank3 float64) {
	t0, t1, t2 := tFn(0)/100, tFn(1)/100, tFn(2)/100
	pRank2 = (1 - t0) * t1
	pRank3 = (1 - t0) * (1 - t1) * t2
	return pRank2, pRank3
}

// TestRendezvousSubsetPriority_HarmonicDecayMatchesFormula sends a large
// number of independent requests for priorityTenant under
// "hash-decay harmonic 0.5" with hash-threshold 50, and checks the
// empirical fraction landing on rank 2 (s1) and rank 3 (s15) against the
// exact harmonic formula - and confirms that fraction is NOT what geometric
// decay would have produced with the same threshold/rate, proving the
// harmonic branch is genuinely selected and computing its own formula.
func TestRendezvousSubsetPriority_HarmonicDecayMatchesFormula(t *testing.T) {
	const threshold = 50.0
	const rate = 0.5
	const trials = 4000

	wantRank2, wantRank3 := rankProbabilities(func(i int) float64 { return harmonicThreshold(threshold, rate, i) })
	geomRank2, geomRank3 := rankProbabilities(func(i int) float64 { return geometricThreshold(threshold, rate, i) })
	t.Logf("theoretical P(rank2)=%.4f P(rank3)=%.4f (harmonic) vs %.4f/%.4f (geometric, for contrast)",
		wantRank2, wantRank3, geomRank2, geomRank3)

	backends := haharness.StartBackends(t, numServers, 0)
	cfg := haharness.Config{
		Backends:       backends,
		HashCandidates: priorityY,
		HashSubsetMode: "priority",
		HashThreshold:  int(threshold),
		HashDecayKind:  "harmonic",
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

	// Sequential, not concurrent: each request is an independent Bernoulli
	// trial of the roll, and we don't want overlapping in-flight requests
	// perturbing s->served / queueslength between trials.
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

	// Tolerance: with 4000 trials, standard error for p~0.17 is ~0.6pp, so
	// 2.5pp is a comfortable ~4-sigma margin against flakes while still
	// being tighter than the harmonic/geometric gap (~4.2pp on rank2,
	// ~3.6pp on rank3) - the two predictions must stay distinguishable.
	const tol = 0.025
	if math.Abs(gotRank2-wantRank2) > tol {
		t.Errorf("P(rank2)=%.4f, want %.4f +/- %.3f (harmonic formula)", gotRank2, wantRank2, tol)
	}
	if math.Abs(gotRank3-wantRank3) > tol {
		t.Errorf("P(rank3)=%.4f, want %.4f +/- %.3f (harmonic formula)", gotRank3, wantRank3, tol)
	}

	// And confirm it's NOT what geometric would have produced - otherwise
	// a bug that always ran the geometric branch regardless of
	// hash-decay-kind could slip through the tolerance check above if the
	// two predictions were ever close (they aren't here, by construction).
	if math.Abs(gotRank2-geomRank2) < tol && math.Abs(gotRank3-geomRank3) < tol {
		t.Errorf("observed rates (%.4f, %.4f) are also consistent with geometric decay (%.4f, %.4f) - test parameters no longer distinguish the two kinds",
			gotRank2, gotRank3, geomRank2, geomRank3)
	}
}
