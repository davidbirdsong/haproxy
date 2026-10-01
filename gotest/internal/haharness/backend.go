package haharness

import (
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// Backend is one counting HTTP backend server: it records how many requests
// it has handled, optionally blocking for a fixed duration before replying
// so tests can force concurrency/queueing against a real TCP server rather
// than an in-process fake.
type Backend struct {
	ID    string // matches the haproxy "server <ID> ..." name, e.g. "s1"
	Port  int
	Count atomic.Int64

	srv      *httptest.Server
	stopOnce sync.Once
}

// Stop shuts down this backend's listener immediately, simulating a dead
// server so haproxy's health check (configured with "fall 1" in the
// rendered config, see config.go) marks it down after at most one check
// interval. Safe to call multiple times (including via the automatic
// t.Cleanup registered by StartBackends).
func (b *Backend) Stop() {
	b.stopOnce.Do(b.srv.Close)
}

// StartBackends starts n counting backends named s1..sN (stable naming
// regardless of their randomly assigned ports, so that haproxy's hash-key id
// default - based on declaration order/puid, not on port - gives
// reproducible tenant-to-server-name routing across runs). Each request
// blocks for blockFor before replying with its own ID as the body, and an
// X-Backend-Id header for easy assertions. All servers are closed via
// t.Cleanup.
func StartBackends(t *testing.T, n int, blockFor time.Duration) []*Backend {
	t.Helper()

	backends := make([]*Backend, n)
	for i := range n {
		b := &Backend{ID: fmt.Sprintf("s%d", i+1)}

		mux := http.NewServeMux()
		mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
			b.Count.Add(1)
			if blockFor > 0 {
				time.Sleep(blockFor)
			}
			w.Header().Set("X-Backend-Id", b.ID)
			fmt.Fprintln(w, b.ID)
		})

		srv := httptest.NewServer(mux)
		b.srv = srv
		t.Cleanup(b.Stop)

		port, err := portOf(srv)
		if err != nil {
			t.Fatalf("haharness: backend %s: %v", b.ID, err)
		}

		b.Port = port
		backends[i] = b
	}
	return backends
}

func portOf(srv *httptest.Server) (int, error) {
	addr, ok := srv.Listener.Addr().(*net.TCPAddr)
	if !ok {
		return 0, fmt.Errorf("unexpected listener address type %T", srv.Listener.Addr())
	}
	return addr.Port, nil
}

// FreeTCPPort finds a currently-unused TCP port on 127.0.0.1 by binding to
// port 0 and releasing it immediately. Small TOCTOU race in principle
// (another process could grab it first) but standard practice for test
// harnesses and fine for our purposes.
func FreeTCPPort(t *testing.T) int {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("haharness: allocating free port: %v", err)
	}
	defer l.Close()
	return l.Addr().(*net.TCPAddr).Port
}
