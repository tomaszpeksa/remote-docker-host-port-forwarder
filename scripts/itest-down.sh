#!/bin/bash
# Retire only this fixture, with no live/uncertain Make publisher invocation.
set -euo pipefail
umask 077
shopt -s nullglob dotglob
cd "$(dirname "$0")/.."
root="$PWD/.itests"
receipt="$PWD/.itests-cleanup-owner"
[ -e "$root" ] || [ -L "$root" ] || [ -e "$receipt" ] || [ -L "$receipt" ] || exit 0
[ ! -L "$root" ] && [ ! -L "$receipt" ] || exit 1
if [ -f "$root/owner" ] && [ ! -L "$root/owner" ]; then
  owner=$(cat "$root/owner")
elif [ -f "$receipt" ]; then
  owner=$(cat "$receipt")
else
  echo 'Refusing unowned/legacy .itests state.' >&2; exit 1
fi
[[ "$owner" =~ ^[1-9][0-9]*-[1-9][0-9]*$ ]] || { echo 'Refusing malformed fixture owner.' >&2; exit 1; }
if [ -e "$receipt" ]; then
  [ -f "$receipt" ] && [ "$(cat "$receipt")" = "$owner" ] || exit 1
fi
# A receipt survives failure of the FINAL rmdir, including when owner was removed.
if [ ! -e "$root" ]; then rm "$receipt"; exit 0; fi
mkdir "$root/operation" 2>/dev/null || { echo 'Fixture operation active or uncertain; refusing teardown.' >&2; exit 1; }
claimed=1
trap 'if [ "$claimed" = 1 ]; then rmdir "$root/operation"; fi' EXIT
# Explicit provisioned entries only; never recursively delete this root.
for entry in "$root"/* "$root/bin"/* "$root/home"/* "$root/home/.ssh"/*; do
  case "$entry" in
    "$root/bin"|"$root/home"|"$root/home/.ssh"|"$root/operation")
      [ -d "$entry" ] && [ ! -L "$entry" ] || exit 1;;
    "$root/owner"|"$root/container-id"|"$root/container-removed"|"$root/host-key.pub"|"$root/provenance.txt"|"$root/bin/ssh"|"$root/home/.ssh/id_ed25519"|"$root/home/.ssh/id_ed25519.pub"|"$root/home/.ssh/config"|"$root/home/.ssh/known_hosts"|"$root/home/.ssh/global_known_hosts")
      [ -f "$entry" ] && [ ! -L "$entry" ] || exit 1;;
    *) echo "Refusing unexpected fixture entry: $entry" >&2; exit 1;;
  esac
done
present=0
if [ -s "$root/container-id" ]; then
  id=$(cat "$root/container-id")
  [[ "$id" =~ ^[a-f0-9]{64}$ ]] || exit 1
  if actual=$(docker inspect --format '{{index .Config.Labels "rdhpf.itest"}}' "$id"); then
    [ "$actual" = "$owner" ] || { echo 'Refusing foreign fixture container.' >&2; exit 1; }
    present=1
  else
    # Failure alone does not establish absence: require a successful daemon
    # inventory query. This also recovers a stop between rm and receipt rename.
    remaining=$(docker ps --all --quiet --no-trunc --filter "id=$id")
    [ -z "$remaining" ] || { echo 'Cannot verify fixture container absence.' >&2; exit 1; }
  fi
fi
if [ ! -e "$receipt" ]; then (set -C; printf '%s\n' "$owner" > "$receipt"); fi
if [ -s "$root/container-id" ]; then
  if [ "$present" = 1 ]; then docker rm -f "$id" >/dev/null; fi
  mv "$root/container-id" "$root/container-removed"
fi
rm -f "$root/host-key.pub" "$root/provenance.txt" \
  "$root/bin/ssh" "$root/home/.ssh/id_ed25519" "$root/home/.ssh/id_ed25519.pub" \
  "$root/home/.ssh/config" "$root/home/.ssh/known_hosts" "$root/home/.ssh/global_known_hosts"
for directory in "$root/bin" "$root/home/.ssh" "$root/home"; do
  if [ -e "$directory" ]; then rmdir "$directory"; fi
done
rm -f "$root/container-id" "$root/container-removed" "$root/owner"
rmdir "$root/operation"
claimed=0
rmdir "$root"
rm "$receipt"
echo 'Owned SSH fixture and private keys removed.'
