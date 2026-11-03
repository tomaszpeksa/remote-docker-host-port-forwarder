//go:build rdhpf_integration

package main

import (
	"debug/buildinfo"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"github.com/tomaszpeksa/remote-docker-host-port-forwarder/internal/instancepath"
	"github.com/tomaszpeksa/remote-docker-host-port-forwarder/internal/testutil/sshfixture"
)

// This uses actual Engine publications, real OpenSSH and the normal built CLI.
// The fixture's bridge targets are sufficient for lifecycle proof, not an S16
// assertion about literal/wildcard endpoint semantics.
func TestShutdownOwnedCLISignals(t *testing.T) {
	for _, signal := range []syscall.Signal{syscall.SIGINT, syscall.SIGTERM} {
		t.Run(signal.String(), func(t *testing.T) {
			f := sshfixture.NewWatchdog(t)
			binary := os.Getenv("RDHPF_WATCHDOG_BINARY")
			require.True(t, filepath.IsAbs(binary), "normal exact-source built CLI required")
			info, err := buildinfo.ReadFile(binary)
			require.NoError(t, err)
			require.Equal(t, "github.com/tomaszpeksa/remote-docker-host-port-forwarder/cmd/rdhpf", info.Path)
			t.Logf("S13_CLI_BUILD %s", info.String())
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
				body := fmt.Sprintf("s13-%s-%d", f.Alias, port)
				f.Docker("exec", id, "sh", "-c", "printf %s "+body+" > /usr/share/nginx/html/index.html")
				backends = append(backends, backend{id: id, body: body, port: port})
			}
			client := &http.Client{Timeout: time.Second, Transport: &http.Transport{Proxy: nil, DisableKeepAlives: true}}
			defer client.CloseIdleConnections()
			traffic := func() bool {
				for _, b := range backends {
					response, err := client.Get(fmt.Sprintf("http://127.0.0.1:%d/", b.port))
					if err != nil {
						return false
					}
					body, readErr := io.ReadAll(io.LimitReader(response.Body, 4096))
					closeErr := response.Body.Close()
					if readErr != nil || closeErr != nil || response.StatusCode != http.StatusOK || string(body) != b.body {
						return false
					}
					t.Logf("S13_HTTP full-ID=%s port=%d token=%q", b.id, b.port, body)
				}
				return true
			}
			var accounting sync.Once
			for generation := 0; generation < 2; generation++ {
				var logs watchdogCLILog
				cmd := exec.Command(binary, "run", "--host", f.Host, "--log-level", "debug")
				cmd.Stdout = io.MultiWriter(os.Stderr, &logs)
				cmd.Stderr, cmd.WaitDelay = cmd.Stdout, 2*time.Second
				require.NoError(t, cmd.Start())
				done := make(chan struct{})
				var result error
				go func() { result = cmd.Wait(); close(done) }()
				// Own release and original CLI Wait before any traffic assertion.
				t.Cleanup(func() {
					defer func() { accounting.Do(func() { f.SchedulePingAccounting(false, true) }) }()
					joinShutdownCLI(t, cmd, done, func() {}, f.Close, time.After(15*time.Second))
				})
				require.Eventually(t, traffic, 20*time.Second, 25*time.Millisecond, "two real HTTP tokens before shutdown/successor")
				requested := time.Now()
				require.NoError(t, cmd.Process.Signal(signal))
				// These are repeated real signals to the retained CLI, not context cancellation.
				require.NoError(t, cmd.Process.Signal(syscall.SIGTERM))
				require.NoError(t, cmd.Process.Signal(syscall.SIGINT))
				select {
				case <-done:
				case <-time.After(12 * time.Second):
					t.Fatal("S13_ORDINARY_SHUTDOWN_BUDGET: 10s plus 2s observer tolerance")
				}
				joined := time.Now()
				require.NoError(t, result, "ordinary signals and repeated requests must exit0")
				require.NotNil(t, cmd.ProcessState)
				var status syscall.WaitStatus
				_, waitErr := syscall.Wait4(cmd.Process.Pid, &status, syscall.WNOHANG, nil)
				require.ErrorIs(t, waitErr, syscall.ECHILD)
				text := logs.String()
				require.Equal(t, 1, strings.Count(text, "owned SSH child retired"), "one original Master Wait retirement event")
				retired, err := watchdogCLITimestamp(text, "retired_at")
				require.NoError(t, err)
				require.False(t, retired.Before(requested))
				require.False(t, retired.After(joined))
				socketPath, err := instancepath.StatusPath(f.Host, ".sock")
				require.NoError(t, err)
				statePath, err := instancepath.StatusPath(f.Host, ".state.json")
				require.NoError(t, err)
				for _, path := range []string{socketPath, statePath} {
					_, err := os.Lstat(path)
					require.True(t, os.IsNotExist(err), "publication removed only after joins")
				}
				for _, b := range backends {
					conn, dialErr := net.DialTimeout("tcp4", fmt.Sprintf("127.0.0.1:%d", b.port), time.Second)
					if conn != nil {
						_ = conn.Close()
					}
					require.Error(t, dialErr, "local listener removed after original retirement receipt")
					require.Equal(t, "true", f.Docker("inspect", "--format", "{{.State.Running}}", b.id), "independent backend must survive application cleanup")
				}
				owner, err := instancepath.Acquire(f.Host)
				require.NoError(t, err, "successor admitted only after joined CLI completion")
				require.NoError(t, owner.Close())
				t.Logf("S13_CLI_JOIN pid=%d signal=%s generation=%d requested=%s actual-retirement=%s original-CLI-Wait=%s elapsed=%s ECHILD=%v admission=true", cmd.Process.Pid, signal, generation, requested, retired, joined, joined.Sub(requested), waitErr)
			}
			f.Close()
		})
	}
}
