#!/bin/bash
# Source-bound diagnostic overlays, never reported as frozen-candidate execution.
set -euo pipefail
cd "$(dirname "$0")/.."
test "${GITHUB_ACTIONS:-}" = true
test "${RDHPF_HOSTED_TRUST:-}" = 1
test "${SSH_TEST_HOST:-}" = ssh://testuser@localhost:2222
git diff --exit-code
git diff --cached --exit-code
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
source_baseline=
restore_attempted=0
restore_status=0
begin_mutation() {
  local file=$1
  local baseline="$evidence/baseline-${file//\//-}"
  # Read into evidence first; a failed Git read must not truncate live source.
  git show "HEAD:$file" > "$baseline.git"
  cmp "$file" "$baseline.git"
  cp -p "$file" "$baseline"
  source_file=$file
  source_baseline=$baseline
  restore_attempted=0
  restore_status=0
}
restore() {
  local name=$1 status=$restore_status
  if [ -n "$source_file" ] && [ "$restore_attempted" -eq 0 ]; then
    restore_attempted=1
    printf 'cp -p %q %q; cmp; verify mode\n' "$source_baseline" "$source_file" > "$evidence/$name.command"
    if cp -p "$source_baseline" "$source_file" && cmp "$source_baseline" "$source_file" &&
       FILE="$source_file" BASELINE="$source_baseline" python3 - <<'PY'
import os,pathlib,stat
p=pathlib.Path(os.environ['FILE']);b=pathlib.Path(os.environ['BASELINE'])
assert p.is_file() and not p.is_symlink()
assert stat.S_IMODE(p.stat().st_mode)==stat.S_IMODE(b.stat().st_mode)
PY
    then
      sha256sum "$source_file" > "$evidence/$name.source.sha256" || status=1
    else
      status=1
    fi
  fi
  if git diff --exit-code > "$evidence/$name.tracked.diff"; then
    printf '0\n' > "$evidence/$name.tracked.exit"
  else
    printf '%s\n' "$?" > "$evidence/$name.tracked.exit"
    status=1
  fi
  if git diff --cached --exit-code > "$evidence/$name.index.diff"; then
    printf '0\n' > "$evidence/$name.index.exit"
  else
    printf '%s\n' "$?" > "$evidence/$name.index.exit"
    status=1
  fi
  git status --porcelain=v1 --untracked-files=all > "$evidence/$name.status" || status=1
  restore_status=$status
  printf '%s\n' "$status" > "$evidence/$name.exit"
  return "$status"
}
finish() {
  local primary=$? restored=0
  trap - EXIT
  printf '%s\n' "$primary" > "$evidence/primary.exit"
  restore exit-restoration || restored=$?
  if [ "$primary" -ne 0 ]; then exit "$primary"; fi
  exit "$restored"
}
trap finish EXIT

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
    any(.[]; .Action == "run" and .Test == $name) and
    any(.[]; .Action == "fail" and .Test == $name) and
    (any(.[]; .Action == "skip") | not) and
    any(.[]; .Test == $name and ((.Output // "") | contains($reason))) and
    (any(.[]; (.Output // "") | test("test timed out|deadline kill|outer deadline")) | not)
  ' "$evidence/$name.jsonl"
  restore "$name-restoration"
  source_file=
  printf 'go test -tags=rdhpf_integration -json -count=1 -timeout=45s -run %q %q\n' "$selector" "$package" > "$evidence/$name-green.command"
  set +e
  go test -tags=rdhpf_integration -json -count=1 -timeout=45s -run "$selector" "$package" 2> "$evidence/$name-green.stderr" | tee "$evidence/$name-green.jsonl"
  status=("${PIPESTATUS[@]}")
  set -e
  printf 'go=%s tee=%s\n' "${status[0]}" "${status[1]}" > "$evidence/$name-green.exit"
  test "${status[0]}" -eq 0
  test "${status[1]}" -eq 0
  jq -e -s --arg name "$expected" '
    any(.[]; .Action == "run" and .Test == $name) and
    any(.[]; .Action == "pass" and .Test == $name) and
    (any(.[]; .Action == "fail" or .Action == "skip") | not)
  ' "$evidence/$name-green.jsonl"
}

# Immutable historical policy input, not historical whole-file execution.
# Acquire it before selecting a mutation target: failed reads cannot truncate it.
git show "$base:internal/ssh/master.go" > "$evidence/accepted-A-master.go"
apply_historical_policy() {
  begin_mutation internal/ssh/master.go
  HISTORICAL="$evidence/accepted-A-master.go" python3 - <<'PY'
import os,pathlib
historical=pathlib.Path(os.environ['HISTORICAL']).read_text()
options=['StrictHostKeyChecking=accept-new','UserKnownHostsFile=/dev/null']
for option in options:
 assert historical.count('"-o", "'+option+'"')==1, ('historical policy input',option)
p=pathlib.Path('internal/ssh/master.go');text=p.read_text()
start=text.index('func (m *Master) openConnection(')
end=text.index('\nfunc ',start+1)
body=text[start:end];needle='\tif port != "" {'
assert body.count(needle)==1
policy='\targs = append(args, '+', '.join('"-o", "'+option+'"' for option in options)+')\n'
p.write_text(text[:start]+body.replace(needle,policy+needle,1)+text[end:])
PY
}
apply_historical_policy
run_red historical-policy-user-pin ./tests/integration '^TestSSHTrust$/^UserPinMismatch$' TestSSHTrust/UserPinMismatch 'An error is expected but got nil'
apply_historical_policy
run_red historical-policy-default-unknown ./tests/integration '^TestSSHTrust$/^DefaultUnknown$' TestSSHTrust/DefaultUnknown 'An error is expected but got nil'
apply_historical_policy
run_red historical-policy-enrollment ./tests/integration '^TestSSHTrust$/^ConfiguredAcceptNew$' TestSSHTrust/ConfiguredAcceptNew 'does not contain'

# Each row removes exactly one production BatchMode option in its actual method.
while IFS='|' read -r name file method package selector expected; do
  begin_mutation "$file"
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
ports|internal/docker/inspect.go|InspectPorts|./tests/integration|^TestDockerMissingMuxTrust$/^ports$/^encrypted$|TestDockerMissingMuxTrust/ports/encrypted
labels|internal/docker/inspect.go|inspectPortsFromLabels|./internal/docker|^TestLabelMissingMuxTrust$/^encrypted$|TestLabelMissingMuxTrust/encrypted
events|internal/docker/events.go|stream|./tests/integration|^TestDockerMissingMuxTrust$/^events$/^encrypted$|TestDockerMissingMuxTrust/events/encrypted
startup|internal/manager/manager.go|listRunning|./internal/manager|^TestManagerMissingMuxTrust$/^startup$/^encrypted$|TestManagerMissingMuxTrust/startup/encrypted
infrastructure|internal/manager/manager.go|hasTestInfrastructureLabel|./internal/manager|^TestManagerMissingMuxTrust$/^label$/^encrypted$|TestManagerMissingMuxTrust/label/encrypted
ping|internal/manager/manager.go|Ping|./internal/manager|^TestManagerMissingMuxTrust$/^ping$/^encrypted$|TestManagerMissingMuxTrust/ping/encrypted
connectivity|internal/manager/manager.go|validateDockerConnectivity|./internal/manager|^TestManagerMissingMuxTrust$/^connectivity$/^encrypted$|TestManagerMissingMuxTrust/connectivity/encrypted
CASES

# Selected A uses mux requests, not helper SSH, for forward/cancel. These two
# disposable counterfactuals actually launch and join a helper after the mux
# reports NotDispatched. BatchMode and forwarding suppression remain intact.
for operation in forward cancel; do
  begin_mutation internal/ssh/forward.go
  OPERATION="$operation" python3 - <<'PY'
import os,pathlib
p=pathlib.Path('internal/ssh/forward.go');text=p.read_text()
operation=os.environ['OPERATION'];call={'forward':'Open','cancel':'Close'}[operation]
needle='\teffect, err := connection.'+call+'(ctx, localPort, remotePort)\n'
assert text.count(needle)==1
helper='''\tif effect == ForwardNotDispatched && err != nil {
		host, port, parseErr := ParseHost(connection.master.host)
		if parseErr == nil {
			args := []string{"-o", "BatchMode=yes", "-o", "ClearAllForwardings=yes", "-o", "Tunnel=no",
				"-S", connection.master.controlPath, "-O", "OPERATION", "-L", fmt.Sprintf("%d:localhost:%d", localPort, remotePort)}
			if port != "" { args = append(args, "-p", port) }
			args = append(args, "--", host)
			helper := exec.CommandContext(ctx, "ssh", args...)
			helperErr := helper.Run()
			if helper.ProcessState != nil {
				logger.Info("S12 bad helper original Wait", "pid", helper.ProcessState.Pid(), "state", helper.ProcessState.String(), "error", helperErr)
			}
		}
	}
'''.replace('OPERATION',operation)
assert text.count('\t"log/slog"\n')==1 and '"os/exec"' not in text
text=text.replace('\t"log/slog"\n','\t"log/slog"\n\t"os/exec"\n',1)
p.write_text(text.replace(needle,needle+helper,1))
PY
  if [ "$operation" = forward ]; then
    reason=FORWARD_MUST_NOT_START_SSH_WITHOUT_OWNED_READY_CONNECTION
  else
    reason=CANCEL_MUST_NOT_START_SSH_WITHOUT_OWNED_READY_CONNECTION
  fi
  run_red "$operation-no-helper" ./internal/ssh "^TestControlRequestTrust$/^$operation$/^yes$/^encrypted$" "TestControlRequestTrust/$operation/yes/encrypted" "$reason"
done
git diff --exit-code
git diff --cached --exit-code
printf 'All fourteen source-bound red controls and restored greens observed; candidate source restored.\n' > "$evidence/result.txt"
