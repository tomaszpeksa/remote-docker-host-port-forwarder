#!/bin/bash
# Start integration test harness with containerized SSH server + docker shim
set -e

SCENARIO="${SCENARIO:-basic-forward}"
CONTAINER_NAME="rdhpf-sshd-stub"
SSH_PORT="2222"

echo "🚀 Starting integration test harness..."
echo "   Scenario: $SCENARIO"

# Build sshd-stub image
echo "📦 Building sshd-stub Docker image..."
docker build -t rdhpf-sshd-stub -f docker/sshd-stub/Dockerfile tests/integration/harness/ || {
    echo "❌ Failed to build Docker image"
    exit 1
}

# Generate test SSH key if needed
mkdir -p .itests/home/.ssh
if [ ! -f .itests/home/.ssh/id_ed25519 ]; then
    echo "🔑 Generating test SSH key..."
    ssh-keygen -t ed25519 -f .itests/home/.ssh/id_ed25519 -N "" -C "rdhpf-itest"
fi

# Start container
echo "🐳 Starting SSH container..."
docker run -d --name "$CONTAINER_NAME" \
  -p "$SSH_PORT:22" \
  -v "$(pwd)/tests/integration/harness/scenarios/$SCENARIO:/opt/rdhpf-scenarios/default:ro" \
  rdhpf-sshd-stub || {
    echo "❌ Failed to start container"
    exit 1
}

# Wait for SSH to be ready
echo "⏳ Waiting for SSH server..."
sleep 2

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
  LogLevel ERROR
EOF
chmod 600 .itests/home/.ssh/config

echo ""
echo "✅ Integration test harness is ready!"
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