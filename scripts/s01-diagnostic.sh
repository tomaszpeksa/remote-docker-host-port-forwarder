#!/usr/bin/env bash
set -euo pipefail

BASE=8aa0a309ae8d10cbd94252f02683f61498734513
CHANGED=905609d772f1e42da7fed2fa2f8023169df86129
ROOT=$(pwd)
OUT="$RUNNER_TEMP/s01-diagnostic-evidence"
WORK="$RUNNER_TEMP/s01-diagnostic-work"
mkdir -p "$OUT" "$WORK"
export GOTOOLCHAIN=local
export SSH_TEST_HOST=ssh://testuser@localhost:2222
export SSH_TEST_KEY_PATH="$WORK/id_test"
CONTROL="/tmp/rdhpf-$(printf %s "$SSH_TEST_HOST" | sha256sum | cut -c1-16).sock"
OWNER="s01-${GITHUB_RUN_ID}-${GITHUB_RUN_ATTEMPT}"
fixture=""
cleanup_pair() {
  timeout 5s ssh -S "$CONTROL" -O exit -p 2222 testuser@localhost >/dev/null 2>&1 || true
  docker ps -aq --filter "label=rdhpf.s01-diagnostic=$OWNER" | xargs -r docker rm -f || true
  # Only this test runs on the fresh hosted runner; these are its watchdog pings.
  docker ps -aq --filter label=rdhpf.health-check=true | xargs -r docker rm -f || true
  rm -f "$CONTROL"
}
trap cleanup_pair EXIT

{
  printf 'instrumentation_commit='; git rev-parse HEAD
  printf 'base=%s\nchanged=%s\n' "$BASE" "$CHANGED"
  go version
  go env GOOS GOARCH GOTOOLCHAIN
  docker version
  uname -a
} > "$OUT/identity.txt"
# Derive one identical instrumentation overlay, separate from either source SHA.
git diff "$CHANGED" HEAD -- tests/integration/end_to_end_test.go > "$WORK/test-overlay.patch"
cp "$WORK/test-overlay.patch" "$OUT/"
cp tests/integration/s01_diagnostic_test.go "$OUT/"
sha256sum "$WORK/test-overlay.patch" tests/integration/s01_diagnostic_test.go docker/sshd-stub-dind/entrypoint.sh > "$OUT/instrumentation-sha256.txt"

for variant in base changed; do
  sha=$BASE
  if [ "$variant" = changed ]; then sha=$CHANGED; fi
  code="$WORK/$variant"
  mkdir -p "$code"
  git archive "$sha" | tar -x -C "$code"
  (cd "$code" && git apply "$WORK/test-overlay.patch")
  cp tests/integration/s01_diagnostic_test.go "$code/tests/integration/"
  printf '%s %s\n' "$variant" "$sha" >> "$OUT/code-shas.txt"
  (cd "$code" && sha256sum tests/integration/end_to_end_test.go tests/integration/s01_diagnostic_test.go) > "$OUT/$variant-instrumented-tests.sha256"
  (cd "$code" && go test -c -o "$WORK/$variant.test" ./tests/integration) > "$OUT/$variant-compile.log" 2>&1
  sha256sum "$WORK/$variant.test" >> "$OUT/binaries.sha256"
done
cmp "$OUT/base-instrumented-tests.sha256" "$OUT/changed-instrumented-tests.sha256"

docker build --no-cache -t s01-diagnostic-ssh -f docker/sshd-stub-dind/Dockerfile docker/sshd-stub-dind/ > "$OUT/image-build.log" 2>&1
SSH_IMAGE=$(docker image inspect --format '{{.Id}}' s01-diagnostic-ssh)
docker pull nginx:alpine > "$OUT/nginx-pull.log" 2>&1
NGINX_IMAGE=$(docker image inspect --format '{{.Id}}' nginx:alpine)
docker image inspect --format '{{.Id}} {{json .RepoDigests}}' "$SSH_IMAGE" nginx:alpine > "$OUT/images.txt"
ssh-keygen -q -t ed25519 -f "$SSH_TEST_KEY_PATH" -N '' -C s01-diagnostic

failed=0
for variant in base changed; do
  cleanup_pair
  fixture="s01-${variant}-${GITHUB_RUN_ID}"
  export S01_DIAGNOSTIC_OWNER="$OWNER"
  export S01_DIAGNOSTIC_FIXTURE="$fixture"
  [ "$(docker image inspect --format '{{.Id}}' nginx:alpine)" = "$NGINX_IMAGE" ]
  docker run -d --name "$fixture" -p 2222:22 \
    -v /var/run/docker.sock:/var/run/docker.sock \
    --label rdhpf.test-infrastructure=true \
    --label "rdhpf.s01-diagnostic=$OWNER" "$SSH_IMAGE" > "$OUT/$variant-fixture-id.txt"
  # Infrastructure startup only; no application exists during this wait.
  sleep 4
  docker cp "$SSH_TEST_KEY_PATH.pub" "$fixture:/tmp/pubkey"
  docker exec "$fixture" sh -c 'mv /tmp/pubkey /home/testuser/.ssh/authorized_keys; chmod 600 /home/testuser/.ssh/authorized_keys; chown testuser:testuser /home/testuser/.ssh/authorized_keys'
  ssh -i "$SSH_TEST_KEY_PATH" -o BatchMode=yes -o StrictHostKeyChecking=no -o UserKnownHostsFile=/dev/null -p 2222 testuser@localhost docker info > "$OUT/$variant-preflight.log" 2>&1
  docker inspect --format '{{.Id}} image={{.Image}}' "$fixture" >> "$OUT/$variant-preflight.log"
  date -u --iso-8601=ns > "$OUT/$variant-test-start.txt"
  set +e
  (cd "$WORK/$variant" && timeout 185s "$WORK/$variant.test" -test.v -test.count=1 -test.timeout=3m -test.run '^TestManager_ContainerWithOnePort$') > "$OUT/$variant-test.log" 2>&1
  rc=$?
  set -e
  printf '%s source=%s exit=%s\n' "$variant" "$(grep "^$variant " "$OUT/code-shas.txt" | cut -d' ' -f2)" "$rc" | tee -a "$OUT/results.txt"
  if [ "$rc" != 0 ]; then failed=1; fi
  docker logs --timestamps "$fixture" > "$OUT/$variant-sshd.log" 2>&1 || true
  docker ps -a --filter "label=rdhpf.s01-diagnostic=$OWNER" > "$OUT/$variant-owned-resources.txt"
  cleanup_pair
  docker ps -a --filter "label=rdhpf.s01-diagnostic=$OWNER" > "$OUT/$variant-after-cleanup.txt"
done
exit "$failed"
