//go:build rdhpf_integration

package manager

import (
	"context"
	"log/slog"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"github.com/tomaszpeksa/remote-docker-host-port-forwarder/internal/config"
	"github.com/tomaszpeksa/remote-docker-host-port-forwarder/internal/reconcile"
	"github.com/tomaszpeksa/remote-docker-host-port-forwarder/internal/ssh"
	"github.com/tomaszpeksa/remote-docker-host-port-forwarder/internal/state"
	"github.com/tomaszpeksa/remote-docker-host-port-forwarder/internal/testutil/sshfixture"
)

func TestManagerMissingMuxTrust(t *testing.T) {
	for _, operation := range []string{"startup", "label", "connectivity"} {
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
					// Preserve production's short-ID behavior; independently observe
					// Docker ps rather than changing reconciliation to --no-trunc.
					psID := f.Docker("ps", "--filter", "id="+id, "--format", "{{.ID}}")
					require.NotEmpty(t, psID)
					require.True(t, strings.HasPrefix(id, psID))
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
					require.True(t, m.hasTestInfrastructureLabel(ctx, master.ControlPath(), f.Container) == success)
				case "connectivity":
					err = m.validateDockerConnectivity(ctx, master.ControlPath())
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
