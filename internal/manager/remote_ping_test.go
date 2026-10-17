package manager

import (
	"bytes"
	"context"
	"errors"
	"io"
	"log/slog"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"github.com/tomaszpeksa/remote-docker-host-port-forwarder/internal/config"
	"github.com/tomaszpeksa/remote-docker-host-port-forwarder/internal/ssh"
)

func TestRemoteDockerPing(t *testing.T) {
	for _, tc := range []struct {
		name, host, destination string
		port                    []string
	}{
		{"default", "ssh://app@fixture.invalid", "app@fixture.invalid", nil},
		{"port", "ssh://app@fixture.invalid:2222", "app@fixture.invalid", []string{"-p", "2222"}},
		{"option", "ssh://-oProxyCommand=false", "-oProxyCommand=false", nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			t.Setenv("HOME", dir)
			t.Setenv("PATH", dir)
			t.Setenv("PING_DIR", dir)
			t.Setenv("DOCKER_HOST", "ssh://unrelated.invalid:1")
			t.Setenv("DOCKER_CONTEXT", "unrelated-context")
			t.Setenv("DOCKER_TLS_VERIFY", "1")
			t.Setenv("DOCKER_CERT_PATH", "/unrelated-certificates")
			require.NoError(t, os.WriteFile(filepath.Join(dir, "ssh"), []byte("#!/bin/sh\nprintf '%s\\000' \"$@\" > \"$PING_DIR/ssh-args\"\nexport PING_REMOTE=yes\nfor last do :; done\nexec /bin/sh -c \"$last\"\n"), 0700))
			require.NoError(t, os.WriteFile(filepath.Join(dir, "docker"), []byte("#!/bin/sh\nif [ \"${PING_REMOTE:-}\" != yes ]; then printf local > \"$PING_DIR/local-docker\"; fi\nprintf '%s\\000' \"$@\" > \"$PING_DIR/docker-args\"\nprintf works\nexit \"${PING_EXIT:-0}\"\n"), 0700))
			logger := slog.New(slog.NewTextHandler(io.Discard, nil))
			master, err := ssh.NewMaster(tc.host, logger)
			require.NoError(t, err)
			t.Cleanup(func() { require.NoError(t, master.Close()) })
			m := NewManager(&config.Config{Host: tc.host}, nil, nil, master, nil, nil, logger)
			ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
			defer cancel()
			var previous string
			for range 2 {
				require.NoError(t, m.watchdog.dockerPing.Ping(ctx))
				require.NoFileExists(t, filepath.Join(dir, "local-docker"), "ping used ambient Docker instead of application SSH target")
				data, err := os.ReadFile(filepath.Join(dir, "ssh-args"))
				require.NoError(t, err)
				args := strings.Split(strings.TrimSuffix(string(data), "\x00"), "\x00")
				want := append([]string{"-o", "BatchMode=yes", "-S", master.ControlPath()}, tc.port...)
				want = append(want, "--", tc.destination)
				require.Equal(t, want, args[:len(args)-1])
				data, err = os.ReadFile(filepath.Join(dir, "docker-args"))
				require.NoError(t, err)
				operands := strings.Split(strings.TrimSuffix(string(data), "\x00"), "\x00")
				require.Len(t, operands, 9)
				name := operands[3]
				require.Regexp(t, `^rdhpf-ping-[0-9]+$`, name)
				require.NotEqual(t, previous, name)
				previous = name
				require.Equal(t, []string{"run", "--rm", "--name", name, "--label", "rdhpf.health-check=true", "alpine:latest", "echo", "works"}, operands)
			}
			t.Setenv("PING_EXIT", "17")
			require.ErrorContains(t, m.watchdog.dockerPing.Ping(ctx), "event health ping docker run failed: exit status 17")
			cancel()
			require.ErrorIs(t, m.watchdog.dockerPing.Ping(ctx), context.Canceled, "cancellation must not be mistaken for the prior exit-status failure")
		})
	}
}

// This child transfers copies of the command's descriptors to the test owner.
// No orphan grandchild or elapsed sleep is used to keep the exec copiers open.
func TestRemoteDockerPingDescriptorChild(t *testing.T) {
	path := os.Getenv("PING_DESCRIPTOR_SOCKET")
	if path == "" {
		return
	}
	conn, err := net.DialUnix("unix", nil, &net.UnixAddr{Name: path, Net: "unix"})
	if err != nil {
		os.Exit(21)
	}
	_, _, err = conn.WriteMsgUnix([]byte("fds"), syscall.UnixRights(1, 2), nil)
	if err != nil {
		os.Exit(22)
	}
	_ = conn.Close()
	os.Exit(0)
}

func TestRemoteDockerPingOwnedLifetime(t *testing.T) {
	t.Run("LargeDiagnostics", func(t *testing.T) {
		dir := t.TempDir()
		t.Setenv("HOME", dir)
		t.Setenv("PATH", dir)
		payload := strings.Repeat("o", 4096)
		script := "#!/bin/sh\ni=0; while [ \"$i\" -lt 40 ]; do printf '%s' '" + payload + "'; printf '%s' '" + payload + "' >&2; i=$((i+1)); done\n"
		require.NoError(t, os.WriteFile(filepath.Join(dir, "ssh"), []byte(script), 0700))
		var logs bytes.Buffer
		r := newRemoteDockerPingRunner("ssh://owned.invalid", "/unused", slog.New(slog.NewTextHandler(&logs, &slog.HandlerOptions{Level: slog.LevelDebug})))
		require.NoError(t, r.Ping(context.Background()))
		require.LessOrEqual(t, logs.Len(), 2*(64<<10)+1024, "PROBE_OUTPUT_BOUND")
		require.Contains(t, logs.String(), "truncated=true")
	})
	t.Run("InheritedOutput", func(t *testing.T) {
		dir, err := os.MkdirTemp("/tmp", "ping-fd-")
		require.NoError(t, err)
		t.Cleanup(func() { require.NoError(t, os.RemoveAll(dir)) })
		t.Setenv("HOME", dir)
		t.Setenv("PATH", dir)
		socket := filepath.Join(dir, "fd")
		listener, err := net.ListenUnix("unix", &net.UnixAddr{Name: socket, Net: "unix"})
		require.NoError(t, err)
		defer listener.Close()
		t.Setenv("PING_DESCRIPTOR_SOCKET", socket)
		executable, err := os.Executable()
		require.NoError(t, err)
		t.Setenv("PING_TEST_EXECUTABLE", executable)
		require.NoError(t, os.WriteFile(filepath.Join(dir, "ssh"), []byte("#!/bin/sh\nexec \"$PING_TEST_EXECUTABLE\" -test.run '^TestRemoteDockerPingDescriptorChild$'\n"), 0700))
		r := newRemoteDockerPingRunner("ssh://owned.invalid", "/unused", slog.New(slog.NewTextHandler(io.Discard, nil)))
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		done := make(chan error, 1)
		go func() { done <- r.Ping(ctx) }()
		joined := false
		var held []*os.File
		defer func() {
			for _, f := range held {
				_ = f.Close()
			}
			cancel()
			if !joined {
				<-done
			}
		}()
		require.NoError(t, listener.SetDeadline(time.Now().Add(3*time.Second)))
		conn, err := listener.AcceptUnix()
		require.NoError(t, err)
		defer conn.Close()
		require.NoError(t, conn.SetDeadline(time.Now().Add(3*time.Second)))
		data, control := make([]byte, 16), make([]byte, syscall.CmsgSpace(8))
		_, n, _, _, err := conn.ReadMsgUnix(data, control)
		require.NoError(t, err)
		messages, err := syscall.ParseSocketControlMessage(control[:n])
		require.NoError(t, err)
		for _, message := range messages {
			fds, err := syscall.ParseUnixRights(&message)
			require.NoError(t, err)
			for _, fd := range fds {
				held = append(held, os.NewFile(uintptr(fd), "held-output"))
			}
		}
		require.Len(t, held, 2)
		select {
		case err := <-done:
			joined = true
			require.True(t, errors.Is(err, exec.ErrWaitDelay), "PROBE_PIPE_JOIN: %v", err)
		case <-time.After(3 * time.Second):
			t.Error("PROBE_PIPE_JOIN: direct child exited but inherited output blocked completion")
		}
	})
	t.Run("CanceledInFlight", func(t *testing.T) {
		dir := t.TempDir()
		t.Setenv("HOME", dir)
		t.Setenv("PATH", dir)
		t.Setenv("PING_READY", filepath.Join(dir, "ready"))
		require.NoError(t, os.WriteFile(filepath.Join(dir, "ssh"), []byte("#!/bin/sh\nprintf ready > \"$PING_READY\"\nwhile :; do :; done\n"), 0700))
		r := newRemoteDockerPingRunner("ssh://owned.invalid", "/unused", slog.New(slog.NewTextHandler(io.Discard, nil)))
		ctx, cancel := context.WithCancel(context.Background())
		done := make(chan error, 1)
		go func() { done <- r.Ping(ctx) }()
		joined := false
		defer func() {
			cancel()
			if !joined {
				<-done
			}
		}()
		require.Eventually(t, func() bool { _, err := os.Stat(filepath.Join(dir, "ready")); return err == nil }, time.Second, time.Millisecond)
		cancel()
		err := <-done
		joined = true
		require.Error(t, err)
	})
}
