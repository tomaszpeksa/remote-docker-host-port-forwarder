#!/bin/bash
# Owned SSH fixture using the explicitly selected isolated Docker daemon.
set -euo pipefail
umask 077
cd "$(dirname "$0")/.."
root="$PWD/.itests"
name=rdhpf-sshd-stub
# Never adopt legacy state or remove a same-named container to make startup work.
if [ -e "$root" ]; then echo 'Refusing existing .itests; use its owned itest-down first.' >&2; exit 1; fi
if docker container inspect "$name" >/dev/null 2>&1; then echo 'Refusing existing SSH fixture container.' >&2; exit 1; fi
mkdir "$root"
owner="$(date +%s)-$$"
printf '%s\n' "$owner" > "$root/owner"
complete=0
trap 'if [ "$complete" != 1 ]; then bash scripts/itest-down.sh; fi' EXIT
mkdir -p "$root/home/.ssh" "$root/bin"
ssh_executable=$(command -v ssh)
case "$ssh_executable" in /*) ;; *) echo 'SSH executable must be absolute' >&2; exit 1 ;; esac
ssh-keygen -q -t ed25519 -N '' -f "$root/home/.ssh/id_ed25519"

docker build -t rdhpf-sshd-stub -f docker/sshd-stub-dind/Dockerfile docker/sshd-stub-dind/
# create publishes the ID before start, so cleanup owns even failed startup.
docker create --name "$name" --label "rdhpf.itest=$owner" --label rdhpf.test-infrastructure=true \
  -p 127.0.0.1:2222:22 -v /var/run/docker.sock:/var/run/docker.sock rdhpf-sshd-stub > "$root/container-id"
id=$(cat "$root/container-id")
docker start "$id" >/dev/null
for attempt in {1..50}; do
  if docker exec "$id" test -s /etc/ssh/ssh_host_ed25519_key.pub; then break; fi
  sleep .1
done
docker cp "$root/home/.ssh/id_ed25519.pub" "$id:/tmp/rdhpf-authorized-key"
docker exec "$id" sh -ec 'cat /tmp/rdhpf-authorized-key > /home/testuser/.ssh/authorized_keys; chmod 600 /home/testuser/.ssh/authorized_keys; chown testuser:testuser /home/testuser/.ssh/authorized_keys; rm /tmp/rdhpf-authorized-key'
# Public key obtained through the retained container, never unauthenticated keyscan.
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
{
  docker image inspect rdhpf-sshd-stub --format '{{.Id}}'
  docker exec "$id" apk info -v
  docker exec "$id" /usr/sbin/sshd -V
  docker exec "$id" docker version
} > "$root/provenance.txt" 2>&1
complete=1
echo 'Owned fixture ready. Run make itest; then make itest-down.'
