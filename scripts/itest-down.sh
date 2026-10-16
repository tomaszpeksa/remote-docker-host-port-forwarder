#!/bin/bash
# Retire only this fixture, with no live/uncertain Make publisher invocation.
set -euo pipefail
umask 077
shopt -s nullglob dotglob
cd "$(dirname "${BASH_SOURCE[0]}")/.."
root="$PWD/.itests"
receipt="$PWD/.itests-cleanup-owner"
# Private startup-to-cleanup handoff, not a public option or stale-claim recovery.
# Only the initiating live parent may transfer its existing operation claim.
provision_owner=${_RDHPF_ITEST_PROVISION_OWNER:-}
if [ -n "$provision_owner" ]; then
  if ! [[ "$provision_owner" =~ ^[1-9][0-9]*-[1-9][0-9]*$ ]] ||
    [ "${provision_owner##*-}" != "$PPID" ] ||
    [ ! -f "$root/owner" ] || [ -L "$root/owner" ] ||
    [ "$(cat "$root/owner")" != "$provision_owner" ] ||
    [ ! -f "$root/operation/provisioner" ] || [ -L "$root/operation/provisioner" ] ||
    [ "$(cat "$root/operation/provisioner")" != "$provision_owner" ]; then
    echo 'Refusing stale or unowned provisioning handoff.' >&2
    exit 1
  fi
fi
[ -e "$root" ] || [ -L "$root" ] || [ -e "$receipt" ] || [ -L "$receipt" ] || exit 0
[ ! -L "$root" ] && [ ! -L "$receipt" ] || exit 1
receipt_value=''
receipt_owner=''
removed_id=''
if [ -e "$receipt" ]; then
  [ -f "$receipt" ] || exit 1
  receipt_value=$(cat "$receipt"; printf '.')
  receipt_value=${receipt_value%.} # Preserve final newlines for exact field validation.
  receipt_owner=${receipt_value%%$'\n'*}
  if [ "$receipt_value" != "$receipt_owner"$'\n' ]; then
    prefix="$receipt_owner"$'\nremoved='
    removed_id=${receipt_value#"$prefix"}
    removed_id=${removed_id%$'\n'}
    [[ "$receipt_value" = "$prefix$removed_id"$'\n' && "$removed_id" =~ ^[a-f0-9]{64}$ ]] || exit 1
  fi
fi
if [ -f "$root/owner" ] && [ ! -L "$root/owner" ]; then
  owner=$(cat "$root/owner")
elif [ -f "$receipt" ]; then
  owner=$receipt_owner
else
  echo 'Refusing unowned/legacy .itests state.' >&2; exit 1
fi
[[ "$owner" =~ ^[1-9][0-9]*-[1-9][0-9]*$ ]] || { echo 'Refusing malformed fixture owner.' >&2; exit 1; }
if [ -e "$receipt" ]; then
  [ "$receipt_owner" = "$owner" ] || exit 1
fi
# A receipt survives failure of the FINAL rmdir, including when owner was removed.
if [ ! -e "$root" ]; then rm "$receipt"; exit 0; fi
if [ -z "$provision_owner" ]; then
  mkdir "$root/operation" 2>/dev/null || { echo 'Fixture operation active or uncertain; refusing teardown.' >&2; exit 1; }
else
  [ "$owner" = "$provision_owner" ] || exit 1
  rm "$root/operation/provisioner"
fi
claimed=1
trap 'if [ "$claimed" = 1 ]; then rm -f "$root/operation/receipt"; rmdir "$root/operation"; fi' EXIT
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
# Redirection creates this file before docker create runs. Empty output may
# mean an interrupted response after creation, never verified absence.
if [ -e "$root/container-id" ] && [ ! -s "$root/container-id" ]; then
  echo 'Container creation outcome uncertain; retaining fixture ownership.' >&2
  exit 1
fi
if [ -e "$root/container-removed" ]; then
  previous_id=$(cat "$root/container-removed")
  [[ "$previous_id" =~ ^[a-f0-9]{64}$ ]] || exit 1
  [ -z "$removed_id" ] || [ "$previous_id" = "$removed_id" ] || exit 1
  [ ! -e "$root/container-id" ] || exit 1
  if [ -z "$removed_id" ]; then
    printf '%s\nremoved=%s\n' "$owner" "$previous_id" > "$root/operation/receipt"
    mv "$root/operation/receipt" "$receipt"
  fi
fi
present=0
if [ -s "$root/container-id" ]; then
  id=$(cat "$root/container-id")
  [[ "$id" =~ ^[a-f0-9]{64}$ ]] || exit 1
  [ -z "$removed_id" ] || [ "$id" = "$removed_id" ] || exit 1
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
  printf '%s\nremoved=%s\n' "$owner" "$id" > "$root/operation/receipt"
  mv "$root/operation/receipt" "$receipt"
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
