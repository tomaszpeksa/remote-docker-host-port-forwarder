#!/bin/bash
# Owned SSH fixture using the explicitly selected isolated Docker daemon.
set -euo pipefail
umask 077
cd "$(dirname "$0")/.."
root="$PWD/.itests"
name=rdhpf-sshd-stub
# Never adopt legacy state or remove a same-named container to make startup work.
if [ -e "$root" ] || [ -L "$root" ] || [ -e "$PWD/.itests-cleanup-owner" ] || [ -L "$PWD/.itests-cleanup-owner" ]; then echo 'Refusing existing .itests; use its owned itest-down first.' >&2; exit 1; fi
if docker container inspect "$name" >/dev/null 2>&1; then echo 'Refusing existing SSH fixture container.' >&2; exit 1; fi
mkdir "$root"
owner="$(date +%s)-$$"
# Claim before publishing usable ownership. Down refuses the unpublished root;
# once owner exists, every supported operation must respect this same claim.
mkdir "$root/operation"
printf '%s\n' "$owner" > "$root/operation/provisioner"
[ ! -e "$PWD/.itests-cleanup-owner" ] && [ ! -L "$PWD/.itests-cleanup-owner" ] || { echo 'Retirement overlaps startup; retaining unpublished state.' >&2; exit 1; }
printf '%s\n' "$owner" > "$root/owner"
complete=0
uncertain=0
owns_operation() {
  [ -f "$root/owner" ] && [ ! -L "$root/owner" ] &&
    [ "$(cat "$root/owner")" = "$owner" ] &&
    [ -f "$root/operation/provisioner" ] && [ ! -L "$root/operation/provisioner" ] &&
    [ "$(cat "$root/operation/provisioner")" = "$owner" ]
}
finish_provisioning() {
  status=$?
  trap - EXIT
  if ! owns_operation; then
    echo 'Startup ownership changed; refusing stale writes or cleanup.' >&2
    exit 1
  fi
  if [ "$uncertain" = 1 ] || [ "$status" -ge 128 ]; then
    echo 'Provisioning outcome uncertain; retaining ownership and operation claim.' >&2
    exit "$status"
  fi
  if [ "$complete" = 1 ]; then
    rm "$root/operation/provisioner"
    rmdir "$root/operation"
    echo 'Owned fixture ready. Run make itest; then make itest-down.'
  else
    # Foreground commands have joined. Transfer the existing claim without an
    # unlock window; down validates this private handoff against our PID/token.
    _RDHPF_ITEST_PROVISION_OWNER="$owner" bash scripts/itest-down.sh || {
      echo 'Failed-start cleanup incomplete; retained fixture state.' >&2
    }
  fi
  exit "$status"
}
trap finish_provisioning EXIT
trap 'uncertain=1; exit 129' HUP
trap 'uncertain=1; exit 130' INT
trap 'uncertain=1; exit 143' TERM
mkdir -p "$root/home/.ssh" "$root/bin"
ssh_executable=$(command -v ssh)
case "$ssh_executable" in /*) ;; *) echo 'SSH executable must be absolute' >&2; exit 1 ;; esac
ssh-keygen -q -t ed25519 -N '' -f "$root/home/.ssh/id_ed25519"

docker build -t rdhpf-sshd-stub -f docker/sshd-stub-dind/Dockerfile docker/sshd-stub-dind/
# The held claim binds this redirection to our generation; never truncate a
# newer fixture's identity after an obsolete startup resumes.
owns_operation
docker create --name "$name" --label "rdhpf.itest=$owner" --label rdhpf.test-infrastructure=true \
  -p 127.0.0.1:2222:22 -v /var/run/docker.sock:/var/run/docker.sock rdhpf-sshd-stub > "$root/container-id"
id=$(cat "$root/container-id")
docker start "$id" >/dev/null
for attempt in {1..50}; do
  if docker exec "$id" test -s /etc/ssh/ssh_host_ed25519_key.pub; then break; fi
  sleep .1
done
docker exec "$id" /usr/sbin/sshd -T | grep -qx 'persourcepenalties no'
docker cp "$root/home/.ssh/id_ed25519.pub" "$id:/tmp/rdhpf-authorized-key"
docker exec "$id" sh -ec 'cat /tmp/rdhpf-authorized-key > /home/testuser/.ssh/authorized_keys; chmod 600 /home/testuser/.ssh/authorized_keys; chown testuser:testuser /home/testuser/.ssh/authorized_keys; rm /tmp/rdhpf-authorized-key'
# Public key obtained through the retained container, never unauthenticated keyscan.
owns_operation
docker exec "$id" cat /etc/ssh/ssh_host_ed25519_key.pub > "$root/host-key.pub"
awk '{print "[localhost]:2222 " $1 " " $2; print "[127.0.0.1]:2222 " $1 " " $2}' "$root/host-key.pub" > "$root/home/.ssh/known_hosts"
: > "$root/home/.ssh/global_known_hosts"
cat > "$root/home/.ssh/config" <<EOF
Host localhost 127.0.0.1
  User testuser
  Port 2222
  IdentityFile "$root/home/.ssh/id_ed25519"
  IdentitiesOnly yes
  IdentityAgent none
  StrictHostKeyChecking yes
  UserKnownHostsFile "$root/home/.ssh/known_hosts"
  GlobalKnownHostsFile "$root/home/.ssh/global_known_hosts"
  LogLevel ERROR
EOF
# %q is Bash quoting. The launcher adds only -F, never policy under test.
printf '#!/bin/bash\nexec %q -F %q "$@"\n' "$ssh_executable" "$root/home/.ssh/config" > "$root/bin/ssh"
chmod 700 "$root/bin/ssh"
"$root/bin/ssh" -o BatchMode=yes -o ConnectTimeout=5 -- testuser@localhost docker info >/dev/null
# Keep native package/runtime/API provenance public and separate from key material.
owns_operation
{
  docker image inspect rdhpf-sshd-stub --format '{{.Id}}'
  docker exec "$id" apk info -v
  docker exec "$id" /usr/sbin/sshd -V
  docker exec "$id" /usr/sbin/sshd -T | grep -E '^(persourcepenalties|passwordauthentication|pubkeyauthentication|kbdinteractiveauthentication|permitrootlogin) '
  docker exec "$id" docker version
} > "$root/provenance.txt" 2>&1
complete=1
