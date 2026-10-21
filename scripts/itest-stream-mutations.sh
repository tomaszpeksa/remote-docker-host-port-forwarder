#!/bin/bash
# Six narrow diagnostic overlays; retain assertion reds and exact restored greens.
set -euo pipefail
cd "$(dirname "$0")/.."
test -z "${SSH_TEST_HOST:-}"
test -z "${SSH_TEST_KEY_PATH:-}"
scripts/check-tools.sh go jq python3
evidence=${STREAM_CONTROL_EVIDENCE:-"$PWD/test-evidence/stream-controls"}
mkdir -p "$evidence"
git rev-parse HEAD HEAD^{tree} > "$evidence/base.txt"
git diff HEAD > "$evidence/candidate.diff"
source_file=
restore() { if [ -n "$source_file" ]; then cp "$case_dir/original.go" "$source_file"; fi; }
trap restore EXIT
controls=("$@")
if [ "${#controls[@]}" -eq 0 ]; then controls=(no-pacing no-processing no-retirement no-direct-wait-join no-wait-delay no-watcher-join); fi
for control in "${controls[@]}"; do
  case_dir="$evidence/$control"; mkdir -p "$case_dir"
  package=./internal/manager
  test_flags=(-json -count=1 -timeout=45s)
  source_file=internal/manager/manager.go
  case "$control" in
    no-pacing) selector='^TestManagerStreamRestartPacing$'; marker=CLEAN_EOF_MUST_PACE ;;
    no-processing) selector='^TestManagerDiscoveryBufferedTerminal$'; marker=BUFFERED_TERMINAL_MUST_DRAIN ;;
    no-retirement) selector='^TestManagerDiscoveryBufferedTerminal$'; marker=BUFFERED_TERMINAL_MUST_RETIRE ;;
    no-direct-wait-join) test_flags+=(-race); source_file=internal/docker/events.go; package=./internal/docker; selector='^TestEventReaderTerminalJoinsDirectWait$'; marker=STREAM_TERMINAL_MUST_JOIN_DIRECT_WAIT ;;
    no-wait-delay) source_file=internal/docker/events.go; package=./internal/docker; selector='^TestEventReaderInheritedDescriptors$'; marker=STREAM_INHERITED_COPY_MUST_JOIN ;;
    no-watcher-join) source_file=internal/docker/events.go; package=./internal/docker; selector='^TestEventReaderTerminalJoinsWatcher$'; marker=STREAM_TERMINAL_MUST_JOIN_WATCHER ;;
    *) printf 'unknown stream control: %s\n' "$control" >&2; exit 1 ;;
  esac
  cp "$source_file" "$case_dir/original.go"
  CONTROL="$control" FILE="$source_file" python3 - <<'PY'
import os
from pathlib import Path
p = Path(os.environ['FILE'])
s = p.read_text()
control = os.environ['CONTROL']
old, new = {
    'no-pacing': ('timer := time.NewTimer(baseDelay)', 'timer := time.NewTimer(0)'),
    'no-processing': ('switch event.Type {', 'switch "dropped" {'),
    'no-retirement': ('m.retireEvent(accepted.token)', '// diagnostic: omit accepted reservation retirement'),
    # Keep the actual command and real Wait independently owned by test cleanup.
    # The gate prevents the reaper racing the premature SUT terminal observation.
    'no-direct-wait-join': ('waitErr := cmd.Wait()', '''go func() {
            defer close(streamWaitFault.joined)
            <-streamWaitFault.releaseReaper
            streamWaitFault.joined <- cmd.Wait()
        }()
        close(streamWaitFault.started)
        <-streamWaitFault.entered
        waitErr := error(nil)'''),
    'no-wait-delay': ('writer, diagnostics, 2*time.Second', 'writer, diagnostics, 0'),
    'no-watcher-join': ('close(finished); <-cleanupDone', 'close(finished); _ = cleanupDone'),
}[control]
assert s.count(old) == 1, (control, s.count(old))
p.write_text(s.replace(old, new, 1))
PY
  git diff -- "$source_file" > "$case_dir/overlay.diff"
  for phase in red restored-green; do
    if [ "$phase" = restored-green ]; then restore; cmp "$case_dir/original.go" "$source_file"; fi
    printf '%s\n' "go test ${test_flags[*]} -run '$selector' $package" > "$case_dir/$phase.command"
    set +e
    go test "${test_flags[@]}" -run "$selector" "$package" > "$case_dir/$phase.jsonl" 2> "$case_dir/$phase.stderr"
    status=$?
    set -e
    printf '%s\n' "$status" > "$case_dir/$phase.exit"
    if [ "$phase" = red ]; then
      test "$status" -eq 1
      jq -e -s --arg marker "$marker" 'any(.[]; .Action == "fail" and .Test != null) and any(.[]; (.Output // "") | contains($marker)) and (any(.[]; (.Output // "") | test("test timed out|panic:|DATA RACE")) | not)' "$case_dir/$phase.jsonl"
    else
      test "$status" -eq 0
      jq -e -s 'any(.[]; .Action == "pass" and .Test != null) and (any(.[]; .Action == "fail") | not)' "$case_dir/$phase.jsonl"
    fi
    if [ "$control" = no-direct-wait-join ]; then
      jq -e -s --arg phase "$phase" '
        [.[] | select(.Package == "github.com/tomaszpeksa/remote-docker-host-port-forwarder/internal/docker" and .Test == "TestEventReaderTerminalJoinsDirectWait") | .Output // ""] as $lines |
        any($lines[]; contains("STREAM_WAIT_FIXTURE_ENTERED")) and
        any($lines[]; contains("STREAM_WAIT_SUT_JOINED")) and
        any($lines[]; contains("STREAM_WAIT_CLEANUP_REAPED")) and
        ($phase != "red" or any($lines[]; contains("STREAM_WAIT_FAULT_REAPER_JOINED"))) and
        ([$lines[] | select(test("STREAM_WAIT_(FIXTURE_ENTERED|SUT_JOINED|CLEANUP_REAPED|FAULT_REAPER_JOINED)")) | capture("pid=(?<pid>[0-9]+)").pid] | unique | length) == 1
      ' "$case_dir/$phase.jsonl" > "$case_dir/$phase.ownership-valid"
    fi
  done
  source_file=
done
printf 'Named stream assertion reds and exact restored greens observed: %s\n' "${controls[*]}" > "$evidence/result.txt"
