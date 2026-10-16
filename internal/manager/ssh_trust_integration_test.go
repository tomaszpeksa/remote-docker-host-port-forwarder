//go:build rdhpf_integration

package manager

import (
	"context"
	"fmt"
	"log/slog"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"github.com/tomaszpeksa/remote-docker-host-port-forwarder/internal/config"
	"github.com/tomaszpeksa/remote-docker-host-port-forwarder/internal/docker"
	"github.com/tomaszpeksa/remote-docker-host-port-forwarder/internal/reconcile"
	"github.com/tomaszpeksa/remote-docker-host-port-forwarder/internal/ssh"
	"github.com/tomaszpeksa/remote-docker-host-port-forwarder/internal/state"
	"github.com/tomaszpeksa/remote-docker-host-port-forwarder/internal/testutil/sshfixture"
)

func TestManagerMissingMuxTrust(t *testing.T) {
	for _, operation := range []string{"startup", "label", "connectivity", "ping"} {
		for _, scenario := range []string{"matching", "mismatch", "unknown", "accept-new", "encrypted", "password"} {
			t.Run(operation+"/"+scenario, func(t *testing.T) {
				f := sshfixture.New(t)
				policy, key, identity := "yes", f.PublicKey, f.Key
				success := scenario == "matching" || scenario == "accept-new"
				switch scenario {
				case "mismatch":
					key = f.WrongKey
				case "unknown":
					policy = ""
					key = ""
				case "accept-new":
					policy = "accept-new"
					key = ""
				case "encrypted":
					identity = f.EncryptedKey
				case "password":
					identity = filepath.Join(f.Dir, "no-identity")
					f.PasswordChallenge()
				}
				f.Configure(policy, key, "", identity, "no")
				if scenario == "unknown" || scenario == "encrypted" || scenario == "password" {
					f.PromptControl()
				}
				before := f.Contents(f.UserHosts)
				master, err := ssh.NewMaster(f.Host, slog.Default())
				require.NoError(t, err)
				defer func() { require.NoError(t, master.Close()) }()
				st, history := state.NewState(), state.NewHistory()
				m := NewManager(&config.Config{Host: f.Host}, nil, reconcile.NewReconciler(st, history, slog.Default()), master, st, history, slog.Default())
				ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
				defer cancel()
				switch operation {
				case "startup":
					id := f.Backend(sshfixture.Nginx, 18083)
					// Independently observe full inventory identity, then exercise startup.
					psID := f.Docker("ps", "--no-trunc", "--filter", "id="+id, "--format", "{{.ID}}")
					require.Equal(t, id, psID)
					err = m.reconcileStartup(ctx)
					if success {
						require.NoError(t, err)
						require.Contains(t, st.GetDesired(), state.ContainerPorts{ContainerID: psID, Ports: []int{18083}})
					} else {
						require.Error(t, err)
						require.Empty(t, st.GetDesired())
					}
				case "label":
					// This real container is known to carry true; false alone would not prove auth failure.
					infrastructure, inspectErr := m.hasTestInfrastructureLabel(ctx, master.ControlPath(), f.Container)
					require.Equal(t, success, infrastructure)
					if success {
						require.NoError(t, inspectErr)
					} else {
						require.Error(t, inspectErr)
					}
				case "ping", "connectivity":
					if operation == "ping" {
						err = m.watchdog.dockerPing.Ping(ctx)
					} else {
						err = m.validateDockerConnectivity(ctx, master.ControlPath())
					}
					if success {
						require.NoError(t, err)
					} else {
						require.Error(t, err)
					}
				}
				require.NoError(t, ctx.Err(), "deadline kill is not rejection proof")
				f.NoPrompt()
				if scenario == "accept-new" {
					require.Contains(t, f.Contents(f.UserHosts), f.PublicKey)
				} else {
					require.Equal(t, before, f.Contents(f.UserHosts))
				}
			})
		}
	}
}

// This separately proves the real ping reaches the same remote event stream,
// rather than treating authentication success or a fake-shell capture as traffic.
func TestRemoteDockerPingOwnedEvent(t *testing.T) {
	f := sshfixture.New(t)
	f.Configure("yes", f.PublicKey, "", f.Key, "no")
	master, err := ssh.NewMaster(f.Host, slog.Default())
	require.NoError(t, err)
	defer func() { require.NoError(t, master.Close()) }()
	m := NewManager(&config.Config{Host: f.Host}, nil, nil, master, nil, nil, slog.Default())
	backend := f.Backend(sshfixture.Nginx, 18089)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	events, errs := streamChannelsForTest(ctx, docker.NewEventReader(f.Host, master.ControlPath(), slog.Default()))
	defer func() {
		cancel()
		for range events {
		}
		for range errs {
		}
	}()
	// Allow initial SSH setup, then require an actual owned event before probing.
	time.Sleep(time.Second)
	f.Docker("stop", "--time", "0", backend)
	waitStart := func(ids map[string]bool, kind string) {
		for {
			select {
			case event, ok := <-events:
				require.True(t, ok, "application event stream closed")
				if ids[event.ContainerID] && event.Type == kind {
					t.Logf("application stream observed %s container=%s", kind, event.ContainerID)
					return
				}
			case err, ok := <-errs:
				require.True(t, ok, "application error stream closed")
				require.NoError(t, err)
			case <-ctx.Done():
				t.Fatal("application event not observed: ", ctx.Err())
			}
		}
	}
	waitStart(map[string]bool{backend: true}, "stop")
	since := time.Now().UTC().Format(time.RFC3339Nano)
	require.True(t, t.Run("ApplicationTarget", func(t *testing.T) {
		t.Setenv("DOCKER_HOST", "ssh://unrelated.invalid:1")
		t.Setenv("DOCKER_CONTEXT", "unrelated-context")
		t.Setenv("DOCKER_TLS_VERIFY", "1")
		t.Setenv("DOCKER_CERT_PATH", "/unrelated-certificates")
		require.NoError(t, m.watchdog.dockerPing.Ping(ctx))
	}))
	ids := strings.Fields(f.Docker("events", "--since", since, "--until", fmt.Sprint(time.Now().Unix()+1), "--filter", "label=rdhpf.health-check=true", "--format", "{{.Actor.ID}}"))
	require.NotEmpty(t, ids, "native daemon recorded no health-ping events")
	wanted := make(map[string]bool)
	for _, id := range ids {
		wanted[id] = true
	}
	require.Len(t, wanted, 1, "exactly one owned test ping expected")
	waitStart(wanted, "start")
	require.Empty(t, f.Docker("ps", "-a", "--filter", "label=rdhpf.health-check=true", "-q"), "ping container must be removed by --rm")
	require.NoError(t, ctx.Err())
	f.NoPrompt()
}

// Adapt inherited channel-based tests only. S09 lifecycle proof consumes the
// joined result directly; this bridge does not supply readiness or replay.
func streamChannelsForTest(ctx context.Context, r *docker.EventReader) (<-chan docker.Event, <-chan error) {
	events := make(chan docker.Event, 10)
	errs := make(chan error, 1)
	done := r.Stream(ctx, func(ctx context.Context, e docker.Event) error {
		select {
		case events <- e:
			return nil
		case <-ctx.Done():
			return ctx.Err()
		}
	})
	go func() {
		result := <-done
		if result.Err != nil && !result.Canceled {
			errs <- result.Err
		}
		close(events)
		close(errs)
	}()
	return events, errs
}
