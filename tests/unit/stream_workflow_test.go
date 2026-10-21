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

// A zero script exit without the completed selected matrix is not proof.
func TestStreamWorkflowControlCompletion(t *testing.T) {
	source, err := os.ReadFile("../../.github/workflows/integration-test.yml")
	require.NoError(t, err)
	var workflow struct {
		Jobs map[string]struct{ Steps []struct{ Name, Run string } }
	}
	require.NoError(t, yaml.Unmarshal(source, &workflow))
	command := ""
	for _, step := range workflow.Jobs["integration-test"].Steps {
		if step.Name == "Source-bound stream controls" {
			command = step.Run
		}
	}
	require.NotEmpty(t, command)
	complete := "Named stream assertion reds and exact restored greens observed: no-pacing no-processing no-retirement no-direct-wait-join no-wait-delay no-watcher-join\n"
	for _, variant := range []string{"complete", "missing", "partial", "old-five"} {
		t.Run(variant, func(t *testing.T) {
			root := t.TempDir()
			require.NoError(t, os.Mkdir(filepath.Join(root, "scripts"), 0700))
			require.NoError(t, os.MkdirAll(filepath.Join(root, "test-evidence/stream-controls"), 0700))
			require.NoError(t, os.WriteFile(filepath.Join(root, "scripts/itest-stream-mutations.sh"), []byte("#!/bin/bash\nexit 0\n"), 0600))
			receipt := complete
			if variant == "partial" {
				receipt = "no-pacing\n"
			} else if variant == "old-five" {
				receipt = strings.Replace(complete, "no-direct-wait-join ", "", 1)
			}
			if variant != "missing" {
				require.NoError(t, os.WriteFile(filepath.Join(root, "test-evidence/stream-controls/result.txt"), []byte(receipt), 0600))
			}
			cmd := exec.Command("/bin/bash", "-eo", "pipefail", "-c", command)
			cmd.Dir = root
			output, err := cmd.CombinedOutput()
			if variant == "complete" {
				require.NoError(t, err, "%s", output)
			} else {
				require.Error(t, err, "STREAM_CONTROL_MUST_REJECT_%s: %s", variant, output)
			}
		})
	}
}

func TestStreamWorkflowRequiredPasses(t *testing.T) {
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
		if step.Name == "Require owned stream passes" {
			guard = step.Run
		}
	}
	require.NotEmpty(t, guard)
	required, err := os.ReadFile("../../.github/required-stream-tests.txt")
	require.NoError(t, err)
	names := strings.Fields(string(required))
	require.Equal(t, []string{"TestManagerStreamOwnedTermination/EOF", "TestManagerStreamOwnedTermination/Exit7"}, names)
	for _, variant := range []string{"all-pass", "missing", "skip", "wrong-package", "malformed", "unfinished", "failure"} {
		t.Run(variant, func(t *testing.T) {
			root := t.TempDir()
			require.NoError(t, os.Mkdir(filepath.Join(root, ".github"), 0700))
			require.NoError(t, os.Mkdir(filepath.Join(root, "test-evidence"), 0700))
			require.NoError(t, os.WriteFile(filepath.Join(root, ".github/required-stream-tests.txt"), required, 0600))
			var records strings.Builder
			emit := func(action, name, pkg string) {
				data, err := json.Marshal(map[string]string{"Action": action, "Test": name, "Package": pkg})
				require.NoError(t, err)
				records.Write(data)
				records.WriteByte('\n')
			}
			for index, name := range names {
				pkg, action := "github.com/tomaszpeksa/remote-docker-host-port-forwarder/internal/manager", "pass"
				if index == 0 {
					switch variant {
					case "missing":
						continue
					case "skip":
						action = "skip"
					case "wrong-package":
						pkg = "wrong/package"
					case "failure":
						action = "fail"
					}
				}
				emit("run", name, pkg)
				emit(action, name, pkg)
			}
			if variant == "malformed" {
				records.WriteString("{broken\n")
			}
			if variant == "unfinished" {
				emit("run", "TestNeverFinished", "owned/package")
			}
			require.NoError(t, os.WriteFile(filepath.Join(root, "test-evidence/stream-owned.jsonl"), []byte(records.String()), 0600))
			cmd := exec.Command("/bin/bash", "-eo", "pipefail", "-c", guard)
			cmd.Dir = root
			output, err := cmd.CombinedOutput()
			if variant == "all-pass" {
				require.NoError(t, err, "%s", output)
			} else {
				require.Error(t, err, "STREAM_GUARD_MUST_REJECT_%s: %s", variant, output)
			}
		})
	}
}
