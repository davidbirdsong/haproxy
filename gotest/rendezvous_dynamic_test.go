// Proves hash-type rendezvous-subset correctly supports runtime
// "add server"/"del server" (BE_LB_PROP_DYN on lb_rdvz_ops in
// src/lb_rdvz.c): a dynamically added server joins the ranking and can
// receive traffic, a dynamically added backup server is refused outright
// (same restriction as at config time, see check_config_validity() in
// proxy.c), and a dynamically removed server stops receiving traffic with
// no crash - the real risk being a use-after-free in
// rdvz_server_deinit()'s table purge, which would most likely manifest as
// the haproxy process dying and turning every subsequent request into a
// connection error.
package gotest

import (
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"

	"gotest/internal/haharness"
)

func TestRendezvousSubset_DynamicAddRemove(t *testing.T) {
	// Start 4 backends up front; s4 isn't declared in the initial config -
	// it's added dynamically later, pointing at its already-running port.
	backends := haharness.StartBackends(t, 4, 0)
	initial := backends[:3]

	cfg := haharness.Config{
		Backends: initial,
		// Y set higher than any server count this test will ever reach,
		// so every configured+usable server is always a candidate for
		// every tenant - this test is about the dynamic lifecycle, not
		// about HRW rank discovery.
		HashCandidates: 10,
		HashSubsetMode: "leastconn",
		MaxConnPerSrv:  100,
		MaxQueue:       100,
		ServerTimeout:  "5s",
		QueueTimeout:   "5s",
	}
	cfgPath, frontendAddr, sockPath := haharness.RenderConfig(t, cfg)

	bin := haharness.BuildHAProxy(t)
	haharness.ValidateConfig(t, bin, cfgPath)
	haharness.Start(t, bin, cfgPath, frontendAddr)

	client := &http.Client{Timeout: 5 * time.Second}

	usedServers := func(tenants int) map[string]bool {
		t.Helper()
		used := map[string]bool{}
		for i := range tenants {
			status, id := getTenant(t, client, frontendAddr, fmt.Sprintf("dyn-tenant-%d", i))
			if status != http.StatusOK {
				t.Fatalf("tenant %d: status %d, want 200 (ample capacity, no reason to ever fail here)", i, status)
			}
			used[id] = true
		}
		return used
	}
	assertOnly := func(used map[string]bool, allowed ...string) {
		t.Helper()
		for id := range used {
			ok := false
			for _, a := range allowed {
				if id == a {
					ok = true
					break
				}
			}
			if !ok {
				t.Errorf("unexpected server %s in use, want only one of %v", id, allowed)
			}
		}
	}

	// Baseline: only the 3 initially-configured servers ever appear.
	before := usedServers(20)
	t.Logf("before add: used=%v", before)
	assertOnly(before, "s1", "s2", "s3")

	// A dynamically-added backup server must be refused outright, same as
	// at config time (check_config_validity()).
	reply := haharness.RunCLI(t, sockPath, "add server be1/s5 127.0.0.1:1 backup")
	t.Logf("add backup server reply: %q", reply)
	if !strings.Contains(reply, "does not support backup servers") {
		t.Fatalf("expected backup rejection mentioning 'does not support backup servers', got: %q", reply)
	}

	// Add s4 (already running, just not yet declared) and bring it into
	// service.
	s4 := backends[3]
	reply = haharness.RunCLI(t, sockPath, fmt.Sprintf("add server be1/%s 127.0.0.1:%d", s4.ID, s4.Port))
	t.Logf("add server reply: %q", reply)
	if !strings.Contains(reply, "New server registered") {
		t.Fatalf("expected successful add mentioning 'New server registered', got: %q", reply)
	}
	reply = haharness.RunCLI(t, sockPath, "enable server be1/"+s4.ID)
	t.Logf("enable server reply: %q", reply)

	// s4 must now be reachable for at least one of several tenants.
	after := usedServers(30)
	t.Logf("after add+enable: used=%v", after)
	assertOnly(after, "s1", "s2", "s3", "s4")
	if !after["s4"] {
		t.Errorf("s4 never received traffic after being dynamically added and enabled")
	}

	// Remove s2: "del server" requires maint state first (its own
	// precondition). Traffic must never land on it again, and - the real
	// risk under test - nothing must crash afterward.
	haharness.SetServerState(t, sockPath, "be1", "s2", "maint")
	reply = haharness.RunCLI(t, sockPath, "del server be1/s2")
	t.Logf("del server reply: %q", reply)

	finalUsed := usedServers(40)
	t.Logf("after del s2: used=%v", finalUsed)
	assertOnly(finalUsed, "s1", "s3", "s4")
	if finalUsed["s2"] {
		t.Errorf("s2 still received traffic after being deleted")
	}
}
