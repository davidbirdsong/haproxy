package gotest

import (
	"net/http"
	"testing"
	"time"

	"gotest/internal/haharness"
)

// TestDiscoverLeastConnRankOrder is a one-off discovery/verification tool:
// it checks whether leastconn produces the same tie-break order as
// priority mode for the same tenant, under zero concurrent load (so
// leastconn's weighted-argmin ties all break the same way: the first
// entry in the score-sorted candidate array wins, which is the same
// highest-HRW-score entry priority mode calls "rank 1"). The HRW ranking
// array itself (see rdvz_get_server_hash in src/lb_rdvz.c) doesn't depend
// on hash-subset-mode at all - only the selection rule applied to it does -
// so this is expected to match priorityRankOrder exactly.
func TestDiscoverLeastConnRankOrder(t *testing.T) {
	const y = 3

	backends := haharness.StartBackends(t, numServers, 0)
	cfg := haharness.Config{
		Backends:       backends,
		HashCandidates: y,
		HashSubsetMode: "leastconn",
		MaxConnPerSrv:  1000, // no saturation, so argmin ties purely on score order
		MaxQueue:       1000,
		ServerTimeout:  "5s",
		QueueTimeout:   "5s",
	}
	cfgPath, frontendAddr, sockPath := haharness.RenderConfig(t, cfg)

	bin := haharness.BuildHAProxy(t)
	haharness.ValidateConfig(t, bin, cfgPath)
	haharness.Start(t, bin, cfgPath, frontendAddr)

	client := &http.Client{Timeout: 5 * time.Second}
	var order []string

	for range y + 1 {
		status, id := getTenant(t, client, frontendAddr, priorityTenant)
		if status != http.StatusOK {
			t.Logf("status %d (expected once all %d ranks are down)", status, y)
			break
		}
		order = append(order, id)
		t.Logf("rank %d: %s", len(order), id)
		haharness.SetServerState(t, sockPath, "be1", id, "maint")
	}

	t.Logf("leastconn order: %v", order)
	t.Logf("priority order:    %v", priorityRankOrder)
	if len(order) != len(priorityRankOrder) {
		t.Fatalf("order length mismatch: leastconn=%v priority=%v", order, priorityRankOrder)
	}
	for i := range order {
		if order[i] != priorityRankOrder[i] {
			t.Errorf("rank %d: leastconn=%s, priority=%s - orders diverge, mode is NOT independent of selection rule as assumed",
				i+1, order[i], priorityRankOrder[i])
		}
	}
}
