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
	"syscall"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"github.com/tomaszpeksa/remote-docker-host-port-forwarder/internal/statefile"
	"github.com/tomaszpeksa/remote-docker-host-port-forwarder/internal/testutil/sshfixture"
)

func TestForwardingOwnedCLIConflict(t *testing.T) {
	f := sshfixture.NewWatchdog(t)
	binary := os.Getenv("RDHPF_WATCHDOG_BINARY")
	require.True(t, filepath.IsAbs(binary), "actual built CLI required")
	info, err := buildinfo.ReadFile(binary)
	require.NoError(t, err)
	require.Equal(t, "github.com/tomaszpeksa/remote-docker-host-port-forwarder/cmd/rdhpf", info.Path)
	t.Logf("S12_CLI_BUILD %s", info.String())
	type backend struct {
		id, body string
		port     int
	}
	makeBackend := func() backend {
		listener, err := net.Listen("tcp4", "127.0.0.1:0")
		require.NoError(t, err)
		port := listener.Addr().(*net.TCPAddr).Port
		require.NoError(t, listener.Close())
		id := f.Backend(sshfixture.Nginx, port)
		body := fmt.Sprintf("s12-cli-%s-%d", f.Alias, port)
		f.Docker("exec", id, "sh", "-c", "printf %s "+body+" > /usr/share/nginx/html/index.html")
		return backend{id: id, body: body, port: port}
	}
	target, sibling := makeBackend(), makeBackend()
	conflict, err := net.Listen("tcp4", fmt.Sprintf("127.0.0.1:%d", target.port))
	require.NoError(t, err)
	t.Cleanup(func() { _ = conflict.Close() })
	client := &http.Client{Timeout: time.Second, Transport: &http.Transport{Proxy: nil, DisableKeepAlives: true}}
	defer client.CloseIdleConnections()
	httpOK := func(b backend) bool {
		response, err := client.Get(fmt.Sprintf("http://127.0.0.1:%d/", b.port))
		if err != nil {
			return false
		}
		body, readErr := io.ReadAll(io.LimitReader(response.Body, 4096))
		closeErr := response.Body.Close()
		return readErr == nil && closeErr == nil && response.StatusCode == http.StatusOK && string(body) == b.body
	}
	reader, err := statefile.NewReader(f.Host)
	require.NoError(t, err)
	var logs watchdogCLILog
	cmd := exec.Command(binary, "run", "--host", f.Host, "--log-level", "debug")
	cmd.Stdout = io.MultiWriter(os.Stderr, &logs)
	cmd.Stderr = cmd.Stdout
	cmd.WaitDelay = 2 * time.Second
	require.NoError(t, cmd.Start())
	done := make(chan struct{})
	var result error
	go func() { result = cmd.Wait(); close(done) }()
	defer func() {
		joined := false
		defer func() { f.SchedulePingAccounting(false, joined) }()
		select {
		case <-done:
		default:
			_ = cmd.Process.Signal(syscall.SIGTERM)
		}
		f.Close()
		select {
		case <-done:
		case <-time.After(15 * time.Second):
			t.Fatal("S12_CLI_JOIN_PENDING")
		}
		joined = true
		require.NoError(t, result, "normal owned CLI shutdown")
		var status syscall.WaitStatus
		_, waitErr := syscall.Wait4(cmd.Process.Pid, &status, syscall.WNOHANG, nil)
		require.ErrorIs(t, waitErr, syscall.ECHILD)
		t.Logf("S12_CLI_ORIGINAL_WAIT pid=%d error=%v state=%v ECHILD=%v", cmd.Process.Pid, result, cmd.ProcessState, waitErr)
	}()
	require.Eventually(t, func() bool {
		snapshot, err := reader.Read()
		if err != nil {
			return false
		}
		for _, forward := range snapshot.Forwards {
			if forward.ContainerID == target.id && forward.Port == target.port && forward.Status == "conflict" {
				return httpOK(sibling)
			}
		}
		return false
	}, 20*time.Second, 25*time.Millisecond, "S12_CLI_CONFLICT_AND_HEALTHY_SIBLING")
	time.Sleep(1600 * time.Millisecond)
	require.True(t, httpOK(sibling))
	require.NoError(t, conflict.Close())
	release := time.Now()
	require.Eventually(t, func() bool { return httpOK(target) && httpOK(sibling) }, 10*time.Second, 25*time.Millisecond, "S12_CLI_RELEASED_CONFLICT_RECOVERS")
	t.Logf("S12_CLI_TWO_HTTP_TOKENS target=%s port=%d body=%q sibling=%s port=%d body=%q release=%s restored=%s; no target event/restart/manual action", target.id, target.port, target.body, sibling.id, sibling.port, sibling.body, release, time.Now())
}
