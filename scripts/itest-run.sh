#!/bin/bash
# Only the documented Make test modes may use this disposable runtime HOME.
set -euo pipefail
umask 077
cd "$(dirname "$0")/.."
root="$PWD/.itests"
mode=${1:-}
case "$mode" in
  itest) budget=15m;;
  stability) budget=10m;;
  *) echo 'Expected itest or stability mode.' >&2; exit 1;;
esac
shift
if [ "$mode" = stability ]; then set -- "$@" -run 'TestManager_(LongRunning|TunnelStability)'; fi
[ -f "$root/owner" ] && [ ! -L "$root/owner" ] || exit 1
owner=$(cat "$root/owner")
[[ "$owner" =~ ^[1-9][0-9]*-[1-9][0-9]*$ ]] || exit 1
# Shared with teardown. Claim first, then recheck retirement/ownership before
# starting any publisher; a caller which read owner before retirement cannot win.
mkdir "$root/operation" 2>/dev/null || { echo 'Fixture operation active or uncertain; refusing test start.' >&2; exit 1; }
if [ -e "$PWD/.itests-cleanup-owner" ] || [ ! -f "$root/owner" ] || [ "$(cat "$root/owner")" != "$owner" ]; then
  rmdir "$root/operation"
  echo 'Fixture retiring; refusing test start.' >&2
  exit 1
fi
printf 'run\n' > "$root/operation/role"
printf '%s\n' "$owner" > "$root/operation/owner"
trap 'echo "Incomplete fixture runtime cleanup; retained $root/operation and its runtime HOME. Confirm all owned processes joined before manual recovery." >&2' EXIT
# Resolve Go before changing HOME (which may otherwise redirect mise's shims).
go_executable="$(go env GOROOT)/bin/go"
runtime_home=$(mktemp -d /tmp/rdhpf-itest.XXXXXXXX)
printf '%s\n' "$runtime_home" > "$root/operation/home"
printf '%s\n' "$owner" > "$runtime_home/.fixture-owner"
printf 'RUNTIME_HOME=%s\n' "$runtime_home"
set +e
PATH="$root/bin:$(dirname "$go_executable"):$PATH" HOME="$runtime_home" \
  SSH_TEST_HOST=ssh://testuser@localhost:2222 SSH_TEST_KEY_PATH="$root/home/.ssh/id_ed25519" \
  "$go_executable" test "$@" -count=1 -timeout="$budget" ./tests/integration/...
status=$?
set -e
printf '%s\n' "$status" > "$root/operation/status"
[ "$status" -eq 0 ] || exit "$status"
# Successful tests join owned Managers/children before releasing publisher
# admission. No other caller may enter this private per-run HOME; the operation
# claim prevents a supported Make caller or teardown from overlapping retirement.
[[ "$runtime_home" =~ ^/tmp/rdhpf-itest\.[[:alnum:]]{8}$ ]] || exit 1
[ -d "$runtime_home" ] && [ ! -L "$runtime_home" ] || exit 1
[ "$(cat "$runtime_home/.fixture-owner")" = "$owner" ] || exit 1
rm -rf -- "$runtime_home"
printf 'RUNTIME_HOME_REMOVED_AFTER_JOIN=%s\n' "$runtime_home"
rm "$root/operation/role" "$root/operation/owner" "$root/operation/home" "$root/operation/status"
rmdir "$root/operation"
trap - EXIT
