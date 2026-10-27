package unit

import (
	"context"
	"fmt"
	"go/parser"
	"go/token"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// The policy overlay must keep current lifecycle APIs, not install an obsolete
// historical Master wholesale. The owned command boundary records the actual
// source presented to Go; it never compiles or contacts SSH/Docker.
func TestTrustPolicyOverlayPreservesCurrentMaster(t *testing.T) {
	root := t.TempDir()
	for _, dir := range []string{"scripts", "bin", "internal/ssh"} {
		require.NoError(t, os.MkdirAll(filepath.Join(root, dir), 0700))
	}
	script, err := os.ReadFile("../../scripts/itest-trust-mutations.sh")
	require.NoError(t, err)
	current, err := os.ReadFile("../../internal/ssh/master.go")
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(filepath.Join(root, "scripts/itest-trust-mutations.sh"), script, 0700))
	require.NoError(t, os.WriteFile(filepath.Join(root, "original-master"), current, 0600))
	require.NoError(t, os.WriteFile(filepath.Join(root, "internal/ssh/master.go"), current, 0644))
	git := `#!/bin/sh
set -eu
case "$1" in
 diff|cat-file) exit 0;;
 rev-parse)
  case "$*" in
   *'5acdc328ae148c538cf076c3c62cc0f5d2726d8d^{commit}'*) printf '%s\n' 5acdc328ae148c538cf076c3c62cc0f5d2726d8d;;
   *'5acdc328ae148c538cf076c3c62cc0f5d2726d8d:internal/ssh/master.go'*) printf '%s\n' 12232176fbc37250c000ea6478d3c36b761c9858;;
   *) printf 'candidate\ncandidate-tree\n';;
  esac;;
 show)
  case "$2" in
   5acdc328*:*) printf 'package ssh\nfunc historicalMaster() { args := []string{"-o", "StrictHostKeyChecking=accept-new", "-o", "UserKnownHostsFile=/dev/null"}; _ = args }\n';;
   *) /bin/cat "$ROOT/original-master";;
  esac;;
 status) exit 0;;
 *) exit 99;;
esac
`
	goCommand := "#!/bin/sh\n/bin/cp \"$ROOT/internal/ssh/master.go\" \"$ROOT/observed-master\"\nexit 73\n"
	for name, data := range map[string]string{"git": git, "go": goCommand, "jq": "#!/bin/sh\nexit 1\n", "sha256sum": "#!/bin/sh\n/usr/bin/shasum -a 256 \"$@\"\n"} {
		require.NoError(t, os.WriteFile(filepath.Join(root, "bin", name), []byte(data), 0700))
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "/bin/bash", filepath.Join(root, "scripts/itest-trust-mutations.sh"))
	cmd.Env = []string{"PATH=" + filepath.Join(root, "bin") + ":/usr/bin:/bin", "HOME=" + root, "ROOT=" + root, "GITHUB_ACTIONS=true", "RDHPF_HOSTED_TRUST=1", "SSH_TEST_HOST=ssh://testuser@localhost:2222"}
	output, err := cmd.CombinedOutput()
	require.NoError(t, ctx.Err())
	require.Error(t, err, "%s", output)
	observed, err := os.ReadFile(filepath.Join(root, "observed-master"))
	require.NoError(t, err, "%s", output)
	require.Contains(t, string(observed), "func (m *Master) associatedConnection(", "TRUST_POLICY_OVERLAY_MUST_PRESERVE_CURRENT_ASSOCIATED_CONNECTION")
	require.Contains(t, string(observed), "StrictHostKeyChecking=accept-new")
	require.Contains(t, string(observed), "UserKnownHostsFile=/dev/null")
	require.Contains(t, string(observed), "BatchMode=yes")
	restored, err := os.ReadFile(filepath.Join(root, "internal/ssh/master.go"))
	require.NoError(t, err)
	require.Equal(t, current, restored)
	require.FileExists(t, filepath.Join(root, "test-evidence/trust-mutations/exit-restoration.exit"), "TRUST_FAILURE_MUST_RECORD_ACTUAL_SOURCE_RESTORATION")
}

// Exercise the entire actual script on a private repository. Only Go's costly
// hosted scenario command and the immutable historical Git object are stand-ins;
// source transformations, Git diffs/restoration and jq scenario guards are real.
func runTrustMutationScript(t *testing.T, mode string) (string, []byte, error) {
	t.Helper()
	root := t.TempDir()
	for _, file := range []string{"scripts/itest-trust-mutations.sh", "internal/ssh/master.go", "internal/ssh/forward.go", "internal/docker/inspect.go", "internal/docker/events.go", "internal/manager/manager.go"} {
		data, err := os.ReadFile(filepath.Join("../..", file))
		require.NoError(t, err)
		if mode == "duplicate-batch" && file == "internal/ssh/master.go" {
			data = []byte(strings.Replace(string(data), `"-o", "BatchMode=yes"`, `"-o", "BatchMode=yes", "-o", "BatchMode=yes"`, 1))
		}
		require.NoError(t, os.MkdirAll(filepath.Dir(filepath.Join(root, file)), 0700))
		require.NoError(t, os.WriteFile(filepath.Join(root, file), data, 0644))
	}
	git, err := exec.LookPath("git")
	require.NoError(t, err)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	env := append(os.Environ(), "HOME="+root, "ROOT="+root, "MODE="+mode)
	for _, args := range [][]string{{"init", "-q"}, {"add", "."}, {"-c", "user.name=Owned fixture", "-c", "user.email=fixture@example.invalid", "commit", "-qm", "owned fixture"}} {
		cmd := exec.CommandContext(ctx, git, args...)
		cmd.Dir, cmd.Env = root, env
		output, err := cmd.CombinedOutput()
		require.NoError(t, err, "%s", output)
	}
	bin := filepath.Join(root, "bin")
	require.NoError(t, os.Mkdir(bin, 0700))
	gitBoundary := fmt.Sprintf(`#!/bin/sh
set -eu
case "$1:$2" in
 cat-file:*) exit 0;;
 rev-parse:--verify)
  case "$3" in
   *'^{commit}') printf '5acdc328ae148c538cf076c3c62cc0f5d2726d8d\n';;
   *) printf '12232176fbc37250c000ea6478d3c36b761c9858\n';;
  esac; exit 0;;
 show:5acdc328*)
  if [ "$MODE" = primary-and-cleanup-failure ]; then touch "$ROOT/cleanup-fault"; exit 73; fi
  printf 'package ssh\nfunc historicalMaster() { args := []string{"-o", "StrictHostKeyChecking=accept-new", "-o", "UserKnownHostsFile=/dev/null"}; _ = args }\n'; exit 0;;
 diff:*) if [ -f "$ROOT/cleanup-fault" ]; then exit 75; fi;;
esac
exec %q "$@"
`, git)
	goBoundary := `#!/usr/bin/env python3
import json,os,pathlib,subprocess,sys
selector=sys.argv[sys.argv.index('-run')+1]
name='/'.join(part.strip('^$') for part in selector.split('/'))
files=subprocess.check_output(['git','diff','--name-only'],text=True).splitlines()
if files:
 pathlib.Path('observed').mkdir(exist_ok=True)
 pathlib.Path('observed/'+name.replace('/','_')+'.go').write_bytes(pathlib.Path(files[0]).read_bytes())
reason='native askpass invoked'
if name.endswith(('UserPinMismatch','DefaultUnknown')):reason='An error is expected but got nil'
if name.endswith('ConfiguredAcceptNew'):reason='does not contain'
if '/forward/' in name:reason='FORWARD_MUST_NOT_START_SSH_WITHOUT_OWNED_READY_CONNECTION'
if '/cancel/' in name:reason='CANCEL_MUST_NOT_START_SSH_WITHOUT_OWNED_READY_CONNECTION'
mode=os.environ['MODE']
if mode=='compile-failure':
 print(json.dumps({'Action':'fail','Output':'build failed'}));sys.exit(73)
if mode=='wrong-leaf':name+='/wrong'
if mode=='wrong-reason':reason='unrelated failure'
if mode=='timeout':reason+=' test timed out'
if mode!='missing-run':print(json.dumps({'Action':'run','Test':name}))
if mode=='skip' and files:print(json.dumps({'Action':'skip','Test':name}))
failed=bool(files) and mode!='unexpected-green' or not files and mode=='restored-green-failure'
if failed:print(json.dumps({'Action':'output','Test':name,'Output':reason}))
print(json.dumps({'Action':'fail' if failed else 'pass','Test':name}))
sys.exit(1 if failed else 0)
`
	cpBoundary := `#!/bin/sh
case "$2" in
 */baseline-*)
  if [ "$MODE" = restore-copy-failure ]; then exit 75; fi
  if [ "$MODE" = restore-mode-failure ]; then /bin/cp "$@"; chmod 0600 "$3"; exit 0; fi;;
esac
exec /bin/cp "$@"
`
	teeBoundary := "#!/bin/sh\nif [ \"$MODE\" = tee-failure ]; then /usr/bin/tee \"$@\"; exit 74; fi\nexec /usr/bin/tee \"$@\"\n"
	for name, text := range map[string]string{"git": gitBoundary, "go": goBoundary, "cp": cpBoundary, "tee": teeBoundary, "sha256sum": "#!/bin/sh\n/usr/bin/shasum -a 256 \"$@\"\n"} {
		require.NoError(t, os.WriteFile(filepath.Join(bin, name), []byte(text), 0700))
	}
	cmd := exec.CommandContext(ctx, "/bin/bash", filepath.Join(root, "scripts/itest-trust-mutations.sh"))
	cmd.Env = append(env, "PATH="+bin+string(os.PathListSeparator)+os.Getenv("PATH"), "GITHUB_ACTIONS=true", "RDHPF_HOSTED_TRUST=1", "SSH_TEST_HOST=ssh://testuser@localhost:2222")
	output, err := cmd.CombinedOutput()
	require.NoError(t, ctx.Err())
	return root, output, err
}

func TestTrustMutationsFourteenControls(t *testing.T) {
	root, output, err := runTrustMutationScript(t, "happy")
	require.NoError(t, err, "TRUST_CONTROL_INVENTORY_MUST_EXERCISE_ALL_FOURTEEN: %s", output)
	evidence := filepath.Join(root, "test-evidence/trust-mutations")
	for _, name := range []string{"historical-policy-user-pin", "historical-policy-default-unknown", "historical-policy-enrollment", "master", "check", "forward-no-helper", "cancel-no-helper", "ports", "labels", "events", "startup", "infrastructure", "ping", "connectivity"} {
		for _, suffix := range []string{".diff", ".jsonl", "-green.jsonl", "-restoration.exit"} {
			require.FileExists(t, filepath.Join(evidence, name+suffix))
		}
		data, err := os.ReadFile(filepath.Join(evidence, name+"-restoration.exit"))
		require.NoError(t, err)
		require.Equal(t, "0\n", string(data))
		diff, err := os.ReadFile(filepath.Join(evidence, name+".diff"))
		require.NoError(t, err)
		switch {
		case strings.HasPrefix(name, "historical-policy-"):
			require.Contains(t, string(diff), `+`+"\t"+`args = append(args, "-o", "StrictHostKeyChecking=accept-new", "-o", "UserKnownHostsFile=/dev/null")`)
		case strings.HasSuffix(name, "-no-helper"):
			require.Contains(t, string(diff), "helper.Run()")
			require.Contains(t, string(diff), `"-o", "BatchMode=yes", "-o", "ClearAllForwardings=yes", "-o", "Tunnel=no"`)
		default:
			require.Equal(t, 1, strings.Count(string(diff), "BatchMode=yes"), "exactly one option must be removed: %s", name)
		}
	}
	mutants, err := filepath.Glob(filepath.Join(root, "observed/*.go"))
	require.NoError(t, err)
	require.Len(t, mutants, 14)
	for _, file := range mutants {
		_, err := parser.ParseFile(token.NewFileSet(), file, nil, parser.AllErrors)
		require.NoError(t, err, "actual overlay syntax: %s", file)
	}
	data, err := os.ReadFile(filepath.Join(evidence, "exit-restoration.exit"))
	require.NoError(t, err)
	require.Equal(t, "0\n", string(data))
}

func TestTrustMutationsRejectInvalidEvidence(t *testing.T) {
	for _, mode := range []string{"compile-failure", "wrong-leaf", "wrong-reason", "missing-run", "skip", "timeout", "unexpected-green", "tee-failure", "restored-green-failure", "restore-copy-failure", "restore-mode-failure", "primary-and-cleanup-failure", "duplicate-batch"} {
		t.Run(mode, func(t *testing.T) {
			t.Parallel()
			root, output, err := runTrustMutationScript(t, mode)
			require.Error(t, err, "TRUST_CONTROL_MUST_REJECT_INVALID_EVIDENCE: %s", output)
			evidence := filepath.Join(root, "test-evidence/trust-mutations")
			require.NoFileExists(t, filepath.Join(evidence, "result.txt"))
			data, readErr := os.ReadFile(filepath.Join(evidence, "exit-restoration.exit"))
			require.NoError(t, readErr)
			want := "0\n"
			if mode == "restore-copy-failure" || mode == "restore-mode-failure" || mode == "primary-and-cleanup-failure" {
				want = "1\n"
			}
			require.Equal(t, want, string(data), "TRUST_FINAL_RESTORATION_MUST_NOT_HIDE_EARLIER_FAILURE")
			if mode == "primary-and-cleanup-failure" {
				var exit *exec.ExitError
				require.ErrorAs(t, err, &exit)
				require.Equal(t, 73, exit.ExitCode(), "cleanup must not replace primary failure")
			}
		})
	}
}

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
			// Each script has private files and an explicit child environment;
			// no process-wide environment or Git state is shared between cases.
			t.Parallel()
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
