package gotest

import (
	"fmt"
	"io"
	"net/http"
	"os"
	"testing"
	"time"

	"gotest/internal/haharness"
)

// TestHealthChecksFirePeriodically documents a known, currently-unexplained
// environment issue: periodic health checks ("check inter 300ms") fire
// exactly once at haproxy startup and never again, even with zero HTTP
// traffic. This reproduces identically with plain "balance roundrobin" (no
// rendezvous-subset involved), from a manual shell invocation outside this
// harness, and on a vanilla v3.4.0 release build in a separate worktree -
// so it predates this feature and isn't specific to this dev build. Not
// something we're chasing further for now; rendezvous-subset tests that
// need to simulate a down server use the stats socket
// (haharness.SetServerState) instead, which doesn't depend on the checker.
// Skipped so it doesn't make the suite red for an unrelated, already-triaged
// issue; unskip to re-check if this environment's behavior ever changes.
func TestHealthChecksFirePeriodically(t *testing.T) {
	t.Skip("known environment issue: periodic health checks don't re-fire after startup here (reproduces on vanilla v3.4.0 too); see doc comment")
	backends := haharness.StartBackends(t, 2, 0)
	bin := haharness.BuildHAProxy(t)
	frontendPort := haharness.FreeTCPPort(t)
	cfgPath := writeRRConfig(t, backends, frontendPort)
	frontendAddr := fmt.Sprintf("127.0.0.1:%d", frontendPort)

	haharness.ValidateConfig(t, bin, cfgPath)
	proc := haharness.Start(t, bin, cfgPath, frontendAddr)

	time.Sleep(3 * time.Second)
	firstCount := countOccurrences(proc.Log(), "Health check")

	time.Sleep(3 * time.Second)
	secondCount := countOccurrences(proc.Log(), "Health check")

	t.Logf("health check log line count: after 3s=%d, after 6s=%d", firstCount, secondCount)
	if secondCount <= firstCount {
		t.Fatalf("health checks appear to have stopped firing: count did not increase between 3s (%d) and 6s (%d)\n--- log ---\n%s",
			firstCount, secondCount, proc.Log())
	}
}

func countOccurrences(s, substr string) int {
	n := 0
	for i := 0; i+len(substr) <= len(s); i++ {
		if s[i:i+len(substr)] == substr {
			n++
		}
	}
	return n
}

// TestIsolateHealthCheckBehavior was used to confirm the health-check issue
// documented on TestHealthChecksFirePeriodically affects plain "balance
// roundrobin" too (no rendezvous-subset involved), by sending multiple
// requests after a Backend.Stop() rather than just one (round robin
// naturally alternates servers regardless of health, so a single lucky
// success proves nothing). See that test's doc comment for the full story;
// skipped for the same reason.
func TestIsolateHealthCheckBehavior(t *testing.T) {
	t.Skip("known environment issue: see TestHealthChecksFirePeriodically's doc comment")
	backends := haharness.StartBackends(t, 2, 0)

	bin := haharness.BuildHAProxy(t)
	frontendPort := haharness.FreeTCPPort(t)
	cfgPath := writeRRConfig(t, backends, frontendPort)
	frontendAddr := fmt.Sprintf("127.0.0.1:%d", frontendPort)

	haharness.ValidateConfig(t, bin, cfgPath)
	proc := haharness.Start(t, bin, cfgPath, frontendAddr)

	client := &http.Client{Timeout: 5 * time.Second}
	get := func() (status int, id string, err error) {
		resp, err := client.Get("http://" + frontendAddr + "/")
		if err != nil {
			return 0, "", err
		}
		defer resp.Body.Close()
		io.Copy(io.Discard, resp.Body)
		return resp.StatusCode, resp.Header.Get("X-Backend-Id"), nil
	}

	status, id, err := get()
	if err != nil {
		t.Fatalf("first request: %v", err)
	}
	t.Logf("first request: status=%d id=%s", status, id)

	var dead *haharness.Backend
	for _, b := range backends {
		if b.ID == id {
			dead = b
		}
	}
	dead.Stop()
	t.Logf("stopped backend %s, waiting for health check...", dead.ID)
	time.Sleep(2 * time.Second)

	// With only 2 servers, round robin naturally alternates s1,s2,s1,s2,...
	// regardless of health. A single lucky success proves nothing - if
	// health-check exclusion isn't actually working, every OTHER request
	// in this loop should still try to reach the dead server and fail.
	for i := range 6 {
		status, id, err := get()
		t.Logf("after-stop request %d: status=%d id=%s err=%v", i, status, id, err)
		if err != nil || status != http.StatusOK || id == dead.ID {
			t.Logf("--- haproxy log ---\n%s", proc.Log())
			t.Fatalf("request %d still reached (or failed trying to reach) stopped backend %s: status=%d err=%v",
				i, dead.ID, status, err)
		}
	}
	t.Logf("SUCCESS: all requests correctly avoided stopped backend %s", dead.ID)
}

// TestDiscoverPriorityRankOrder is a one-off discovery tool, not a regular
// assertion test: it reveals the exact HRW rank order (best to worst) that
// "tenant-priority-1" resolves to against the fixed 20-server set, by
// repeatedly asking for one request, noting the winner, then killing that
// winner's backend and asking again. Run with
// `go test -run TestDiscoverPriorityRankOrder -v` whenever the server count
// or declaration order changes, and copy the logged order into the
// priorityRank* constants in rendezvous_priority_test.go.
func TestDiscoverPriorityRankOrder(t *testing.T) {
	const tenant = "tenant-priority-1"
	const y = 3

	backends := haharness.StartBackends(t, numServers, 0)

	cfg := haharness.Config{
		Backends:       backends,
		HashCandidates: y,
		HashSubsetMode: "priority",
		HashThreshold:  100,
		HashDecayKind:  "geometric",
		HashDecayRate:  "1",
		MaxConnPerSrv:  100,
		MaxQueue:       100,
		ServerTimeout:  "5s",
		QueueTimeout:   "5s",
	}
	cfgPath, frontendAddr, sockPath := haharness.RenderConfig(t, cfg)
	if content, err := os.ReadFile(cfgPath); err == nil {
		t.Logf("--- rendered config ---\n%s", content)
	}

	bin := haharness.BuildHAProxy(t)
	haharness.ValidateConfig(t, bin, cfgPath)
	proc := haharness.Start(t, bin, cfgPath, frontendAddr)

	client := &http.Client{Timeout: 5 * time.Second}
	var order []string

	for range y + 1 { // one extra attempt: after killing all y, expect failure
		req, err := http.NewRequest(http.MethodGet, "http://"+frontendAddr+"/", nil)
		if err != nil {
			t.Fatal(err)
		}
		req.Header.Set("X-Tenant", tenant)

		resp, err := client.Do(req)
		if err != nil {
			t.Logf("request error (expected once all %d ranks are down): %v", y, err)
			break
		}
		id := resp.Header.Get("X-Backend-Id")
		io.Copy(io.Discard, resp.Body)
		resp.Body.Close()

		if resp.StatusCode != http.StatusOK {
			t.Logf("status %d (expected once all %d ranks are down)", resp.StatusCode, y)
			t.Logf("--- haproxy log at failure ---\n%s", proc.Log())
			break
		}

		order = append(order, id)
		t.Logf("rank %d: %s", len(order), id)

		// Force this rank admin-down instantly via the stats socket,
		// rather than Backend.Stop() + waiting on the periodic health
		// checker (which does not reliably re-fire in this environment -
		// see TestHealthChecksFirePeriodically).
		haharness.SetServerState(t, sockPath, "be1", id, "maint")
	}

	t.Logf("discovered rank order for tenant %q: %v", tenant, order)
	fmt.Println("RANK_ORDER:", order)
}

func writeRRConfig(t *testing.T, backends []*haharness.Backend, frontendPort int) string {
	t.Helper()
	var sb []byte
	app := func(s string) { sb = append(sb, s...) }
	app("global\n    maxconn 1000\n")
	app("defaults\n    mode http\n    timeout client 5s\n    timeout connect 1s\n    timeout server 5s\n    option httplog\n    option log-health-checks\n    log stdout local0\n")
	app(fmt.Sprintf("frontend fe1\n    bind 127.0.0.1:%d\n    default_backend be1\n", frontendPort))
	app("backend be1\n    balance roundrobin\n")
	for _, b := range backends {
		// Deliberately no "rise 1": see TestHealthChecksFirePeriodically's
		// doc comment for why.
		app(fmt.Sprintf("    server %s 127.0.0.1:%d check inter 300ms fall 1\n", b.ID, b.Port))
	}

	dir := t.TempDir()
	path := dir + "/rr.cfg"
	if err := os.WriteFile(path, sb, 0o644); err != nil {
		t.Fatalf("writing config: %v", err)
	}
	return path
}
