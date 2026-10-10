#!/usr/bin/env bash
# Diagnostic-only orchestration for one hosted traffic observation.
set -euo pipefail

OUT="$RUNNER_TEMP/traffic-diagnostic-evidence"
mkdir -p "$OUT"

case "${1:?expected prepare, phase or collect-cleanup}" in
  prepare)
    {
      echo "base=59e7917c366c9dcbb613f04988b142b082476ed8"
      printf 'diagnostic_head='; git rev-parse HEAD
      printf 'tree='; git rev-parse HEAD^{tree}
      echo "run=$GITHUB_RUN_ID attempt=$GITHUB_RUN_ATTEMPT"
      go version
      go env GOOS GOARCH GOTOOLCHAIN
      uname -a
      timeout -k 1s 3s docker version
      ssh -V
    } > "$OUT/identity.txt" 2>&1
    git diff --exit-code 59e7917c366c9dcbb613f04988b142b082476ed8 HEAD -- cmd internal go.mod go.sum > "$OUT/production-unchanged.diff"
    git diff --binary 59e7917c366c9dcbb613f04988b142b082476ed8 HEAD -- > "$OUT/diagnostic.diff"
    git ls-tree -r HEAD > "$OUT/source-tree.txt"
    cat > "$OUT/selection.txt" <<'SELECTION'
ssh: TestSSHMaster (8 top-level tests; dynamic subtests counted separately)
stream: TestDockerEventsStream (2 top-level tests)
one-port: ^TestManager_ContainerWithOnePort$ (1 execution)
All other integration tests/full-suite phase: deliberately not selected.
Missing phase log/exit or missing RUN record: unrun/missing evidence, not pass.
SELECTION
    timeout -k 1s 3s docker ps -a > "$OUT/containers-before.txt"
    ;;
  phase)
    phase=${2:?phase}; test_timeout=${3:?Go timeout}; bound=${4:?outer seconds}; selector=${5:?selector}; expected=${6:?top-level count}
    printf 'phase=%s utc=%s selector=%s count=1 timeout=%s outer=%s\n' "$phase" "$(date -u +%FT%TZ)" "$selector" "$test_timeout" "$bound" > "$OUT/$phase-command.txt"
    set +e
    timeout -k 1s "$bound" go test -v -count=1 -timeout="$test_timeout" -run "$selector" ./tests/integration/ 2>&1 | tee "$OUT/$phase.log"
    exits=("${PIPESTATUS[@]}")
    set -e
    printf 'test_exit=%s tee_exit=%s\n' "${exits[0]}" "${exits[1]}" > "$OUT/$phase-exit.txt"
    if [ "${exits[0]}" -ne 0 ]; then exit "${exits[0]}"; fi
    if [ "${exits[1]}" -ne 0 ]; then exit "${exits[1]}"; fi
    # The existing TestMain can exit zero before running any test.
    actual=$(grep -c '^--- PASS: ' "$OUT/$phase.log" || true)
    if [ "$actual" -ne "$expected" ]; then
      echo "Missing expected executed passes: actual=$actual expected=$expected" | tee "$OUT/$phase-evidence-failure.txt"
      exit 1
    fi
    ;;
  collect-cleanup)
    # Collection failures remain explicit; do not prevent owned cleanup/upload.
    set +e
    timeout -k 1s 3s docker logs --timestamps "$TRAFFIC_DIAGNOSTIC_FIXTURE" > "$OUT/sshd-full.log" 2>&1
    echo "sshd_log_exit=$?" >> "$OUT/collection-exits.txt"
    timeout -k 1s 3s docker inspect --format 'id={{.Id}} image={{.Image}} state={{json .State}} networks={{json .NetworkSettings.Networks}}' "$TRAFFIC_DIAGNOSTIC_FIXTURE" > "$OUT/fixture-final.txt" 2>&1
    echo "fixture_inspect_exit=$?" >> "$OUT/collection-exits.txt"
    timeout -k 1s 3s docker image inspect --format 'id={{.Id}} digests={{json .RepoDigests}}' rdhpf-sshd-stub nginx:alpine alpine:latest > "$OUT/images-final.txt" 2>&1
    echo "image_inspect_exit=$?" >> "$OUT/collection-exits.txt"
    timeout -k 1s 3s docker ps -a > "$OUT/containers-before-cleanup.txt" 2>&1
    echo "container_list_exit=$?" >> "$OUT/collection-exits.txt"
    control="/tmp/rdhpf-$(printf %s "$SSH_TEST_HOST" | sha256sum | cut -c1-16).sock"
    timeout -k 1s 3s ssh -S "$control" -O exit -p 2222 testuser@localhost > "$OUT/control-cleanup.txt" 2>&1
    echo "control_exit=$? (already-closed master may be absent)" >> "$OUT/cleanup-exits.txt"
    ids=$(timeout -k 1s 3s docker ps -aq --filter "label=rdhpf.traffic-diagnostic=$TRAFFIC_DIAGNOSTIC_OWNER")
    list_exit=$?
    echo "owned_list_exit=$list_exit" >> "$OUT/cleanup-exits.txt"
    cleanup_exit=$list_exit
    if [ "$list_exit" -eq 0 ]; then
      for id in $ids; do
        timeout -k 1s 3s docker rm -f "$id" >> "$OUT/container-cleanup.txt" 2>&1
        rc=$?
        echo "remove=$id exit=$rc" >> "$OUT/cleanup-exits.txt"
        if [ "$rc" -ne 0 ]; then cleanup_exit=$rc; fi
      done
    fi
    rm -f "$HOME/.ssh/id_rdhpf_test" "$HOME/.ssh/id_rdhpf_test.pub" "$HOME/.ssh/config" "$control"
    rc=$?
    echo "key_config_socket_remove_exit=$rc" >> "$OUT/cleanup-exits.txt"
    if [ "$rc" -ne 0 ]; then cleanup_exit=$rc; fi
    timeout -k 1s 3s docker ps -a --filter "label=rdhpf.traffic-diagnostic=$TRAFFIC_DIAGNOSTIC_OWNER" > "$OUT/owned-after-cleanup.txt" 2>&1
    rc=$?
    echo "verify_owned_list_exit=$rc" >> "$OUT/cleanup-exits.txt"
    if [ "$rc" -ne 0 ]; then cleanup_exit=$rc; fi
    remaining=$(timeout -k 1s 3s docker ps -aq --filter "label=rdhpf.traffic-diagnostic=$TRAFFIC_DIAGNOSTIC_OWNER")
    rc=$?
    if [ "$rc" -ne 0 ] || [ -n "$remaining" ]; then cleanup_exit=1; fi
    timeout -k 1s 3s docker ps -a > "$OUT/all-after-cleanup.txt" 2>&1
    echo "all_after_list_exit=$?" >> "$OUT/cleanup-exits.txt"
    printf 'cleanup_exit=%s\n' "$cleanup_exit" >> "$OUT/cleanup-exits.txt"
    exit "$cleanup_exit"
    ;;
  *) echo "Unknown diagnostic action" >&2; exit 2 ;;
esac
