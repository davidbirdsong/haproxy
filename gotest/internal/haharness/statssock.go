package haharness

import (
	"fmt"
	"io"
	"net"
	"strings"
	"testing"
	"time"
)

// SetServerState sends "set server <backend>/<server> state <state>" over
// the admin stats socket and returns haproxy's response. This changes a
// server's administrative state instantly, independent of the periodic
// health checker (which, as of this writing, has been observed not to
// re-fire reliably in this environment - see TestHealthChecksFirePeriodically
// in discover_rank_test.go). <state> is typically "maint" (admin-down,
// excluded from LB regardless of reachability) or "ready" (clears maint).
func SetServerState(t *testing.T, sockPath, backend, server, state string) {
	t.Helper()
	cmd := fmt.Sprintf("set server %s/%s state %s\n", backend, server, state)
	reply := sendStatsCommand(t, sockPath, cmd)
	if strings.Contains(strings.ToLower(reply), "unknown") || strings.Contains(strings.ToLower(reply), "error") {
		t.Fatalf("haharness: stats socket command %q failed: %s", strings.TrimSpace(cmd), reply)
	}
}

// RunCLI sends a raw command over the admin stats socket and returns
// haproxy's full response, for one-off commands (add/enable/del server,
// etc.) that don't have a dedicated typed wrapper here.
func RunCLI(t *testing.T, sockPath, cmd string) string {
	t.Helper()
	if !strings.HasSuffix(cmd, "\n") {
		cmd += "\n"
	}
	return sendStatsCommand(t, sockPath, cmd)
}

// sendStatsCommand opens a short-lived connection to the admin stats
// socket, sends one command, and returns the full response. haproxy closes
// the connection after replying in this (non-interactive) mode.
func sendStatsCommand(t *testing.T, sockPath, cmd string) string {
	t.Helper()

	var lastErr error
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		conn, err := net.DialTimeout("unix", sockPath, 500*time.Millisecond)
		if err != nil {
			lastErr = err
			time.Sleep(50 * time.Millisecond)
			continue
		}

		conn.SetDeadline(time.Now().Add(2 * time.Second))
		if _, err := conn.Write([]byte(cmd)); err != nil {
			conn.Close()
			t.Fatalf("haharness: writing to stats socket %s: %v", sockPath, err)
		}
		reply, err := io.ReadAll(conn)
		conn.Close()
		if err != nil {
			t.Fatalf("haharness: reading stats socket %s reply: %v", sockPath, err)
		}
		return string(reply)
	}
	t.Fatalf("haharness: stats socket %s never became available: %v", sockPath, lastErr)
	return ""
}
