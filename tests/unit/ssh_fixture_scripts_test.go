package unit

import (
	"bufio"
	"bytes"
	"context"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"github.com/tomaszpeksa/remote-docker-host-port-forwarder/internal/instancepath"
)

func TestOwnedSSHFixtureScripts(t *testing.T) {
	root := t.TempDir()
	bin := filepath.Join(root, "bin")
	scripts := filepath.Join(root, "scripts")
	require.NoError(t, os.Mkdir(bin, 0700))
	require.NoError(t, os.Mkdir(scripts, 0700))
	for _, name := range []string{"itest-up.sh", "itest-down.sh", "itest-run.sh"} {
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
	write("go", `if [ "$1" = env ]; then printf '%s\n' "$FIXTURE_ROOT"; exit 0; fi
exec "$FIXTURE_TEST_EXE" -test.run=^TestOwnedFixturePublisherProcess$
`)
	write("ssh", `printf '%s\n' "$@" > "$FIXTURE_ARGV"
`)
	write("docker", `printf '%s\n' "$@" >> "$FIXTURE_ROOT/docker.log"
case "$1" in
container) exit 1;;
create) rm -f "$FIXTURE_REMOVED"; printf '%064d\n' 1;;
inspect) if [ -f "$FIXTURE_ROOT/daemon-fail" ]; then exit 31; fi
if [ -f "$FIXTURE_REMOVED" ]; then exit 1; fi
if [ -f "$FIXTURE_FOREIGN" ]; then echo foreign; else cat "$FIXTURE_ROOT/.itests/owner"; fi;;
ps) if [ -f "$FIXTURE_ROOT/daemon-fail" ]; then exit 31; fi
if [ ! -f "$FIXTURE_REMOVED" ]; then printf '%064d\n' 1; fi;;
exec) if [ "$3" = /usr/sbin/sshd ] && [ "$4" = -T ]; then printf 'persourcepenalties no\n'; elif [ "$3" = cat ]; then printf 'ssh-ed25519 owned-public-key\n'; fi;;
rm) printf removed > "$FIXTURE_REMOVED"; if [ -f "$FIXTURE_ROOT/remove-fail" ]; then exit 23; fi;;
esac
`)
	write("rmdir", `if [ "$1" = "$FIXTURE_ROOT/.itests" ] && [ -f "$FIXTURE_ROOT/rmdir-fail" ]; then exit 27; fi
exec /bin/rmdir "$@"
`)
	write("mkdir", `last=${!#}
if [ "$last" = "$FIXTURE_ROOT/.itests/operation" ] && [ -n "${FIXTURE_CLAIM_GATE:-}" ] && [ "${FIXTURE_CLAIM_POSITION:-}" = before ]; then "$FIXTURE_TEST_EXE" -test.run=^TestOwnedFixtureClaimProcess$; fi
/bin/mkdir "$@"
if [ "$last" = "$FIXTURE_ROOT/.itests/operation" ] && [ -n "${FIXTURE_CLAIM_GATE:-}" ] && [ "${FIXTURE_CLAIM_POSITION:-}" = after ]; then "$FIXTURE_TEST_EXE" -test.run=^TestOwnedFixtureClaimProcess$; fi
`)
	argv := filepath.Join(root, "argv")
	foreign := filepath.Join(root, "foreign")
	removed := filepath.Join(root, "removed")
	executable, err := os.Executable()
	require.NoError(t, err)
	environment := []string{"FIXTURE_PUBLISHER_CHILD=1", "FIXTURE_TEST_EXE=" + executable, "FIXTURE_PUBLISHER_HOME=" + filepath.Join(root, "publisher-home"), "PWD=" + root, "GORACE=atexit_sleep_ms=0", "PATH=" + bin + ":/usr/bin:/bin", "HOME=" + root, "FIXTURE_ROOT=" + root, "FIXTURE_ARGV=" + argv, "FIXTURE_FOREIGN=" + foreign, "FIXTURE_REMOVED=" + removed}
	run := func(path string, args ...string) ([]byte, error) {
		cmd := exec.Command(path, args...)
		cmd.Env = environment
		cmd.Dir = root
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
	ownerPath := filepath.Join(root, ".itests/owner")
	owner, err := os.ReadFile(ownerPath)
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(ownerPath, []byte("malformed\n"), 0600))
	output, err = run("/bin/bash", filepath.Join(scripts, "itest-down.sh"))
	require.Error(t, err, "matching malformed labels must not grant cleanup: %s", output)
	require.NoFileExists(t, removed)
	require.NoError(t, os.WriteFile(ownerPath, owner, 0600))
	require.NoError(t, os.WriteFile(foreign, []byte("foreign"), 0600))
	output, err = run("/bin/bash", filepath.Join(scripts, "itest-down.sh"))
	require.Error(t, err)
	require.Contains(t, string(output), "Refusing foreign")
	require.NoFileExists(t, removed)
	require.FileExists(t, filepath.Join(root, ".itests/home/.ssh/id_ed25519"))
	require.NoError(t, os.Remove(foreign))
	// Extract the actual Make entrypoint, not a reconstructed HOME assignment.
	makeSource, err := os.ReadFile("../../Makefile")
	require.NoError(t, err)
	start := strings.Index(string(makeSource), "\nitest:")
	end := strings.Index(string(makeSource)[start:], "\nitest-stability:") + start
	require.Greater(t, start, 0)
	require.Greater(t, end, start)
	require.NoError(t, os.WriteFile(filepath.Join(root, "Makefile"), makeSource[start:end], 0600))
	output, err = run("/usr/bin/make", "itest")
	require.NoError(t, err, "%s", output)
	selectedHome, err := os.ReadFile(filepath.Join(root, "publisher-home"))
	require.NoError(t, err)
	t.Logf("real publisher acquired/closed in Make-selected HOME=%s", selectedHome)
	require.NoDirExists(t, string(selectedHome), "successful invocation must retire its complete runtime/cache tree")
	require.NoDirExists(t, filepath.Join(root, ".itests/home/.rdhpf"))
	output, err = run("/bin/bash", filepath.Join(scripts, "itest-down.sh"))
	require.NoError(t, err, "%s", output)
	require.FileExists(t, removed)
	require.NoDirExists(t, filepath.Join(root, ".itests"))
	output, err = run("/bin/bash", filepath.Join(scripts, "itest-up.sh"))
	require.NoError(t, err, "second startup: %s", output)

	secondOwner, err := os.ReadFile(ownerPath)
	require.NoError(t, err)
	// Empty create output is an uncertain container outcome, not absence.
	idPath := filepath.Join(root, ".itests/container-id")
	retainedID, err := os.ReadFile(idPath)
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(idPath, nil, 0600))
	output, err = run("/bin/bash", filepath.Join(scripts, "itest-down.sh"))
	require.Error(t, err, "empty create outcome: %s", output)
	require.FileExists(t, ownerPath)
	require.FileExists(t, idPath)
	require.NoFileExists(t, removed)
	require.NoError(t, os.WriteFile(idPath, retainedID, 0600))

	// Unknown provisioned-root entries refuse before destructive cleanup.
	unknown := filepath.Join(root, ".itests/unexpected")
	require.NoError(t, os.WriteFile(unknown, []byte("foreign"), 0600))
	output, err = run("/bin/bash", filepath.Join(scripts, "itest-down.sh"))
	require.Error(t, err, "%s", output)
	require.FileExists(t, ownerPath)
	require.NoFileExists(t, removed)
	require.FileExists(t, unknown)
	require.NoError(t, os.Remove(unknown))

	// A real publisher holds its lease while teardown is attempted. The supported
	// invocation's operation claim must prevent teardown, not probe/unlink its lock.
	publisherListener, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	commandCtx, stop := context.WithTimeout(context.Background(), 15*time.Second)
	defer stop()
	cmd := exec.CommandContext(commandCtx, "/usr/bin/make", "itest")
	cmd.Dir = root
	cmd.Env = append(append([]string{}, environment...), "FIXTURE_PUBLISHER_GATE="+publisherListener.Addr().String())
	var activeOutput bytes.Buffer
	cmd.Stdout = &activeOutput
	cmd.Stderr = &activeOutput
	require.NoError(t, cmd.Start())
	require.NoError(t, publisherListener.(*net.TCPListener).SetDeadline(time.Now().Add(5*time.Second)))
	active, err := publisherListener.Accept()
	require.NoError(t, err)
	t.Cleanup(func() { _ = active.Close() })
	require.NoError(t, publisherListener.Close())
	activeHome, err := bufio.NewReader(active).ReadString('\n')
	require.NoError(t, err)
	activeHome = strings.TrimSpace(activeHome)
	lock := filepath.Join(activeHome, ".rdhpf/owner.lock")
	before, err := os.Stat(lock)
	require.NoError(t, err)
	output, err = run("/bin/bash", filepath.Join(scripts, "itest-down.sh"))
	require.Error(t, err, "%s", output)
	after, err := os.Stat(lock)
	require.NoError(t, err)
	require.True(t, os.SameFile(before, after))
	require.NoFileExists(t, removed)
	_, err = active.Write([]byte{1})
	require.NoError(t, err)
	require.NoError(t, active.Close())
	require.NoError(t, cmd.Wait(), "%s", activeOutput.String())
	require.NoDirExists(t, activeHome)

	// Deterministically suspend test-start after its initial owner read but BEFORE
	// mkdir. Teardown begins, removes the container, then fails. On resume, the
	// stale starter claims operation but must recheck the retirement receipt.
	require.NoError(t, os.Remove(filepath.Join(root, "publisher-home")))
	blocked, release, buffer := startFixtureClaimCommand(t, root, environment, "before", "/usr/bin/make", "itest")
	require.NoError(t, os.WriteFile(filepath.Join(root, "remove-fail"), nil, 0600))
	output, err = run("/bin/bash", filepath.Join(scripts, "itest-down.sh"))
	require.Error(t, err, "%s", output)
	require.FileExists(t, ownerPath)
	require.FileExists(t, filepath.Join(root, ".itests-cleanup-owner"))
	release()
	require.Error(t, blocked.Wait(), "%s", buffer.String())
	require.NoFileExists(t, filepath.Join(root, "publisher-home"))
	// Arbitrary daemon failure is not absence; keep all retry authority.
	require.NoError(t, os.WriteFile(filepath.Join(root, "daemon-fail"), nil, 0600))
	output, err = run("/bin/bash", filepath.Join(scripts, "itest-down.sh"))
	require.Error(t, err, "%s", output)
	require.FileExists(t, ownerPath)
	require.FileExists(t, filepath.Join(root, ".itests/container-id"))
	require.NoError(t, os.Remove(filepath.Join(root, "daemon-fail")))
	require.NoError(t, os.Remove(filepath.Join(root, "remove-fail")))
	// Failed final rmdir must retain authority even after the inner owner is gone.
	require.NoError(t, os.WriteFile(filepath.Join(root, "rmdir-fail"), nil, 0600))
	output, err = run("/bin/bash", filepath.Join(scripts, "itest-down.sh"))
	require.Error(t, err, "%s", output)
	require.NoFileExists(t, ownerPath)
	require.FileExists(t, filepath.Join(root, ".itests-cleanup-owner"))
	output, err = run("/bin/bash", filepath.Join(scripts, "itest-up.sh"))
	require.Error(t, err, "%s", output)
	receiptPath := filepath.Join(root, ".itests-cleanup-owner")
	receiptBytes, err := os.ReadFile(receiptPath)
	require.NoError(t, err)
	require.Equal(t, string(secondOwner)+"removed="+string(retainedID), string(receiptBytes))
	beforeRetry, err := os.ReadFile(filepath.Join(root, "docker.log"))
	require.NoError(t, err)
	for _, bad := range []string{"malformed\n", strings.TrimSuffix(string(secondOwner), "\n"), string(secondOwner) + "\n", string(receiptBytes) + "removed=" + string(retainedID)} {
		require.NoError(t, os.WriteFile(receiptPath, []byte(bad), 0600))
		output, err = run("/bin/bash", filepath.Join(scripts, "itest-down.sh"))
		require.Error(t, err, "malformed receipt: %s", output)
		require.DirExists(t, filepath.Join(root, ".itests"))
	}
	require.NoError(t, os.WriteFile(filepath.Join(root, ".itests/container-removed"), retainedID, 0600))
	conflicting := string(secondOwner) + "removed=" + strings.Repeat("f", 64) + "\n"
	require.NoError(t, os.WriteFile(receiptPath, []byte(conflicting), 0600))
	output, err = run("/bin/bash", filepath.Join(scripts, "itest-down.sh"))
	require.Error(t, err, "conflicting receipt: %s", output)
	require.NoError(t, os.WriteFile(receiptPath, secondOwner, 0600))
	// Preserve a known inner removal receipt even if the outer receipt still
	// contains only owner (an interrupted/older cleanup record).
	output, err = run("/bin/bash", filepath.Join(scripts, "itest-down.sh"))
	require.Error(t, err, "final rmdir still fails: %s", output)
	promoted, err := os.ReadFile(receiptPath)
	require.NoError(t, err)
	require.Equal(t, string(receiptBytes), string(promoted))
	require.NoError(t, os.Remove(filepath.Join(root, "rmdir-fail")))
	output, err = run("/bin/bash", filepath.Join(scripts, "itest-down.sh"))
	require.NoError(t, err, "retry: %s", output)
	require.NoDirExists(t, filepath.Join(root, ".itests"))
	require.NoFileExists(t, filepath.Join(root, ".itests-cleanup-owner"))
	afterRetry, err := os.ReadFile(filepath.Join(root, "docker.log"))
	require.NoError(t, err)
	require.Equal(t, string(beforeRetry), string(afterRetry), "receipt-only retry must not target another container")

	output, err = run("/bin/bash", filepath.Join(scripts, "itest-up.sh"))
	require.NoError(t, err, "%s", output)
	// Hold teardown AFTER its claim: another supported Make start must refuse.
	blocked, release, buffer = startFixtureClaimCommand(t, root, environment, "after", "/bin/bash", filepath.Join(scripts, "itest-down.sh"))
	output, err = run("/usr/bin/make", "itest")
	require.Error(t, err, "%s", output)
	require.NoFileExists(t, filepath.Join(root, "publisher-home"))
	release()
	require.NoError(t, blocked.Wait(), "%s", buffer.String())

	output, err = run("/bin/bash", filepath.Join(scripts, "itest-up.sh"))
	require.NoError(t, err, "%s", output)
	// A nonzero command preserves uncertain runtime state, even though this inert
	// child deliberately closes its owner first. Never call this complete cleanup.
	require.NoError(t, os.WriteFile(filepath.Join(root, "publisher-fail"), nil, 0600))
	output, err = run("/usr/bin/make", "itest")
	require.Error(t, err, "%s", output)
	require.Contains(t, string(output), "Incomplete fixture runtime cleanup")
	retained, err := os.ReadFile(filepath.Join(root, ".itests/operation/home"))
	require.NoError(t, err)
	runtimeHome := strings.TrimSpace(string(retained))
	require.DirExists(t, runtimeHome)
	require.FileExists(t, filepath.Join(runtimeHome, ".rdhpf/owner.lock"))
	output, err = run("/bin/bash", filepath.Join(scripts, "itest-down.sh"))
	require.Error(t, err, "%s", output)
	require.FileExists(t, ownerPath)
	require.NoFileExists(t, removed)
	// Unit-test owner has joined the child and knows its lease was closed. Explicit
	// test cleanup is not a public recovery command or an automatic failure retry.
	require.NoError(t, os.RemoveAll(runtimeHome))
	require.NoError(t, os.RemoveAll(filepath.Join(root, ".itests/operation")))
	output, err = run("/bin/bash", filepath.Join(scripts, "itest-down.sh"))
	require.NoError(t, err, "%s", output)
}

func TestOwnedFixturePublisherProcess(t *testing.T) {
	if os.Getenv("FIXTURE_PUBLISHER_CHILD") != "1" {
		return
	}
	home, err := os.UserHomeDir()
	require.NoError(t, err)
	owner, err := instancepath.Acquire("ssh://owned-inert-fixture")
	require.NoError(t, err)
	if gate := os.Getenv("FIXTURE_PUBLISHER_GATE"); gate != "" {
		conn, err := net.Dial("tcp", gate)
		require.NoError(t, err)
		require.NoError(t, conn.SetDeadline(time.Now().Add(10*time.Second)))
		_, err = fmt.Fprintln(conn, home)
		require.NoError(t, err)
		_, err = bufio.NewReader(conn).ReadByte()
		require.NoError(t, err)
		require.NoError(t, conn.Close())
	}
	require.NoError(t, owner.Close())
	require.NoError(t, os.MkdirAll(filepath.Join(home, ".cache/go-build"), 0700))
	require.NoError(t, os.WriteFile(filepath.Join(home, ".cache/go-build/owned-cache"), nil, 0600))
	require.FileExists(t, filepath.Join(home, ".rdhpf", "owner.lock"))
	require.NoError(t, os.WriteFile(os.Getenv("FIXTURE_PUBLISHER_HOME"), []byte(home), 0600))
	fmt.Println("REAL_PUBLISHER_ACQUIRED_AND_CLOSED")
	if _, err := os.Stat(filepath.Join(os.Getenv("FIXTURE_ROOT"), "publisher-fail")); err == nil {
		os.Exit(23)
	}
}

func TestOwnedFixtureClaimProcess(t *testing.T) {
	gate := os.Getenv("FIXTURE_CLAIM_GATE")
	if gate == "" {
		return
	}
	conn, err := net.Dial("tcp", gate)
	require.NoError(t, err)
	require.NoError(t, conn.SetDeadline(time.Now().Add(10*time.Second)))
	_, err = fmt.Fprintln(conn, "claimed phase")
	require.NoError(t, err)
	_, err = bufio.NewReader(conn).ReadByte()
	require.NoError(t, err)
	require.NoError(t, conn.Close())
}

func startFixtureClaimCommand(t *testing.T, root string, environment []string, position, path string, args ...string) (*exec.Cmd, func(), *bytes.Buffer) {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	t.Cleanup(cancel)
	cmd := exec.CommandContext(ctx, path, args...)
	cmd.Dir = root
	cmd.Env = append(append([]string{}, environment...), "FIXTURE_CLAIM_GATE="+listener.Addr().String(), "FIXTURE_CLAIM_POSITION="+position)
	output := &bytes.Buffer{}
	cmd.Stdout = output
	cmd.Stderr = output
	require.NoError(t, cmd.Start())
	require.NoError(t, listener.(*net.TCPListener).SetDeadline(time.Now().Add(5*time.Second)))
	conn, err := listener.Accept()
	require.NoError(t, err)
	require.NoError(t, listener.Close())
	t.Cleanup(func() { _ = conn.Close() })
	_, err = bufio.NewReader(conn).ReadString('\n')
	require.NoError(t, err)
	return cmd, func() { _, err := conn.Write([]byte{1}); require.NoError(t, err); require.NoError(t, conn.Close()) }, output
}
