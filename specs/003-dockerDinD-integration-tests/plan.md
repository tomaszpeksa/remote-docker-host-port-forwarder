# Plan: Docker-in-Docker Integration Tests to Reproduce Stream Bugs

**Goal:** Replace Docker shim with real Docker to catch stream persistence bugs in CI.

---

## Problem Statement

### Current Limitation

Our integration tests **passed** but **failed to reproduce** the reported bug:

```
✅ CI Tests Pass (Docker Shim)
  - Shim emits events from events.jsonl
  - Shim exits immediately after emitting
  - Tests can't detect if real stream would close early

❌ Production Fails (Real Docker)  
  - Stream closes after 38ms
  - No containers detected
  - No port forwards created
```

**Root cause:** The docker-shim (`tests/integration/harness/docker-shim/docker`) is a bash script that:
1. Reads events from `events.jsonl`
2. Emits them
3. **Exits immediately**

This **mimics the bug** we're trying to catch, so tests can't distinguish between:
- ✅ Working: Real `docker events` stays open listening
- ❌ Broken: Stream closes immediately (the bug!)

### Why We Need Real Docker

```bash
# Docker Shim (current - can't test stream persistence)
docker events --format '{{json .}}' --filter type=container
  → Shim reads events.jsonl
  → Shim echoes events
  → Shim exits ← Can't tell if this is a bug or expected!

# Real Docker (needed - reveals stream behavior)
docker events --format '{{json .}}' --filter type=container
  → Process stays alive
  → Waits for new events
  → Only exits on signal/error ← Actual production behavior
```

---

## Solution: Docker-in-Docker (DinD)

### Architecture

```
┌─────────────────────────────────────────────────────┐
│ GitHub Actions Runner                               │
│                                                     │
│  ┌───────────────────────────────────────────────┐ │
│  │ sshd-stub-dind Container (--privileged)       │ │
│  │                                               │ │
│  │  ┌──────────┐         ┌─────────────────┐   │ │
│  │  │   sshd   │◀────SSH─│ Integration Test│   │ │
│  │  └──────────┘         └─────────────────┘   │ │
│  │       │                                      │ │
│  │       │ Execute                              │ │
│  │       ▼                                      │ │
│  │  ┌──────────┐                               │ │
│  │  │ dockerd  │ Real Docker Daemon            │ │
│  │  └──────────┘                               │ │
│  │       │                                      │ │
│  │       │ Manages                              │ │
│  │       ▼                                      │ │
│  │  ┌──────────┐                               │ │
│  │  │Container │ Real containers               │ │
│  │  └──────────┘                               │ │
│  │                                              │ │
│  └───────────────────────────────────────────────┘ │
│                                                     │
└─────────────────────────────────────────────────────┘
```

### Key Changes

1. **Base image:** `alpine:3.19` → `docker:dind-alpine`
2. **Add dockerd:** Real Docker daemon running in container
3. **Privileged mode:** Required for Docker-in-Docker
4. **Remove shim:** No more fake docker command
5. **Real commands:** Actual `docker events`, `docker inspect`, `docker ps`

---

## Implementation Plan

### Phase 1: Create DinD Dockerfile

**File:** `docker/sshd-stub-dind/Dockerfile`

```dockerfile
FROM docker:dind-alpine

# Install OpenSSH server
RUN apk add --no-cache openssh-server

# Generate SSH host keys
RUN ssh-keygen -A

# Configure sshd for key-based auth
RUN sed -i 's/#PubkeyAuthentication yes/PubkeyAuthentication yes/' /etc/ssh/sshd_config && \
    sed -i 's/#PasswordAuthentication yes/PasswordAuthentication no/' /etc/ssh/sshd_config && \
    sed -i 's/#PermitRootLogin prohibit-password/PermitRootLogin no/' /etc/ssh/sshd_config

# Create test user
RUN adduser -D -s /bin/sh testuser && \
    mkdir -p /home/testuser/.ssh && \
    chown testuser:testuser /home/testuser/.ssh && \
    chmod 700 /home/testuser/.ssh

# Add testuser to docker group (created by docker:dind)
RUN addgroup testuser docker || addgroup testuser ping

# Create entrypoint script
COPY entrypoint.sh /entrypoint.sh
RUN chmod +x /entrypoint.sh

EXPOSE 22

ENTRYPOINT ["/entrypoint.sh"]
```

**File:** `docker/sshd-stub-dind/entrypoint.sh`

```bash
#!/bin/sh
set -e

echo "Starting Docker daemon..."
dockerd &
DOCKERD_PID=$!

# Wait for Docker to be ready
echo "Waiting for Docker daemon..."
timeout 30 sh -c 'until docker info >/dev/null 2>&1; do sleep 1; done'

if ! docker info >/dev/null 2>&1; then
    echo "ERROR: Docker daemon failed to start"
    exit 1
fi

echo "Docker daemon ready"

echo "Starting SSH daemon..."
/usr/sbin/sshd -D -e &
SSHD_PID=$!

echo "SSH daemon started (PID: $SSHD_PID)"
echo "Docker daemon running (PID: $DOCKERD_PID)"

# Wait for either process to exit
wait -n $DOCKERD_PID $SSHD_PID
```

### Phase 2: Update GitHub Actions Workflow

**File:** `.github/workflows/integration-test.yml`

**Changes:**

```yaml
# Build step
- name: Build SSH test container with real Docker
  run: |
    echo "Building sshd-stub-dind (Docker-in-Docker) image..."
    docker build -t rdhpf-sshd-stub-dind \
      -f docker/sshd-stub-dind/Dockerfile \
      docker/sshd-stub-dind/

# Start container with --privileged (required for DinD)
- name: Start SSH test container with Docker-in-Docker
  run: |
    echo "Starting SSH container with Docker daemon..."
    docker run -d \
      --name rdhpf-sshd-stub \
      --privileged \
      -p 2222:22 \
      rdhpf-sshd-stub-dind
    
    echo "Waiting for Docker daemon to be ready in container..."
    timeout 30 sh -c 'until docker exec rdhpf-sshd-stub docker info >/dev/null 2>&1; do sleep 1; done'
    
    echo "Waiting for SSH server to be ready..."
    sleep 3
    
    # Configure SSH key (same as before)
    docker cp $HOME/.ssh/id_rdhpf_test.pub rdhpf-sshd-stub:/tmp/pubkey
    docker exec rdhpf-sshd-stub sh -c "
      mv /tmp/pubkey /home/testuser/.ssh/authorized_keys && \
      chmod 600 /home/testuser/.ssh/authorized_keys && \
      chown testuser:testuser /home/testuser/.ssh/authorized_keys
    "
    
    # Test SSH + Docker access
    echo "Testing SSH connection and Docker access..."
    ssh -i $HOME/.ssh/id_rdhpf_test \
      -o StrictHostKeyChecking=no \
      -o UserKnownHostsFile=/dev/null \
      -o LogLevel=ERROR \
      -p 2222 testuser@localhost docker info
    
    echo "✓ SSH and Docker both working"
```

### Phase 3: Add Stream Persistence Test

**File:** `tests/integration/docker_events_stream_test.go`

```go
package integration

import (
	"context"
	"testing"
	"time"

	"github.com/tomaszpeksa/remote-docker-host-port-forwarder/internal/docker"
	"github.com/tomaszpeksa/remote-docker-host-port-forwarder/internal/logging"
	"github.com/tomaszpeksa/remote-docker-host-port-forwarder/internal/ssh"
)

// TestDockerEventsStreamPersistence verifies that the docker events stream
// stays open and doesn't close immediately. This test specifically catches
// the bug where SSH ControlMaster causes streams to close prematurely.
func TestDockerEventsStreamPersistence(t *testing.T) {
	sshHost := getTestSSHHost(t)
	
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	
	// Setup SSH ControlMaster
	logger := logging.NewLogger("debug")
	master, err := ssh.NewMaster(sshHost, logger)
	if err != nil {
		t.Fatalf("Failed to create SSH master: %v", err)
	}
	
	err = master.Open(ctx)
	if err != nil {
		t.Fatalf("Failed to open SSH ControlMaster: %v", err)
	}
	defer master.Close()
	
	// Get control path
	controlPath, err := ssh.DeriveControlPath(sshHost)
	if err != nil {
		t.Fatalf("Failed to derive control path: %v", err)
	}
	
	// Start docker events stream
	t.Log("Starting docker events stream...")
	reader := docker.NewEventReader(sshHost, controlPath, logger)
	events, errs := reader.Stream(ctx)
	
	// CRITICAL TEST: Stream should NOT close immediately
	// The bug this catches: stream closes after ~40ms
	// Expected behavior: stream stays open for at least 5 seconds
	
	streamStartTime := time.Now()
	
	select {
	case <-events:
		elapsed := time.Since(streamStartTime)
		t.Fatalf("❌ BUG DETECTED: Event stream closed after %v (should stay open)", elapsed)
		
	case err := <-errs:
		elapsed := time.Since(streamStartTime)
		t.Fatalf("❌ BUG DETECTED: Error channel closed after %v: %v", elapsed, err)
		
	case <-time.After(5 * time.Second):
		t.Log("✅ Stream stayed open for 5+ seconds (correct behavior)")
	}
	
	// Verify stream can be cancelled cleanly
	cancel()
	
	select {
	case <-events:
		t.Log("✅ Stream closed cleanly after cancellation")
	case <-time.After(2 * time.Second):
		t.Error("⚠️  Stream didn't close within 2s after cancel")
	}
}
```

### Phase 4: Cleanup Old Shim References

**Files to remove:**
- `tests/integration/harness/docker-shim/docker` (bash shim)
- `tests/integration/harness/scenarios/` (canned events)

**Files to update:**
- `scripts/itest-up.sh` - Build dind image instead of shim
- `scripts/itest-down.sh` - No changes needed
- `docs/ci-integration-tests.md` - Update to reflect DinD approach

### Phase 5: Update Documentation

**File:** `docs/ci-integration-tests.md`

**Changes:**

```markdown
## CI Environment Setup

### Docker-in-Docker SSH Server

GitHub Actions uses a containerized SSH server with real Docker daemon:

```bash
# SSH server configuration
- docker:dind-alpine base image
- Real Docker daemon (dockerd) running
- Runs on port 2222 (mapped from container port 22)
- --privileged mode (required for Docker-in-Docker)
- Test user: testuser (member of docker group)
```

### Why Docker-in-Docker?

Real Docker instead of a shim allows us to:
- ✅ Test actual `docker events` stream behavior
- ✅ Catch bugs where streams close prematurely
- ✅ Test against production-like Docker commands
- ✅ Verify container lifecycle management
```

---

## Implementation Checklist

- [ ] Create `docker/sshd-stub-dind/` directory
- [ ] Create `docker/sshd-stub-dind/Dockerfile`
- [ ] Create `docker/sshd-stub-dind/entrypoint.sh`
- [ ] Update `.github/workflows/integration-test.yml`
  - [ ] Change build path to sshd-stub-dind
  - [ ] Add `--privileged` flag
  - [ ] Add Docker readiness check
  - [ ] Update test command
- [ ] Create `tests/integration/docker_events_stream_test.go`
- [ ] Update `scripts/itest-up.sh` for DinD
- [ ] Remove old shim files
  - [ ] `tests/integration/harness/docker-shim/docker`
  - [ ] `tests/integration/harness/scenarios/` (optional - keep for reference)
- [ ] Update documentation
  - [ ] `docs/ci-integration-tests.md`
  - [ ] `CHANGELOG.md`
- [ ] Test locally
  - [ ] Build DinD image
  - [ ] Start container with --privileged
  - [ ] Run stream persistence test
  - [ ] Verify it catches the bug (if reproduced)

---

## Testing Strategy

### Local Testing

```bash
# 1. Build DinD image
docker build -t rdhpf-sshd-stub-dind -f docker/sshd-stub-dind/Dockerfile docker/sshd-stub-dind/

# 2. Start container
docker run -d --name rdhpf-test \
  --privileged \
  -p 2222:22 \
  rdhpf-sshd-stub-dind

# 3. Wait for Docker to start
sleep 5
docker exec rdhpf-test docker info

# 4. Configure SSH
ssh-keygen -t ed25519 -f ~/.ssh/id_test -N ""
docker cp ~/.ssh/id_test.pub rdhpf-test:/tmp/pubkey
docker exec rdhpf-test sh -c "
  mv /tmp/pubkey /home/testuser/.ssh/authorized_keys &&
  chmod 600 /home/testuser/.ssh/authorized_keys &&
  chown testuser:testuser /home/testuser/.ssh/authorized_keys
"

# 5. Test SSH + Docker
ssh -i ~/.ssh/id_test -p 2222 testuser@localhost docker events --since 1m

# Should see real docker events and stream stays open!

# 6. Run integration tests
SSH_TEST_HOST=ssh://testuser@localhost:2222 \
  go test -v -run TestDockerEventsStreamPersistence ./tests/integration/

# 7. Cleanup
docker stop rdhpf-test
docker rm rdhpf-test
```

### Expected Results

**With the bug (before fix):**
```
=== RUN   TestDockerEventsStreamPersistence
    docker_events_stream_test.go:XX: Starting docker events stream...
    docker_events_stream_test.go:XX: ❌ BUG DETECTED: Event stream closed after 45ms (should stay open)
--- FAIL: TestDockerEventsStreamPersistence (0.05s)
```

**After fix:**
```
=== RUN   TestDockerEventsStreamPersistence
    docker_events_stream_test.go:XX: Starting docker events stream...
    docker_events_stream_test.go:XX: ✅ Stream stayed open for 5+ seconds (correct behavior)
    docker_events_stream_test.go:XX: ✅ Stream closed cleanly after cancellation
--- PASS: TestDockerEventsStreamPersistence (7.02s)
```

---

## Risk Assessment

### High Risk
❌ **None** - All changes are isolated to test infrastructure

### Medium Risk
⚠️ **DinD startup time** - Docker daemon needs time to initialize
- **Mitigation:** Add proper readiness checks with timeout

⚠️ **Resource usage** - DinD is more resource-intensive
- **Mitigation:** Monitor CI run times, may need timeout adjustments

⚠️ **Privileged mode security** - Container runs with elevated privileges
- **Mitigation:** Only in CI, container is isolated and ephemeral

### Low Risk
✅ **Test flakiness** - DinD can be slower than shim
- **Mitigation:** Generous timeouts (30s for daemon start)

✅ **Compatibility** - Works on all platforms where Docker runs
- **Mitigation:** GitHub Actions runners support --privileged

---

## Success Criteria

- [ ] DinD container starts successfully in CI
- [ ] Docker daemon becomes ready within 30 seconds
- [ ] SSH authentication works
- [ ] `TestDockerEventsStreamPersistence` runs and validates stream behavior
- [ ] All existing integration tests still pass
- [ ] Test would catch the reported bug (stream closing at 40ms)
- [ ] CI runtime increase is acceptable (<5 minutes overhead)

---

## Rollback Plan

If DinD causes issues:

1. **Revert workflow** to use old shim-based approach
2. **Keep DinD Dockerfile** for future use
3. **Document issues** for later investigation
4. **Consider alternatives** (e.g., external test environment)

Simple rollback command:
```bash
git revert <commit-sha>
```

All test infrastructure is isolated, so rollback is clean.

---

## Future Enhancements

After DinD is working:

1. **Performance optimization**
   - Cache dockerd startup state
   - Pre-pull common images
   - Parallel test execution

2. **Additional tests**
   - Container start/stop lifecycle
   - Port conflict scenarios
   - Network isolation tests

3. **Monitoring**
   - Track CI runtime metrics
   - Alert on test flakiness
   - Resource usage dashboards