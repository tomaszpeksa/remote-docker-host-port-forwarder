# Integration Test Procedure - v0.1.4 Shell Quoting Fix

## Purpose

Verify that the shell quoting fix for `{{...}}` templates works correctly with a real Docker host over SSH.

## Prerequisites

- Test Docker host accessible via SSH (e.g., `ssh://docker@linux-docker-01`)
- SSH key authentication configured
- Docker running on remote host
- Go 1.23+ installed locally

## Test Environment

- **Local**: Development machine with fixed code
- **Remote**: Docker host accessible via SSH
- **Test Duration**: ~2 minutes

## Test Procedure

### Step 1: Build Test Binary

```bash
# Build the fixed version
go build -o /tmp/rdhpf-test ./cmd/rdhpf

# Verify build
ls -lh /tmp/rdhpf-test
```

**Expected**: Binary created successfully, ~10-15MB size

---

### Step 2: Prepare Test Environment

```bash
# Set test host (adjust as needed)
export TEST_SSH_HOST="ssh://docker@linux-docker-01"

# Verify SSH connectivity
ssh ${TEST_SSH_HOST#ssh://} 'echo "SSH connection OK"'

# Verify Docker is running
ssh ${TEST_SSH_HOST#ssh://} 'docker ps'
```

**Expected**:

- SSH connection succeeds
- Docker ps returns (may be empty, that's OK)

---

### Step 3: Run rdhpf with Debug Logging

```bash
# Start rdhpf in one terminal with debug logs
/tmp/rdhpf-test run \
  --host $TEST_SSH_HOST \
  --log-level debug \
  2>&1 | tee /tmp/rdhpf-test-output.log
```

**Expected within 5 seconds**:
```
level=INFO msg="SSH master connection established"
level=INFO msg="executing docker events command via shell"
level=DEBUG msg="..." dockerCmd="docker events --format '{{json .}}' ..."
level=INFO msg="docker events stream started"
```

**Critical**: Check logs for these patterns:
- ✅ MUST contain: `sh -c "docker events --format '{{json .}}'`
- ✅ MUST contain: `docker events stream started`
- ❌ MUST NOT contain: `accepts no arguments`
- ❌ MUST NOT contain: `exit status 1` (within first 5 seconds)

---

### Step 4: Trigger Docker Events (in separate terminal)

```bash
# Start a test container
ssh ${TEST_SSH_HOST#ssh://} \
  'docker run --rm -d -p 8080:80 --name rdhpf-test nginx:alpine'

# Wait 2 seconds
sleep 2

# Stop the container
ssh ${TEST_SSH_HOST#ssh://} \
  'docker stop rdhpf-test'
```

**Expected**: Container starts and stops successfully

---

### Step 5: Verify Event Processing

Check the rdhpf log output from Step 3:

**Expected log entries**:
```
level=DEBUG msg="docker event received" type=start containerID=abc123...
level=INFO msg="handling container start" containerID=abc123...
level=INFO msg="container ports discovered" containerID=abc123... ports=[8080]
level=INFO msg="reconciliation starting" actions=1
level=INFO msg="reconciliation complete"
level=DEBUG msg="docker event received" type=die containerID=abc123...
level=INFO msg="handling container stop" containerID=abc123...
level=INFO msg="reconciliation starting" actions=1
level=INFO msg="reconciliation complete"
```

**Success Criteria**:
- ✅ Both start and stop/die events captured
- ✅ Port 8080 discovered correctly
- ✅ Reconciliation triggered for both events
- ✅ No errors in event processing

---

### Step 6: Verify Long-Running Stability

Let rdhpf run for at least 30 seconds while monitoring the log.

**Expected**:
- ✅ Event stream stays connected
- ✅ No reconnection attempts or errors
- ✅ No "accepts no arguments" errors
- ✅ SSH master health checks passing

---

### Step 7: Test Startup Reconciliation

```bash
# Start a container BEFORE running rdhpf
ssh ${TEST_SSH_HOST#ssh://} \
  'docker run --rm -d -p 9090:80 --name rdhpf-startup-test nginx:alpine'

# Now start rdhpf
/tmp/rdhpf-test run \
  --host $TEST_SSH_HOST \
  --log-level debug \
  2>&1 | tee /tmp/rdhpf-startup-test.log
```

**Expected in logs**:
```
level=INFO msg="performing startup reconciliation"
level=INFO msg="found running containers" count=1
level=INFO msg="startup: adding container to desired state" containerID=... ports=[9090]
level=INFO msg="reconciliation starting" actions=1
level=INFO msg="startup reconciliation complete" containers=1
```

**Success Criteria**:
- ✅ Running container detected
- ✅ Port 9090 discovered via `docker inspect`
- ✅ NO "accepts no arguments" error from docker ps or docker inspect

---

### Step 8: Cleanup

```bash
# Stop test containers
ssh ${TEST_SSH_HOST#ssh://} 'docker stop rdhpf-startup-test 2>/dev/null || true'

# Stop rdhpf (Ctrl+C in terminal)

# Remove test binary
rm /tmp/rdhpf-test
rm /tmp/rdhpf-test-output.log
rm /tmp/rdhpf-startup-test.log
```

---

## Success Criteria Summary

### MUST PASS ✅

1. Docker events command uses `sh -c` with quoted template
2. Event stream starts successfully (no "accepts no arguments")
3. Event stream stays connected for >30 seconds
4. Container start events captured and processed
5. Container stop/die events captured and processed
6. Published ports discovered correctly via docker inspect
7. Startup reconciliation finds running containers via docker ps
8. No shell expansion errors in any docker command

### MUST NOT OCCUR ❌

1. "accepts no arguments" error
2. Immediate exit with status 1
3. Event stream disconnections/reconnections
4. Errors parsing docker command output

---

## Automated Test Commands


Quick test sequence (runs all steps):
```bash
#!/bin/bash
set -e

TEST_SSH_HOST="ssh://docker@linux-docker-01"

echo "=== Building test binary ==="
go build -o /tmp/rdhpf-test ./cmd/rdhpf

echo "=== Testing SSH connectivity ==="
ssh ${TEST_SSH_HOST#ssh://} 'docker ps' > /dev/null

echo "=== Starting rdhpf in background ==="
/tmp/rdhpf-test run --host $TEST_SSH_HOST --log-level debug \
  2>&1 | tee /tmp/rdhpf-test.log &
RDHPF_PID=$!

echo "=== Waiting for startup (5 sec) ==="
sleep 5

echo "=== Checking for errors ==="
if grep -q "accepts no arguments" /tmp/rdhpf-test.log; then
  echo "❌ FAILED: Shell expansion error detected"
  kill $RDHPF_PID
  exit 1
fi

if grep -q "docker events stream started" /tmp/rdhpf-test.log; then
  echo "✅ Event stream started successfully"
else
  echo "❌ FAILED: Event stream did not start"
  kill $RDHPF_PID
  exit 1
fi

echo "=== Triggering test event ==="
ssh ${TEST_SSH_HOST#ssh://} \
  'docker run --rm -d -p 8080:80 --name rdhpf-test nginx:alpine'
sleep 3
ssh ${TEST_SSH_HOST#ssh://} 'docker stop rdhpf-test'

echo "=== Waiting for event processing (3 sec) ==="
sleep 3

echo "=== Verifying events captured ==="
if grep -q "docker event received.*start" /tmp/rdhpf-test.log && \
   grep -q "docker event received.*die\|stop" /tmp/rdhpf-test.log; then
  echo "✅ Events captured successfully"
else
  echo "❌ FAILED: Events not captured"
  kill $RDHPF_PID
  exit 1
fi

echo "=== Cleanup ==="
kill $RDHPF_PID
rm /tmp/rdhpf-test /tmp/rdhpf-test.log

echo "✅ ALL TESTS PASSED"
```

---

## Troubleshooting

### If "accepts no arguments" still appears

1. Check that sh is available on remote host: `ssh host 'which sh'`
2. Verify template quoting in logs: look for `'{{json .}}'`
3. Check for shell compatibility issues

### If event stream doesn't start

1. Check SSH master connection: `ssh -O check -S /tmp/rdhpf-*.sock host`
2. Verify docker daemon is running: `ssh host 'docker info'`
3. Check firewall/network connectivity

### If events aren't captured

1. Verify event stream is running: check logs for "stream started"
2. Ensure container publishes ports: use -p flag
3. Check event filtering: only start/die/stop events processed

---

## Expected Test Duration

- Preparation: 1 minute
- Execution: 2 minutes  
- Verification: 1 minute
- **Total**: ~4 minutes

---

## Sign-off


Test executed by: _________________
Date: _________________
Result: ☐ PASS  ☐ FAIL
Notes: _________________________________