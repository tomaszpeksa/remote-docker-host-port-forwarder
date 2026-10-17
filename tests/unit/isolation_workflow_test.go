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

func TestIntegrationWorkflowMandatoryIsolationPasses(t *testing.T) {
	source, err := os.ReadFile("../../.github/workflows/integration-test.yml")
	require.NoError(t, err)
	var workflow struct {
		Jobs map[string]struct{ Steps []struct{ Name, Run string } }
	}
	require.NoError(t, yaml.Unmarshal(source, &workflow))
	phase := ""
	for _, step := range workflow.Jobs["integration-test"].Steps {
		if step.Name == "Owned process isolation" {
			phase = step.Run
		}
	}
	require.Contains(t, phase, "bash scripts/check-isolation-evidence.sh test-evidence/isolation-owned.jsonl")
	require.Contains(t, phase, "go build -o \"$RDHPF_ISOLATION_BINARY\" ./cmd/rdhpf")
	require.Contains(t, phase, "${status[0]}")
	require.Contains(t, phase, "${status[1]}")
	required, err := os.ReadFile("../../.github/required-isolation-tests.txt")
	require.NoError(t, err)
	names := strings.Fields(string(required))
	require.ElementsMatch(t, []string{
		"github.com/tomaszpeksa/remote-docker-host-port-forwarder/tests/integration|TestOwnedCLIIsolation",
		"github.com/tomaszpeksa/remote-docker-host-port-forwarder/tests/integration|TestOwnedMasterProcessIsolation",
		"github.com/tomaszpeksa/remote-docker-host-port-forwarder/internal/ssh|TestMasterPrivatePathNativeBind",
	}, names)
	for _, variant := range []string{"complete", "missing", "skipped", "wrong-name", "wrong-package", "malformed", "unfinished-test", "unfinished-package", "pass-without-run", "package-failed"} {
		for _, target := range names {
			t.Run(variant+"/"+strings.Split(target, "|")[1], func(t *testing.T) {
				t.Parallel()
				var events []map[string]string
				packages := map[string]bool{}
				for _, pair := range names {
					packages[strings.Split(pair, "|")[0]] = true
				}
				for pkg := range packages {
					events = append(events, map[string]string{"Action": "start", "Package": pkg})
				}
				for _, pair := range names {
					parts := strings.Split(pair, "|")
					pkg, name, action := parts[0], parts[1], "pass"
					if pair == target {
						switch variant {
						case "missing":
							continue
						case "skipped":
							action = "skip"
						case "wrong-name":
							name += "Wrong"
						case "wrong-package":
							pkg += "/wrong"
						}
					}
					if pair != target || variant != "pass-without-run" {
						events = append(events, map[string]string{"Action": "run", "Package": pkg, "Test": name})
					}
					if pair != target || variant != "unfinished-test" {
						events = append(events, map[string]string{"Action": action, "Package": pkg, "Test": name})
					}
				}
				for pkg := range packages {
					if pkg == strings.Split(target, "|")[0] && variant == "unfinished-package" {
						continue
					}
					action := "pass"
					if pkg == strings.Split(target, "|")[0] && variant == "package-failed" {
						action = "fail"
					}
					events = append(events, map[string]string{"Action": action, "Package": pkg})
				}
				var data strings.Builder
				encoder := json.NewEncoder(&data)
				for _, event := range events {
					require.NoError(t, encoder.Encode(event))
				}
				if variant == "malformed" {
					data.WriteString("{broken\n")
				}
				path := filepath.Join(t.TempDir(), "events.jsonl")
				require.NoError(t, os.WriteFile(path, []byte(data.String()), 0600))
				output, err := exec.Command("/bin/bash", "../../scripts/check-isolation-evidence.sh", path).CombinedOutput()
				if variant == "complete" {
					require.NoError(t, err, "%s", output)
				} else {
					require.Error(t, err, "ISOLATION_GUARD_ACCEPTED_INVALID_EVIDENCE: %s", output)
				}
			})
		}
	}
}
