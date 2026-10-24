//go:build rdhpf_integration

package ssh

import (
	"bufio"
	"context"
	"fmt"
	"log/slog"
	"net"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"github.com/tomaszpeksa/remote-docker-host-port-forwarder/internal/testutil/sshfixture"
)

func TestControlRequestTrust(t *testing.T) {
	for _, operation := range []string{"forward", "cancel", "check"} {
		for _, mode := range []string{"no", "auto", "yes", "ask"} {
			for _, challenge := range []string{"unknown", "encrypted"} {
				t.Run(operation+"/"+mode+"/"+challenge, func(t *testing.T) {
					f := sshfixture.New(t)
					policy, key, identity := "", "", f.Key
					if challenge == "encrypted" {
						policy = "yes"
						key = f.PublicKey
						identity = f.EncryptedKey
					}
					f.Configure(policy, key, "", identity, mode)
					f.PromptControl()
					ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
					defer cancel()
					switch operation {
					case "forward", "cancel":
						// No owned/ready connection means no helper handshake,
						// regardless of inherited ControlMaster policy.
						master, err := NewMaster(f.Host, slog.Default())
						require.NoError(t, err)
						defer func() { require.NoError(t, master.Close()) }()
						require.NoError(t, master.WithForwarding(ctx, func(connection *Forwarding) error {
							var effect ForwardEffect
							if operation == "forward" {
								effect, err = AddForward(ctx, connection, 18084, 18084, slog.Default())
							} else {
								effect, err = CancelForward(ctx, connection, 18084, 18084, slog.Default())
							}
							require.Error(t, err)
							require.Equal(t, ForwardNotDispatched, effect)
							return nil
						}))
					case "check":
						require.Error(t, checkAfterOwnedSocketRemoval(t, f.Host, f.SSH, ctx))
					}
					require.NoError(t, ctx.Err(), "deadline kill is not nonprompt proof")
					f.NoPrompt()
				})
			}
		}
	}
}

// checkAfterOwnedSocketRemoval is mixed proof, not a real Master lifetime.
// The owned fake child closes its socket only after associate's connection closes.
// A test-only exec gate then passes the production argv unchanged to real SSH.
func checkAfterOwnedSocketRemoval(t *testing.T, host, executable string, ctx context.Context) error {
	t.Helper()
	home, path := os.Getenv("HOME"), os.Getenv("PATH")
	master, dir := masterFixture(t)
	gate := filepath.Join(dir, "remove-mux")
	t.Setenv("S07_REMOVE_MUX_GATE", gate)
	require.NoError(t, master.Open(ctx))
	// Open checks the mux between TWO associations. Acknowledge both EOFs,
	// not merely the first, before arming removal for Check's association.
	require.Eventually(t, func() bool { return startupAssociationsConsumed(gate) }, time.Second, 5*time.Millisecond)
	master.host = host
	launcher := t.TempDir()
	script := fmt.Sprintf(`#!/bin/bash
for ((attempt=0; attempt<100; attempt++)); do
  if [ -f %q ]; then
    printf '%%s\n' "$@" > %q
    exec %q "$@"
  fi
  /bin/sleep .01
done
exit 125
`, gate+".ready", gate+".argv", executable)
	require.NoError(t, os.WriteFile(filepath.Join(launcher, "ssh"), []byte(script), 0700))
	t.Setenv("PATH", launcher+string(os.PathListSeparator)+path)
	t.Setenv("HOME", home)
	require.NoError(t, os.WriteFile(gate, []byte("armed\n"), 0600))
	started := time.Now()
	err := master.Check()
	elapsed := time.Since(started)
	require.Less(t, elapsed, masterCheckTimeout-250*time.Millisecond, "internal deadline termination is not command proof")
	require.FileExists(t, gate+".ready", "owned child did not remove its socket after association")
	argv, readErr := os.ReadFile(gate + ".argv")
	require.NoError(t, readErr)
	require.NoFileExists(t, master.ControlPath())
	require.NoError(t, master.connection.cmd.Process.Signal(syscall.Signal(0)), "retain exact owned child until cleanup")
	require.NoError(t, master.Close())
	t.Logf("mixed owned fake-child / real Check command after socket removal: elapsed=%s argv=%q", elapsed, argv)
	return err
}

func startupAssociationsConsumed(gate string) bool {
	_, err := os.Stat(gate + ".startup-complete")
	return err == nil
}

// This regression holds the final startup EOF while the real child's first
// acknowledgement exists. No sleep or scheduler luck selects the bad window.
func TestOwnedAssociationCheckFallbackRejectsStaleStartupAck(t *testing.T) {
	dir, err := os.MkdirTemp("/tmp", "s07-startup-eof-")
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, os.RemoveAll(dir)) })
	listener, err := net.ListenUnix("unix", &net.UnixAddr{Name: filepath.Join(dir, "gate"), Net: "unix"})
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, listener.Close()) })
	require.NoError(t, listener.SetDeadline(time.Now().Add(3*time.Second)))
	t.Setenv("S07_FINAL_STARTUP_EOF_GATE", listener.Addr().String())
	observed := make(chan error, 1)
	go func() {
		conn, err := listener.Accept()
		if err != nil {
			observed <- err
			return
		}
		defer conn.Close()
		if err = conn.SetDeadline(time.Now().Add(3 * time.Second)); err != nil {
			observed <- err
			return
		}
		line, err := bufio.NewReader(conn).ReadString('\n')
		if err != nil {
			observed <- err
			return
		}
		gate := strings.TrimSpace(line)
		_, firstErr := os.Stat(gate + ".idle")
		var observation error
		if firstErr != nil {
			observation = fmt.Errorf("first startup acknowledgement missing: %w", firstErr)
		}
		if startupAssociationsConsumed(gate) {
			observation = fmt.Errorf("stale first acknowledgement accepted while final startup EOF is held")
		}
		_, err = conn.Write([]byte{1})
		if err != nil {
			observation = err
		}
		observed <- observation
	}()
	t.Cleanup(func() { require.NoError(t, <-observed) })
	executable := filepath.Join(t.TempDir(), "native-command-standin")
	require.NoError(t, os.WriteFile(executable, []byte("#!/bin/sh\nexit 23\n"), 0700))
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	require.ErrorContains(t, checkAfterOwnedSocketRemoval(t, "ssh://fixture.invalid", executable, ctx), "exit status 23")
}

func TestOwnedAssociationCheckFallbackGate(t *testing.T) {
	executable := filepath.Join(t.TempDir(), "native-command-standin")
	require.NoError(t, os.WriteFile(executable, []byte("#!/bin/sh\nexit 23\n"), 0700))
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	require.ErrorContains(t, checkAfterOwnedSocketRemoval(t, "ssh://fixture.invalid", executable, ctx), "exit status 23")
}
