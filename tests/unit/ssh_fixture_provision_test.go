package unit

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// These tools model only an owned disposable daemon. Barriers suspend actual
// script commands; no timing sleeps, real Docker or local SSH daemon are used.
func provisionScripts(t *testing.T) (string, []string, func(string) ([]byte, error)) {
	t.Helper()
	root := t.TempDir()
	bin := filepath.Join(root, "bin")
	require.NoError(t, os.Mkdir(bin, 0700))
	require.NoError(t, os.Mkdir(filepath.Join(root, "scripts"), 0700))
	for _, name := range []string{"itest-up.sh", "itest-down.sh"} {
		data, err := os.ReadFile("../../scripts/" + name)
		require.NoError(t, err)
		require.NoError(t, os.WriteFile(filepath.Join(root, "scripts", name), data, 0700))
	}
	write := func(name, body string) {
		require.NoError(t, os.WriteFile(filepath.Join(bin, name), []byte("#!/bin/bash\nset -eu\n"+body), 0700))
	}
	write("ssh-keygen", `while [ "$1" != -f ]; do shift; done
printf fixture > "$2"; printf 'ssh-ed25519 fixture\n' > "$2.pub"
`)
	write("ssh", `exit 0
`)
	write("docker", `printf '%s\n' "$*" >> "$FIXTURE_ROOT/docker.log"
if [ "$1" = "${FIXTURE_CLAIM_POSITION:-}" ]; then "$FIXTURE_TEST_EXE" -test.run=^TestOwnedFixtureClaimProcess$ >&2; fi
case "$1" in
container) test -f "$FIXTURE_ROOT/live-id";;
build) if [ -f "$FIXTURE_ROOT/build-fail" ]; then exit 17; fi;;
create)
 test ! -f "$FIXTURE_ROOT/live-id" || exit 19
 if [ -f "$FIXTURE_ROOT/create-uncertain" ]; then exit 23; fi
 n=0; if [ -f "$FIXTURE_ROOT/serial" ]; then n=$(cat "$FIXTURE_ROOT/serial"); fi
 n=$((n+1)); printf '%s\n' "$n" > "$FIXTURE_ROOT/serial"
 printf '%064d\n' "$n" > "$FIXTURE_ROOT/live-id"
 cat "$FIXTURE_ROOT/.itests/owner" > "$FIXTURE_ROOT/live-owner"
 cat "$FIXTURE_ROOT/live-id";;
inspect) test -f "$FIXTURE_ROOT/live-id"; cat "$FIXTURE_ROOT/live-owner";;
ps) if [ -f "$FIXTURE_ROOT/live-id" ]; then cat "$FIXTURE_ROOT/live-id"; fi;;
rm) test "$3" = "$(cat "$FIXTURE_ROOT/live-id")"; rm "$FIXTURE_ROOT/live-id" "$FIXTURE_ROOT/live-owner";;
exec) if [ "$3" = /usr/sbin/sshd ] && [ "$4" = -T ]; then printf 'persourcepenalties no\n'; elif [ "$3" = cat ]; then printf 'ssh-ed25519 fixture\n'; fi;;
esac
`)
	// Preserve the caller relationship when observing the private failure handoff.
	// Sourcing executes down in this same child; no extra parent PID is inserted.
	write("bash", `case "${FIXTURE_CLAIM_POSITION:-}" in
cleanup-before) "$FIXTURE_TEST_EXE" -test.run=^TestOwnedFixtureClaimProcess$; exec /bin/bash "$@";;
cleanup-after) . "$1"; "$FIXTURE_TEST_EXE" -test.run=^TestOwnedFixtureClaimProcess$;;
*) exec /bin/bash "$@";;
esac
`)
	executable, err := os.Executable()
	require.NoError(t, err)
	env := []string{"PATH=" + bin + ":/usr/bin:/bin", "HOME=" + root, "PWD=" + root, "FIXTURE_ROOT=" + root, "FIXTURE_TEST_EXE=" + executable, "GORACE=atexit_sleep_ms=0"}
	run := func(script string) ([]byte, error) {
		cmd := exec.Command("/bin/bash", filepath.Join(root, "scripts", script))
		cmd.Dir = root
		cmd.Env = env
		return cmd.CombinedOutput()
	}
	return root, env, run
}

func TestOwnedFixtureProvisionExcludesTeardown(t *testing.T) {
	for _, phase := range []string{"build", "create"} {
		t.Run(phase, func(t *testing.T) {
			root, env, run := provisionScripts(t)
			up, release, output := startFixtureClaimCommand(t, root, env, phase, "/bin/bash", filepath.Join(root, "scripts/itest-up.sh"))
			owner, err := os.ReadFile(filepath.Join(root, ".itests/owner"))
			require.NoError(t, err)
			assert.DirExists(t, filepath.Join(root, ".itests/operation"), "provisioning must retain its exclusive claim")
			if _, statErr := os.Stat(filepath.Join(root, ".itests/operation/provisioner")); statErr == nil {
				foreign := exec.Command("/bin/bash", filepath.Join(root, "scripts/itest-down.sh"))
				foreign.Env = append(append([]string{}, env...), "_RDHPF_ITEST_PROVISION_OWNER="+strings.TrimSpace(string(owner)))
				foreign.Dir = root
				result, err := foreign.CombinedOutput()
				require.Error(t, err, "matching token from a different parent must not steal the claim: %s", result)
			}
			result, downErr := run("itest-down.sh")
			assert.Error(t, downErr, "down must refuse a suspended provisioning command: %s", result)
			// On reviewed source down may succeed. Complete a newer legitimate startup
			// before releasing A to demonstrate its stale create redirection/cleanup.
			if downErr == nil {
				result, err = run("itest-up.sh")
				require.NoError(t, err, "new startup: %s", result)
				newerID, err := os.ReadFile(filepath.Join(root, ".itests/container-id"))
				require.NoError(t, err)
				release()
				_ = up.Wait()
				retained, err := os.ReadFile(filepath.Join(root, ".itests/container-id"))
				assert.NoError(t, err, "obsolete startup must not remove newer identity: %s", output.String())
				assert.Equal(t, string(newerID), string(retained), "obsolete startup must not truncate newer identity")
				return
			}
			retained, err := os.ReadFile(filepath.Join(root, ".itests/owner"))
			require.NoError(t, err)
			require.Equal(t, string(owner), string(retained))
			release()
			require.NoError(t, up.Wait(), "%s", output.String())
			id, err := os.ReadFile(filepath.Join(root, ".itests/container-id"))
			require.NoError(t, err)
			live, err := os.ReadFile(filepath.Join(root, "live-id"))
			require.NoError(t, err)
			require.Equal(t, string(id), string(live))
			require.NoDirExists(t, filepath.Join(root, ".itests/operation"), "successful provisioner releases claim")
			result, err = run("itest-down.sh")
			require.NoError(t, err, "%s", result)
			require.NoFileExists(t, filepath.Join(root, "live-id"))
		})
	}
}

func TestOwnedFixtureProvisionFailedHandoff(t *testing.T) {
	for _, phase := range []string{"cleanup-before", "cleanup-after"} {
		t.Run(phase, func(t *testing.T) {
			root, env, run := provisionScripts(t)
			require.NoError(t, os.WriteFile(filepath.Join(root, "build-fail"), nil, 0600))
			up, release, output := startFixtureClaimCommand(t, root, env, phase, "/bin/bash", filepath.Join(root, "scripts/itest-up.sh"))
			require.NoError(t, os.Remove(filepath.Join(root, "build-fail")))
			if phase == "cleanup-before" {
				result, err := run("itest-down.sh")
				assert.Error(t, err, "joined failure must hold claim through handoff: %s", result)
				if err != nil {
					release()
					require.Error(t, up.Wait(), "original build failure retained: %s", output.String())
					require.NoDirExists(t, filepath.Join(root, ".itests"), "joined failure cleanup must not deadlock")
					return
				}
			}
			// Once cleanup is actually complete, another supported generation may start
			// even while the old startup's finalizer has not returned to its caller.
			result, err := run("itest-up.sh")
			require.NoError(t, err, "%s", result)
			owner, err := os.ReadFile(filepath.Join(root, ".itests/owner"))
			require.NoError(t, err)
			id, err := os.ReadFile(filepath.Join(root, ".itests/container-id"))
			require.NoError(t, err)
			release()
			require.Error(t, up.Wait(), "%s", output.String())
			ownerAfter, err := os.ReadFile(filepath.Join(root, ".itests/owner"))
			assert.NoError(t, err)
			assert.Equal(t, string(owner), string(ownerAfter), "old cleanup must not delete newer owner")
			idAfter, err := os.ReadFile(filepath.Join(root, ".itests/container-id"))
			assert.NoError(t, err)
			assert.Equal(t, string(id), string(idAfter), "old cleanup must not delete newer ID")
			live, err := os.ReadFile(filepath.Join(root, "live-id"))
			assert.NoError(t, err)
			assert.Equal(t, string(id), string(live), "old cleanup must not remove newer container")
		})
	}
}

func TestOwnedFixtureProvisionUncertainAndStaleHandoff(t *testing.T) {
	t.Run("empty-create", func(t *testing.T) {
		root, _, run := provisionScripts(t)
		require.NoError(t, os.WriteFile(filepath.Join(root, "create-uncertain"), nil, 0600))
		output, err := run("itest-up.sh")
		require.Error(t, err, "%s", output)
		require.FileExists(t, filepath.Join(root, ".itests/owner"))
		require.FileExists(t, filepath.Join(root, ".itests/container-id"))
		output, err = run("itest-down.sh")
		require.Error(t, err, "%s", output)
		require.Contains(t, string(output), "uncertain")
	})
	t.Run("interrupted-build", func(t *testing.T) {
		root, env, run := provisionScripts(t)
		up, release, output := startFixtureClaimCommand(t, root, env, "build", "/bin/bash", filepath.Join(root, "scripts/itest-up.sh"))
		require.NoError(t, up.Process.Signal(syscall.SIGTERM))
		release()
		require.Error(t, up.Wait(), "%s", output.String())
		require.FileExists(t, filepath.Join(root, ".itests/owner"))
		require.DirExists(t, filepath.Join(root, ".itests/operation"))
		result, err := run("itest-down.sh")
		require.Error(t, err, "%s", result)
	})
	t.Run("obsolete-owner", func(t *testing.T) {
		root, env, run := provisionScripts(t)
		output, err := run("itest-up.sh")
		require.NoError(t, err, "%s", output)
		oldOwner, err := os.ReadFile(filepath.Join(root, ".itests/owner"))
		require.NoError(t, err)
		output, err = run("itest-down.sh")
		require.NoError(t, err, "%s", output)
		output, err = run("itest-up.sh")
		require.NoError(t, err, "%s", output)
		id, err := os.ReadFile(filepath.Join(root, ".itests/container-id"))
		require.NoError(t, err)
		cmd := exec.Command("/bin/bash", filepath.Join(root, "scripts/itest-down.sh"))
		cmd.Dir = root
		cmd.Env = append(env, "_RDHPF_ITEST_PROVISION_OWNER="+strings.TrimSpace(string(oldOwner)))
		output, err = cmd.CombinedOutput()
		require.Error(t, err, "stale handoff must refuse before acting: %s", output)
		after, err := os.ReadFile(filepath.Join(root, ".itests/container-id"))
		require.NoError(t, err)
		require.Equal(t, string(id), string(after))
	})
}
