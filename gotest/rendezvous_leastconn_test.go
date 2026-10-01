// leastconn down-server failover test, mirroring
// TestRendezvousSubsetPriority_FailoverStaysWithinSubset exactly but for
// hash-subset-mode leastconn. Confirmed via TestDiscoverLeastConnRankOrder
// that the HRW rank order for priorityTenant is identical under both modes
// (the ranking array doesn't depend on hash-subset-mode, only the selection
// rule applied to it does), so priorityRankOrder is reused directly.
package gotest

import (
	"net/http"
	"slices"
	"sync"
	"testing"
	"time"

	"gotest/internal/haharness"
)

func newLeastConnConfig(backends []*haharness.Backend) haharness.Config {
	return haharness.Config{
		Backends:       backends,
		HashCandidates: priorityY,
		HashSubsetMode: "leastconn",
		MaxConnPerSrv:  1000, // no saturation, so argmin ties purely on score order (same rank order as priority)
		MaxQueue:       1000,
		ServerTimeout:  "5s",
		QueueTimeout:   "5s",
	}
}

// TestRendezvousSubsetLeastConn_FailoverStaysWithinSubset walks rank 1 ->
// down, rank 2 -> down, rank 3 -> down via the stats socket, confirming at
// each step that traffic for priorityTenant moves to exactly the next rank
// - never to a different, unranked server among the other 17 - and that
// once all 3 ranks are down, the request fails (503/NOSRV) rather than
// spilling outside the bounded subset. Same core guarantee as the priority
// mode version, proven for the leastconn selection rule
// (rdvz_pick_leastconn in src/lb_rdvz.c) instead.
func TestRendezvousSubsetLeastConn_FailoverStaysWithinSubset(t *testing.T) {
	backends := haharness.StartBackends(t, numServers, 0)
	cfg := newLeastConnConfig(backends)
	cfgPath, frontendAddr, sockPath := haharness.RenderConfig(t, cfg)

	bin := haharness.BuildHAProxy(t)
	haharness.ValidateConfig(t, bin, cfgPath)
	haharness.Start(t, bin, cfgPath, frontendAddr)

	client := &http.Client{Timeout: 5 * time.Second}

	for step, rank := range priorityRankOrder {
		status, id := getTenant(t, client, frontendAddr, priorityTenant)
		if status != http.StatusOK {
			t.Fatalf("step %d: status %d, want 200 (expected rank %s to still be reachable)", step, status, rank)
		}
		if id != rank {
			t.Fatalf("step %d: got %s, want %s - traffic must land on the next rank in order, never a different server", step, id, rank)
		}
		t.Logf("step %d: confirmed traffic on rank %s, taking it down", step, rank)
		haharness.SetServerState(t, sockPath, "be1", rank, "maint")
	}

	// All priorityY ranks are now down. The bounded-subset guarantee means
	// this must fail cleanly, never spill to one of the other 17 servers.
	status, id := getTenant(t, client, frontendAddr, priorityTenant)
	if status != http.StatusServiceUnavailable {
		t.Fatalf("after all %d ranks down: status=%d id=%q, want 503/NOSRV (got a server outside the bounded subset instead of a clean failure)",
			priorityY, status, id)
	}
	t.Logf("SUCCESS: after all %d ranks down, request correctly failed with 503 rather than spilling outside the subset", priorityY)
}

// TestRendezvousSubsetLeastConn_SpreadsLoadWithinSubset proves the
// behavior that actually distinguishes leastconn from priority: when a
// candidate is merely saturated (not down), rdvz_pick_leastconn's weighted
// argmin spreads to the next-least-loaded USABLE candidate, purely driven
// by current load - no roll, no rank bias. With maxconn=1 per server and a
// blocking handler, one in-flight request is enough to saturate its
// server, so firing requests for the same tenant while prior ones are
// still held open forces the selection to move down priorityRankOrder one
// step at a time, and never outside the bounded Y-subset even once all 3
// are simultaneously saturated.
func TestRendezvousSubsetLeastConn_SpreadsLoadWithinSubset(t *testing.T) {
	const blockFor = 2 * time.Second

	backends := haharness.StartBackends(t, numServers, blockFor)
	cfg := haharness.Config{
		Backends:       backends,
		HashCandidates: priorityY,
		HashSubsetMode: "leastconn",
		MaxConnPerSrv:  1, // one in-flight request saturates a server
		MaxQueue:       1000,
		ServerTimeout:  "10s",
		QueueTimeout:   "10s",
	}
	cfgPath, frontendAddr, _ := haharness.RenderConfig(t, cfg)

	bin := haharness.BuildHAProxy(t)
	haharness.ValidateConfig(t, bin, cfgPath)
	haharness.Start(t, bin, cfgPath, frontendAddr)

	client := &http.Client{Timeout: 10 * time.Second}

	// Launch 3 requests staggered just enough (well under blockFor) for
	// each prior one to already be in flight - and therefore counted in
	// its server's "served" - by the time the next one is assigned.
	results := make([]string, 4)
	statuses := make([]int, 4)
	var wg sync.WaitGroup
	for i := range 4 {
		wg.Go(func() {
			statuses[i], results[i] = getTenant(t, client, frontendAddr, priorityTenant)
		})
		time.Sleep(300 * time.Millisecond)
	}
	wg.Wait()

	t.Logf("results in launch order: %v (status: %v)", results, statuses)
	for i, status := range statuses {
		if status != http.StatusOK {
			t.Fatalf("request %d: status %d, want 200", i, status)
		}
	}

	// Requests 0, 1, 2 should land on rank 1, 2, 3 respectively: each
	// prior rank is already saturated (one in-flight request at
	// maxconn=1) by the time the next request is assigned, forcing the
	// argmin to move down the subset one step at a time.
	for i, rank := range priorityRankOrder {
		if results[i] != rank {
			t.Errorf("request %d: got %s, want %s - load should force selection to the next rank in order", i, results[i], rank)
		}
	}

	// Request 3: all 3 ranks are now simultaneously saturated. Must still
	// land within the bounded subset (never one of the other 17 servers),
	// proving the "queue at the argmin" guarantee holds even at full
	// subset saturation.
	if !slices.Contains(priorityRankOrder, results[3]) {
		t.Errorf("request 3 (all ranks saturated): got %s, want one of %v - must stay within the bounded subset even when fully saturated",
			results[3], priorityRankOrder)
	}
	t.Logf("SUCCESS: load spread %v -> %v across the bounded subset, request 3 stayed within it at full saturation",
		priorityRankOrder, results[:3])
}
