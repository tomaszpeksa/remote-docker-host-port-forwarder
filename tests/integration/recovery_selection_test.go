package integration

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestRecoverySelectionSkipsBeforeMutation(t *testing.T) {
	for _, actions := range []string{"false", "true"} {
		t.Run(actions, func(t *testing.T) {
			home := t.TempDir()
			t.Setenv("HOME", home)
			t.Setenv("PATH", t.TempDir())
			t.Setenv("GITHUB_ACTIONS", actions)
			t.Setenv("RDHPF_HOSTED_RECOVERY", "")
			// TestMain already ran with the outer environment; this invokes the real
			// scenario body without contacting requested local infrastructure.
			t.Setenv("SSH_TEST_HOST", "ssh://configured-local.invalid:2222")
			configDir := filepath.Join(home, ".ssh")
			require.NoError(t, os.Mkdir(configDir, 0700))
			config := filepath.Join(configDir, "config")
			require.NoError(t, os.WriteFile(config, []byte("unchanged"), 0600))
			skipped := false
			t.Run("scenario", func(t *testing.T) { defer func() { skipped = t.Skipped() }(); TestConnectionLossRecovery(t) })
			require.True(t, skipped, "hosted-only body must produce a named skip before mutation")
			data, err := os.ReadFile(config)
			require.NoError(t, err)
			require.Equal(t, "unchanged", string(data))
			entries, err := os.ReadDir(home)
			require.NoError(t, err)
			require.Len(t, entries, 1, "no admission/private fixture mutation")
		})
	}
}
