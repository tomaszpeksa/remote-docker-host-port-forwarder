#!/bin/bash
# Source-bound diagnostic overlays, never reported as frozen-candidate execution.
set -euo pipefail
cd "$(dirname "$0")/.."
test "${GITHUB_ACTIONS:-}" = true
test "${RDHPF_HOSTED_TRUST:-}" = 1
test "${SSH_TEST_HOST:-}" = ssh://testuser@localhost:2222
git diff --exit-code
evidence="$PWD/test-evidence/trust-mutations"
mkdir -p "$evidence"
base=5acdc328ae148c538cf076c3c62cc0f5d2726d8d
master_blob=12232176fbc37250c000ea6478d3c36b761c9858
# Rewritten/squashed ancestry need not contain historical accepted A. Resolve
# this immutable diagnostic input before any redirection can mutate source.
if ! git cat-file -e "$base^{commit}" 2>/dev/null; then
  printf 'git fetch --no-tags --no-write-fetch-head --depth=1 origin %s\n' "$base" > "$evidence/accepted-A-fetch.command"
  fetch_status=0
  git fetch --no-tags --no-write-fetch-head --depth=1 origin "$base" > "$evidence/accepted-A-fetch.stdout" 2> "$evidence/accepted-A-fetch.stderr" || fetch_status=$?
  printf '%s\n' "$fetch_status" > "$evidence/accepted-A-fetch.exit"
  if [ "$fetch_status" -ne 0 ]; then
    printf 'Cannot fetch immutable accepted-A source; refusing mutation.\n' >&2
    exit 1
  fi
fi
if [ "$(git rev-parse --verify "$base^{commit}")" != "$base" ] ||
   [ "$(git rev-parse --verify "$base:internal/ssh/master.go")" != "$master_blob" ] ||
   ! git cat-file -e "$base:internal/ssh/master.go"; then
  printf 'Unexpected accepted-A commit/Master blob identity; refusing mutation.\n' >&2
  exit 1
fi
printf '%s\n%s\n' "$base" "$master_blob" > "$evidence/accepted-A-identity.txt"
git rev-parse HEAD HEAD^{tree} > "$evidence/candidate.txt"
printf '%s\n' "$base" > "$evidence/accepted-A.txt"
source_file=
restore() { if [ -n "$source_file" ]; then git show "HEAD:$source_file" > "$source_file"; fi; }
trap restore EXIT

run_red() {
  local name=$1 package=$2 selector=$3 expected=$4 reason=${5:-native askpass invoked}
  git diff -- "$source_file" > "$evidence/$name.diff"
  sha256sum "$source_file" > "$evidence/$name.source.sha256"
  printf 'go test -tags=rdhpf_integration -json -count=1 -timeout=45s -run %q %q\n' "$selector" "$package" > "$evidence/$name.command"
  set +e
  go test -tags=rdhpf_integration -json -count=1 -timeout=45s -run "$selector" "$package" 2> "$evidence/$name.stderr" | tee "$evidence/$name.jsonl"
  local status=("${PIPESTATUS[@]}")
  set -e
  printf 'go=%s tee=%s\n' "${status[0]}" "${status[1]}" > "$evidence/$name.exit"
  test "${status[1]}" -eq 0
  test "${status[0]}" -ne 0
  # A compiler/load error, skip or deadline kill is not a sensitive scenario.
  jq -e -s --arg name "$expected" --arg reason "$reason" '
    any(.[]; .Action == "fail" and .Test == $name) and
    any(.[]; .Test == $name and ((.Output // "") | contains($reason))) and
    (any(.[]; (.Output // "") | test("test timed out|deadline kill|outer deadline")) | not)
  ' "$evidence/$name.jsonl"
  restore
  source_file=
}

# Genuine A trust behavior with B-owned fixture/tests, explicitly an overlay.
source_file=internal/ssh/master.go
git show "$base:$source_file" > "$source_file"
run_red accepted-A-user-pin ./tests/integration '^TestSSHTrust$/^UserPinMismatch$' TestSSHTrust/UserPinMismatch 'An error is expected but got nil'
source_file=internal/ssh/master.go
git show "$base:$source_file" > "$source_file"
run_red accepted-A-default-unknown ./tests/integration '^TestSSHTrust$/^DefaultUnknown$' TestSSHTrust/DefaultUnknown 'An error is expected but got nil'
source_file=internal/ssh/master.go
git show "$base:$source_file" > "$source_file"
run_red accepted-A-enrollment ./tests/integration '^TestSSHTrust$/^ConfiguredAcceptNew$' TestSSHTrust/ConfiguredAcceptNew 'does not contain'

# Each row removes exactly one production BatchMode option in its actual method.
while IFS='|' read -r name file method package selector expected; do
  source_file=$file
  METHOD="$method" FILE="$file" python3 - <<'PY'
import os,pathlib
p=pathlib.Path(os.environ['FILE']);text=p.read_text();method=os.environ['METHOD']
lines=text.splitlines(keepends=True);starts=[i for i,l in enumerate(lines) if l.startswith('func ') and ((' '+method+'(' in l) or (') '+method+'(' in l))]
assert len(starts)==1,(method,starts)
start=starts[0];end=next((i for i in range(start+1,len(lines)) if lines[i].startswith('func ')),len(lines))
body=''.join(lines[start:end]);needle='"-o", "BatchMode=yes", '
# Master's final option has no trailing comma.
if needle in body: assert body.count(needle)==1;body=body.replace(needle,'',1)
else:
 needle=', "-o", "BatchMode=yes"';assert body.count(needle)==1;body=body.replace(needle,'',1)
p.write_text(''.join(lines[:start])+body+''.join(lines[end:]))
PY
  run_red "$name" "$package" "$selector" "$expected"
done <<'CASES'
master|internal/ssh/master.go|openConnection|./tests/integration|^TestSSHTrust$/^EncryptedIdentityNoPrompt$|TestSSHTrust/EncryptedIdentityNoPrompt
check|internal/ssh/master.go|checkConnection|./internal/ssh|^TestControlRequestTrust$/^check$/^yes$/^encrypted$|TestControlRequestTrust/check/yes/encrypted
forward|internal/ssh/forward.go|AddForward|./internal/ssh|^TestControlRequestTrust$/^forward$/^yes$/^encrypted$|TestControlRequestTrust/forward/yes/encrypted
cancel|internal/ssh/forward.go|CancelForward|./internal/ssh|^TestControlRequestTrust$/^cancel$/^yes$/^encrypted$|TestControlRequestTrust/cancel/yes/encrypted
ports|internal/docker/inspect.go|InspectPorts|./tests/integration|^TestDockerMissingMuxTrust$/^ports$/^encrypted$|TestDockerMissingMuxTrust/ports/encrypted
labels|internal/docker/inspect.go|inspectPortsFromLabels|./internal/docker|^TestLabelMissingMuxTrust$/^encrypted$|TestLabelMissingMuxTrust/encrypted
events|internal/docker/events.go|stream|./tests/integration|^TestDockerMissingMuxTrust$/^events$/^encrypted$|TestDockerMissingMuxTrust/events/encrypted
startup|internal/manager/manager.go|listRunning|./internal/manager|^TestManagerMissingMuxTrust$/^startup$/^encrypted$|TestManagerMissingMuxTrust/startup/encrypted
infrastructure|internal/manager/manager.go|hasTestInfrastructureLabel|./internal/manager|^TestManagerMissingMuxTrust$/^label$/^encrypted$|TestManagerMissingMuxTrust/label/encrypted
ping|internal/manager/manager.go|Ping|./internal/manager|^TestManagerMissingMuxTrust$/^ping$/^encrypted$|TestManagerMissingMuxTrust/ping/encrypted
connectivity|internal/manager/manager.go|validateDockerConnectivity|./internal/manager|^TestManagerMissingMuxTrust$/^connectivity$/^encrypted$|TestManagerMissingMuxTrust/connectivity/encrypted
CASES
git diff --exit-code
printf 'All fourteen source-bound red controls observed; candidate source restored.\n' > "$evidence/result.txt"
