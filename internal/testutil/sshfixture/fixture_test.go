//go:build rdhpf_integration

package sshfixture

import (
	"os"
	"os/exec"
	"os/user"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// The subprocess reaches the actual New boundary. Fake Docker always fails,
// so even the old implementation cannot reach the account's SSH config.
func TestFixtureOwnerProcess(t *testing.T) {
	if os.Getenv("RDHPF_OWNER_PROCESS") != "1" {
		return
	}
	New(t)
	t.Fatal("malformed/fake fixture unexpectedly admitted")
}

func TestFixtureRejectsMalformedOwnerBeforeDocker(t *testing.T) {
	account, err := user.Current()
	require.NoError(t, err)
	executable, err := os.Executable()
	require.NoError(t, err)
	for _, tc := range []struct {
		name, owner string
		wantDocker  bool
	}{
		{"empty", "", false}, {"newline", "\n", false}, {"text", "foreign\n", false},
		{"missing-pid", "1792000000-\n", false}, {"zero-pid", "1792000000-0\n", false},
		{"space", " 1792000000-42\n", false}, {"extra-line", "1792000000-42\njunk\n", false},
		{"producer-syntax", "1792000000-42\n", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root := t.TempDir()
			require.NoError(t, os.Mkdir(filepath.Join(root, ".itests"), 0700))
			require.NoError(t, os.Mkdir(filepath.Join(root, "bin"), 0700))
			require.NoError(t, os.WriteFile(filepath.Join(root, "go.mod"), []byte("module owned.invalid\n"), 0600))
			require.NoError(t, os.WriteFile(filepath.Join(root, ".itests/container-id"), []byte(strings.Repeat("a", 64)+"\n"), 0600))
			require.NoError(t, os.WriteFile(filepath.Join(root, ".itests/owner"), []byte(tc.owner), 0600))
			marker := filepath.Join(root, "docker-called")
			require.NoError(t, os.WriteFile(filepath.Join(root, "bin/docker"), []byte("#!/bin/sh\nprintf called > \"$OWNER_MARKER\"\nexit 23\n"), 0700))
			cmd := exec.Command(executable, "-test.run=^TestFixtureOwnerProcess$", "-test.v")
			cmd.Dir = root
			cmd.Env = []string{"PATH=" + filepath.Join(root, "bin"), "HOME=" + account.HomeDir, "GITHUB_ACTIONS=true", "SSH_TEST_HOST=ssh://testuser@localhost:2222", "RDHPF_HOSTED_TRUST=1", "RDHPF_OWNER_PROCESS=1", "OWNER_MARKER=" + marker}
			output, err := cmd.CombinedOutput()
			require.Error(t, err, "%s", output)
			_, statErr := os.Stat(marker)
			require.Equal(t, tc.wantDocker, statErr == nil, "must reject malformed ownership before comparison/inspection: %s", output)
		})
	}
}

func TestBackendUnboundNamesAreDistinct(t *testing.T) {
	first, second := recordedBackendNames(t, 0)
	require.NotEqual(t, first, second, "UNBOUND_BACKEND_NAMES_MUST_BE_DISTINCT")
}

func TestBackendPublishedNamesRetainCollisionIdentity(t *testing.T) {
	first, second := recordedBackendNames(t, 18090)
	require.Equal(t, "owned-name-fixture-18090", first, "PUBLISHED_BACKEND_MUST_RETAIN_COLLISION_IDENTITY")
	require.Equal(t, first, second, "REPEATED_PUBLISHED_BACKEND_MUST_RETAIN_COLLISION_IDENTITY")
}

func recordedBackendNames(t *testing.T, port int) (string, string) {
	t.Helper()
	root := t.TempDir()
	calls := filepath.Join(root, "calls")
	// Record actual Backend argv and cleanup identity, not a real Docker conflict.
	// No Docker daemon or target traffic is used.
	script := `#!/bin/sh
case "$1" in
run) printf 'run %s\n' "$4" >> "$BACKEND_CALLS"; printf '%064d\n' 1 ;;
inspect) printf '%s\n' "$BACKEND_OWNER" ;;
rm) printf 'rm %s\n' "$3" >> "$BACKEND_CALLS" ;;
*) exit 23 ;;
esac
`
	require.NoError(t, os.WriteFile(filepath.Join(root, "docker"), []byte(script), 0700))
	t.Setenv("PATH", root)
	t.Setenv("BACKEND_CALLS", calls)
	t.Setenv("BACKEND_OWNER", "owned-name-fixture")
	t.Run("create-and-clean", func(t *testing.T) {
		f := &Fixture{T: t, Alias: "owned-name-fixture"}
		f.Backend(Alpine, port)
		f.Backend(Alpine, port)
	})
	data, err := os.ReadFile(calls)
	require.NoError(t, err)
	lines := strings.Split(strings.TrimSpace(string(data)), "\n")
	require.Len(t, lines, 4)
	first, second := strings.TrimPrefix(lines[0], "run "), strings.TrimPrefix(lines[1], "run ")
	require.Equal(t, "rm "+second, lines[2])
	require.Equal(t, "rm "+first, lines[3])
	return first, second
}

func TestAuthorizedKeysExactBytes(t *testing.T) {
	for _, original := range []string{"", "  ssh-ed25519 original comment  ", "ssh-ed25519 original\n\n", "\t# comment\r\nssh-ed25519 original\r\n"} {
		t.Run(strings.ReplaceAll(original, "\n", "_"), func(t *testing.T) {
			root := t.TempDir()
			file := filepath.Join(root, "authorized_keys")
			require.NoError(t, os.WriteFile(file, []byte(original), 0600))
			// Fixed Docker stand-in copies stdin/output verbatim. No daemon, SSH or keys.
			require.NoError(t, os.WriteFile(filepath.Join(root, "docker"), []byte("#!/bin/sh\nprintf diagnostic >&2\nif [ \"$2\" = -i ]; then /bin/cat > \"$AUTH_FILE\"; else /bin/cat \"$AUTH_FILE\"; fi\n"), 0700))
			t.Setenv("PATH", root)
			t.Setenv("AUTH_FILE", file)
			f := &Fixture{T: t, Container: strings.Repeat("a", 64)}
			captured := f.dockerOutput("exec", f.Container, "cat", "/home/testuser/.ssh/authorized_keys")
			require.Equal(t, original, captured, "capture must not trim")
			t.Run("temporary augmentation", func(t *testing.T) {
				f.T = t
				f.authorizeTemporaryKey("ssh-ed25519 temporary\n")
				augmented, err := os.ReadFile(file)
				require.NoError(t, err)
				want := original
				if want != "" && !strings.HasSuffix(want, "\n") {
					want += "\n"
				}
				require.Equal(t, want+"ssh-ed25519 temporary\n", string(augmented))
			})
			restored, err := os.ReadFile(file)
			require.NoError(t, err)
			require.Equal(t, []byte(original), restored, "restore must not append a newline")
		})
	}
}
