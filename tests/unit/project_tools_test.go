package unit

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// The real Make recipe must reject a missing managed tool or an ambient shadow
// before executing it. The binaries are inert fixtures, not host toolchains.
func TestMakeRejectsUnmanagedTools(t *testing.T) {
	makePath, err := exec.LookPath("make")
	require.NoError(t, err)
	root, err := filepath.Abs("../..")
	require.NoError(t, err)
	for _, tc := range []struct {
		name, target, missing string
		shadow, wantError     bool
	}{
		{"managed_go", "build", "", false, false},
		{"ambient_go", "build", "", true, true},
		{"missing_go", "build", "go", true, true},
		{"missing_mise", "build", "mise", true, true},
		{"missing_linter", "lint", "golangci-lint", false, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			managed, ambient := filepath.Join(dir, "managed"), filepath.Join(dir, "ambient")
			require.NoError(t, os.MkdirAll(managed, 0700))
			require.NoError(t, os.MkdirAll(ambient, 0700))
			write := func(path, script string) {
				t.Helper()
				require.NoError(t, os.WriteFile(path, []byte("#!/bin/sh\nset -eu\n"+script), 0700))
			}
			write(filepath.Join(managed, "go"), "printf '%s\\n' \"$*\" >> \"$TOOL_CALLS\"\n")
			write(filepath.Join(managed, "gofmt"), "exit 0\n")
			write(filepath.Join(ambient, "go"), "printf 'ambient %s\\n' \"$*\" >> \"$TOOL_CALLS\"\n")
			if tc.missing != "mise" {
				write(filepath.Join(managed, "mise"), `
# A tool-provider lookup double; Make and its guard are real production recipes.
[ "$1" = which ] || exit 2
shift
binary=$1
[ "$binary" != "$MISSING_TOOL" ] || exit 1
printf '%s/%s\n' "$MANAGED_TOOLS" "$binary"
`)
			}
			path := managed + ":/usr/bin:/bin"
			if tc.shadow {
				path = ambient + ":" + path
			}
			calls := filepath.Join(dir, "calls")
			cmd := exec.Command(makePath, "-f", filepath.Join(root, "Makefile"), tc.target, "GOOS=darwin", "GOARCH=arm64", "BUILD_DIR="+filepath.Join(dir, "out"))
			cmd.Dir = root
			cmd.Env = []string{"PATH=" + path, "HOME=" + dir, "TOOL_CALLS=" + calls, "MANAGED_TOOLS=" + managed, "MISSING_TOOL=" + tc.missing, "GOTOOLCHAIN=local"}
			output, err := cmd.CombinedOutput()
			if tc.wantError {
				require.Error(t, err, "unmanaged tool accepted: %s", output)
				require.Contains(t, string(output), "required project tool", "must fail at the tool boundary: %s", output)
				data, readErr := os.ReadFile(calls)
				require.True(t, os.IsNotExist(readErr), "tool ran before rejection: %s", data)
			} else {
				require.NoError(t, err, "%s", output)
				data, err := os.ReadFile(calls)
				require.NoError(t, err)
				require.True(t, strings.HasPrefix(string(data), "build "), "managed build did not run: %s", data)
			}
		})
	}
}

func TestGolangciReleaseProvider(t *testing.T) {
	root, err := filepath.Abs("../..")
	require.NoError(t, err)
	bin := t.TempDir()
	lookup := `#!/bin/sh
[ "$*" = "which golangci-lint --tool aqua:golangci/golangci-lint" ] || exit 2
printf '%s/golangci-lint\n' "$MANAGED_TOOLS"
`
	require.NoError(t, os.WriteFile(filepath.Join(bin, "mise"), []byte(lookup), 0700))
	require.NoError(t, os.WriteFile(filepath.Join(bin, "golangci-lint"), []byte("#!/bin/sh\nexit 99\n"), 0700))
	cmd := exec.Command(filepath.Join(root, "scripts", "check-tools.sh"), "golangci-lint")
	cmd.Env = []string{"PATH=" + bin + ":/usr/bin:/bin", "MANAGED_TOOLS=" + bin, "HOME=" + bin}
	output, err := cmd.CombinedOutput()
	require.NoError(t, err, "guard must require the release provider without executing the tool: %s", output)
}

func TestProjectToolGuardPathIdentity(t *testing.T) {
	root, err := filepath.Abs("../..")
	require.NoError(t, err)
	basename, err := exec.LookPath("basename")
	require.NoError(t, err)
	for _, name := range []string{"identical", "directory-alias", "ambient-shadow", "missing-managed", "nonexecutable-managed"} {
		t.Run(name, func(t *testing.T) {
			dir := t.TempDir()
			managed, ambient := filepath.Join(dir, "managed"), filepath.Join(dir, "ambient")
			require.NoError(t, os.Mkdir(managed, 0700))
			require.NoError(t, os.Mkdir(ambient, 0700))
			write := func(path, body string) {
				t.Helper()
				require.NoError(t, os.WriteFile(path, []byte("#!/bin/sh\nset -eu\n"+body), 0700))
			}
			write(filepath.Join(managed, "mise"), "[ \"$*\" = 'which go --tool core:go' ] || exit 2\nprintf '%s\\n' \"$*\" >> \"$LOOKUP_CALLS\"\nprintf '%s/go\\n' \"$MANAGED_TOOLS\"\n")
			write(filepath.Join(managed, "basename"), "printf '%s\\n' \"$*\" >> \"$NORMALIZATION_CALLS\"\nexec \"$REAL_BASENAME\" \"$@\"\n")
			tool := "printf 'unexpected tool execution\\n' >> \"$TOOL_CALLS\"\nexit 99\n"
			write(filepath.Join(managed, "go"), tool)
			write(filepath.Join(ambient, "go"), tool)
			path, wantError, wantNormalization := managed, "", 0
			switch name {
			case "directory-alias":
				path = filepath.Join(dir, "alias")
				require.NoError(t, os.Symlink(managed, path))
				wantNormalization = 2
			case "ambient-shadow":
				path = ambient + ":" + managed
				wantError, wantNormalization = "resolves to", 2
			case "missing-managed":
				require.NoError(t, os.Remove(filepath.Join(managed, "go")))
				path, wantError = managed+":"+ambient, "is not installed"
			case "nonexecutable-managed":
				require.NoError(t, os.Chmod(filepath.Join(managed, "go"), 0600))
				path, wantError = managed+":"+ambient, "is not installed"
			}
			lookup, normalization, calls := filepath.Join(dir, "lookup"), filepath.Join(dir, "normalization"), filepath.Join(dir, "calls")
			cmd := exec.Command(filepath.Join(root, "scripts/check-tools.sh"), "go")
			cmd.Env = []string{"PATH=" + path + ":/usr/bin:/bin", "HOME=" + dir, "MANAGED_TOOLS=" + managed,
				"LOOKUP_CALLS=" + lookup, "NORMALIZATION_CALLS=" + normalization, "TOOL_CALLS=" + calls, "REAL_BASENAME=" + basename}
			output, err := cmd.CombinedOutput()
			if wantError == "" {
				require.NoError(t, err, "%s", output)
			} else {
				require.Error(t, err, "%s", output)
				require.Contains(t, string(output), wantError)
			}
			data, err := os.ReadFile(lookup)
			require.NoError(t, err)
			require.Equal(t, "which go --tool core:go\n", string(data), "every path must query the required provider")
			data, err = os.ReadFile(normalization)
			if !os.IsNotExist(err) {
				require.NoError(t, err)
			}
			require.Equal(t, wantNormalization, strings.Count(string(data), "\n"), "PROVIDER_NORMALIZATION_WORK: identical spellings need no redundant normalization subprocesses")
			_, err = os.Stat(calls)
			require.True(t, os.IsNotExist(err), "provider checking must not execute either tool")
		})
	}
}

func TestSecurityRecipeRejectsIncompleteReports(t *testing.T) {
	makePath, err := exec.LookPath("make")
	require.NoError(t, err)
	guardOutput, guardErr := exec.Command("../../scripts/check-tools.sh", "jq").CombinedOutput()
	require.NoError(t, guardErr, "managed jq required: %s", guardOutput)
	jq, err := exec.LookPath("jq")
	require.NoError(t, err, "install the ordinary project tool set")
	root, err := filepath.Abs("../..")
	require.NoError(t, err)
	const clean = `{"Issues":null,"Report":{"Linters":[{"Name":"gosec","Enabled":true},{"Name":"typecheck","Enabled":true}]}}`
	for _, tc := range []struct {
		name, report string
		scannerExit  string
		wantError    bool
	}{
		{"clean", clean, "0", false},
		{"finding", `{"Issues":[{"FromLinter":"gosec","Text":"G702: control"}],"Report":{"Linters":[{"Name":"gosec","Enabled":true},{"Name":"typecheck","Enabled":true}]}}`, "0", true},
		{"loader_failure", clean, "3", true},
		{"malformed", `{broken`, "0", true},
		{"missing", "", "0", true},
		{"wrong_engine", `{"Issues":[],"Report":{"Linters":[{"Name":"unused","Enabled":true}]}}`, "0", true},
		{"missing_issues", `{"Report":{"Linters":[{"Name":"gosec","Enabled":true}]}}`, "0", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			bin := filepath.Join(dir, "bin")
			require.NoError(t, os.Mkdir(bin, 0700))
			write := func(name, body string) {
				t.Helper()
				require.NoError(t, os.WriteFile(filepath.Join(bin, name), []byte("#!/bin/sh\nset -eu\n"+body), 0700))
			}
			write("mise", "[ \"$1\" = which ] || exit 2\nprintf '%s/%s\\n' \"$MANAGED_TOOLS\" \"$2\"\n")
			write("go", "exit 0\n")
			producer := "if [ -n \"$FIXTURE_REPORT\" ]; then printf '%s' \"$FIXTURE_REPORT\" > \"$REPORT_PATH\"; fi\nexit \"$SCANNER_EXIT\"\n"
			write("gosec", producer)
			write("golangci-lint", producer)
			require.NoError(t, os.Symlink(jq, filepath.Join(bin, "jq")))
			report := filepath.Join(dir, "security.json")
			// A stale successful report must not hide a producer that wrote nothing.
			require.NoError(t, os.WriteFile(report, []byte(clean), 0600))
			cmd := exec.Command(makePath, "-f", filepath.Join(root, "Makefile"), "security", "SECURITY_REPORT="+report)
			cmd.Dir = root
			cmd.Env = []string{"PATH=" + bin + ":/usr/bin:/bin", "HOME=" + dir, "MANAGED_TOOLS=" + bin, "FIXTURE_REPORT=" + tc.report, "REPORT_PATH=" + report, "SCANNER_EXIT=" + tc.scannerExit}
			output, err := cmd.CombinedOutput()
			if tc.wantError {
				require.Error(t, err, "incomplete or positive report accepted: %s", output)
				require.NotContains(t, string(output), "✓ Security checks passed")
			} else {
				require.NoError(t, err, "%s", output)
				require.Contains(t, string(output), "✓ Security checks passed")
			}
		})
	}
}
