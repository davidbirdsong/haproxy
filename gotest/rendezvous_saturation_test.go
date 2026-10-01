// Checks precisely what happens when all Y candidates are saturated with
// NO queue room at all (maxconn=1, maxqueue=1, deliberately small), as
// opposed to being merely down/maint. This distinction matters for
// accurately describing the feature's behavior: down/maint candidates are
// excluded from the ranking in rdvz_pick_leastconn/rdvz_pick_priority,
// producing an immediate NOSRV. Saturated-but-healthy candidates are NOT
// excluded (the "momentarily full" fallback guarantee) - this test checks
// what haproxy's generic per-server admission path does once even that
// fallback candidate's queue is full too, and whether "option redispatch"
// changes the outcome by retrying a sibling within the same bounded Y set.
package gotest

import (
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"gotest/internal/haharness"
)

func runSaturationScenario(t *testing.T, redispatch bool) {
	const blockFor = 3 * time.Second

	backends := haharness.StartBackends(t, numServers, blockFor)
	frontendPort := haharness.FreeTCPPort(t)

	var sb []byte
	app := func(s string) { sb = append(sb, s...) }
	app("global\n    maxconn 1000\n")
	app("defaults\n    mode http\n    timeout client 10s\n    timeout connect 1s\n    timeout server 10s\n    timeout queue 10s\n    option httplog\n    log stdout local0\n")
	if redispatch {
		app("    option redispatch\n    retries 3\n")
	}
	app(fmt.Sprintf("frontend fe1\n    bind 127.0.0.1:%d\n    default_backend be1\n", frontendPort))
	app("backend be1\n    balance hash req.hdr(X-Tenant)\n    hash-type rendezvous-subset\n")
	app(fmt.Sprintf("    hash-candidates %d\n    hash-subset-mode leastconn\n", priorityY))
	for _, b := range backends {
		app(fmt.Sprintf("    server %s 127.0.0.1:%d check inter 300ms fall 1 maxconn 1 maxqueue 1\n", b.ID, b.Port))
	}

	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "haproxy.cfg")
	if err := os.WriteFile(cfgPath, sb, 0o644); err != nil {
		t.Fatalf("writing config: %v", err)
	}
	frontendAddr := fmt.Sprintf("127.0.0.1:%d", frontendPort)

	bin := haharness.BuildHAProxy(t)
	haharness.ValidateConfig(t, bin, cfgPath)
	haharness.Start(t, bin, cfgPath, frontendAddr)

	client := &http.Client{Timeout: 10 * time.Second}

	// Capacity is priorityY * (maxconn=1 + maxqueue=1) = 6; fire well past
	// that so at least one request must be rejected outright.
	n := priorityY*2 + 2
	statuses := make([]int, n)
	elapsed := make([]time.Duration, n)
	var wg sync.WaitGroup
	start := time.Now()
	for i := range n {
		wg.Go(func() {
			t0 := time.Now()
			statuses[i], _ = getTenant(t, client, frontendAddr, priorityTenant)
			elapsed[i] = time.Since(t0)
		})
		time.Sleep(300 * time.Millisecond)
	}
	wg.Wait()

	t.Logf("redispatch=%v total wall time: %v", redispatch, time.Since(start))
	for i := range n {
		t.Logf("  request %d: status=%d after %v", i, statuses[i], elapsed[i])
	}
}

func TestRendezvousSubsetLeastConn_AllSaturatedNoQueueRoom(t *testing.T) {
	t.Run("no_redispatch", func(t *testing.T) { runSaturationScenario(t, false) })
	t.Run("with_redispatch", func(t *testing.T) { runSaturationScenario(t, true) })
}
