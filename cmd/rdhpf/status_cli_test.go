package main

import (
	"bytes"
	"context"
	"encoding/json"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"github.com/tomaszpeksa/remote-docker-host-port-forwarder/internal/socket"
	"github.com/tomaszpeksa/remote-docker-host-port-forwarder/internal/state"
	"github.com/tomaszpeksa/remote-docker-host-port-forwarder/internal/statefile"
	"go.yaml.in/yaml/v3"
	"golang.org/x/sys/unix"
)

// This is the actual binary/exit/stdout/stderr boundary, with no SSH or Engine.
func TestBuiltStatusObservationMatrix(t *testing.T) {
	binary := filepath.Join(t.TempDir(), "rdhpf")
	buildCtx, buildCancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer buildCancel()
	build := exec.CommandContext(buildCtx, "go", "build", "-o", binary, ".")
	data, err := build.CombinedOutput()
	require.NoError(t, err, string(data))
	cases := []struct {
		name, socket, file    string
		failure, stale, empty bool
	}{
		{name: "live", socket: "valid"},
		{name: "absent-empty", empty: true},
		{name: "refused-empty", socket: "refused", empty: true},
		{name: "fresh-file", file: "valid"},
		{name: "refused-file", socket: "refused", file: "valid"},
		{name: "stale-file", file: "stale", stale: true},
		{name: "silent-fallback", socket: "silent", file: "valid"},
		{name: "partial-fallback", socket: "partial", file: "valid"},
		{name: "corrupt-socket-fallback", socket: "corrupt", file: "valid"},
		{name: "version-socket-fallback", socket: "unsupported-version", file: "valid"},
		{name: "host-socket-fallback", socket: "wrong-host", file: "valid"},
		{name: "silent-missing", socket: "silent", failure: true},
		{name: "partial-missing", socket: "partial", failure: true},
		{name: "corrupt-socket-missing", socket: "corrupt", failure: true},
		{name: "missing-version-socket", socket: "missing-version", failure: true},
		{name: "version-socket-missing", socket: "unsupported-version", failure: true},
		{name: "host-socket-missing", socket: "wrong-host", failure: true},
		{name: "corrupt-file", file: "corrupt", failure: true},
		{name: "missing-version-file", file: "missing-version", failure: true},
		{name: "version-file", file: "unsupported-version", failure: true},
		{name: "host-file", file: "wrong-host", failure: true},
		{name: "unreadable-file", file: "unreadable", failure: true},
		{name: "fifo-file", file: "fifo", failure: true},
		{name: "held-lock-shared-budget", socket: "silent", file: "locked", failure: true},
	}
	const host = "ssh://status.invalid"
	for _, tc := range cases {
		for _, format := range []string{"table", "json", "yaml"} {
			t.Run(tc.name+"/"+format, func(t *testing.T) {
				statusHome(t)
				now := time.Now()
				snapshot := statefile.NewSnapshot(host, 123, now.Add(-time.Hour), now,
					[]state.ForwardState{{ContainerID: "current-owner", Port: 8080, Status: "active", CreatedAt: now.Add(-time.Minute), UpdatedAt: now}},
					[]state.HistoryEntry{{ContainerID: "past-owner", Port: 9090, StartedAt: now.Add(-45 * time.Minute), EndedAt: now.Add(-20 * time.Minute), EndReason: "container stopped", FinalStatus: "active"}})
				payload := func(kind string) []byte {
					s := snapshot
					switch kind {
					case "missing-version":
						s.Version = ""
					case "unsupported-version":
						s.Version = "9.0"
					case "wrong-host":
						s.Host = "ssh://private-host.invalid"
					case "stale":
						s.UpdatedAt = now.Add(-11 * time.Second)
					case "corrupt":
						return []byte(`{"private-payload":!?}`)
					}
					b, e := json.Marshal(s)
					require.NoError(t, e)
					return b
				}
				if tc.file != "" {
					path, e := statefile.GetStateFilePath(host)
					require.NoError(t, e)
					if tc.file == "fifo" {
						require.NoError(t, unix.Mkfifo(path, 0600))
					} else {
						require.NoError(t, os.WriteFile(path, payload(tc.file), 0600))
						if tc.file == "unreadable" {
							require.NoError(t, os.Chmod(path, 0000))
						}
						if tc.file == "locked" {
							holdStatusFile(t, host, 8*time.Second)
						}
					}
				}
				switch tc.socket {
				case "":
				case "refused":
					path, e := socket.GetSocketPath(host)
					require.NoError(t, e)
					listener, e := net.ListenUnix("unix", &net.UnixAddr{Name: path, Net: "unix"})
					require.NoError(t, e)
					listener.SetUnlinkOnClose(false)
					require.NoError(t, listener.Close())
				case "silent":
					statusPeer(t, host, "", true)
				case "partial":
					statusPeer(t, host, `{"version":"2.0",`, true)
				default:
					statusPeer(t, host, string(payload(tc.socket)), false)
				}
				// Eight seconds is the observer's kill/join rescue, not the product budget.
				ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
				defer cancel()
				cmd := exec.CommandContext(ctx, binary, "status", "--host", host, "--format", format)
				var stdout, stderr bytes.Buffer
				cmd.Stdout = &stdout
				cmd.Stderr = &stderr
				start := time.Now()
				err := cmd.Run()
				elapsed := time.Since(start)
				require.NoError(t, ctx.Err(), "observer rescue fired; original CLI Run has joined")
				if tc.failure {
					var exit *exec.ExitError
					require.ErrorAs(t, err, &exit)
					require.Equal(t, 1, exit.ExitCode())
					require.Empty(t, stdout.String())
					require.NotEmpty(t, stderr.String())
				} else {
					require.NoError(t, err, stderr.String())
					if tc.stale {
						require.Contains(t, stderr.String(), "stale")
						require.Contains(t, stderr.String(), "not verified running")
					} else {
						require.Empty(t, stderr.String())
					}
					if format == "table" {
						if tc.empty {
							require.Equal(t, "No forwards\n", stdout.String())
						} else {
							require.Contains(t, stdout.String(), "current-owner")
							require.Contains(t, stdout.String(), "past-owner")
							require.Contains(t, stdout.String(), "45m ago")
							require.Contains(t, stdout.String(), "20m ago")
						}
					} else {
						var output struct {
							Forwards []map[string]any `json:"forwards" yaml:"forwards"`
						}
						if format == "json" {
							require.NoError(t, json.Unmarshal(stdout.Bytes(), &output))
						} else {
							require.NoError(t, yaml.Unmarshal(stdout.Bytes(), &output))
						}
						require.NotNil(t, output.Forwards, "empty structured forwards must not be null")
						if tc.empty {
							require.Empty(t, output.Forwards)
						} else {
							require.Len(t, output.Forwards, 2)
							require.Equal(t, "current-owner", output.Forwards[0]["container_id"])
							require.Equal(t, "active", output.Forwards[0]["state"])
							require.Equal(t, "past-owner", output.Forwards[1]["container_id"])
							require.Equal(t, "25m0s", output.Forwards[1]["duration"])
							require.Equal(t, "container stopped", output.Forwards[1]["reason"])
						}
					}
				}
				for _, secret := range []string{"private-payload", "private-host", os.Getenv("HOME")} {
					require.NotContains(t, stderr.String(), secret)
				}
				if tc.file == "locked" {
					require.GreaterOrEqual(t, elapsed, 4900*time.Millisecond, "must use remaining budget after socket attempt")
					require.Less(t, elapsed, 6500*time.Millisecond, "product budget is 5s; extra 1.5s is observer scheduling tolerance only")
				} else if strings.Contains(tc.socket, "silent") || tc.socket == "partial" {
					require.GreaterOrEqual(t, elapsed, 900*time.Millisecond)
					require.Less(t, elapsed, 2*time.Second, "socket cap is 1s, not the full command budget")
				}
				t.Logf("exit=%v elapsed=%s stdout=%q stderr=%q", err, elapsed, stdout.String(), stderr.String())
			})
		}
	}
}

// ExecuteContext is the caller-cancellation entry to the actual command tree.
// A subprocess preserves the CLI exit/stdout/stderr boundary for all formats.
func TestStatusCanceledCommandProcess(t *testing.T) {
	if os.Getenv("RDHPF_STATUS_CANCEL_HELPER") == "" {
		return
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	rootCmd.SetArgs([]string{"status", "--host", "ssh://status.invalid", "--format", os.Getenv("RDHPF_STATUS_CANCEL_HELPER")})
	if executeCommand(ctx) != nil {
		os.Exit(1)
	}
	os.Exit(0)
}

func TestStatusCanceledCommandExitFormats(t *testing.T) {
	statusHome(t)
	executable, err := os.Executable()
	require.NoError(t, err)
	for _, format := range []string{"table", "json", "yaml"} {
		t.Run(format, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			cmd := exec.CommandContext(ctx, executable, "-test.run=^TestStatusCanceledCommandProcess$")
			cmd.Env = append(os.Environ(), "RDHPF_STATUS_CANCEL_HELPER="+format)
			var stdout, stderr bytes.Buffer
			cmd.Stdout = &stdout
			cmd.Stderr = &stderr
			err := cmd.Run()
			require.NoError(t, ctx.Err())
			var exit *exec.ExitError
			require.ErrorAs(t, err, &exit)
			require.Equal(t, 1, exit.ExitCode())
			require.Empty(t, stdout.String())
			require.Contains(t, stderr.String(), "context canceled")
		})
	}
}
