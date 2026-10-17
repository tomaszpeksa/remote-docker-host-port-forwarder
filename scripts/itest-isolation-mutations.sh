#!/bin/bash
# Named behavioral controls on actual source; exact restoration precedes green.
set -euo pipefail
cd "$(dirname "$0")/.."
scripts/check-tools.sh go jq python3
controls='flock publication-join private-path'
if [ "${1:-}" = --native ]; then
  controls='publication-join private-path'
else
  test "${GITHUB_ACTIONS:-}" = true
  test "${RDHPF_HOSTED_ISOLATION:-}" = 1
  test "${RDHPF_HOSTED_TRUST:-}" = 1
  test "${SSH_TEST_HOST:-}" = ssh://testuser@localhost:2222
fi
git diff --exit-code
base_evidence=${RDHPF_ISOLATION_MUTATION_EVIDENCE:-$PWD/test-evidence/isolation-mutations}
mkdir -p "$base_evidence"
git rev-parse HEAD HEAD^{tree} > "$base_evidence/candidate.txt"
source_file=
restore() { if [ -n "$source_file" ]; then cp "$case_dir/original.txt" "$source_file"; fi; }
trap restore EXIT
for control in $controls; do
  case_dir="$base_evidence/$control"; mkdir -p "$case_dir"
  case "$control" in
    flock)
      source_file=internal/instancepath/owner_unix.go
      package=./tests/integration; name=TestOwnedCLIIsolation
      marker=ISOLATION_LOSER_BEFORE_SSH ;;
    publication-join)
      source_file=internal/manager/publication.go
      package=./internal/manager; name=TestPublicationJoinsFinalWriterBeforeCleanup
      marker='cleanup/admission release preceded final publication|successor admitted while final publication was blocked' ;;
    private-path)
      source_file=internal/ssh/namespace.go
      package=./internal/ssh; name=TestMasterPrivatePathNativeBind
      marker=ISOLATION_PRIVATE_PATH_CONSTRUCTION ;;
  esac
  git show "HEAD:$source_file" > "$case_dir/original.txt"
  cmp "$case_dir/original.txt" "$source_file"
  CONTROL="$control" FILE="$source_file" python3 - <<'PY'
import os
from pathlib import Path
p = Path(os.environ['FILE'])
s = p.read_text()
old, new = {
    'flock': ('if err = unix.Flock(int(file.Fd()), unix.LOCK_EX|unix.LOCK_NB); err != nil {', 'if err = nil; err != nil {'),
    'publication-join': ('\t\t<-writerDone\n', ''),
    'private-path': ('filepath.Join(dir, "mux")', 'filepath.Join(dir, "' + 'm' * 80 + '")'),
}[os.environ['CONTROL']]
assert s.count(old) == 1
p.write_text(s.replace(old, new, 1))
PY
  git diff -- "$source_file" > "$case_dir/mutation.diff"
  shasum -a 256 "$source_file" > "$case_dir/mutant.sha256"
  for phase in red restored-green; do
    if [ "$phase" = restored-green ]; then
      restore
      cmp "$case_dir/original.txt" "$source_file"
      shasum -a 256 "$source_file" > "$case_dir/restored.sha256"
      git diff --exit-code
    fi
    mkdir -p "$case_dir/$phase"
    if [ "$control" = flock ]; then
      export RDHPF_ISOLATION_BINARY="$case_dir/$phase/rdhpf"
      export RDHPF_ISOLATION_EVIDENCE="$case_dir/$phase/runtime"
      go build -o "$RDHPF_ISOLATION_BINARY" ./cmd/rdhpf
      go version -m "$RDHPF_ISOLATION_BINARY" > "$case_dir/$phase/binary.txt"
      shasum -a 256 "$RDHPF_ISOLATION_BINARY" >> "$case_dir/$phase/binary.txt"
    fi
    printf 'go test -tags=rdhpf_integration -json -count=1 -timeout=3m -run ^%s$ %s\n' "$name" "$package" > "$case_dir/$phase.command"
    set +e
    go test -tags=rdhpf_integration -json -count=1 -timeout=3m -run "^$name$" "$package" > "$case_dir/$phase.jsonl" 2> "$case_dir/$phase.stderr"
    status=$?
    set -e
    printf '%s\n' "$status" > "$case_dir/$phase.exit"
    if [ "$phase" = red ]; then
      test "$status" -eq 1
      jq -e -s --arg name "$name" --arg marker "$marker" '
        any(.[]; .Action == "fail" and .Test == $name) and
        any(.[]; ((.Test // "") | startswith($name)) and ((.Output // "") | test($marker))) and
        (any(.[]; (.Output // "") | test("test timed out|outer deadline|ISOLATION_CLEANUP_UNCERTAIN|context deadline exceeded")) | not)
      ' "$case_dir/$phase.jsonl"
    else
      test "$status" -eq 0
      jq -e -s --arg name "$name" 'any(.[]; .Action == "pass" and .Test == $name)' "$case_dir/$phase.jsonl"
    fi
  done
  source_file=
done
printf 'Named behavioral reds and exact-restored-source greens: %s\n' "$controls" > "$base_evidence/result.txt"
