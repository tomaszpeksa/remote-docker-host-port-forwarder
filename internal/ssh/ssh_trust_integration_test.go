//go:build rdhpf_integration

package ssh

import (
	"context"
	"log/slog"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"github.com/tomaszpeksa/remote-docker-host-port-forwarder/internal/testutil/sshfixture"
)

func TestControlRequestTrust(t *testing.T) {
	for _, operation := range []string{"forward", "cancel", "check"} {
		for _, mode := range []string{"no", "yes", "ask"} {
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
					path := filepath.Join(f.Dir, "missing-mux")
					switch operation {
					case "forward":
						require.Error(t, AddForward(ctx, path, f.Host, 18084, 18084, slog.Default()))
					case "cancel":
						require.NoError(t, CancelForward(ctx, path, f.Host, 18084, 18084, slog.Default()), "preserve noncritical cancellation")
					case "check":
						if mode == "no" {
							master, err := NewMaster(f.Host, slog.Default())
							require.NoError(t, err)
							require.Error(t, master.Check())
							require.NoError(t, master.Close())
						} else {
							// Mixed proof: real owned fake-child association, then the actual Check
							// command uses real OpenSSH and its conditional handshake. Not a real
							// Master lifecycle/recovery claim (those have separate HTTP scenarios).
							home := os.Getenv("HOME")
							master, _ := masterFixture(t)
							require.NoError(t, master.Open(ctx))
							master.host = f.Host
							t.Setenv("PATH", f.Path)
							t.Setenv("HOME", home)
							require.Error(t, master.Check())
							require.NoError(t, master.Close())
							t.Log("mixed owned fake-child / real production check-command proof")
						}
					}
					require.NoError(t, ctx.Err(), "deadline kill is not nonprompt proof")
					f.NoPrompt()
				})
			}
		}
	}
}
