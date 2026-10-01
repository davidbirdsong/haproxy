package haharness

import (
	"bytes"
	"net"
	"os/exec"
	"sync"
	"syscall"
	"testing"
	"time"
)

// syncBuffer is a goroutine-safe io.Writer, needed because the subprocess
// writes to it concurrently with the test reading it for diagnostics.
type syncBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *syncBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *syncBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

// ValidateConfig runs "haproxy -c -f <cfgPath>" and fails the test
// immediately, with haproxy's own diagnostic output attached, if the config
// is rejected. Catching this here gives a much clearer failure than waiting
// for a connection-refused on the frontend port.
func ValidateConfig(t *testing.T, binPath, cfgPath string) {
	t.Helper()
	cmd := exec.Command(binPath, "-c", "-f", cfgPath)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("haharness: config %s rejected: %v\n--- haproxy -c output ---\n%s", cfgPath, err, out)
	}
}

// Process is a running haproxy subprocess.
type Process struct {
	cmd *exec.Cmd
	log *syncBuffer

	waitDone chan struct{} // closed once cmd.Wait() returns
	waitErr  error         // valid only after waitDone is closed
}

// Log returns everything haproxy has written to stdout/stderr so far
// (access logs included, since the rendered config uses "log stdout").
func (p *Process) Log() string { return p.log.String() }

// exited reports whether the process has already terminated, without
// blocking.
func (p *Process) exited() bool {
	select {
	case <-p.waitDone:
		return true
	default:
		return false
	}
}

// Start launches haproxy against cfgPath and waits until frontendAddr is
// accepting TCP connections (or fails the test after a timeout, dumping
// haproxy's own log for diagnosis). The process (and its whole process
// group, in case haproxy forks workers) is killed on test cleanup no matter
// how the test exits.
func Start(t *testing.T, binPath, cfgPath, frontendAddr string) *Process {
	t.Helper()

	cmd := exec.Command(binPath, "-f", cfgPath)
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}

	logBuf := &syncBuffer{}
	cmd.Stdout = logBuf
	cmd.Stderr = logBuf

	if err := cmd.Start(); err != nil {
		t.Fatalf("haharness: starting haproxy: %v", err)
	}

	proc := &Process{cmd: cmd, log: logBuf, waitDone: make(chan struct{})}
	go func() {
		proc.waitErr = cmd.Wait()
		close(proc.waitDone)
	}()

	t.Cleanup(func() {
		killProcessGroup(cmd)
		<-proc.waitDone
	})

	waitForListener(t, frontendAddr, proc)
	return proc
}

func killProcessGroup(cmd *exec.Cmd) {
	if cmd.Process == nil {
		return
	}
	if pgid, err := syscall.Getpgid(cmd.Process.Pid); err == nil {
		_ = syscall.Kill(-pgid, syscall.SIGKILL)
		return
	}
	_ = cmd.Process.Kill()
}

func waitForListener(t *testing.T, addr string, proc *Process) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	var lastErr error
	for time.Now().Before(deadline) {
		if proc.exited() {
			t.Fatalf("haharness: haproxy exited before frontend %s came up: %v\n--- haproxy log ---\n%s",
				addr, proc.waitErr, proc.Log())
		}
		conn, err := net.DialTimeout("tcp", addr, 100*time.Millisecond)
		if err == nil {
			conn.Close()
			return
		}
		lastErr = err
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatalf("haharness: frontend %s never came up: %v\n--- haproxy log ---\n%s", addr, lastErr, proc.Log())
}
