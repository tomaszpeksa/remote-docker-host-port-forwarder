package unit

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestOwnedSSHFixtureScripts(t *testing.T) {
	root := t.TempDir()
	bin := filepath.Join(root, "bin")
	scripts := filepath.Join(root, "scripts")
	require.NoError(t, os.Mkdir(bin, 0700))
	require.NoError(t, os.Mkdir(scripts, 0700))
	for _, name := range []string{"itest-up.sh", "itest-down.sh"} {
		b, err := os.ReadFile("../../scripts/" + name)
		require.NoError(t, err)
		require.NoError(t, os.WriteFile(filepath.Join(scripts, name), b, 0700))
	}
	write := func(name, body string) {
		require.NoError(t, os.WriteFile(filepath.Join(bin, name), []byte("#!/bin/bash\nset -eu\n"+body), 0700))
	}
	write("ssh-keygen", `while [ "$1" != -f ]; do shift; done
printf fixture > "$2"
printf 'ssh-ed25519 owned-public-key\n' > "$2.pub"
`)
	write("ssh", `printf '%s\n' "$@" > "$FIXTURE_ARGV"
`)
	write("docker", `case "$1" in
container) exit 1;;
create) printf '%064d\n' 1;;
inspect) if [ -f "$FIXTURE_FOREIGN" ]; then echo foreign; else cat "$FIXTURE_ROOT/.itests/owner"; fi;;
exec) if [ "$3" = cat ]; then printf 'ssh-ed25519 owned-public-key\n'; fi;;
rm) printf removed > "$FIXTURE_REMOVED";;
esac
`)
	argv := filepath.Join(root, "argv")
	foreign := filepath.Join(root, "foreign")
	removed := filepath.Join(root, "removed")
	environment := []string{"PATH=" + bin + ":/usr/bin:/bin", "HOME=" + root, "FIXTURE_ROOT=" + root, "FIXTURE_ARGV=" + argv, "FIXTURE_FOREIGN=" + foreign, "FIXTURE_REMOVED=" + removed}
	run := func(path string, args ...string) ([]byte, error) {
		cmd := exec.Command(path, args...)
		cmd.Env = environment
		return cmd.CombinedOutput()
	}
	output, err := run("/bin/bash", filepath.Join(scripts, "itest-up.sh"))
	require.NoError(t, err, "%s", output)
	// Production local launcher adds only -F and preserves every supplied operand.
	_, err = run(filepath.Join(root, ".itests/bin/ssh"), "-i", "identity with spaces", "--", "-oProxyCommand=false", "remote command")
	require.NoError(t, err)
	got, err := os.ReadFile(argv)
	require.NoError(t, err)
	require.Equal(t, "-F\n"+root+"/.itests/home/.ssh/config\n-i\nidentity with spaces\n--\n-oProxyCommand=false\nremote command\n", string(got))
	// A second startup refuses rather than adopting or overwriting owned state.
	output, err = run("/bin/bash", filepath.Join(scripts, "itest-up.sh"))
	require.Error(t, err)
	require.Contains(t, string(output), "Refusing existing .itests")
	require.NoError(t, os.WriteFile(foreign, []byte("foreign"), 0600))
	output, err = run("/bin/bash", filepath.Join(scripts, "itest-down.sh"))
	require.Error(t, err)
	require.Contains(t, string(output), "Refusing foreign")
	require.NoFileExists(t, removed)
	require.FileExists(t, filepath.Join(root, ".itests/home/.ssh/id_ed25519"))
	require.NoError(t, os.Remove(foreign))
	output, err = run("/bin/bash", filepath.Join(scripts, "itest-down.sh"))
	require.NoError(t, err, "%s", output)
	require.FileExists(t, removed)
	require.NoDirExists(t, filepath.Join(root, ".itests"))
}
