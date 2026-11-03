package unit

import (
	"encoding/json"
	"os"
	"os/exec"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// These are the actual jq programs, not a second implementation of the guards.
func watchdogFailurePredicate(t *testing.T, script, result string) string {
	t.Helper()
	lines := strings.Split(script, "\n")
	for end, line := range lines {
		if !strings.Contains(line, `> "$evidence/$mode.`+result+`"`) {
			continue
		}
		for start := end - 1; start >= 0; start-- {
			if strings.Contains(lines[start], "jq -e -s") {
				require.True(t, strings.HasSuffix(lines[start], " '"))
				return strings.Join(lines[start+1:end], "\n")
			}
		}
	}
	t.Fatalf("missing watchdog failure-accounting predicate %s", result)
	return ""
}

func TestWatchdogFailureAccountingBackendDiagnostics(t *testing.T) {
	output, err := exec.Command("../../scripts/check-tools.sh", "jq").CombinedOutput()
	require.NoError(t, err, "%s", output)
	script, err := os.ReadFile("../../scripts/itest-watchdog-failure-accounting.sh")
	require.NoError(t, err)
	// Verbatim relevant events from run 40352999952, commit 599993c.
	// Unrelated output is omitted; no SSH, Docker or integration driver is run.
	sample, err := os.ReadFile("testdata/watchdog-failure-accounting-backend.jsonl")
	require.NoError(t, err)
	const backendName = "rdhpf-trust-5400cf653363664d-43955"
	const backendID = "1808fd75acc82a134308f4517a124ac4cb09824922dcc07d201092df2f07708c"
	const pingID = "89f1593dd43043cb45d97e26b506ab7ece2724ae2d37f5020defea66a9bc7428"
	for _, tc := range []struct{ name, rejection string }{
		{"lowercase", ""}, {"uppercase", ""},
		{"wrong-backend-name", "backend-failure-valid"},
		{"wrong-backend-id", "backend-failure-valid"},
		{"missing-error", "backend-failure-valid"},
		{"extra-error", "backend-failure-valid"},
		{"missing-removal", "backend-failure-valid"},
		{"duplicate-removal", "backend-failure-valid"},
		{"removal-before-conflict", "backend-failure-valid"},
		{"missing-join", "expected-failure-valid"},
		{"late-join", "accounting-valid"},
		{"wrong-ping-id", "accounting-valid"},
		{"missing-accounting", "accounting-valid"},
		{"duplicate-accounting", "accounting-valid"},
		{"unknown-accounting", "accounting-valid"},
		{"early-retirement", "accounting-valid"},
		{"early-config-restore", "accounting-valid"},
		{"duplicate-config-restore", "accounting-valid"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var rows []map[string]any
			var deferred []map[string]any
			for _, line := range strings.Split(strings.TrimSpace(string(sample)), "\n") {
				var row map[string]any
				require.NoError(t, json.Unmarshal([]byte(line), &row))
				text, _ := row["Output"].(string)
				conflict := strings.Contains(text, "owned Docker fixture:") && strings.Contains(text, "Conflict.")
				absent := strings.Contains(text, "no such object: "+backendName)
				removal := strings.Contains(text, "removed owned backend name=")
				joined := strings.Contains(text, "WATCHDOG_RUN_JOINED")
				account := strings.Contains(text, "WATCHDOG_PING_ACCOUNT name=")
				restored := strings.Contains(text, "restored exact disposable-account SSH config")
				switch {
				case tc.name == "uppercase" && absent:
					row["Output"] = strings.Replace(text, "no such object:", "No such object:", 1)
				case tc.name == "wrong-backend-name" && absent:
					row["Output"] = strings.Replace(text, backendName, "wrong-owned-name", 1)
				case tc.name == "wrong-backend-id" && conflict:
					row["Output"] = strings.Replace(text, backendID, strings.Repeat("0", 64), 1)
				case tc.name == "missing-error" && absent,
					tc.name == "missing-removal" && removal,
					tc.name == "missing-join" && joined,
					tc.name == "missing-accounting" && account:
					continue
				case tc.name == "extra-error" && absent,
					tc.name == "duplicate-removal" && removal,
					tc.name == "duplicate-accounting" && account,
					tc.name == "duplicate-config-restore" && restored:
					rows = append(rows, row)
				case tc.name == "removal-before-conflict" && removal,
					tc.name == "early-config-restore" && restored:
					rows = append([]map[string]any{row}, rows...)
					continue
				case tc.name == "late-join" && joined:
					deferred = append(deferred, row)
					continue
				case tc.name == "wrong-ping-id" && account:
					row["Output"] = strings.Replace(text, pingID, strings.Repeat("0", 64), 1)
				case tc.name == "unknown-accounting" && strings.Contains(text, "unknown=0"):
					row["Output"] = strings.Replace(text, "unknown=0", "unknown=1", 1)
				case tc.name == "early-retirement" && removal:
					// Keep conflict < removal < absent, but violate join/account < removal.
					for i, prior := range rows {
						priorText, _ := prior["Output"].(string)
						if strings.Contains(priorText, "WATCHDOG_RUN_JOINED") {
							rows = append(rows[:i], append([]map[string]any{row}, rows[i:]...)...)
							break
						}
					}
					continue
				}
				rows = append(rows, row)
			}
			rows = append(rows, deferred...)
			var events strings.Builder
			for _, row := range rows {
				require.NoError(t, json.NewEncoder(&events).Encode(row))
			}
			for _, result := range []string{"expected-failure-valid", "backend-failure-valid", "accounting-valid"} {
				predicate := watchdogFailurePredicate(t, string(script), result)
				cmd := exec.Command("jq", "-e", "-s", "--arg", "name", "TestWatchdogOwnedWarmProcessing", "--arg", "joined", "WATCHDOG_RUN_JOINED", "--arg", "failure", "WATCHDOG_INTENTIONAL_BACKEND_COLLISION", predicate)
				cmd.Stdin = strings.NewReader(events.String())
				output, err := cmd.CombinedOutput()
				if result == tc.rejection {
					require.Error(t, err, "WATCHDOG_ACCOUNTING_MUST_REJECT_%s: %s", tc.name, output)
					require.Equal(t, "false\n", string(output), "a jq/setup error is not a negative control")
					return
				}
				require.NoError(t, err, "WATCHDOG_BACKEND_DIAGNOSTIC_CASE_MUST_ACCEPT_RECORDED_%s at %s: %s", strings.ToUpper(tc.name), result, output)
				require.Equal(t, "true\n", string(output))
			}
		})
	}
}
