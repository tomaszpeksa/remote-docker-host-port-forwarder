#!/bin/bash
# Diagnostic source overlays only. Every red has a named assertion and a fresh
# exact-restored-source green; neither can be mistaken for candidate execution.
set -euo pipefail
cd "$(dirname "$0")/.."
test "${GITHUB_ACTIONS:-}" = true
test "${RDHPF_HOSTED_TRUST:-}" = 1
test "${SSH_TEST_HOST:-}" = ssh://testuser@localhost:2222
git diff --exit-code
evidence="$PWD/test-evidence/discovery-mutations"
mkdir -p "$evidence"
git rev-parse HEAD HEAD^{tree} > "$evidence/candidate.txt"
source_file=
restore() { if [ -n "$source_file" ]; then cp "$evidence/original.txt" "$source_file"; fi; }
trap restore EXIT
run_case() {
  local phase=$1
  printf '%s\n' "go test -tags=rdhpf_integration -json -count=1 -timeout=3m -run '$selector' $package" > "$case_dir/$phase.command"
  set +e
  go test -tags=rdhpf_integration -json -count=1 -timeout=3m -run "$selector" "$package" 2> "$case_dir/$phase.stderr" | tee "$case_dir/$phase.jsonl"
  local status=("${PIPESTATUS[@]}")
  set -e
  printf 'go=%s tee=%s\n' "${status[0]}" "${status[1]}" > "$case_dir/$phase.exit"
  test "${status[1]}" -eq 0
  if [ "$phase" = red ]; then
    test "${status[0]}" -eq 1
    jq -e -s --arg names "$names" --arg marker "$marker" '
      . as $events | all(($names|split(","))[]; . as $name |
        any($events[]; .Action == "fail" and .Test == $name) and
        any($events[]; .Test == $name and ((.Output // "") | test($marker)))) and
      (any($events[]; (.Output // "") | test("test timed out|context deadline exceeded|did not join|did not finish Wait|overlap gate exceeded")) | not)
    ' "$case_dir/$phase.jsonl"
  else
    test "${status[0]}" -eq 0
    jq -e -s --arg names "$names" '. as $events | all(($names|split(","))[]; . as $name | any($events[]; .Action == "pass" and .Test == $name))' "$case_dir/$phase.jsonl"
  fi
}
for control in disable-fence disable-retry disable-absence disable-partial-preserve disable-unapplied disable-size-error; do
  case_dir="$evidence/$control"; mkdir -p "$case_dir"
  package=./internal/manager
  case "$control" in
    disable-fence)
      source_file=internal/manager/discovery.go
      selector='^TestManagerDiscoveryOwnedInventoryOverlap$'
      names=TestManagerDiscoveryOwnedInventoryOverlap; marker=DISCOVERY_STALE_FENCE ;;
    disable-retry)
      source_file=internal/manager/discovery.go
      selector='^TestManagerDiscoveryOwnedInspectRetry$'
      names=TestManagerDiscoveryOwnedInspectRetry; marker=DISCOVERY_INSPECT_RETRY ;;
    disable-absence)
      source_file=internal/state/model.go
      selector='^TestManagerDiscoveryOwned(StartupGap|Downtime)$'
      names=TestManagerDiscoveryOwnedStartupGap,TestManagerDiscoveryOwnedDowntime
      marker='DISCOVERY_(STARTUP_GAP_TIMER|DOWNTIME_TIMER)' ;;
    disable-partial-preserve)
      source_file=internal/state/model.go
      selector='^TestManagerDiscoveryInventory$/^PartialABC$'
      names=TestManagerDiscoveryInventory/PartialABC; marker=PARTIAL_PRESERVE ;;
    disable-unapplied)
      source_file=internal/manager/discovery.go
      selector='^TestManagerDiscoveryUnappliedEOF$'
      names=TestManagerDiscoveryUnappliedEOF; marker=EOF_MUST_RETAIN_UNAPPLIED_REQUEST ;;
    disable-size-error)
      source_file=internal/util/capture.go; package=./internal/docker
      selector='^TestInspectPortsCaptureLimit$'
      names=TestInspectPortsCaptureLimit; marker=CAPTURE_OVERFLOW_IS_NOT_EMPTY_SUCCESS ;;
  esac
  git show "HEAD:$source_file" > "$evidence/original.txt"
  cp "$evidence/original.txt" "$case_dir/original.txt"
  sha256sum "$source_file" > "$case_dir/before.sha256"
  CONTROL="$control" FILE="$source_file" python3 - <<'PY'
import os
from pathlib import Path
p = Path(os.environ['FILE']); s = p.read_text(); control = os.environ['CONTROL']
def replace(old, new):
    global s
    assert s.count(old) == 1, (control, old, s.count(old))
    s = s.replace(old, new, 1)
if control == 'disable-fence':
    replace(' || c.fence != (inventoryFence{d.generation, d.revision, d.attempt})', '')
elif control == 'disable-retry':
    replace('retryAt = d.now().Add(backoff)', 'retryAt = d.now().Add(24*time.Hour)')
elif control == 'disable-absence':
    replace('if !present[id] && len(ports) != 0 {', 'if false && !present[id] && len(ports) != 0 {')
elif control == 'disable-partial-preserve':
    start = s.index('func (s *State) MergeInventory')
    before, body = s[:start], s[start:]
    needle = 'changed := false'
    assert body.count(needle) == 1
    s = before + body.replace(needle, 'changed := false\nfor _, id := range listed {s.desired[id]=[]int{};changed=true}', 1)
elif control == 'disable-unapplied':
    replace('m.discovery.request++', '')
elif control == 'disable-size-error':
    replace('b.exceeded = true', 'b.exceeded = false')
    replace('return remaining, ErrOutputLimit', 'return len(p), nil')
p.write_text(s)
PY
  git diff -- "$source_file" > "$case_dir/mutation.diff"
  sha256sum "$source_file" > "$case_dir/mutant.sha256"
  run_case red
  restore
  cmp "$evidence/original.txt" "$source_file"
  sha256sum "$source_file" > "$case_dir/restored.sha256"
  git diff --exit-code
  run_case restored-green
  source_file=
done
printf 'Six source-bound discovery assertion reds and exact restored greens observed.\n' > "$evidence/result.txt"
