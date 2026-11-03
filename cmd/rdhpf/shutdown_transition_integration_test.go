//go:build rdhpf_integration

package main

import (
	"debug/buildinfo"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"github.com/tomaszpeksa/remote-docker-host-port-forwarder/internal/instancepath"
	"github.com/tomaszpeksa/remote-docker-host-port-forwarder/internal/testutil/sshfixture"
)

// This controller owns only the three tagged synchronous log boundaries. It has
// no background workers; cleanup releases exact connections before original Wait.
type shutdownHold struct {
	listener    *net.UnixListener
	connections []net.Conn
	once        sync.Once
}

func newShutdownHold(t *testing.T) *shutdownHold {
	dir, err := os.MkdirTemp("/tmp", "s13-hold-")
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, os.RemoveAll(dir)) })
	listener, err := net.ListenUnix("unix", &net.UnixAddr{Name: filepath.Join(dir, "hold"), Net: "unix"})
	require.NoError(t, err)
	h := &shutdownHold{listener: listener}
	t.Cleanup(h.close)
	t.Setenv("RDHPF_S13_PROOF_SOCKET", listener.Addr().String())
	return h
}
func (h *shutdownHold) close() {
	h.once.Do(func() {
		_ = h.listener.Close()
		for _, conn := range h.connections {
			_ = json.NewEncoder(conn).Encode("release")
			_ = conn.Close()
		}
	})
}
func (h *shutdownHold) await(t *testing.T, phase string, pid int) (net.Conn, shutdownProofReceipt) {
	t.Helper()
	require.NoError(t, h.listener.SetDeadline(time.Now().Add(10*time.Second)))
	conn, err := h.listener.Accept()
	require.NoError(t, err, "S13_TAGGED_BOUNDARY_MISSING: %s", phase)
	h.connections = append(h.connections, conn)
	require.NoError(t, conn.SetDeadline(time.Now().Add(2*time.Second)))
	var receipt shutdownProofReceipt
	require.NoError(t, json.NewDecoder(conn).Decode(&receipt))
	require.Equal(t, phase, receipt.Phase)
	require.Equal(t, pid, receipt.CLI)
	t.Logf("S13_TAGGED_HELD %+v", receipt)
	return conn, receipt
}
func shutdownCommand(t *testing.T, conn net.Conn, command string) shutdownProofReceipt {
	t.Helper()
	require.NoError(t, conn.SetDeadline(time.Now().Add(2*time.Second)))
	require.NoError(t, json.NewEncoder(conn).Encode(command))
	var receipt shutdownProofReceipt
	require.NoError(t, json.NewDecoder(conn).Decode(&receipt))
	t.Logf("S13_TAGGED_COMMAND command=%s receipt=%+v", command, receipt)
	return receipt
}
func shutdownProofBinary(t *testing.T) string {
	t.Helper()
	binary := os.Getenv("RDHPF_SHUTDOWN_BINARY")
	require.True(t, filepath.IsAbs(binary), "separate exact-source integration-tagged CLI required")
	info, err := buildinfo.ReadFile(binary)
	require.NoError(t, err)
	found := false
	for _, setting := range info.Settings {
		if setting.Key == "-tags" && setting.Value == "rdhpf_integration" {
			found = true
		}
	}
	require.True(t, found, "instrumented binary must not be presented as normal released CLI")
	t.Logf("S13_INSTRUMENTED_CLI_BUILD %s", info.String())
	return binary
}

type shutdownCLI struct {
	cmd    *exec.Cmd
	logs   watchdogCLILog
	done   chan struct{}
	result error
}

func startShutdownCLI(t *testing.T, f *sshfixture.WatchdogFixture, binary string, release func()) *shutdownCLI {
	t.Helper()
	c := &shutdownCLI{done: make(chan struct{})}
	c.cmd = exec.Command(binary, "run", "--host", f.Host, "--log-level", "debug")
	c.cmd.Stdout = io.MultiWriter(os.Stderr, &c.logs)
	c.cmd.Stderr, c.cmd.WaitDelay = c.cmd.Stdout, 2*time.Second
	require.NoError(t, c.cmd.Start())
	go func() { c.result = c.cmd.Wait(); close(c.done) }()
	t.Cleanup(func() {
		// joinShutdownCLI's deferred original Wait completes even if a controller
		// assertion aborts its frame; remote accounting is registered afterwards.
		defer func() { f.SchedulePingAccounting(false, true) }()
		joinShutdownCLI(t, c.cmd, c.done, release, f.Close, time.After(15*time.Second))
	})
	return c
}
func (c *shutdownCLI) joined(t *testing.T, success bool) {
	t.Helper()
	select {
	case <-c.done:
	case <-time.After(12 * time.Second):
		t.Fatal("S13_CLI_COMPLETION_MISSING")
	}
	if success {
		require.NoError(t, c.result)
	} else {
		var exit *exec.ExitError
		require.ErrorAs(t, c.result, &exit)
		require.Equal(t, 1, exit.ExitCode())
	}
	require.NotNil(t, c.cmd.ProcessState)
	var status syscall.WaitStatus
	_, err := syscall.Wait4(c.cmd.Process.Pid, &status, syscall.WNOHANG, nil)
	require.ErrorIs(t, err, syscall.ECHILD, "controller Wait for CLI, not SSH-grandchild retirement")
	require.Equal(t, 1, strings.Count(c.logs.String(), "owned SSH child retired"), "production original SSH Wait retirement receipt required")
	t.Logf("S13_ORIGINAL_CLI_WAIT pid=%d result=%v ECHILD=%v", c.cmd.Process.Pid, c.result, err)
}
func shutdownNoPublication(t *testing.T, host string) {
	t.Helper()
	for _, suffix := range []string{".sock", ".state.json"} {
		path, err := instancepath.StatusPath(host, suffix)
		require.NoError(t, err)
		_, err = os.Lstat(path)
		require.True(t, os.IsNotExist(err), "no publication before monitoring or after joined shutdown")
	}
}
func shutdownAdmissionHeld(t *testing.T, host string) {
	t.Helper()
	owner, err := instancepath.Acquire(host)
	if owner != nil {
		_ = owner.Close()
	}
	require.Error(t, err, "S13_ADMISSION_RELEASED_BEFORE_ORIGINAL_COMPLETION")
}

func TestShutdownOwnedCLIStartup(t *testing.T) {
	for _, mode := range []string{"startup-open", "startup-run", "startup-error"} {
		t.Run(mode, func(t *testing.T) {
			f := sshfixture.NewWatchdog(t)
			binary := shutdownProofBinary(t)
			t.Setenv("RDHPF_S13_PROOF_MODE", mode)
			if mode == "startup-error" {
				f.Configure("yes", f.WrongKey, "", f.Key, "no")
				c := startShutdownCLI(t, f, binary, func() {})
				c.joined(t, false)
				shutdownNoPublication(t, f.Host)
				require.NotContains(t, c.logs.String(), "manager starting")
				return
			}
			h := newShutdownHold(t)
			c := startShutdownCLI(t, f, binary, h.close)
			conn, before := h.await(t, mode, c.cmd.Process.Pid)
			require.True(t, before.Deadline.IsZero())
			require.NoError(t, c.cmd.Process.Signal(syscall.SIGTERM))
			var stopped shutdownProofReceipt
			require.Eventually(t, func() bool { stopped = shutdownCommand(t, conn, "snapshot"); return stopped.Stopping }, time.Second, 10*time.Millisecond)
			require.False(t, stopped.Deadline.IsZero())
			shutdownAdmissionHeld(t, f.Host)
			shutdownNoPublication(t, f.Host)
			h.close()
			c.joined(t, true)
			require.NotContains(t, c.logs.String(), "manager starting", "no publishers/monitoring on canceled startup transition")
			shutdownNoPublication(t, f.Host)
		})
	}
}

// Record dispatch at the exact owned launcher before its exec chain. These PIDs
// are observations only; faults still use the retained in-process Master handle.
func recordShutdownMasterStarts(t *testing.T, f *sshfixture.WatchdogFixture) func() []string {
	t.Helper()
	launcher := filepath.Join(f.Dir, "watchdog-bin", "ssh")
	original, err := os.ReadFile(launcher)
	require.NoError(t, err)
	record := filepath.Join(f.Dir, "master-starts")
	prefix := fmt.Sprintf("#!/bin/sh\nif [ \"$1\" = -MN ]; then printf '%%s\\n' \"$$\" >> %q; fi\n", record)
	require.NoError(t, os.WriteFile(launcher, []byte(prefix+strings.TrimPrefix(string(original), "#!/bin/sh\n")), 0700))
	return func() []string {
		data, err := os.ReadFile(record)
		require.NoError(t, err)
		return strings.Fields(string(data))
	}
}
func shutdownRealTraffic(t *testing.T, f *sshfixture.WatchdogFixture) func() bool {
	t.Helper()
	type backend struct {
		id, body string
		port     int
	}
	var backends []backend
	for range 2 {
		listener, err := net.Listen("tcp4", "127.0.0.1:0")
		require.NoError(t, err)
		port := listener.Addr().(*net.TCPAddr).Port
		require.NoError(t, listener.Close())
		id := f.Backend(sshfixture.Nginx, port)
		body := fmt.Sprintf("s13-transition-%s-%d", f.Alias, port)
		f.Docker("exec", id, "sh", "-c", "printf %s "+body+" > /usr/share/nginx/html/index.html")
		backends = append(backends, backend{id, body, port})
	}
	client := &http.Client{Timeout: time.Second, Transport: &http.Transport{Proxy: nil, DisableKeepAlives: true}}
	t.Cleanup(client.CloseIdleConnections)
	return func() bool {
		for _, b := range backends {
			r, err := client.Get(fmt.Sprintf("http://127.0.0.1:%d/", b.port))
			if err != nil {
				return false
			}
			body, readErr := io.ReadAll(io.LimitReader(r.Body, 4096))
			closeErr := r.Body.Close()
			if readErr != nil || closeErr != nil || r.StatusCode != http.StatusOK || string(body) != b.body {
				return false
			}
			t.Logf("S13_REAL_HTTP full-ID=%s port=%d token=%q", b.id, b.port, body)
		}
		return true
	}
}
func TestShutdownOwnedCLIHeldSignals(t *testing.T) {
	f := sshfixture.NewWatchdog(t)
	traffic := shutdownRealTraffic(t, f)
	starts := recordShutdownMasterStarts(t, f)
	h := newShutdownHold(t)
	t.Setenv("RDHPF_S13_PROOF_MODE", "held-stop")
	c := startShutdownCLI(t, f, shutdownProofBinary(t), h.close)
	require.Eventually(t, traffic, 20*time.Second, 25*time.Millisecond)
	require.NoError(t, c.cmd.Process.Signal(syscall.SIGTERM))
	conn, first := h.await(t, "retirement-completion", c.cmd.Process.Pid)
	require.True(t, first.Stopping)
	require.False(t, first.Deadline.IsZero())
	for _, sig := range []syscall.Signal{syscall.SIGINT, syscall.SIGTERM, syscall.SIGINT} {
		require.NoError(t, c.cmd.Process.Signal(sig))
		snapshot := shutdownCommand(t, conn, "snapshot")
		require.Equal(t, first.Deadline, snapshot.Deadline, "real repeated signals cannot renew the first epoch")
	}
	select {
	case <-c.done:
		t.Fatal("repeated signal bypassed held owned completion")
	case <-time.After(100 * time.Millisecond):
	}
	shutdownAdmissionHeld(t, f.Host)
	require.Len(t, starts(), 1)
	h.close()
	c.joined(t, true)
	shutdownNoPublication(t, f.Host)
	require.Len(t, starts(), 1)
	t.Log("S13_HELD_SIGNALS joined; hold was post-original-Wait retirement completion, not original Wait")
}
func TestShutdownOwnedCLIRecoveryFence(t *testing.T) {
	f := sshfixture.NewWatchdog(t)
	traffic := shutdownRealTraffic(t, f)
	starts := recordShutdownMasterStarts(t, f)
	h := newShutdownHold(t)
	t.Setenv("RDHPF_S13_PROOF_MODE", "recovery")
	c := startShutdownCLI(t, f, shutdownProofBinary(t), h.close)
	running, _ := h.await(t, "running", c.cmd.Process.Pid)
	require.Eventually(t, traffic, 20*time.Second, 25*time.Millisecond)
	fault := shutdownCommand(t, running, "fault-retained-master")
	require.Empty(t, fault.Error)
	require.True(t, fault.OriginalWait, "in-process original SSH Wait, not controller child Wait")
	require.Equal(t, []string{strconv.Itoa(fault.Child)}, starts())
	retiring, before := h.await(t, "retirement-completion", c.cmd.Process.Pid)
	require.False(t, before.Stopping, "recovery retirement began before terminal request")
	require.NoError(t, c.cmd.Process.Signal(syscall.SIGTERM))
	var stopped shutdownProofReceipt
	require.Eventually(t, func() bool { stopped = shutdownCommand(t, retiring, "snapshot"); return stopped.Stopping }, time.Second, 10*time.Millisecond)
	require.False(t, stopped.Deadline.IsZero())
	shutdownAdmissionHeld(t, f.Host)
	require.Len(t, starts(), 1, "no replacement dispatch before held retirement completes")
	h.close()
	c.joined(t, true)
	require.Equal(t, []string{strconv.Itoa(fault.Child)}, starts(), "no actual successor dispatch after terminal fence")
	require.NotContains(t, c.logs.String(), "SSH ControlMaster recreated successfully")
	shutdownNoPublication(t, f.Host)
	t.Log("S13_RECOVERY_FENCE original SSH Wait, controller and original CLI Wait joined; no successor dispatch")
}

func TestShutdownOwnedCLIFailureChild(t *testing.T) {
	if os.Getenv("RDHPF_S13_FAILURE_CHILD") != "1" {
		return
	}
	f := sshfixture.NewWatchdog(t)
	_ = f.Backend(sshfixture.Alpine, 0)
	_ = startShutdownCLI(t, f, os.Getenv("RDHPF_WATCHDOG_BINARY"), func() {})
	require.Eventually(t, func() bool {
		for _, a := range f.Snapshot() {
			if a.Operation == "events" && a.RealChildStarted {
				return true
			}
		}
		return false
	}, 10*time.Second, 10*time.Millisecond)
	t.Fatal("S13_INTENTIONAL_ASSERTION_AFTER_ALLOCATION before manual cleanup")
}
func TestShutdownOwnedCLIFailureCleanup(t *testing.T) {
	if os.Getenv("RDHPF_HOSTED_TRUST") != "1" {
		t.Skip("owned hosted assertion-failure cleanup control")
	}
	executable, err := os.Executable()
	require.NoError(t, err)
	cmd := exec.Command(executable, "-test.v", "-test.timeout=90s", "-test.run=^TestShutdownOwnedCLIFailureChild$")
	cmd.Env = append(os.Environ(), "RDHPF_S13_FAILURE_CHILD=1")
	cmd.WaitDelay = 2 * time.Second
	output, err := cmd.CombinedOutput()
	t.Logf("S13_FAILURE_CONTROL actual child output:\n%s", output)
	require.Error(t, err, "deliberate post-allocation assertion must fail")
	text := string(output)
	markers := []string{"S13_INTENTIONAL_ASSERTION_AFTER_ALLOCATION", "WATCHDOG_FIXTURE_JOINED", "S13_CLI_JOINED", "WATCHDOG_PING_ACCOUNT_COMPLETE", "removed owned backend", "restored exact disposable-account SSH config"}
	last := -1
	for _, marker := range markers {
		index := strings.Index(text, marker)
		require.Greater(t, index, last, "failure/control/CLI/accounting/environment ordering: %s", marker)
		last = index
	}
	require.Contains(t, text, "owned SSH child retired", "actual CLI-owned original SSH Wait proof required")
	require.NotContains(t, text, "JOIN_DIAGNOSTIC")
	require.NotContains(t, text, "test timed out")
	require.Contains(t, text, "unknown=0")
}
