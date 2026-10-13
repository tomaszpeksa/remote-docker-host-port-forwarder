package unit

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestTestEntrypointsRejectAmbientJQ(t *testing.T) {
	makePath, err := exec.LookPath("make")
	require.NoError(t, err)
	root, err := filepath.Abs("../..")
	require.NoError(t, err)
	for _, target := range []string{"test-unit", "test", "check", "coverage", "TestSecurityRecipeRejectsIncompleteReports", "TestIntegrationWorkflowMandatoryRecoveryPasses"} {
		t.Run(target, func(t *testing.T) {
			dir := t.TempDir()
			managed := filepath.Join(dir, "managed")
			ambient := filepath.Join(dir, "ambient")
			require.NoError(t, os.Mkdir(managed, 0700))
			require.NoError(t, os.Mkdir(ambient, 0700))
			write := func(path, body string) {
				t.Helper()
				require.NoError(t, os.WriteFile(path, []byte("#!/bin/sh\n"+body+"\n"), 0700))
			}
			// Only provider lookup is doubled; the entrypoint and guard are real.
			write(filepath.Join(managed, "mise"), `[ "$1" = which ] || exit 2
[ "$2" != jq ] || exit 1
printf '%s/%s\n' "$MANAGED_TOOLS" "$2"`)
			write(filepath.Join(managed, "go"), `if [ "$1" = test ]; then jq; fi`)
			write(filepath.Join(managed, "gofmt"), "exit 0")
			write(filepath.Join(managed, "golangci-lint"), "exit 0")
			marker := filepath.Join(dir, "ambient-jq-ran")
			write(filepath.Join(ambient, "jq"), "printf executed > '"+marker+"'")
			var cmd *exec.Cmd
			if target == "TestSecurityRecipeRejectsIncompleteReports" || target == "TestIntegrationWorkflowMandatoryRecoveryPasses" {
				cmd = exec.Command(os.Args[0], "-test.run=^"+target+"$")
			} else {
				cmd = exec.Command(makePath, "-f", filepath.Join(root, "Makefile"), target, "GOOS=linux", "GOARCH=amd64", "COVERAGE_DIR="+filepath.Join(dir, "coverage"))
				cmd.Dir = root
			}
			cmd.Env = append(os.Environ(), "PATH="+managed+":"+ambient+":/usr/bin:/bin", "HOME="+dir, "MANAGED_TOOLS="+managed, "JQ_MARKER="+marker)
			output, err := cmd.CombinedOutput()
			_, statErr := os.Stat(marker)
			require.True(t, os.IsNotExist(statErr), "ambient jq executed: %s", output)
			require.Error(t, err, "missing managed jq accepted: %s", output)
			require.Contains(t, string(output), "required project tool", "%s", output)
		})
	}
}
