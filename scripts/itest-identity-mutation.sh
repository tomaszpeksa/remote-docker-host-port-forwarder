#!/bin/bash
# One diagnostic source overlay; never confuse its red with candidate execution.
set -euo pipefail
cd "$(dirname "$0")/.."
test "${GITHUB_ACTIONS:-}" = true
test "${RDHPF_HOSTED_TRUST:-}" = 1
test "${SSH_TEST_HOST:-}" = ssh://testuser@localhost:2222
git diff --exit-code
evidence="$PWD/test-evidence/identity-mutation"
mkdir -p "$evidence"
source_file=internal/manager/manager.go
git rev-parse HEAD HEAD^{tree} > "$evidence/candidate.txt"
git show "HEAD:$source_file" > "$evidence/original.go.txt"
restore() { cp "$evidence/original.go.txt" "$source_file"; }
trap restore EXIT
python3 - <<'PY'
from pathlib import Path
p = Path('internal/manager/manager.go')
source = p.read_text()
needle = 'dockerCmd := "docker ps --no-trunc --format'
assert source.count(needle) == 1
p.write_text(source.replace(needle, 'dockerCmd := "docker ps --format', 1))
PY
git diff -- "$source_file" > "$evidence/without-flag.diff"
sha256sum "$source_file" > "$evidence/mutant.sha256"
run_case() {
  local name=$1
  printf '%s\n' "go test -tags=rdhpf_integration -json -count=1 -timeout=90s -run '^TestManagerPreexistingContainerIdentity$' ./tests/integration" > "$evidence/$name.command"
  set +e
  go test -tags=rdhpf_integration -json -count=1 -timeout=90s -run '^TestManagerPreexistingContainerIdentity$' ./tests/integration 2> "$evidence/$name.stderr" | tee "$evidence/$name.jsonl"
  local status=("${PIPESTATUS[@]}")
  set -e
  printf 'go=%s tee=%s\n' "${status[0]}" "${status[1]}" > "$evidence/$name.exit"
  test "${status[1]}" -eq 0
  if [ "$name" = red ]; then
    test "${status[0]}" -eq 1
    jq -e -s '
      any(.[]; .Action == "fail" and .Test == "TestManagerPreexistingContainerIdentity") and
      any(.[]; .Test == "TestManagerPreexistingContainerIdentity" and ((.Output // "") | contains("IDENTITY_FULL_STARTUP_OWNER"))) and
      (any(.[]; (.Output // "") | test("test timed out|deadline|did not join|did not finish Wait")) | not)
    ' "$evidence/$name.jsonl"
  else
    test "${status[0]}" -eq 0
    jq -e -s 'any(.[]; .Action == "pass" and .Test == "TestManagerPreexistingContainerIdentity")' "$evidence/$name.jsonl"
  fi
}
run_case red
restore
cmp "$evidence/original.go.txt" "$source_file"
sha256sum "$source_file" > "$evidence/restored.sha256"
git diff --exit-code
run_case restored-green
printf 'One-flag identity red and exact restored-source green observed.\n' > "$evidence/result.txt"
