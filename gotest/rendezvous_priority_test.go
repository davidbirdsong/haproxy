// Priority-mode ("hash-subset-mode priority") tests. The exact HRW rank
// order below was captured once via TestDiscoverPriorityRankOrder (see
// discover_rank_test.go) against the fixed 20-server set (s1..s20, same
// declaration order every run - haproxy's default "hash-key id" keys off
// declaration order/puid, not the random ports httptest assigns, so this
// mapping is reproducible). Re-run that discovery test and update these
// constants if numServers or the server declaration order ever changes.
package gotest

import (
	"io"
	"net/http"
	"testing"
	"time"

	"gotest/internal/haharness"
)

const (
	priorityTenant = "tenant-priority-1"
	priorityY      = 3

	// Rank order for priorityTenant, best (rank 1) to worst (rank 3).
	priorityRank1 = "s18"
	priorityRank2 = "s1"
	priorityRank3 = "s15"
)

var priorityRankOrder = []string{priorityRank1, priorityRank2, priorityRank3}

func newPriorityConfig(backends []*haharness.Backend) haharness.Config {
	return haharness.Config{
		Backends:       backends,
		HashCandidates: priorityY,
		HashSubsetMode: "priority",
		HashThreshold:  100,
		HashDecayKind:  "geometric",
		HashDecayRate:  "1", // no decay: every rank gets a 100% roll if healthy/unsaturated
		MaxConnPerSrv:  100,
		MaxQueue:       100,
		ServerTimeout:  "5s",
		QueueTimeout:   "5s",
	}
}

func getTenant(t *testing.T, client *http.Client, frontendAddr, tenant string) (status int, id string) {
	t.Helper()
	req, err := http.NewRequest(http.MethodGet, "http://"+frontendAddr+"/", nil)
	if err != nil {
		t.Fatalf("building request: %v", err)
	}
	req.Header.Set("X-Tenant", tenant)

	resp, err := client.Do(req)
	if err != nil {
		t.Fatalf("request: %v", err)
	}
	defer resp.Body.Close()
	io.Copy(io.Discard, resp.Body)
	return resp.StatusCode, resp.Header.Get("X-Backend-Id")
}

// TestRendezvousSubsetPriority_RankOrderIsStable confirms the discovered
// mapping still holds: with threshold=100 and no decay, every candidate
// that is healthy and unsaturated has a 100% roll, so rank 1 must win every
// single time, deterministically (no concurrency needed to show this,
// unlike leastconn which spreads load).
func TestRendezvousSubsetPriority_RankOrderIsStable(t *testing.T) {
	backends := haharness.StartBackends(t, numServers, 0)
	cfg := newPriorityConfig(backends)
	cfgPath, frontendAddr, _ := haharness.RenderConfig(t, cfg)

	bin := haharness.BuildHAProxy(t)
	haharness.ValidateConfig(t, bin, cfgPath)
	haharness.Start(t, bin, cfgPath, frontendAddr)

	client := &http.Client{Timeout: 5 * time.Second}
	for i := range 10 {
		status, id := getTenant(t, client, frontendAddr, priorityTenant)
		if status != http.StatusOK {
			t.Fatalf("request %d: status %d, want 200", i, status)
		}
		if id != priorityRank1 {
			t.Fatalf("request %d: got %s, want rank-1 %s (threshold=100 should always pick the top rank when healthy)",
				i, id, priorityRank1)
		}
	}
}

// TestRendezvousSubsetPriority_FailoverStaysWithinSubset is the centerpiece
// test: it walks rank 1 -> down, rank 2 -> down, rank 3 -> down, confirming
// at each step that traffic for priorityTenant moves to exactly the next
// rank in priorityRankOrder - never to a different, unranked server among
// the other 17 - and that once all 3 ranks are down, the request fails
// (503/NOSRV) rather than spilling outside the bounded subset. This is the
// core guarantee rendezvous-subset exists to provide: a fixed, small,
// health-independent candidate set that no failure elsewhere in the
// backend can push traffic outside of.
func TestRendezvousSubsetPriority_FailoverStaysWithinSubset(t *testing.T) {
	backends := haharness.StartBackends(t, numServers, 0)
	cfg := newPriorityConfig(backends)
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
