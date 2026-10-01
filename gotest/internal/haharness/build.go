// Package haharness builds haproxy, spins up counting backend servers,
// renders a config from a template, and runs haproxy as a managed
// subprocess for use from table-driven tests.
package haharness

import (
	"bytes"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"testing"
)

// RepoRoot returns the absolute path to the haproxy repo root, derived from
// this source file's own location so it works regardless of the test's
// working directory.
func RepoRoot(t *testing.T) string {
	t.Helper()
	_, thisFile, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("haharness: could not determine source file location")
	}
	// this file lives at <repo>/gotest/internal/haharness/build.go
	root, err := filepath.Abs(filepath.Join(filepath.Dir(thisFile), "..", "..", ".."))
	if err != nil {
		t.Fatalf("haharness: resolving repo root: %v", err)
	}
	return root
}

// defaultMakeTarget picks a haproxy Makefile TARGET for the current OS.
// Override with the HAPROXY_MAKE_TARGET env var if it ever guesses wrong.
func defaultMakeTarget() string {
	if v := os.Getenv("HAPROXY_MAKE_TARGET"); v != "" {
		return v
	}
	switch runtime.GOOS {
	case "darwin":
		return "osx"
	case "linux":
		return "linux-glibc"
	default:
		return "generic"
	}
}

// BuildHAProxy builds the haproxy binary via the repo's Makefile and returns
// its absolute path. Set HAPROXY_BIN to skip the build entirely and point
// tests at an already-built binary. Otherwise the build output always lands
// at <repo root>/haproxy (the Makefile's own hardcoded output name); `make`
// is incremental, so repeated test runs only rebuild what changed.
func BuildHAProxy(t *testing.T) string {
	t.Helper()

	if bin := os.Getenv("HAPROXY_BIN"); bin != "" {
		abs, err := filepath.Abs(bin)
		if err != nil {
			t.Fatalf("haharness: resolving HAPROXY_BIN=%q: %v", bin, err)
		}
		if _, err := os.Stat(abs); err != nil {
			t.Fatalf("haharness: HAPROXY_BIN=%q: %v", abs, err)
		}
		return abs
	}

	root := RepoRoot(t)
	target := defaultMakeTarget()

	cmd := exec.Command("make", fmt.Sprintf("TARGET=%s", target), fmt.Sprintf("-j%d", runtime.NumCPU()))
	cmd.Dir = root
	var out bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &out
	if err := cmd.Run(); err != nil {
		t.Fatalf("haharness: building haproxy (TARGET=%s) failed: %v\n--- make output ---\n%s", target, err, out.String())
	}

	bin := filepath.Join(root, "haproxy")
	if _, err := os.Stat(bin); err != nil {
		t.Fatalf("haharness: expected binary at %s after build: %v\n--- make output ---\n%s", bin, err, out.String())
	}
	return bin
}
