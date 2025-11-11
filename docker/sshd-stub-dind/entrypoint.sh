#!/bin/sh
# Entrypoint script to configure localhost to point to Docker gateway
# so SSH port forwards can reach host's published Docker ports

# Fix docker group GID to match the mounted socket's GID
if [ -e /var/run/docker.sock ]; then
  SOCK_GID=$(stat -c '%g' /var/run/docker.sock)
  echo "Adjusting docker group from GID 970 to $SOCK_GID to match socket"
  delgroup docker 2>/dev/null || true
  addgroup -g "$SOCK_GID" docker
  addgroup testuser docker
fi

# Get Docker bridge gateway IP (where host ports are published)
GATEWAY=$(ip route show default | awk '/default/ {print $3}')

# Replace localhost entries (both IPv4 and IPv6) to point to gateway
# We need to remove IPv6 localhost to prevent it from being preferred
cp /etc/hosts /tmp/hosts.new
sed "s/^127.0.0.1[[:space:]]*localhost.*/$GATEWAY localhost/" /tmp/hosts.new | \
  sed "/^::1[[:space:]]*localhost/d" > /etc/hosts.tmp
cat /etc/hosts.tmp > /etc/hosts
rm /tmp/hosts.new /etc/hosts.tmp

echo "Configured localhost -> $GATEWAY for SSH port forwarding (removed IPv6 entry)"

# Start sshd
exec /usr/sbin/sshd -D -e