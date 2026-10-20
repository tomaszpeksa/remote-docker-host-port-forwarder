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
