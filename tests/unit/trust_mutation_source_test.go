package unit

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// Execute the actual control script with an owned Git boundary. No remote,
// Docker or SSH is contacted; the first overlay stops with a distinct exit.
func TestTrustMutationsAcceptedASource(t *testing.T) {
	source, err := os.ReadFile("../../scripts/itest-trust-mutations.sh")
	require.NoError(t, err)
	for _, tc := range []struct {
		name         string
		fetch, valid bool
	}{
		{"present", false, true}, {"absent", true, true},
		{"fetch-fails", true, false}, {"wrong-commit", false, false}, {"wrong-blob", false, false}, {"missing-blob", false, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root := t.TempDir()
			for _, dir := range []string{"scripts", "bin", "internal/ssh"} {
				require.NoError(t, os.MkdirAll(filepath.Join(root, dir), 0700))
			}
			script := filepath.Join(root, "scripts/itest-trust-mutations.sh")
			require.NoError(t, os.WriteFile(script, source, 0700))
			master := filepath.Join(root, "internal/ssh/master.go")
			require.NoError(t, os.WriteFile(master, []byte("original Master bytes\n"), 0600))
			fake := `#!/bin/sh
set -eu
base=5acdc328ae148c538cf076c3c62cc0f5d2726d8d
blob=12232176fbc37250c000ea6478d3c36b761c9858
printf '%s\000' "$@" >> "$ROOT/git-argv"
case "$1" in
 diff) exit 0;;
 cat-file)
  if [ "$MODE" = missing-blob ] && [ "$3" = "$base:internal/ssh/master.go" ]; then exit 1; fi
  case "$MODE" in absent|fetch-fails) [ -f "$ROOT/fetched" ]; exit $?;; esac; exit 0;;
 fetch) printf '%s\n' "$*" > "$ROOT/fetched"; [ "$MODE" != fetch-fails ]; exit $?;;
 rev-parse)
  case "$*" in
   *"$base^{commit}"*) if [ "$MODE" = wrong-commit ]; then printf wrong; else printf '%s\n' "$base"; fi;;
   *"$base:internal/ssh/master.go"*) if [ "$MODE" = wrong-blob ]; then printf wrong; else printf '%s\n' "$blob"; printf verified > "$ROOT/verified"; fi;;
   *) printf 'candidate\ncandidate-tree\n';;
  esac;;
 show)
  if [ "$2" = HEAD:internal/ssh/master.go ]; then printf 'original Master bytes\n'; else printf mutation > "$ROOT/mutation"; exit 73; fi;;
 *) exit 99;;
esac
`
			require.NoError(t, os.WriteFile(filepath.Join(root, "bin/git"), []byte(fake), 0700))
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			cmd := exec.CommandContext(ctx, "/bin/bash", script)
			cmd.Env = []string{"PATH=" + filepath.Join(root, "bin") + ":/usr/bin:/bin", "HOME=" + root, "ROOT=" + root, "MODE=" + tc.name, "GITHUB_ACTIONS=true", "RDHPF_HOSTED_TRUST=1", "SSH_TEST_HOST=ssh://testuser@localhost:2222"}
			output, err := cmd.CombinedOutput()
			require.NoError(t, ctx.Err(), "deadline is not a prerequisite refusal")
			require.Error(t, err)
			if tc.valid {
				require.Equal(t, 73, cmd.ProcessState.ExitCode(), "%s", output)
				require.FileExists(t, filepath.Join(root, "verified"))
				require.FileExists(t, filepath.Join(root, "mutation"))
			} else {
				require.Equal(t, 1, cmd.ProcessState.ExitCode(), "%s", output)
				require.NoFileExists(t, filepath.Join(root, "mutation"), "source mutation preceded accepted-A identity validation")
				require.Contains(t, string(output), "accepted-A")
			}
			if tc.fetch {
				data, err := os.ReadFile(filepath.Join(root, "fetched"))
				require.NoError(t, err)
				require.Equal(t, "fetch --no-tags --no-write-fetch-head --depth=1 origin 5acdc328ae148c538cf076c3c62cc0f5d2726d8d", strings.TrimSpace(string(data)))
			} else {
				require.NoFileExists(t, filepath.Join(root, "fetched"))
			}
			data, err := os.ReadFile(master)
			require.NoError(t, err)
			require.Equal(t, "original Master bytes\n", string(data))
		})
	}
}
