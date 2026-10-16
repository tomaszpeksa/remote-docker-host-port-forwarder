//go:build rdhpf_integration

package docker

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"github.com/tomaszpeksa/remote-docker-host-port-forwarder/internal/testutil/sshfixture"
)

// Directly exercise the private label command: an earlier port-inspection
// authentication failure must not conceal a missing BatchMode at this site.
func TestLabelMissingMuxTrust(t *testing.T) {
	for _, scenario := range []string{"matching", "mismatch", "unknown", "accept-new", "encrypted", "password"} {
		t.Run(scenario, func(t *testing.T) {
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
			id := f.Backend(sshfixture.Alpine, 0, "rdhpf.forward.18081=80")
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			ports, err := inspectPortsFromLabels(ctx, f.Host, filepath.Join(f.Dir, "absent-mux"), id)
			require.NoError(t, ctx.Err())
			if success {
				require.NoError(t, err)
				require.Equal(t, []int{18081}, ports)
			} else {
				require.Error(t, err)
			}
			f.NoPrompt()
			if scenario == "accept-new" {
				require.Contains(t, f.Contents(f.UserHosts), f.PublicKey)
			} else {
				require.Equal(t, before, f.Contents(f.UserHosts))
			}
		})
	}
}
