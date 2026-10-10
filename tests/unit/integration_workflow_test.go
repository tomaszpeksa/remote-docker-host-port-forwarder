package unit

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"gopkg.in/yaml.v3"
)

// Execute the workflow's actual run block, not a separately implemented guard.
// PATH exposes only a private fake Go and controlled tee/jq boundaries: this
// test cannot start the integration package or contact SSH/Docker.
func TestIntegrationWorkflowMandatoryRecoveryPasses(t *testing.T) {
	bash, err := exec.LookPath("bash")
	require.NoError(t, err)
	jq, err := exec.LookPath("jq")
	if errors.Is(err, exec.ErrNotFound) {
		t.Skip("workflow-shell regression requires jq (not found)")
	}
	require.NoError(t, err)
	tee, err := exec.LookPath("tee")
	require.NoError(t, err)
	data, err := os.ReadFile("../../.github/workflows/integration-test.yml")
	require.NoError(t, err)
	var workflow struct {
		Jobs map[string]struct {
			Steps []struct {
				Name string `yaml:"name"`
				Run  string `yaml:"run"`
			} `yaml:"steps"`
		} `yaml:"jobs"`
	}
	require.NoError(t, yaml.Unmarshal(data, &workflow))
	var phase string
	for _, step := range workflow.Jobs["integration-test"].Steps {
		if step.Name == "Phase 3 - Run all integration tests" {
			require.Empty(t, phase, "phase must be unambiguous")
			phase = step.Run
		}
	}
	require.NotEmpty(t, phase)
	t.Logf("actual phase SHA256=%x bash=%s jq=%s tee=%s", sha256.Sum256([]byte(phase)), bash, jq, tee)
	const graceful = `{"Action":"pass","Test":"TestConnectionLossRecovery"}`
	const hard = `{"Action":"pass","Test":"TestOwnedChildHardLossRecovery"}`
	both := graceful + "\n" + hard
	cases := []struct {
		name, output                  string
		goStatus, teeStatus, jqStatus int
		wantExit                      int // -1 means any nonzero parser error
	}{
		{name: "missing-graceful", output: hard, wantExit: 1},
		{name: "missing-hard-loss", output: graceful, wantExit: 1},
		{name: "both-missing", output: `{"Action":"pass","Test":"Unrelated"}`, wantExit: 1},
		{name: "skip-only", output: `{"Action":"skip","Test":"TestConnectionLossRecovery"}` + "\n" + `{"Action":"skip","Test":"TestOwnedChildHardLossRecovery"}`, wantExit: 1},
		{name: "empty-output", wantExit: 1},
		{name: "malformed-json", output: "{broken", wantExit: -1},
		{name: "jq-failure", output: both, jqStatus: 73, wantExit: 73},
		{name: "both-passes", output: both},
		{name: "go-failure", output: both, goStatus: 23, wantExit: 23},
		{name: "tee-failure", output: both, teeStatus: 24, wantExit: 24},
		{name: "go-and-tee-failure", output: both, goStatus: 23, teeStatus: 24, wantExit: 23},
	}
	// Cover the Actions default bash -e and explicit bash/pipefail execution.
	for _, pipefail := range []bool{false, true} {
		for _, tc := range cases {
			t.Run(fmt.Sprintf("pipefail=%t/%s", pipefail, tc.name), func(t *testing.T) {
				root := t.TempDir()
				bin := filepath.Join(root, "bin")
				require.NoError(t, os.Mkdir(bin, 0700))
				require.NoError(t, os.Mkdir(filepath.Join(root, "test-evidence"), 0700))
				writeCommand := func(name, body string) {
					require.NoError(t, os.WriteFile(filepath.Join(bin, name), []byte("#!"+bash+"\n"+body), 0700))
				}
				writeCommand("go", "printf '%s\\n' \"$@\" > go-args.txt\nprintf '%s\\n' \"$FIXTURE_OUTPUT\"\nexit \"$GO_STATUS\"\n")
				writeCommand("tee", "\"$REAL_TEE\" \"$@\" || exit $?\nexit \"$TEE_STATUS\"\n")
				writeCommand("jq", "if [ \"$JQ_STATUS\" -ne 0 ]; then exit \"$JQ_STATUS\"; fi\nexec \"$REAL_JQ\" \"$@\"\n")
				script := filepath.Join(root, "phase.sh")
				require.NoError(t, os.WriteFile(script, []byte(phase), 0600))
				ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
				defer cancel()
				args := []string{"--noprofile", "--norc", "-e"}
				if pipefail {
					args = append(args, "-o", "pipefail")
				}
				cmd := exec.CommandContext(ctx, bash, append(args, script)...)
				cmd.Dir = root
				cmd.Env = []string{"PATH=" + bin, "HOME=" + root, "SSH_TEST_HOST=ssh://nonexecuted-fixture.invalid", "FIXTURE_OUTPUT=" + tc.output, "REAL_TEE=" + tee, "REAL_JQ=" + jq,
					fmt.Sprintf("GO_STATUS=%d", tc.goStatus), fmt.Sprintf("TEE_STATUS=%d", tc.teeStatus), fmt.Sprintf("JQ_STATUS=%d", tc.jqStatus)}
				output, runErr := cmd.CombinedOutput()
				require.NoError(t, ctx.Err(), "phase must not hang")
				exit := cmd.ProcessState.ExitCode()
				t.Logf("phase exit=%d expected=%d output:\n%s", exit, tc.wantExit, output)
				if tc.wantExit == -1 {
					require.Error(t, runErr)
					require.NotZero(t, exit)
				} else {
					require.Equal(t, tc.wantExit, exit)
				}
				const success = "✓ Phase 3 complete: All integration tests passed"
				if tc.wantExit == 0 {
					require.NoError(t, runErr)
					require.Contains(t, string(output), success)
				} else {
					require.NotContains(t, string(output), success)
				}
				status, err := os.ReadFile(filepath.Join(root, "test-evidence", "all.exit"))
				require.NoError(t, err)
				require.Equal(t, fmt.Sprintf("go=%d tee=%d\n", tc.goStatus, tc.teeStatus), string(status))
				argv, err := os.ReadFile(filepath.Join(root, "go-args.txt"))
				require.NoError(t, err)
				require.Equal(t, "test\n-tags=rdhpf_integration\n-json\n-count=1\n-timeout=15m\n-p\n1\n-coverpkg=./cmd/...,./internal/...\n-covermode=atomic\n-coverprofile=test-evidence/all.coverage\n./tests/integration/...\n", string(argv))
			})
		}
	}
}

func TestIntegrationWorkflowMissingJQSkips(t *testing.T) {
	root := t.TempDir()
	for _, name := range []string{"bash", "tee"} {
		path, err := exec.LookPath(name)
		require.NoError(t, err)
		require.NoError(t, os.Symlink(path, filepath.Join(root, name)))
	}
	executable, err := os.Executable()
	require.NoError(t, err)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, executable, "-test.run=^TestIntegrationWorkflowMandatoryRecoveryPasses$", "-test.v")
	cmd.Dir = root
	cmd.Env = []string{"PATH=" + root, "HOME=" + root, "GORACE=atexit_sleep_ms=0"}
	output, err := cmd.CombinedOutput()
	t.Logf("missing jq selection outcome:\n%s", output)
	require.NoError(t, ctx.Err())
	require.NoError(t, err, string(output))
	require.Contains(t, string(output), "--- SKIP: TestIntegrationWorkflowMandatoryRecoveryPasses")
	require.Contains(t, string(output), "workflow-shell regression requires jq (not found)")
	require.NotContains(t, string(output), "actual phase SHA256=", "missing jq must not be reported as guard execution")
}
