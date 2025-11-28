# CI Integration Tests

This document explains how integration tests run in GitHub Actions CI and how they validate the application works correctly.

## Overview


Integration tests run automatically on every pull request that modifies Go code. They verify real SSH connections, Docker container port forwarding, and end-to-end functionality using actual system components rather than mocks.

## Test Phases


Tests run in three phases for optimal feedback and debugging:

### Phase 1: SSH Master Tests (~30 seconds)

**Purpose:** Verify SSH ControlMaster connection management works correctly.

**Tests:**

- `TestSSHMaster_OpenAndClose` - Basic lifecycle
- `TestSSHMaster_Check` - Health checks
- `TestSSHMaster_CheckAfterClose` - Proper cleanup
- `TestSSHMaster_MultipleOpenClose` - Reconnection handling
- `TestSSHMaster_ControlPathReleased` - Resource cleanup
- `TestSSHMaster_ConcurrentCheck` - Concurrent operations
- `TestSSHMaster_ContextCancellation` - Cancellation handling
- `TestSSHMaster_InvalidHost` - Error handling

**Why first:** Fast, stable, no Docker dependencies. Catches SSH configuration issues early.

### Phase 2: Stream Persistence Tests (~90 seconds)

**Purpose:** Verify docker events stream stays open and doesn't close prematurely.

**Tests:**

- `TestDockerEventsStreamPersistence` - Verifies stream stays open for 5+ seconds
- `TestDockerEventsStreamReceivesEvents` - Verifies stream receives actual events

**What it validates:**
- Docker events stream doesn't close immediately (<5s)
- Stream receives events when containers start/stop
- Stream can be cancelled cleanly
- **Critical:** Catches the v0.1.4 bug where stream closed after 40ms

**Why this matters:** The v0.1.4 bug caused infinite restart loops because the stream closed immediately after starting, preventing any container detection.

### Phase 2b: E2E Container Test (~45 seconds)

**Purpose:** Verify complete port forwarding lifecycle with a real container.

**Test:**

- `TestManager_ContainerWithOnePort` - Start container → detect event → create forward → verify HTTP works → cleanup

**What it validates:**
- Port forwards are created within 3 seconds
- HTTP connections work through the forwarded port
- Port forwards are cleaned up when container stops

### Phase 3: All Integration Tests (~3-5 minutes)

**Purpose:** Complete validation of all functionality.

**Includes:**

- Multi-port container tests
- Container lifecycle tests
- Connection recovery tests
- Graceful shutdown tests
- Conflict handling tests
- Status reporting tests

## CI Environment Setup

### SSH Container with Docker Socket Mount


GitHub Actions uses a containerized SSH server with access to the **host Docker daemon** via socket mount:

```bash
# SSH server configuration
- Alpine-based SSH server
- Docker socket mounted from host (/var/run/docker.sock)
- Runs on port 2222 (mapped from container port 22)
- Key-based authentication (no passwords)
- Test user: testuser (member of docker group)
```

### Why Docker Socket Mount?


Using the host Docker daemon via socket mount allows us to:
- ✅ **Test real `docker events` stream behavior** - catches stream closing bugs
- ✅ **Validate actual container lifecycle** - start, stop, inspect
- ✅ **Reproduce production bugs** - v0.1.4 stream closing issue
- ✅ **Production-like testing** - same Docker commands as real usage
- ✅ **Simpler setup** - no nested Docker daemon or iptables issues
- ✅ **Faster startup** - no Docker daemon initialization wait

### SSH Key Configuration


```bash
# Generate test SSH key
ssh-keygen -t ed25519 -f ~/.ssh/id_rdhpf_test -N ""

# Copy public key to container
docker cp ~/.ssh/id_rdhpf_test.pub rdhpf-sshd-stub:/tmp/pubkey

# Configure in container
docker exec rdhpf-sshd-stub sh -c "
  mv /tmp/pubkey /home/testuser/.ssh/authorized_keys
  chmod 600 /home/testuser/.ssh/authorized_keys
  chown testuser:testuser /home/testuser/.ssh/authorized_keys
"

# Test connection
ssh -i ~/.ssh/id_rdhpf_test -p 2222 testuser@localhost echo "SSH works"
```

### Environment Variables


```bash
SSH_TEST_HOST=ssh://testuser@localhost:2222
```

**Important:**
- Tests require the `ssh://` prefix in `SSH_TEST_HOST`
- Port 2222 is required (SSH container port mapping)

## Running Integration Tests Locally

### Prerequisites


1. **Docker** installed and running
2. **Go** 1.23+ installed

### Using the Test Harness (Recommended)


The project includes scripts to set up the Docker-in-Docker test environment:

```bash
# Start SSH test container with Docker daemon
./scripts/itest-up.sh

# Run integration tests
make itest

# Or run manually
HOME=$(pwd)/.itests/home \
SSH_TEST_HOST=ssh://testuser@localhost:2222 \
go test -v ./tests/integration/...

# Stop test container
./scripts/itest-down.sh
```

### Manual Setup


If you prefer to set up manually:

```bash
# 1. Build SSH test container
docker build -t rdhpf-sshd-stub \
  -f docker/sshd-stub-dind/Dockerfile \
  docker/sshd-stub-dind/

# 2. Generate SSH key
mkdir -p ~/.ssh
ssh-keygen -t ed25519 -f ~/.ssh/id_rdhpf_test -N ""

# 3. Start container with Docker socket mount
docker run -d --name rdhpf-sshd-stub \
  -p 2222:22 \
  -v /var/run/docker.sock:/var/run/docker.sock \
  rdhpf-sshd-stub

# 4. Wait for SSH to be ready
echo "Waiting for SSH server..."
sleep 2

# 5. Verify Docker access via socket
docker exec rdhpf-sshd-stub docker info

# 6. Configure SSH key
docker cp ~/.ssh/id_rdhpf_test.pub rdhpf-sshd-stub:/tmp/pubkey
docker exec rdhpf-sshd-stub sh -c "
  mv /tmp/pubkey /home/testuser/.ssh/authorized_keys
  chmod 600 /home/testuser/.ssh/authorized_keys
  chown testuser:testuser /home/testuser/.ssh/authorized_keys
"

# 7. Test SSH + Docker access
ssh -i ~/.ssh/id_rdhpf_test -p 2222 \
  -o StrictHostKeyChecking=no \
  -o UserKnownHostsFile=/dev/null \
  testuser@localhost docker info

# 8. Run tests
SSH_TEST_HOST=ssh://testuser@localhost:2222 \
go test -v ./tests/integration/...

# 9. Cleanup
docker stop rdhpf-sshd-stub
docker rm rdhpf-sshd-stub
```

## Troubleshooting

### Tests Skip with "SSH_TEST_HOST not set"


**Cause:** Environment variable not set or wrong format.

**Solution:**
```bash
# Correct format (with ssh:// prefix and port)
export SSH_TEST_HOST=ssh://testuser@localhost:2222

# Wrong formats
export SSH_TEST_HOST=testuser@localhost         # ❌ Missing ssh:// prefix
export SSH_TEST_HOST=ssh://testuser@localhost   # ❌ Missing port :2222
```

### SSH Connection Refused


**Cause:** SSH container not running or not accessible.

**Solution:**
```bash
# Check if container is running
docker ps | grep rdhpf-sshd-stub

# Check container logs
docker logs rdhpf-sshd-stub

# Restart container
docker stop rdhpf-sshd-stub
docker rm rdhpf-sshd-stub
./scripts/itest-up.sh
```

### Permission Denied (publickey)


**Cause:** SSH key not properly configured in container.

**Solution:**
```bash
# Verify key exists in container
docker exec rdhpf-sshd-stub ls -la /home/testuser/.ssh/authorized_keys

# Reconfigure key if needed
docker cp ~/.ssh/id_rdhpf_test.pub rdhpf-sshd-stub:/tmp/pubkey
docker exec rdhpf-sshd-stub sh -c "
  mv /tmp/pubkey /home/testuser/.ssh/authorized_keys
  chmod 600 /home/testuser/.ssh/authorized_keys
  chown testuser:testuser /home/testuser/.ssh/authorized_keys
"
```

### Port Already in Use


**Cause:** Previous test run didn't clean up properly.

**Solution:**
```bash
# Find processes using test ports
sudo lsof -i :8080
sudo lsof -i :18080

# Kill processes if needed
sudo kill <PID>

# Clean up Docker containers
docker stop $(docker ps -aq) 2>/dev/null || true
docker rm $(docker ps -aq) 2>/dev/null || true
```

### SSH Connection Timeout


**Cause:** SSH server not running or firewall blocking connections.

**Solution:**
```bash
# Check SSH server status
sudo systemctl status ssh

# Start if not running
sudo systemctl start ssh

# Test connection manually
ssh -v testuser@localhost echo "test"
```

## CI Integration Details

### Workflow Triggers


Integration tests run on:
- ✅ Pull requests to `main` branch (when Go code changes)
- ✅ Manual workflow dispatch
- ✅ Nightly schedule (2 AM UTC)

### Workflow Path Filtering


Tests only run when these files change:
- `**.go` - Any Go source files
- `go.mod` - Go module dependencies
- `go.sum` - Dependency checksums  
- `.github/workflows/integration-test.yml` - The workflow itself

This prevents unnecessary test runs on documentation-only changes.

### Test Results


**Success:** All three phases pass
- Green checkmark in PR checks
- No blocking issues

**Failure:** Any phase fails
- Red X in PR checks
- Detailed logs available in Actions tab
- PR cannot merge until fixed

## Performance Expectations


| Phase | Tests | Expected Time | Timeout |
|-------|-------|---------------|---------|
| Phase 1 | SSH Master (8 tests) | ~30 seconds | 3 minutes |
| Phase 2 | E2E Container (1 test) | ~45 seconds | 3 minutes |
| Phase 3 | All Integration (~14 tests) | ~3-5 minutes | 10 minutes |
| **Total** | **~14 tests** | **~4-6 minutes** | **16 minutes** |

## What These Tests Catch

### Real Issues Caught


1. **SSH ControlMaster connection issues**
   - Socket file creation/cleanup
   - Connection multiplexing
   - Health check failures

2. **Docker events stream problems**
   - Stream closing immediately (like v0.1.4 bug)
   - Event parsing errors
   - Connection timeout issues

3. **Port forwarding failures**
   - SSH tunnel creation issues
   - Port conflicts
   - Cleanup problems

4. **Container lifecycle bugs**
   - Missing container start events
   - Port detection failures
   - Cleanup race conditions

### Example: v0.1.4 Bug Pattern


The v0.1.4 bug (stream closing immediately) would be caught by `TestManager_ContainerWithOnePort`:

```go
// Test expects:
1. Container starts
2. Event stream detects it
3. Port forward created within 3s
4. HTTP connection works

// v0.1.4 behavior:
1. Container starts ✓
2. Event stream closes after 38ms ✗
3. No port forward created ✗
4. HTTP connection fails ✗

// Test result: FAIL (caught the bug!)
```

## Maintenance

### Adding New Integration Tests


1. Create test in `tests/integration/`
2. Use `getTestSSHHost(t)` helper (skips if SSH_TEST_HOST not set)
3. Clean up resources in `defer` statements
4. Test runs automatically in Phase 3

Example:
```go
func TestNewFeature(t *testing.T) {
    sshHost := getTestSSHHost(t)  // Skips if not set
    
    // Test code here
    
    // Cleanup in defer
    defer cleanup()
}
```

### Debugging CI Failures


1. **Check workflow logs** in GitHub Actions
2. **Look for "Collect logs on failure" step** for diagnostic info
3. **Run tests locally** with same environment
4. **Check SSH setup** and Docker access
5. **Verify test timeouts** aren't too aggressive

### Updating Test Environment


If you need to change the test environment:

1. Update `.github/workflows/integration-test.yml`
2. Test changes locally first
3. Create PR and verify tests still pass
4. Document changes in this file

## Future Improvements


Potential enhancements (not currently implemented):

- **Parallel test execution** - Run phases concurrently
- **Test coverage reporting** - Track coverage from integration tests
- **Performance benchmarking** - Measure latency consistently
- **Multi-platform testing** - Run on macOS runners
- **Remote host testing** - Test against actual remote SSH hosts

## References


- **Workflow:** `.github/workflows/integration-test.yml`
- **Test code:** `tests/integration/`
- **Helper functions:** `tests/integration/ssh_master_test.go` (getTestSSHHost)
- **Plan:** `specs/002-ssh-stream-smoke-test/plan.md`