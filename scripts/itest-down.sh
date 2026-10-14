#!/bin/bash
# Remove only the fixture whose retained ID and run label agree.
set -euo pipefail
cd "$(dirname "$0")/.."
root="$PWD/.itests"
[ -e "$root" ] || exit 0
[ -f "$root/owner" ] || { echo 'Refusing unowned/legacy .itests state.' >&2; exit 1; }
owner=$(cat "$root/owner")
[ -n "$owner" ] || exit 1
if [ -s "$root/container-id" ]; then
  id=$(cat "$root/container-id")
  [[ "$id" =~ ^[a-f0-9]{64}$ ]] || exit 1
  actual=$(docker inspect --format '{{index .Config.Labels "rdhpf.itest"}}' "$id")
  [ "$actual" = "$owner" ] || { echo 'Refusing foreign fixture container.' >&2; exit 1; }
  docker rm -f "$id" >/dev/null
fi
# Explicit owned files only; unexpected entries prevent rmdir, not get deleted.
rm -f "$root/container-id" "$root/owner" "$root/host-key.pub" "$root/provenance.txt" \
  "$root/bin/ssh" "$root/home/.ssh/id_ed25519" "$root/home/.ssh/id_ed25519.pub" \
  "$root/home/.ssh/config" "$root/home/.ssh/known_hosts" "$root/home/.ssh/global_known_hosts"
rmdir "$root/bin" "$root/home/.ssh" "$root/home" "$root"
echo 'Owned SSH fixture and private keys removed.'
