#!/bin/bash
# Narrow diagnostic overlays, never candidate evidence. Each assertion red is
# followed by a byte-exact restore and the same selector's uncached green.
set -euo pipefail
cd "$(dirname "$0")/.."
scripts/check-tools.sh go jq python3
git diff --exit-code
git diff --cached --exit-code
evidence="$PWD/test-evidence/watchdog-mutations"
mkdir -p "$evidence"
git rev-parse HEAD HEAD^{tree} > "$evidence/candidate.txt"
source_file=internal/manager/manager.go
git show "HEAD:$source_file" > "$evidence/original.go"
restore() { cp "$evidence/original.go" "$source_file"; }
trap restore EXIT
run_case() {
  local phase=$1
  printf '%s\n' "go test -json -race -count=1 -timeout=90s -run '$selector' ./internal/manager" > "$case_dir/$phase.command"
  set +e
  go test -json -race -count=1 -timeout=90s -run "$selector" ./internal/manager 2> "$case_dir/$phase.stderr" | tee "$case_dir/$phase.jsonl"
  local status=("${PIPESTATUS[@]}")
  set -e
  printf 'go=%s tee=%s\n' "${status[0]}" "${status[1]}" > "$case_dir/$phase.exit"
  test "${status[1]}" -eq 0
  if [ "$phase" = red ]; then
    test "${status[0]}" -eq 1
    jq -e -s --arg marker "$marker" '. as $events | any($events[]; .Action == "fail" and .Test != null) and any($events[]; (.Output // "") | contains($marker)) and (any($events[]; (.Output // "") | test("test timed out|DATA RACE|panic:")) | not)' "$case_dir/$phase.jsonl"
  else
    test "${status[0]}" -eq 0
    jq -e -s --arg name "$name" 'any(.[]; .Action == "pass" and .Test == $name)' "$case_dir/$phase.jsonl"
  fi
}
for control in no-cancel synchronous-probe failed-health lost-cause rearm five-second-probe no-owner-join no-wait-delay; do
  case_dir="$evidence/$control"; mkdir -p "$case_dir"
  case "$control" in
    no-cancel) name=TestManagerSilentOpenStreamWatchdog; marker=WATCHDOG_MUST_CANCEL_OPEN_STREAM ;;
    synchronous-probe) name=TestManagerWatchdogProbeJoinDoesNotGateAction; marker=PROBE_JOIN_MUST_NOT_GATE_ACTION ;;
    failed-health) name=TestManagerWatchdogSuccessfulProcessingOnly; marker=FAILED_PROCESSING_MUST_NOT_RESET ;;
    lost-cause) name=TestManagerSilentOpenStreamWatchdog; marker=WATCHDOG_CAUSE_MUST_SURVIVE ;;
    rearm) name=TestEventWatchdogArmsAtRunOnlyOnce; marker=RETRY_MUST_NOT_REARM ;;
    five-second-probe) name=TestEventWatchdogProbeRemainingAllowance; marker=PROBE_USES_REMAINING_ALLOWANCE ;;
    no-owner-join) name=TestManagerWatchdogProbeJoinDoesNotGateAction; marker=RUN_MUST_RETAIN_OWNED_JOIN ;;
    no-wait-delay) name=TestRemoteDockerPingOwnedLifetime; marker=PROBE_PIPE_JOIN ;;
  esac
  selector="^$name\$"
  CONTROL="$control" FILE="$source_file" python3 - <<'PY'
import hashlib, os
from pathlib import Path
p = Path(os.environ['FILE']); source = p.read_text(); control = os.environ['CONTROL']
def replace(old, new):
    global source
    assert source.count(old) == 1, (control, old, source.count(old))
    source = source.replace(old, new, 1)
if control == 'no-cancel':
    replace('\tw.cancel()', '\t// diagnostic: no cancellation')
elif control == 'synchronous-probe':
    replace('// The payload may be stale after scheduling delay. Tick samples now.', 'if probeDone != nil { <-probeDone }\n// The payload may be stale after scheduling delay. Tick samples now.')
elif control == 'failed-health':
    replace('if err := m.handleStartEvent(ctx, event, generation); err != nil {', 'if err := m.handleStartEvent(ctx, event, generation); err != nil {\nm.watchdog.OnEvent()')
elif control == 'lost-cause':
    replace('runErr = m.watchdog.finish(runErr)', '_ = m.watchdog.finish(runErr)')
elif control == 'rearm':
    replace('if w.armed {\n\t\treturn\n\t}', '// diagnostic: rearm on repeated arm')
elif control == 'five-second-probe':
    replace('context.WithDeadline(ctx, deadline)', 'context.WithTimeout(ctx, 5*time.Second)')
elif control == 'no-owner-join':
    replace('\t\t<-watchdogDone', '\t\t// diagnostic: omit owner join')
elif control == 'no-wait-delay':
    begin = source.index('func (r *remoteDockerPingRunner) Ping(')
    end = source.index('// eventWatchdog', begin)
    body = source[begin:end]
    old = 'cmd.Stdout, cmd.Stderr, cmd.WaitDelay = stdout, stderr, 2*time.Second'
    assert body.count(old) == 1
    source = source[:begin] + body.replace(old, 'cmd.Stdout, cmd.Stderr = stdout, stderr', 1) + source[end:]
print('original_sha256=' + hashlib.sha256(p.read_bytes()).hexdigest())
p.write_text(source)
print('mutant_sha256=' + hashlib.sha256(p.read_bytes()).hexdigest())
PY
  git diff -- "$source_file" > "$case_dir/mutation.diff"
  run_case red
  restore
  cmp "$evidence/original.go" "$source_file"
  git diff --exit-code
  run_case restored-green
done
printf 'Eight source-bound watchdog assertion reds; eight byte-exact restored greens.\n' > "$evidence/result.txt"
set +e
go test -tags=rdhpf_integration -json -race -count=1 -timeout=90s -run '^TestWatchdogFixture' ./internal/testutil/sshfixture 2> test-evidence/watchdog-controls.stderr | tee test-evidence/watchdog-controls.jsonl
status=("${PIPESTATUS[@]}")
set -e
printf 'go=%s tee=%s\n' "${status[0]}" "${status[1]}" > test-evidence/watchdog-controls.exit
test "${status[0]}" -eq 0; test "${status[1]}" -eq 0
jq -e -s 'any(.[]; .Action == "pass" and .Test == "TestWatchdogFixtureProxyDeathJoinsHeldChild") and any(.[]; .Action == "pass" and .Test == "TestWatchdogFixtureCanceledDispatchNeverLaunches") and any(.[]; .Action == "pass" and .Test == "TestWatchdogFixtureAssertionFailureCleanup")' test-evidence/watchdog-controls.jsonl
