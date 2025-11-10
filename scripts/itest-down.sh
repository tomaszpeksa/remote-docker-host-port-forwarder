#!/bin/bash
# Stop and cleanup integration test harness
set -e

CONTAINER_NAME="rdhpf-sshd-stub"

echo "🛑 Stopping integration test harness..."

# Stop and remove container
docker stop "$CONTAINER_NAME" 2>/dev/null || true
docker rm "$CONTAINER_NAME" 2>/dev/null || true

echo "✅ Integration test harness stopped"
echo ""
echo "Note: Test SSH keys remain in .itests/ directory"
echo "      (gitignored, safe to keep for future test runs)"