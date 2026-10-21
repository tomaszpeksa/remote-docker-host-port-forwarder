package unit

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
	"go.yaml.in/yaml/v3"
)

func TestWatchdogWorkflowRequiredPasses(t *testing.T) {
	output, err := exec.Command("../../scripts/check-tools.sh", "jq").CombinedOutput()
	require.NoError(t, err, "%s", output)
	source, err := os.ReadFile("../../.github/workflows/integration-test.yml")
	require.NoError(t, err)
	var workflow struct {
		Jobs map[string]struct{ Steps []struct{ Name, Run string } }
	}
	require.NoError(t, yaml.Unmarshal(source, &workflow))
	guard := ""
	for _, step := range workflow.Jobs["integration-test"].Steps {
		if step.Name == "Require owned watchdog passes" {
			guard = step.Run
		}
	}
	required, err := os.ReadFile("../../.github/required-watchdog-tests.txt")
	require.NoError(t, err)
	names := strings.Fields(string(required))
	require.Len(t, names, 6)
	for _, variant := range []string{"all-pass", "missing", "skip", "wrong-package", "wrong-name", "malformed", "unfinished"} {
		t.Run(variant, func(t *testing.T) {
			root := t.TempDir()
			require.NoError(t, os.Mkdir(filepath.Join(root, ".github"), 0700))
			require.NoError(t, os.Mkdir(filepath.Join(root, "test-evidence"), 0700))
			require.NoError(t, os.WriteFile(filepath.Join(root, ".github/required-watchdog-tests.txt"), required, 0600))
			var events strings.Builder
			emit := func(action, name, pkg string) {
				data, err := json.Marshal(map[string]string{"Action": action, "Test": name, "Package": pkg})
				require.NoError(t, err)
				events.Write(data)
				events.WriteByte('\n')
			}
			for i, name := range names {
				pkg := "github.com/tomaszpeksa/remote-docker-host-port-forwarder/internal/manager"
				if name == "TestWatchdogOwnedCLISilentStream" {
					pkg = "github.com/tomaszpeksa/remote-docker-host-port-forwarder/cmd/rdhpf"
				}
				action := "pass"
				if i == 0 {
					switch variant {
					case "missing":
						continue
					case "skip":
						action = "skip"
					case "wrong-package":
						pkg = "wrong/package"
					case "wrong-name":
						name = "TestUnrelated"
					}
				}
				emit("run", name, pkg)
				emit(action, name, pkg)
			}
			if variant == "malformed" {
				events.WriteString("{broken\n")
			}
			if variant == "unfinished" {
				emit("run", "TestNeverCompleted", "owned/package")
			}
			for _, phase := range []string{"cold", "warm", "cli", "blocked"} {
				text := ""
				if phase == "cold" {
					text = events.String()
				}
				require.NoError(t, os.WriteFile(filepath.Join(root, "test-evidence/watchdog-"+phase+".jsonl"), []byte(text), 0600))
			}
			cmd := exec.Command("/bin/bash", "-eo", "pipefail", "-c", guard)
			cmd.Dir = root
			output, err := cmd.CombinedOutput()
			if variant == "all-pass" {
				require.NoError(t, err, "%s", output)
			} else {
				require.Error(t, err, "WATCHDOG_GUARD_MUST_REJECT_%s: %s", variant, output)
			}
		})
	}
}

func TestWatchdogWorkflowColdPrecedesWarmup(t *testing.T) {
	source, err := os.ReadFile("../../.github/workflows/integration-test.yml")
	require.NoError(t, err)
	var workflow struct {
		Jobs map[string]struct {
			Timeout int `yaml:"timeout-minutes"`
			Steps   []struct{ Name, Run string }
		}
	}
	require.NoError(t, yaml.Unmarshal(source, &workflow))
	job := workflow.Jobs["integration-test"]
	require.Equal(t, 60, job.Timeout, "35m retained + 25m additive phase envelopes")
	positions := map[string]int{}
	for i, step := range job.Steps {
		positions[step.Name] = i
	}
	cold, ok := positions["Owned watchdog cold pull before image warmup"]
	require.True(t, ok)
	warm, ok := positions["Warm unchanged production ping image after cold proof"]
	require.True(t, ok)
	require.Less(t, cold, warm)
	for _, name := range []string{"Owned process isolation", "Source-bound isolation controls"} {
		position, ok := positions[name]
		require.True(t, ok, "isolation phase %s retained", name)
		require.Less(t, cold, position, "WATCHDOG_COLD_MUST_PRECEDE_ISOLATION: %s", name)
	}
	for _, name := range []string{"Phase 3 - Run all integration tests", "Owned discovery repair", "Source-bound discovery controls", "Source-bound trust red controls", "Source-bound container identity control", "Documented Make fixture lifecycle"} {
		_, ok := positions[name]
		require.True(t, ok, "inherited phase %s retained", name)
	}
}
