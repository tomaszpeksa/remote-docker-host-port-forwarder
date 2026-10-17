#!/bin/bash
# Inner tests intentionally FAIL after actual SSH/Engine dispatch. Only explicit
# post-failure join/accounting/retirement observations make this control succeed.
set -euo pipefail
cd "$(dirname "$0")/.."
test "${GITHUB_ACTIONS:-}" = true
test "${RDHPF_HOSTED_TRUST:-}" = 1
test "${SSH_TEST_HOST:-}" = ssh://testuser@localhost:2222
git diff --exit-code
evidence="$PWD/test-evidence/watchdog-failure-accounting"
mkdir -p "$evidence"
git rev-parse HEAD HEAD^{tree} > "$evidence/candidate.txt"
missing=0
while IFS='|' read -r mode package name joined; do
  printf 'RDHPF_WATCHDOG_FAILURE_CONTROL=1 RDHPF_WATCHDOG_BINARY=%q go test -tags=rdhpf_integration -json -count=1 -p 1 -timeout=3m -run %q %q\n' "$PWD/test-evidence/bin/rdhpf" "^$name$" "$package" > "$evidence/$mode.command"
  date -u '+%Y-%m-%dT%H:%M:%SZ' > "$evidence/$mode.started"
  set +e
  RDHPF_WATCHDOG_FAILURE_CONTROL=1 RDHPF_WATCHDOG_BINARY="$PWD/test-evidence/bin/rdhpf" go test -tags=rdhpf_integration -json -count=1 -p 1 -timeout=3m -run "^$name$" "$package" 2> "$evidence/$mode.stderr" | tee "$evidence/$mode.jsonl"
  status=("${PIPESTATUS[@]}")
  set -e
  printf 'go=%s tee=%s\n' "${status[0]}" "${status[1]}" > "$evidence/$mode.exit"
  date -u '+%Y-%m-%dT%H:%M:%SZ' > "$evidence/$mode.finished"
  test "${status[0]}" -eq 1; test "${status[1]}" -eq 0
  jq -e -s --arg name "$name" --arg joined "$joined" '
    [.[] | select(.Test == $name)] as $case |
    any($case[]; .Action == "fail") and
    any($case[]; (.Output // "") | contains("WATCHDOG_FAILURE_CONTROL_DISPATCH")) and
    any($case[]; (.Output // "") | contains("WATCHDOG_INTENTIONAL_FAILURE_AFTER_DISPATCH")) and
    any($case[]; (.Output // "") | contains($joined)) and
    (any(.[]; (.Action == "skip") or ((.Output // "") | test("test timed out|panic:|DATA RACE|JOIN_PENDING"))) | not)
  ' "$evidence/$mode.jsonl" > "$evidence/$mode.expected-failure-valid"
  if ! jq -e -s --arg name "$name" --arg joined "$joined" '
    [.[] | select(.Test == $name and .Output != null) | .Output] as $lines |
    ($lines | map(contains("WATCHDOG_INTENTIONAL_FAILURE_AFTER_DISPATCH")) | index(true)) as $failure |
    ($lines | map(contains($joined)) | index(true)) as $join |
    ($lines[] | select(contains("WATCHDOG_FAILURE_CONTROL_DISPATCH")) | capture("name=(?<name>rdhpf-ping-[0-9]+) Engine_ID=(?<id>[a-f0-9]{64})")) as $attempt |
    [range(0; $lines|length) | select($lines[.] | contains("WATCHDOG_PING_ACCOUNT name=" + $attempt.name))] as $account |
    ($account|length)==1 and $failure < $join and $join < $account[0] and
    ($lines[$account[0]] | contains($attempt.id)) and
    any($lines[]; contains("WATCHDOG_PING_ACCOUNT_COMPLETE attempts=1 unknown=0")) and
    ([range(0; $lines|length) | select($lines[.] | contains("removed owned backend"))] | length >= 1 and all(. > $account[0])) and
    ([range(0; $lines|length) | select($lines[.] | contains("restored exact disposable-account SSH config"))] | length == 1 and all(. > $account[0]))
  ' "$evidence/$mode.jsonl" > "$evidence/$mode.accounting-valid"; then
    printf 'WATCHDOG_FAILURE_ACCOUNTING_MISSING case=%s\n' "$name" | tee "$evidence/$mode.missing"
    missing=1
  fi
done <<'CASES'
manager|./internal/manager|TestWatchdogOwnedWarmProcessing|WATCHDOG_RUN_JOINED
cli|./cmd/rdhpf|TestWatchdogOwnedCLISilentStream|WATCHDOG_CLI_JOINED
CASES
test "$missing" -eq 0
printf 'Both expected Go FAIL cases retained; real dispatch, post-failure joins, exact Engine accounting and fixture retirement verified.\n' > "$evidence/result.txt"
