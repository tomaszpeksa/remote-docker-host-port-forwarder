package unit

import (
	"crypto/sha256"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
	"go.yaml.in/yaml/v3"
)

func TestIntegrationWorkflowMandatoryDiscoveryPasses(t *testing.T) {
	guard, err := exec.Command("../../scripts/check-tools.sh", "jq").CombinedOutput()
	require.NoError(t, err, "%s", guard)
	jq, err := exec.LookPath("jq")
	require.NoError(t, err)
	tee, err := exec.LookPath("tee")
	require.NoError(t, err)
	source, err := os.ReadFile("../../.github/workflows/integration-test.yml")
	require.NoError(t, err)
	var workflow struct {
		Jobs map[string]struct{ Steps []struct{ Name, Run string } }
	}
	require.NoError(t, yaml.Unmarshal(source, &workflow))
	phase := ""
	priorEvidenceProduced := false
	for _, step := range workflow.Jobs["integration-test"].Steps {
		if step.Name == "Phase 3 - Run all integration tests" {
			priorEvidenceProduced = true
		}
		if step.Name == "Owned discovery repair" {
			require.True(t, priorEvidenceProduced, "combined trust guard requires prior all.jsonl production")
			phase = step.Run
		}
	}
	require.NotEmpty(t, phase)
	required, err := os.ReadFile("../../.github/required-discovery-tests.txt")
	require.NoError(t, err)
	names := strings.Fields(string(required))
	require.ElementsMatch(t, []string{"TestManagerDiscoveryOwnedStartupGap", "TestManagerDiscoveryOwnedInventoryOverlap", "TestManagerDiscoveryOwnedDowntime", "TestManagerDiscoveryOwnedInspectRetry"}, names)
	t.Logf("actual discovery phase SHA256=%x mandatory subtests=%d", sha256.Sum256([]byte(phase)), len(names))
	cases := []struct {
		name, missing, skip, action string
		goExit, teeExit, jqExit     string
		pass                        bool
	}{
		{name: "all-pass", action: "pass", pass: true},
		{name: "skips", action: "skip"}, {name: "go-failure", action: "pass", goExit: "23"},
		{name: "tee-failure", action: "pass", teeExit: "24"}, {name: "jq-failure", action: "pass", jqExit: "25"},
		{name: "malformed", action: "malformed"},
	}
	for _, name := range names {
		cases = append(cases, struct {
			name, missing, skip, action string
			goExit, teeExit, jqExit     string
			pass                        bool
		}{name: "missing/" + name, missing: name, action: "pass"})
		cases = append(cases, struct {
			name, missing, skip, action string
			goExit, teeExit, jqExit     string
			pass                        bool
		}{name: "skipped/" + name, skip: name, action: "pass"})
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			// Each inert shell has its own files and explicit environment; no
			// process-wide environment or real SSH/Docker resource is mutated.
			t.Parallel()
			root := t.TempDir()
			bin := filepath.Join(root, "bin")
			require.NoError(t, os.Mkdir(bin, 0700))
			require.NoError(t, os.Mkdir(filepath.Join(root, "test-evidence"), 0700))
			require.NoError(t, os.Mkdir(filepath.Join(root, ".github"), 0700))
			require.NoError(t, os.WriteFile(filepath.Join(root, ".github/required-discovery-tests.txt"), required, 0600))
			var events strings.Builder
			for _, name := range names {
				if name != tc.missing {
					action := tc.action
					if name == tc.skip {
						action = "skip"
					}
					b, err := json.Marshal(map[string]string{"Action": action, "Test": name})
					require.NoError(t, err)
					events.Write(b)
					events.WriteByte('\n')
				}
			}
			if tc.action == "malformed" {
				events.WriteString("{broken\n")
			}
			require.NoError(t, os.WriteFile(filepath.Join(root, "test-evidence/all.jsonl"), []byte(events.String()), 0600))
			write := func(name, body string) {
				require.NoError(t, os.WriteFile(filepath.Join(bin, name), []byte("#!/bin/bash\n"+body), 0700))
			}
			write("go", "printf '%s\\n' \"$FIXTURE_EVENTS\"\nexit ${GO_STATUS:-0}\n")
			write("tee", "\"$REAL_TEE\" \"$@\" || exit $?\nexit ${TEE_STATUS:-0}\n")
			write("jq", "if [ -n \"$JQ_STATUS\" ]; then exit \"$JQ_STATUS\"; fi\nexec \"$REAL_JQ\" \"$@\"\n")
			script := filepath.Join(root, "phase.sh")
			require.NoError(t, os.WriteFile(script, []byte(phase), 0600))
			cmd := exec.Command("/bin/bash", "-eo", "pipefail", script)
			cmd.Dir = root
			cmd.Env = []string{"PATH=" + bin, "HOME=" + root, "REAL_JQ=" + jq, "REAL_TEE=" + tee, "GO_STATUS=" + tc.goExit, "TEE_STATUS=" + tc.teeExit, "JQ_STATUS=" + tc.jqExit, "FIXTURE_EVENTS=" + events.String()}
			output, err := cmd.CombinedOutput()
			if tc.pass {
				require.NoError(t, err, "%s", output)
			} else {
				require.Error(t, err, "mandatory proof was accepted: %s", output)
			}
		})
	}
}
