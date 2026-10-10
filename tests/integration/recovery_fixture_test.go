package integration

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"os/exec"
	"os/user"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"github.com/tomaszpeksa/remote-docker-host-port-forwarder/internal/ssh"
)

// This disruptive fixture is deliberately restricted to the existing disposable
// GitHub-hosted harness, not a user-supplied/ambient Docker installation.
func recoveryAlias(t *testing.T, host string) (string, string) {
	t.Helper()
	require.Equal(t, "true", os.Getenv("GITHUB_ACTIONS"), "recovery disruption requires the owned Actions harness")
	require.Equal(t, "ssh://testuser@localhost:2222", host)
	home, err := os.UserHomeDir()
	require.NoError(t, err)
	account, err := user.Current()
	require.NoError(t, err)
	require.Equal(t, account.HomeDir, home, "must modify only the disposable runner's actual SSH config")
	configPath := filepath.Join(home, ".ssh", "config")
	info, err := os.Lstat(configPath)
	require.NoError(t, err)
	require.True(t, info.Mode().IsRegular())
	previous, err := os.ReadFile(configPath)
	require.NoError(t, err)
	key := filepath.Join(home, ".ssh", "id_rdhpf_test")
	_, err = os.Stat(key)
	require.NoError(t, err)
	var random [8]byte
	_, err = rand.Read(random[:])
	require.NoError(t, err)
	token := hex.EncodeToString(random[:])
	alias := "rdhpf-s06-" + token
	knownHosts := filepath.Join(t.TempDir(), "known_hosts")
	block := fmt.Sprintf("Host %s\n  HostName localhost\n  User testuser\n  Port 2222\n  IdentityFile %s\n  StrictHostKeyChecking accept-new\n  UserKnownHostsFile %s\n  LogLevel ERROR\n\n", alias, key, knownHosts)
	require.NoError(t, os.WriteFile(configPath, append([]byte(block), previous...), info.Mode().Perm()))
	t.Cleanup(func() { require.NoError(t, os.WriteFile(configPath, previous, info.Mode().Perm())) })
	t.Setenv("SSH_TEST_KEY_PATH", key)
	return "ssh://testuser@" + alias + ":2222", token
}

func recoveryRemote(ctx context.Context, host, command string) (string, error) {
	target, port, err := ssh.ParseHost(host)
	if err != nil {
		return "", err
	}
	args := []string{"-o", "BatchMode=yes", "-o", "ConnectTimeout=5"}
	if key := os.Getenv("SSH_TEST_KEY_PATH"); key != "" {
		args = append(args, "-i", key)
	}
	if port != "" {
		args = append(args, "-p", port)
	}
	args = append(args, target, fmt.Sprintf("sh -c %q", command))
	output, err := exec.CommandContext(ctx, "ssh", args...).CombinedOutput()
	if err != nil {
		return "", fmt.Errorf("owned fixture command: %w: %.1024s", err, output)
	}
	return strings.TrimSpace(string(output)), nil
}

func recoveryBackend(t *testing.T, ctx context.Context, host, token string, index, port int) (string, string) {
	t.Helper()
	name := fmt.Sprintf("rdhpf-s06-%s-%d", token, index)
	// Name+label are established before run, so even a lost run reply has an
	// owned cleanup identity. Never stop a name whose ownership label differs.
	t.Cleanup(func() {
		cleanup, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		owner, err := recoveryRemote(cleanup, host, fmt.Sprintf("docker inspect --format '{{index .Config.Labels \"rdhpf.s06\"}}' %s", name))
		if err != nil {
			t.Errorf("cannot verify cleanup ownership of %s: %v", name, err)
			return
		}
		if owner != token {
			t.Errorf("refusing cleanup of foreign container %s", name)
			return
		}
		_, err = recoveryRemote(cleanup, host, "docker rm -f "+name)
		require.NoError(t, err)
	})
	command := fmt.Sprintf("docker run -d --rm --name %s --label rdhpf.s06=%s -p 172.17.0.1:%d:80 nginx:alpine", name, token, port)
	create, cancel := context.WithTimeout(ctx, 30*time.Second)
	id, err := recoveryRemote(create, host, command)
	cancel()
	require.NoError(t, err)
	require.Regexp(t, `^[a-f0-9]{64}$`, id)
	content := fmt.Sprintf("s06-%s-port-%d", token, port)
	configure, configured := context.WithTimeout(ctx, 5*time.Second)
	_, err = recoveryRemote(configure, host, fmt.Sprintf("docker exec %s sh -c 'printf %%s %s > /usr/share/nginx/html/index.html'", id, content))
	configured()
	require.NoError(t, err)
	ready, readyCancel := context.WithTimeout(ctx, 5*time.Second)
	err = waitForNginxBackend(ready, host, id)
	readyCancel()
	require.NoError(t, err)
	t.Logf("owned backend ready id=%s name=%s port=%d content=%s", id, name, port, content)
	return id, content
}

type recoveryObserver struct {
	slog.Handler
	checks *atomic.Uint64
	stream chan struct{}
	once   *sync.Once
}

func (o recoveryObserver) Handle(ctx context.Context, r slog.Record) error {
	if r.Message == "SSH health check passed" {
		o.checks.Add(1)
	}
	if r.Message == "manager event loop started" {
		o.once.Do(func() { close(o.stream) })
	}
	return o.Handler.Handle(ctx, r)
}
func (o recoveryObserver) WithAttrs(attrs []slog.Attr) slog.Handler {
	o.Handler = o.Handler.WithAttrs(attrs)
	return o
}
func (o recoveryObserver) WithGroup(name string) slog.Handler {
	o.Handler = o.Handler.WithGroup(name)
	return o
}

// This only observes or faults the exact kernel-verified connection under the
// production gate. It never calls EnsureAlive/reconciliation or signals a PID.
func recoveryMux(ctx context.Context, m *ssh.Master, host string, exit bool) (uint64, int, error) {
	var generation uint64
	var pid int
	err := m.WithConnection(ctx, func(g uint64, ready bool) error {
		if !ready {
			return fmt.Errorf("owned connection not ready")
		}
		generation = g
		target, port, err := ssh.ParseHost(host)
		if err != nil {
			return err
		}
		command := func(operation string) ([]byte, error) {
			bounded, cancel := context.WithTimeout(ctx, 3*time.Second)
			defer cancel()
			args := []string{"-S", m.ControlPath(), "-O", operation, "-p", port, target}
			return exec.CommandContext(bounded, "ssh", args...).CombinedOutput()
		}
		output, err := command("check")
		if err != nil {
			return err
		}
		if _, err = fmt.Sscanf(strings.TrimSpace(string(output)), "Master running (pid=%d)", &pid); err != nil {
			return err
		}
		if pid <= 0 {
			return fmt.Errorf("invalid observed master PID")
		}
		if exit {
			output, err = command("exit")
			if err != nil {
				return fmt.Errorf("owned mux exit: %w: %s", err, output)
			}
		}
		return nil
	})
	return generation, pid, err
}

func recoveryHTTP(client *http.Client, port int, expected string) error {
	response, err := client.Get(fmt.Sprintf("http://127.0.0.1:%d/", port))
	if err != nil {
		return err
	}
	defer response.Body.Close()
	body, err := io.ReadAll(io.LimitReader(response.Body, 4096))
	if err != nil {
		return err
	}
	if response.StatusCode != http.StatusOK || string(body) != expected {
		return fmt.Errorf("port %d: status %d body %q", port, response.StatusCode, body)
	}
	return nil
}

func recoveryAwait(t *testing.T, ctx context.Context, done <-chan struct{}, description string, condition func() error) {
	t.Helper()
	ticker := time.NewTicker(100 * time.Millisecond)
	defer ticker.Stop()
	var last error
	for {
		select {
		case <-done:
			t.Fatalf("Manager exited while waiting for %s", description)
		default:
		}
		last = condition()
		if last == nil {
			return
		}
		select {
		case <-done:
			t.Fatalf("Manager exited while waiting for %s: %v", description, last)
		case <-ctx.Done():
			t.Fatalf("%s: %v; last observation: %v", description, ctx.Err(), last)
		case <-ticker.C:
		}
	}
}
