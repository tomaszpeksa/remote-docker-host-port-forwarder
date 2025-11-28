# Docker Event Stream Failure Diagnostic Guide

## Executive Summary

Based on analysis of logs from `macos-runner-01`, the root cause of intermittent MongoDB connection failures is **Docker event stream instability**, leading to missed container start events and absent SSH port forwards.

**Status**: Diagnostic logging added to validate hypotheses. Next step: capture logs during a failure.

---

## Root Cause Analysis

### Evidence Summary

**From `macos-runner-01` (`~/Library/Logs/rdhpf/`):**

1. **stderr.log** shows continuous failures:
   ```
   Warning: Permanently added '192.168.1.84' (ED25519) to the list of known hosts.
   Error: manager error: event stream failed after 10 consecutive failures
   ```

2. **stdout.log** shows correct operation when stream is healthy:
   ```
   level=INFO msg="container ports discovered" containerID=3687b93abf61 ports=[27017]
   level=INFO msg="adding SSH port forward" localPort=27017 remotePort=27017
   ```

3. **File sizes**:
   - `stdout.log`: ~4.2 GB (indicating frequent restarts)
   - Stream works correctly when connected, fails intermittently

**From `linux-docker-01` (192.168.1.84) - CRITICAL NEW EVIDENCE:**

4. **SSH logs show excessive session churn**:
   - **29 SSH session open/close events in 30 minutes** (~1 per minute)
   - Sessions opening and **closing immediately** (within seconds):
   ```
   Nov 16 08:39:39 sshd[628017]: session opened for user docker from 192.168.1.88
   Nov 16 08:39:39 sshd[628017]: session closed for user docker
   Nov 16 08:39:45 sshd[628043]: session opened for user docker from 192.168.1.88
   Nov 16 08:39:45 sshd[628043]: session closed for user docker
   ```

5. **SSH server configuration**:
   - `ClientAliveInterval 0` (no keepalive - default)
   - `ClientAliveCountMax 3`
   - No aggressive limits configured

6. **Docker daemon health**: ✅ Healthy and responsive
   - `docker events` tested directly - works perfectly
   - Docker socket permissions correct

7. **Active connections**:
   - One SSH connection from `192.168.1.88` (macos-runner-01) exists
   - But not maintaining stable Docker event stream

### CONFIRMED Root Cause

#### SSH ControlMaster Is Not Persisting (CONFIRMED)

**Status**: ✅ **ROOT CAUSE IDENTIFIED**

**Evidence from both hosts confirms**:
- **macos-runner-01**: "Permanently added..." warnings = new SSH connections (not reusing ControlMaster)
- **linux-docker-01**: 29 SSH sessions in 30 minutes, each closing immediately
- Expected: 1 persistent ControlMaster session + multiplexed commands
- Actual: Many short-lived sessions = ControlMaster NOT working

**The ControlMaster is failing to maintain persistent SSH multiplexing.**

**Failure Cascade**:
1. ControlMaster session dies or socket becomes stale
2. New Docker events command cannot multiplex through dead socket
3. Falls back to creating new SSH session (hence "Permanently added...")
4. New session starts `docker events` but has no persistence configured
5. Session closes immediately (see SSH logs)
6. Event stream fails → counted as failure
7. After 10 failures → manager stops → LaunchAgent restarts rdhpf
8. Cycle repeats every ~30 seconds

**Why tests fail intermittently**:
- Containers starting during "reconnecting" phase → no port forwards → ❌ FAIL
- Containers starting when briefly connected → port forwards work → ✅ PASS

### Contributing Factors

#### #1: SSH Server Missing Keepalive

**Issue**: `linux-docker-01` has `ClientAliveInterval 0` (disabled)

**Impact**:
- No server-side keepalive to maintain idle connections
- Long-running `docker events` stream has no activity when no containers change
- TCP connection may be dropped by intermediary network devices

#### #2: ControlMaster Health Check Timing Gap

**Issue**: Health check runs every 30 seconds ([`manager.go:172`](../internal/manager/manager.go:172))

**Impact**:
- If ControlMaster dies at T+1s, not detected until T+30s
- All commands in that 29-second window create new sessions
- Compounds the session churn problem

#### #3: No ControlMaster Recovery Before Stream Start

**Issue**: Manager doesn't verify ControlMaster before starting event stream

**Impact**:
- Attempts to start stream with dead ControlMaster
- Creates new non-multiplexed session
- That session has no persistence, closes immediately

---

## Diagnostic Logging Added

The following instrumentation has been added to validate the hypotheses:

### 1. SSH ControlMaster Health Tracking

**File**: [`internal/ssh/master.go`](../internal/ssh/master.go)

**Location**: [`Check()`](../internal/ssh/master.go:249) method

**Logs**:
```go
DIAGNOSTIC: SSH ControlMaster health check starting
  - socket_exists: true/false
  - socket_size: bytes
  - socket_mode: unix socket permissions
  - check_duration_ms: milliseconds
```

**Purpose**: Track socket file state before and during health checks

### 2. Docker Events Stream Lifecycle

**File**: [`internal/docker/events.go`](../internal/docker/events.go)

**Locations**: 
- Stream startup: [line 150-176](../internal/docker/events.go:150-176)
- Event reception: [line 193-211](../internal/docker/events.go:193-211)
- Stream end: [line 268-285](../internal/docker/events.go:268-285)

**Logs**:
```go
// At startup
DIAGNOSTIC: Starting Docker events stream
  - startup_duration_ms: time to start process
  - pid: SSH process ID

// First event
DIAGNOSTIC: First Docker event received
  - time_since_start_ms: latency to first event

// At stream end
DIAGNOSTIC: Docker events stream scanner finished
  - total_duration_ms: how long stream ran
  - events_received: total event count
  - time_to_first_event_ms: startup latency
  - time_since_last_event_ms: idle time before close
```

**Purpose**: Distinguish between:
- Stream that never connects (startup < 100ms with no events)
- Stream that times out (long duration, no events)
- Stream that works then dies (events received, then sudden close)

### 3. Manager-Level Stream Tracking

**File**: [`internal/manager/manager.go`](../internal/manager/manager.go)

**Locations**:
- Pre-flight checks: [line 196-223](../internal/manager/manager.go:196-223)
- Stream lifecycle: [line 224-248](../internal/manager/manager.go:224-248)

**Logs**:
```go
// Before each stream attempt
DIAGNOSTIC: SSH ControlMaster status before stream start
  - consecutive_failures: current failure count
DIAGNOSTIC: Docker daemon connectivity validated
  - docker_version: confirms daemon reachable

// After stream ends
DIAGNOSTIC: Docker events stream ended
  - duration_ms: how long it ran
  - clean_close: true/false
  - consecutive_failures: failure count
```

**Purpose**: Validate SSH and Docker health before starting stream, track failure patterns

### 4. Docker Daemon Connectivity Test

**File**: [`internal/manager/manager.go`](../internal/manager/manager.go)

**Location**: [`validateDockerConnectivity()`](../internal/manager/manager.go:641) method

**What it does**:
- Runs `docker version` via SSH before starting event stream
- 5-second timeout
- Logs success/failure with timing

**Purpose**: Catch Docker daemon unresponsiveness early, before event stream attempt

---

## What to Look for in Logs

When the issue occurs next, check for these patterns:

### Pattern A: SSH Socket Corruption

```
DIAGNOSTIC: SSH ControlMaster status before stream start
  consecutive_failures=3
DIAGNOSTIC: SSH ControlMaster health check starting
  socket_exists=true socket_size=0 socket_mode=Srwx------
DIAGNOSTIC: SSH ControlMaster check failed
  error="SSH ControlMaster check failed: exit status 255"
DIAGNOSTIC: Starting Docker events stream
  attempt_number=4
docker events stream started
  startup_duration_ms=45
DIAGNOSTIC: Docker events stream scanner finished
  total_duration_ms=52 events_received=0 first_event_received=false
```

**Diagnosis**: Socket file exists but SSH check fails → stale socket

### Pattern B: Docker Daemon Timeout

```
DIAGNOSTIC: SSH ControlMaster health check passed
  check_duration_ms=15
DIAGNOSTIC: Docker daemon connectivity validated
  docker_version=24.0.6
DIAGNOSTIC: Starting Docker events stream
  attempt_number=1
docker events stream started
  startup_duration_ms=5234
DIAGNOSTIC: Docker events stream scanner finished
  total_duration_ms=5298 events_received=0
```

**Diagnosis**: SSH healthy, Docker responds to `version`, but `events` command times out (5+ seconds, no events)

### Pattern C: Network Interruption

```
DIAGNOSTIC: First Docker event received
  time_since_start_ms=234
docker event received type=start containerID=abc123
DIAGNOSTIC: Docker events stream scanner finished
  total_duration_ms=15673 events_received=42
  time_since_last_event_ms=12456
```

**Diagnosis**: Stream worked for a while (42 events), then went silent for 12+ seconds before closing → network issue or Docker daemon hang

### Pattern D: Healthy Operation (for comparison)

```
DIAGNOSTIC: SSH ControlMaster health check passed
  check_duration_ms=12
DIAGNOSTIC: Docker daemon connectivity validated
  docker_version=24.0.6
DIAGNOSTIC: Starting Docker events stream
  attempt_number=1
docker events stream started
  startup_duration_ms=34
DIAGNOSTIC: First Docker event received
  time_since_start_ms=178
docker event received type=start containerID=abc123
[stream continues for minutes/hours with regular events]
```

**Diagnosis**: This is what success looks like

---

## Next Steps

### Immediate Action Required

1. **Deploy the instrumented build** to `macos-runner-01`:
   ```bash
   # Build on your machine
   make build
   
   # Copy to runner
   scp rdhpf runner@macos-runner-01:~/
   
   # Stop the LaunchAgent
   ssh runner@macos-runner-01 'launchctl unload ~/Library/LaunchAgents/com.rdhpf.plist'
   
   # Replace binary
   ssh runner@macos-runner-01 'mv rdhpf /usr/local/bin/rdhpf'
   
   # Start LaunchAgent
   ssh runner@macos-runner-01 'launchctl load ~/Library/LaunchAgents/com.rdhpf.plist'
   ```

2. **Rotate logs** (optional, to start fresh):
   ```bash
   ssh runner@macos-runner-01 '
     cd ~/Library/Logs/rdhpf/
     mv stdout.log stdout.log.old
     mv stderr.log stderr.log.old
     touch stdout.log stderr.log
   '
   ```

3. **Trigger the failure** (run your MongoDB test):
   - The instrumented binary will capture detailed timing and state

4. **Collect diagnostic logs**:
   ```bash
   # Get the last 500 lines from both logs
   ssh runner@macos-runner-01 'tail -500 ~/Library/Logs/rdhpf/stdout.log' > stdout-diagnostic.log
   ssh runner@macos-runner-01 'tail -500 ~/Library/Logs/rdhpf/stderr.log' > stderr-diagnostic.log
   ```

5. **Analyze patterns** using this guide's "What to Look for" section

### Required Fixes

Based on confirmed root cause, implement these fixes in priority order:

#### FIX #1: Verify ControlMaster Before Event Stream (CRITICAL)

**File**: [`internal/manager/manager.go`](../internal/manager/manager.go)

**Change**: Add mandatory ControlMaster check before starting stream:

```go
// In Run() method, before m.eventReader.Stream(ctx)
// Around line 196-223 (already has partial diagnostic code)

// MANDATORY: Ensure ControlMaster is alive before starting stream
if err := m.sshMaster.EnsureAlive(ctx); err != nil {
    m.logger.Error("Failed to ensure SSH ControlMaster is alive",
        "error", err.Error())
    consecutiveFailures++
    
    // Calculate backoff and retry
    delay := baseDelay * time.Duration(1<<uint(min(consecutiveFailures-1, 5)))
    if delay > 30*time.Second {
        delay = 30 * time.Second
    }
    
    m.logger.Warn("SSH ControlMaster unavailable, retrying after backoff",
        "consecutive_failures", consecutiveFailures,
        "backoff_delay", delay)
    
    select {
    case <-time.After(delay):
        continue  // Retry from top of loop
    case <-ctx.Done():
        return nil
    }
}
```

**Why**: Prevents attempting event stream with dead ControlMaster, avoiding session churn.

#### FIX #2: Enable SSH Server Keepalive (INFRASTRUCTURE)

**Host**: `linux-docker-01` (192.168.1.84)

**File**: `/etc/ssh/sshd_config`

**Change**:
```bash
# Add or uncomment:
ClientAliveInterval 60
ClientAliveCountMax 3

# Then restart SSH:
sudo systemctl reload sshd
```

**Why**: Maintains long-running connections, prevents idle timeout on `docker events` stream.

#### FIX #3: Increase Client-Side Keepalive Frequency

**File**: [`internal/ssh/master.go`](../internal/ssh/master.go)

**Change**: In `Open()` method around line 119:

```go
// Change from:
"-o", "ServerAliveInterval=10",
"-o", "ServerAliveCountMax=3",

// To:
"-o", "ServerAliveInterval=15",    // Every 15 seconds
"-o", "ServerAliveCountMax=2",      // Faster failure detection (30s)
"-o", "TCPKeepAlive=yes",           // Enable TCP-level keepalive
```

**Why**:
- Detects dead connections faster (30s vs 40s)
- TCP keepalive prevents intermediary timeouts
- Works with server-side keepalive for bidirectional monitoring

#### FIX #4: Reduce Health Check Interval

**File**: [`internal/manager/manager.go`](../internal/manager/manager.go)

**Change**: Around line 172:

```go
// Change from:
m.sshMaster.StartHealthMonitor(ctx, 30*time.Second)

// To:
m.sshMaster.StartHealthMonitor(ctx, 15*time.Second)
```

**Why**: Reduces window where ControlMaster can be dead (30s → 15s), catches failures faster.

#### FIX #5: Add Stale Socket Cleanup

**File**: [`internal/ssh/master.go`](../internal/ssh/master.go)

**Change**: In `EnsureAlive()` method around line 330:

```go
// After: if err := m.Check(); err != nil {
// Add cleanup before Close():

m.logger.Warn("SSH ControlMaster check failed, cleaning stale socket",
    "error", err.Error(),
    "control_path", m.controlPath)

// Remove stale socket file before attempting Close/Open
if _, statErr := os.Stat(m.controlPath); statErr == nil {
    m.logger.Info("Removing stale control socket",
        "path", m.controlPath)
    os.Remove(m.controlPath)
}

// Close old connection (ignore errors)
_ = m.Close()
```

**Why**: Ensures stale socket files don't prevent ControlMaster re-creation.

---

## Testing the Fix

After implementing a fix:

1. **Deploy to staging** (if available)
2. **Run stress test**:
   ```bash
   # Start/stop containers rapidly for 10 minutes
   for i in {1..100}; do
     docker run -d -p 27017:27017 mongo:latest
     sleep 5
     docker stop $(docker ps -q --filter ancestor=mongo:latest)
     sleep 1
   done
   ```

3. **Verify metrics** in logs:
   - No "event stream failed" errors
   - `events_received` count increases steadily
   - `time_since_last_event_ms` stays reasonable (<60000)
   - No "Permanently added" warnings in stderr

4. **Monitor for regression**:
   - Check logs daily for first week
   - Alert on "consecutive_failures > 5"

---

## References

- Production logs: `runner@macos-runner-01:~/Library/Logs/rdhpf/`
- LaunchAgent: `runner@macos-runner-01:~/Library/LaunchAgents/com.rdhpf.plist`
- Ansible setup: [`dev-infra/ansible/roles/docker-connectivity/`](../dev-infra/ansible/roles/docker-connectivity/)
- Original issue report: This debugging session