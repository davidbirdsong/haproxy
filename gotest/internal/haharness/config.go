package haharness

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"text/template"
)

// Config holds everything the haproxy config template needs. Backends is
// always the full fixed set (see StartBackends) - server declaration order
// drives haproxy's default "hash-key id" static key, so keeping the same
// servers in the same order across test cases is what makes a given
// tenant's resulting server name reproducible across runs.
type Config struct {
	Backends []*Backend

	FrontendPort int
	StatsSocket  string // unix socket path; RenderConfig fills this in if empty

	HashCandidates int
	HashSubsetMode string // "leastconn" or "priority"
	HashThreshold  int    // priority mode only, 0 to omit
	HashDecayKind  string // "geometric" or "harmonic", "" to omit
	HashDecayRate  string // e.g. "0.5", "" to omit

	MaxConnPerSrv int
	MaxQueue      int

	ServerTimeout string // e.g. "20s"
	QueueTimeout  string // e.g. "20s"
}

const configTmplSrc = `
global
    maxconn 1000
    stats socket {{.StatsSocket}} level admin

defaults
    mode http
    timeout client 5s
    timeout connect 1s
    timeout server {{.ServerTimeout}}
    timeout queue {{.QueueTimeout}}
    option httplog
    option log-health-checks
    log stdout local0

frontend fe1
    bind 127.0.0.1:{{.FrontendPort}}
    default_backend be1

backend be1
    balance hash req.hdr(X-Tenant)
    hash-type rendezvous-subset
    hash-candidates {{.HashCandidates}}
    hash-subset-mode {{.HashSubsetMode}}
{{- if .HashThreshold}}
    hash-threshold {{.HashThreshold}}
{{- end}}
{{- if .HashDecayKind}}
    hash-decay {{.HashDecayKind}} {{.HashDecayRate}}
{{- end}}
{{- range .Backends}}
    server {{.ID}} 127.0.0.1:{{.Port}} check inter 300ms fall 1 maxconn {{$.MaxConnPerSrv}} maxqueue {{$.MaxQueue}}
{{- end}}
`

var configTmpl = template.Must(template.New("haproxy.cfg").Parse(configTmplSrc))

// RenderConfig fills the template and writes it to a temp file (cleaned up
// automatically by the testing package's t.TempDir()). Returns the config
// path and the "host:port" the frontend listens on.
func RenderConfig(t *testing.T, cfg Config) (cfgPath string, frontendAddr string, statsSocket string) {
	t.Helper()

	if cfg.FrontendPort == 0 {
		cfg.FrontendPort = FreeTCPPort(t)
	}
	if cfg.StatsSocket == "" {
		cfg.StatsSocket = tempSocketPath(t)
	}

	var buf bytes.Buffer
	if err := configTmpl.Execute(&buf, cfg); err != nil {
		t.Fatalf("haharness: rendering config template: %v", err)
	}

	dir := t.TempDir()
	path := filepath.Join(dir, "haproxy.cfg")
	if err := os.WriteFile(path, buf.Bytes(), 0o644); err != nil {
		t.Fatalf("haharness: writing config to %s: %v", path, err)
	}

	return path, fmt.Sprintf("127.0.0.1:%d", cfg.FrontendPort), cfg.StatsSocket
}

// tempSocketPath allocates a short-enough path for a unix socket directly
// under the OS temp dir (not t.TempDir(), whose nested-subtest paths can
// exceed the ~104-byte sun_path limit on macOS/BSD).
func tempSocketPath(t *testing.T) string {
	t.Helper()
	f, err := os.CreateTemp("", "hap-*.sock")
	if err != nil {
		t.Fatalf("haharness: allocating stats socket path: %v", err)
	}
	path := f.Name()
	f.Close()
	os.Remove(path) // we only wanted a unique name; haproxy creates the actual socket
	t.Cleanup(func() { os.Remove(path) })
	return path
}
