#!/bin/bash
# Start integration test harness with Docker-in-Docker SSH server
set -e

CONTAINER_NAME="rdhpf-sshd-stub"
SSH_PORT="2222"

echo "🚀 Starting integration test harness (SSH + Docker socket mount)..."

# Build sshd-stub-dind image
echo "📦 Building SSH test container image..."
docker build -t rdhpf-sshd-stub -f docker/sshd-stub-dind/Dockerfile docker/sshd-stub-dind/ || {
    echo "❌ Failed to build Docker image"
    exit 1
}

# Generate test SSH key if needed
mkdir -p .itests/home/.ssh
if [ ! -f .itests/home/.ssh/id_ed25519 ]; then
    echo "🔑 Generating test SSH key..."
    ssh-keygen -t ed25519 -f .itests/home/.ssh/id_ed25519 -N "" -C "rdhpf-itest"
fi

# Start container with Docker socket mount and bridge networking
# Bridge networking allows SSH to forward to Docker bridge gateway (host's published ports)
echo "🐳 Starting SSH container with Docker socket mount..."
docker run -d --name "$CONTAINER_NAME" \
  -p "$SSH_PORT:22" \
  -v /var/run/docker.sock:/var/run/docker.sock \
  --label rdhpf.test-infrastructure=true \
  rdhpf-sshd-stub || {
    echo "❌ Failed to start container"
    exit 1
}

# Wait for SSH to be ready (no Docker daemon to wait for)
echo "⏳ Waiting for SSH server to start..."
sleep 3

# Verify Docker CLI can access host daemon
echo "⏳ Verifying Docker access via socket..."
if ! docker exec "$CONTAINER_NAME" docker info >/dev/null 2>&1; then
  echo "❌ Docker CLI cannot access host daemon"
  docker logs "$CONTAINER_NAME"
  exit 1
fi
echo "✅ Docker CLI ready (using host daemon)"


# Copy public key to container for passwordless auth
echo "🔐 Setting up passwordless SSH..."
docker cp .itests/home/.ssh/id_ed25519.pub "$CONTAINER_NAME:/tmp/pubkey"
docker exec "$CONTAINER_NAME" sh -c "
  mv /tmp/pubkey /home/testuser/.ssh/authorized_keys && \
  chmod 600 /home/testuser/.ssh/authorized_keys && \
  chown testuser:testuser /home/testuser/.ssh/authorized_keys && \
  ls -la /home/testuser/.ssh/authorized_keys
"

# Create SSH config for tests
cat > .itests/home/.ssh/config <<EOF
Host localhost
  IdentityFile $(pwd)/.itests/home/.ssh/id_ed25519
  StrictHostKeyChecking no
  UserKnownHostsFile /dev/null
  GlobalKnownHostsFile /dev/null
  LogLevel ERROR
  Port 2222
EOF
chmod 600 .itests/home/.ssh/config

# Test Docker access over SSH
echo "🔍 Testing Docker access over SSH..."
ssh -i .itests/home/.ssh/id_ed25519 \
  -o StrictHostKeyChecking=no \
  -o UserKnownHostsFile=/dev/null \
  -o LogLevel=ERROR \
  -p "$SSH_PORT" testuser@localhost docker info || {
    echo "❌ Docker access over SSH failed"
    exit 1
}

echo ""
echo "✅ Integration test harness is ready!"
echo "   - SSH server accessible on port $SSH_PORT"
echo "   - Docker CLI using host daemon (via socket mount)"
echo "   - No iptables issues (no nested Docker daemon)"
echo ""
echo "Run tests with:"
echo "  make itest"
echo ""
echo "Or manually:"
echo "  HOME=$(pwd)/.itests/home \\"
echo "  SSH_TEST_HOST=ssh://testuser@localhost:$SSH_PORT \\"
echo "  SSH_TEST_KEY_PATH=$(pwd)/.itests/home/.ssh/id_ed25519 \\"
echo "  go test -v ./tests/integration/..."
echo ""
echo "Stop harness with:"
echo "  make itest-down"