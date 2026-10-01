// Package gotest exercises "hash-type rendezvous-subset" end to end against
// a real haproxy binary and real backend servers, to verify the central
// invariant from design/PLAN.md: a tenant's traffic is bounded to a fixed,
// small top-Y subset of servers, never more.
package gotest

import (
	"io"
	"net/http"
	"sync"
	"testing"
	"time"

	"gotest/internal/haharness"
)

// Fixed across every test case: server declaration order (not port, which
// httptest assigns randomly) drives haproxy's default "hash-key id" static
// key, so keeping the same 20 servers in the same order makes a given
// tenant's resulting server name reproducible across runs.
const numServers = 20

type testCase struct {
	name           string
	tenant         string
	hashCandidates int
	hashSubsetMode string
	maxConnPerSrv  int
	maxQueue       int
	blockFor       time.Duration
	numRequests    int
}

var cases = []testCase{
	{
		name:           "single_tenant_bounded_to_subset",
		tenant:         "tenant-fixed-1",
		hashCandidates: 3,
		hashSubsetMode: "leastconn",
		maxConnPerSrv:  1,
		maxQueue:       2,
		blockFor:       5 * time.Second,
		// exactly fills capacity (3 servers * (1 active + 2 queued)) so we
		// expect every request to succeed while still forcing concurrency
		// across the whole Y-subset.
		numRequests: 9,
	},
}

func TestRendezvousSubset(t *testing.T) {
	bin := haharness.BuildHAProxy(t)

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			backends := haharness.StartBackends(t, numServers, tc.blockFor)

			cfg := haharness.Config{
				Backends:       backends,
				HashCandidates: tc.hashCandidates,
				HashSubsetMode: tc.hashSubsetMode,
				MaxConnPerSrv:  tc.maxConnPerSrv,
				MaxQueue:       tc.maxQueue,
				ServerTimeout:  "20s",
				QueueTimeout:   "20s",
			}
			cfgPath, frontendAddr, _ := haharness.RenderConfig(t, cfg)

			haharness.ValidateConfig(t, bin, cfgPath)
			proc := haharness.Start(t, bin, cfgPath, frontendAddr)

			client := &http.Client{Timeout: 20 * time.Second}
			var wg sync.WaitGroup
			var mu sync.Mutex
			statusCounts := map[int]int{}
			var reqErrs []error

			for range tc.numRequests {
				wg.Go(func() {
					req, err := http.NewRequest(http.MethodGet, "http://"+frontendAddr+"/", nil)
					if err != nil {
						mu.Lock()
						reqErrs = append(reqErrs, err)
						mu.Unlock()
						return
					}
					req.Header.Set("X-Tenant", tc.tenant)

					resp, err := client.Do(req)
					if err != nil {
						mu.Lock()
						reqErrs = append(reqErrs, err)
						mu.Unlock()
						return
					}
					defer resp.Body.Close()
					io.Copy(io.Discard, resp.Body)

					mu.Lock()
					statusCounts[resp.StatusCode]++
					mu.Unlock()
				})
			}
			wg.Wait()

			for _, err := range reqErrs {
				t.Errorf("request failed: %v", err)
			}

			// Any status other than 200 (served) or 503 (queue/maxconn
			// overflow - expected given the deliberately tight maxconn/
			// maxqueue in this test) indicates something unexpected.
			for code, n := range statusCounts {
				if code != http.StatusOK && code != http.StatusServiceUnavailable {
					t.Errorf("got %d responses with unexpected status %d", n, code)
				}
			}

			used := map[string]int64{}
			for _, b := range backends {
				if c := b.Count.Load(); c > 0 {
					used[b.ID] = c
				}
			}

			t.Logf("tenant %q status counts: %v", tc.tenant, statusCounts)
			t.Logf("tenant %q used backends: %v", tc.tenant, used)
			t.Logf("servers seen in access log, in order: %v", haharness.ServedBy(proc.Log()))

			if len(used) == 0 {
				t.Fatalf("no backend served any request\n--- haproxy log ---\n%s", proc.Log())
			}
			if len(used) > tc.hashCandidates {
				t.Errorf("tenant %q spread across %d distinct backends %v, want at most hash-candidates=%d",
					tc.tenant, len(used), used, tc.hashCandidates)
			}
		})
	}
}
