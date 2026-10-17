//go:build rdhpf_integration

package integration

import (
	"bufio"
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"github.com/tomaszpeksa/remote-docker-host-port-forwarder/internal/ssh"
	"github.com/tomaszpeksa/remote-docker-host-port-forwarder/internal/statefile"
	"github.com/tomaszpeksa/remote-docker-host-port-forwarder/internal/testutil/sshfixture"
)

// Processes and runtime directories are retained on uncertain cleanup. A test
// deadline is not authority to delete a live publisher's persistent lock inode.
type isolationProcess struct {
	cmd    *exec.Cmd
	done   chan struct{}
	err    error
	log    string
	stop   func() error
	joined bool
}

func startIsolationProcess(t *testing.T, cmd *exec.Cmd, logPath string, stop func() error) *isolationProcess {
	t.Helper()
	log, err := os.OpenFile(logPath, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	require.NoError(t, err)
	cmd.Stdout, cmd.Stderr = log, log
	p := &isolationProcess{cmd: cmd, done: make(chan struct{}), log: logPath, stop: stop}
	t.Cleanup(func() {
		if cmd.Process != nil {
			p.finish(t)
		}
		require.NoError(t, log.Close())
	})
	require.NoError(t, cmd.Start())
	go func() { p.err = cmd.Wait(); close(p.done) }()
	return p
}

func (p *isolationProcess) finish(t *testing.T) {
	t.Helper()
	if p.joined {
		return
	}
	select {
	case <-p.done:
	default:
		err := p.stop()
		if err != nil && err != os.ErrProcessDone {
			t.Errorf("owned process stop: %v", err)
		}
	}
	select {
	case <-p.done:
		p.joined = true
		t.Logf("owned process joined pid=%d result=%v log=%s", p.cmd.Process.Pid, p.err, p.log)
	case <-time.After(15 * time.Second):
		t.Errorf("ISOLATION_CLEANUP_UNCERTAIN: owned process did not join; preserve runtime and log=%s", p.log)
	}
}

func isolationFixture(t *testing.T) (*sshfixture.Fixture, string) {
	t.Helper()
	if os.Getenv("RDHPF_HOSTED_ISOLATION") != "1" {
		t.Skip("owned isolation phase not selected")
	}
	require.False(t, testing.Short(), "selected isolation proof must execute")
	f := sshfixture.New(t)
	f.Configure("yes", f.PublicKey, "", f.Key, "no")
	evidence := os.Getenv("RDHPF_ISOLATION_EVIDENCE")
	require.True(t, filepath.IsAbs(evidence), "explicit owned evidence directory required")
	evidence = filepath.Join(evidence, t.Name())
	require.NoError(t, os.MkdirAll(evidence, 0700))
	return f, evidence
}

func isolationHome(t *testing.T, processes *[]*isolationProcess) string {
	t.Helper()
	home, err := os.MkdirTemp("/tmp", "rdhpf-isolation-")
	require.NoError(t, err)
	t.Cleanup(func() {
		for _, p := range *processes {
			if !p.joined {
				t.Logf("preserving runtime %s: process cleanup uncertain", home)
				return
			}
		}
		require.NoError(t, os.RemoveAll(home))
		t.Logf("retired quiescent runtime %s", home)
	})
	return home
}

func isolationBackends(t *testing.T, f *sshfixture.Fixture) (map[int]string, map[int]string) {
	t.Helper()
	bodies, ids := map[int]string{}, map[int]string{}
	for i := 0; i < 2; i++ {
		listener, err := net.Listen("tcp4", "127.0.0.1:0")
		require.NoError(t, err)
		port := listener.Addr().(*net.TCPAddr).Port
		require.NoError(t, listener.Close())
		id := f.Backend(sshfixture.Nginx, port)
		body := fmt.Sprintf("isolation-%s-%d", f.Alias, port)
		f.Docker("exec", id, "sh", "-c", "printf %s "+body+" > /usr/share/nginx/html/index.html")
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		err = waitForNginxBackend(ctx, f.Host, id)
		cancel()
		require.NoError(t, err)
		bodies[port], ids[port] = body, id
		t.Logf("owned backend id=%s port=%d exact-body=%q", id, port, body)
	}
	return bodies, ids
}

func isolationHTTP(t *testing.T) *http.Client {
	t.Helper()
	transport := &http.Transport{DisableKeepAlives: true, Proxy: nil}
	t.Cleanup(transport.CloseIdleConnections)
	return &http.Client{Transport: transport, Timeout: 2 * time.Second}
}

func isolationAwait(t *testing.T, p *isolationProcess, description string, condition func() error) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	recoveryAwait(t, ctx, p.done, description, condition)
}

func isolationEnv(home string) []string {
	// Explicit child-local overrides; OpenSSH uses the disposable account's real
	// provisioned config, not runtime HOME. Do not alter that trust in a launcher.
	return append(os.Environ(), "HOME="+home, "RDHPF_ENABLE_LABEL_PORTS=", "RDHPF_LOG_LEVEL=debug")
}

func isolationSnapshot(home string) (statefile.StateFile, string, error) {
	var snapshot statefile.StateFile
	paths, err := filepath.Glob(filepath.Join(home, ".rdhpf", "*.state.json"))
	if err != nil || len(paths) != 1 {
		return snapshot, "", fmt.Errorf("publication paths=%v: %v", paths, err)
	}
	data, err := os.ReadFile(paths[0])
	if err == nil {
		err = json.Unmarshal(data, &snapshot)
	}
	return snapshot, paths[0], err
}

func assertIsolationOwner(t *testing.T, snapshot statefile.StateFile, pid int, host string, ids map[int]string) {
	t.Helper()
	require.Equal(t, pid, snapshot.PID, "ISOLATION_PUBLICATION_OWNER")
	require.Equal(t, host, snapshot.Host)
	for port, id := range ids {
		found := false
		for _, forward := range snapshot.Forwards {
			if forward.Port == port {
				require.Equal(t, id, forward.ContainerID, "ISOLATION_FORWARD_OWNER")
				require.Equal(t, "active", forward.Status)
				found = true
			}
		}
		require.True(t, found, "ISOLATION_FORWARD_OWNER port=%d", port)
	}
}

var isolationMuxPattern = regexp.MustCompile(`controlPath=(/tmp/rdhpf-[^ ]+/mux) pid=([0-9]+)`)

func isolationMux(t *testing.T, p *isolationProcess) string {
	t.Helper()
	data, err := os.ReadFile(p.log)
	require.NoError(t, err)
	match := isolationMuxPattern.FindSubmatch(data)
	require.Len(t, match, 3, "owned application's successful kernel association must be logged: %s", data)
	t.Logf("observed application-owned mux=%s ssh-pid=%s (diagnostic only; never signaled)", match[1], match[2])
	return string(match[1])
}

func TestOwnedCLIIsolation(t *testing.T) {
	f, evidence := isolationFixture(t)
	other := sshfixture.New(t)
	other.Configure("yes", other.PublicKey, "", other.Key, "no")
	bodies, ids := isolationBackends(t, f)
	client := isolationHTTP(t)
	binary := os.Getenv("RDHPF_ISOLATION_BINARY")
	require.True(t, filepath.IsAbs(binary), "actual built CLI required")
	file, err := os.Open(binary)
	require.NoError(t, err)
	hash := sha256.New()
	_, err = io.Copy(hash, file)
	require.NoError(t, err)
	require.NoError(t, file.Close())
	t.Logf("actual CLI executable=%s sha256=%x", binary, hash.Sum(nil))
	var processes []*isolationProcess
	home := isolationHome(t, &processes)
	startCLI := func(name string) *isolationProcess {
		cmd := exec.Command(binary, "run", "--host", f.Host, "--log-level", "debug")
		cmd.Env = isolationEnv(home)
		p := startIsolationProcess(t, cmd, filepath.Join(evidence, name+".log"), func() error { return cmd.Process.Signal(os.Interrupt) })
		processes = append(processes, p)
		return p
	}
	ready := func(p *isolationProcess) (statefile.StateFile, string) {
		var snapshot statefile.StateFile
		var statePath string
		isolationAwait(t, p, "actual CLI publication and two-port HTTP", func() error {
			var e error
			snapshot, statePath, e = isolationSnapshot(home)
			if e != nil {
				return e
			}
			if snapshot.PID != p.cmd.Process.Pid {
				return fmt.Errorf("publication not yet owned by current CLI")
			}
			for port, body := range bodies {
				if e := recoveryHTTP(client, port, body); e != nil {
					return e
				}
			}
			return nil
		})
		assertIsolationOwner(t, snapshot, p.cmd.Process.Pid, f.Host, ids)
		return snapshot, statePath
	}
	incumbent := startCLI("incumbent") // no-owner positive control: real authenticated traffic
	before, statePath := ready(incumbent)
	mux := isolationMux(t, incumbent)
	muxBefore, err := os.Lstat(mux)
	require.NoError(t, err)
	socketPath := strings.TrimSuffix(statePath, ".state.json") + ".sock"
	socketBefore, err := os.Lstat(socketPath)
	require.NoError(t, err)
	lockPath := filepath.Join(home, ".rdhpf", "owner.lock")
	lockBefore, err := os.Stat(lockPath)
	require.NoError(t, err)
	alias := home + "-alias"
	require.NoError(t, os.Symlink(home, alias))
	t.Cleanup(func() { require.NoError(t, os.Remove(alias)) })
	markerDir := filepath.Join(evidence, "contender-bin")
	require.NoError(t, os.Mkdir(markerDir, 0700))
	marker := filepath.Join(markerDir, "started")
	require.NoError(t, os.WriteFile(filepath.Join(markerDir, "ssh"), []byte("#!/bin/sh\nprintf started >> \"$RDHPF_ISOLATION_SSH_MARKER\"\nexit 33\n"), 0700))
	for _, tc := range []struct{ name, home, host string }{{"same-target", home, f.Host}, {"different-target", home, other.Host}, {"directory-alias", alias, f.Host}} {
		t.Run(tc.name, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			cmd := exec.CommandContext(ctx, binary, "run", "--host", tc.host)
			cmd.Env = append(isolationEnv(tc.home), "PATH="+markerDir, "RDHPF_ISOLATION_SSH_MARKER="+marker)
			output, runErr := cmd.CombinedOutput()
			require.NoError(t, os.WriteFile(filepath.Join(evidence, tc.name+".log"), output, 0600))
			require.NoError(t, ctx.Err(), "outer deadline is not duplicate proof")
			_, markerErr := os.Stat(marker)
			require.True(t, os.IsNotExist(markerErr), "ISOLATION_LOSER_BEFORE_SSH: contender dispatched SSH")
			require.Error(t, runErr, "ISOLATION_DUPLICATE_EXIT")
			require.NotNil(t, cmd.ProcessState)
			require.NotZero(t, cmd.ProcessState.ExitCode(), "ISOLATION_DUPLICATE_EXIT")
			require.Contains(t, string(output), "another rdhpf publisher already owns this status directory", "ISOLATION_DUPLICATE_DIAGNOSTIC")
			t.Logf("contender=%s actual_exit=%d", tc.name, cmd.ProcessState.ExitCode())
			select {
			case <-incumbent.done:
				t.Fatal("ISOLATION_INCUMBENT_LIVE")
			default:
			}
			after, _, e := isolationSnapshot(home)
			require.NoError(t, e)
			assertIsolationOwner(t, after, incumbent.cmd.Process.Pid, f.Host, ids)
			require.Equal(t, before.StartedAt, after.StartedAt)
			for _, old := range before.Forwards {
				for _, current := range after.Forwards {
					if old.Port == current.Port {
						require.Equal(t, old.CreatedAt, current.CreatedAt)
					}
				}
			}
			for path, identity := range map[string]os.FileInfo{mux: muxBefore, socketPath: socketBefore, lockPath: lockBefore} {
				info, e := os.Lstat(path)
				require.NoError(t, e)
				require.True(t, os.SameFile(identity, info), "ISOLATION_INCUMBENT_PATH_IDENTITY %s", path)
			}
			conn, e := net.DialTimeout("unix", socketPath, time.Second)
			require.NoError(t, e)
			require.NoError(t, conn.SetDeadline(time.Now().Add(time.Second)))
			var live statefile.StateFile
			e = json.NewDecoder(conn).Decode(&live)
			require.NoError(t, conn.Close())
			require.NoError(t, e)
			assertIsolationOwner(t, live, incumbent.cmd.Process.Pid, f.Host, ids)
			for port, body := range bodies {
				require.NoError(t, recoveryHTTP(client, port, body), "ISOLATION_INCUMBENT_HTTP")
			}
		})
	}
	incumbent.finish(t)
	require.True(t, incumbent.joined, "must not launch successor before quiescence")
	for _, path := range []string{mux, filepath.Dir(mux), socketPath, statePath} {
		_, e := os.Lstat(path)
		require.True(t, os.IsNotExist(e), "owned cleanup left %s", path)
	}
	successor := startCLI("successor")
	ready(successor)
	lockAfter, err := os.Stat(lockPath)
	require.NoError(t, err)
	require.True(t, os.SameFile(lockBefore, lockAfter), "ISOLATION_PERSISTENT_LOCK")
	require.NotEqual(t, mux, isolationMux(t, successor))
	successor.finish(t)
	require.True(t, successor.joined)
	f.NoPrompt()
	t.Log("ISOLATION_CLI_PROVED: three actual rejected CLI processes; incumbent and quiescent successor fresh two-port HTTP")
}

// The helper owns a production Master in a separate native process. EOF from
// its retained parent's pipe requests ordinary Close; no output PID is killed.
func TestIsolationMasterProcess(t *testing.T) {
	if os.Getenv("RDHPF_ISOLATION_MASTER_HELPER") != "1" {
		return
	}
	require.Equal(t, "1", os.Getenv("RDHPF_HOSTED_ISOLATION"))
	require.Equal(t, "true", os.Getenv("GITHUB_ACTIONS"))
	host := os.Getenv("RDHPF_ISOLATION_MASTER_HOST")
	port, err := strconv.Atoi(os.Getenv("RDHPF_ISOLATION_MASTER_PORT"))
	require.NoError(t, err)
	m, err := ssh.NewMaster(host, slog.New(slog.NewTextHandler(os.Stderr, nil)))
	require.NoError(t, err)
	defer func() { require.NoError(t, m.Close()) }()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	require.NoError(t, m.Open(ctx))
	require.NoError(t, ssh.AddForward(ctx, m.ControlPath(), host, port, port, slog.Default()))
	require.NoError(t, m.Check())
	ready, err := json.Marshal(map[string]any{"isolation_ready": m.ControlPath(), "pid": os.Getpid()})
	require.NoError(t, err)
	_, err = os.Stdout.Write(append(ready, '\n'))
	require.NoError(t, err)
	stopped := make(chan struct{})
	go func() { _, _ = bufio.NewReader(os.Stdin).ReadByte(); close(stopped) }()
	select {
	case <-stopped:
	case <-time.After(time.Minute):
		t.Error("owned Master parent did not release helper")
	}
}

func TestOwnedMasterProcessIsolation(t *testing.T) {
	f, evidence := isolationFixture(t)
	bodies, _ := isolationBackends(t, f)
	client := isolationHTTP(t)
	var processes []*isolationProcess
	home := isolationHome(t, &processes)
	executable, err := os.Executable()
	require.NoError(t, err)
	paths := map[*isolationProcess]string{}
	ports := map[*isolationProcess]int{}
	for port := range bodies {
		cmd := exec.Command(executable, "-test.run=^TestIsolationMasterProcess$", "-test.v")
		cmd.Env = append(isolationEnv(home), "SSH_TEST_HOST=", "RDHPF_ISOLATION_MASTER_HELPER=1", "RDHPF_ISOLATION_MASTER_HOST="+f.Host, "RDHPF_ISOLATION_MASTER_PORT="+strconv.Itoa(port))
		input, e := cmd.StdinPipe()
		require.NoError(t, e)
		p := startIsolationProcess(t, cmd, filepath.Join(evidence, strconv.Itoa(port)+".log"), input.Close)
		processes = append(processes, p)
		ports[p] = port
		isolationAwait(t, p, "separate Master process HTTP", func() error {
			data, e := os.ReadFile(p.log)
			if e != nil {
				return e
			}
			for _, line := range strings.Split(string(data), "\n") {
				var ready struct {
					Path string `json:"isolation_ready"`
					PID  int    `json:"pid"`
				}
				if json.Unmarshal([]byte(line), &ready) == nil && ready.Path != "" && ready.PID == cmd.Process.Pid {
					paths[p] = ready.Path
				}
			}
			if paths[p] == "" {
				return fmt.Errorf("helper not ready")
			}
			return recoveryHTTP(client, port, bodies[port])
		})
	}
	a, b := processes[0], processes[1]
	require.NotEqual(t, paths[a], paths[b])
	before, err := os.Lstat(paths[b])
	require.NoError(t, err)
	a.finish(t)
	require.True(t, a.joined)
	require.NoError(t, a.err)
	_, err = os.Lstat(paths[a])
	require.True(t, os.IsNotExist(err))
	select {
	case <-b.done:
		t.Fatal("ISOLATION_OTHER_MASTER_LIVE")
	default:
	}
	after, err := os.Lstat(paths[b])
	require.NoError(t, err)
	require.True(t, os.SameFile(before, after), "ISOLATION_OTHER_MASTER_SOCKET")
	require.NoError(t, recoveryHTTP(client, ports[b], bodies[ports[b]]), "ISOLATION_OTHER_MASTER_HTTP")
	b.finish(t)
	require.True(t, b.joined)
	require.NoError(t, b.err)
	_, err = os.Lstat(paths[b])
	require.True(t, os.IsNotExist(err))
	f.NoPrompt()
	t.Log("ISOLATION_MASTER_PROVED: separate native Master-owning processes; retiring one preserved the other socket and fresh HTTP")
}
