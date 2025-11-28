# Event Stream Failure Fix - Deployment Guide

## Summary

Root cause **CONFIRMED**: SSH ControlMaster is not persisting, causing excessive session churn (29 sessions in 30 minutes instead of 1 persistent session).

**Fixes implemented**: 5 code changes + 1 infrastructure change

---

## What Was Fixed

### Code Changes (Already Implemented)

#### 1. Mandatory ControlMaster Verification (CRITICAL)
**File**: [`internal/manager/manager.go`](../internal/manager/manager.go)
**What**: Added `EnsureAlive()` check before starting event stream
**Why**: Prevents attempting stream with dead ControlMaster
**Impact**: Eliminates non-multiplexed session creation

#### 2. Improved SSH Keepalive Settings
**File**: [`internal/ssh/master.go`](../internal/ssh/master.go)
**Changes**:
- `ServerAliveInterval`: 10s → 15s
- `ServerAliveCountMax`: 3 → 2 (fail faster: 30s vs 40s)
- Added: `TCPKeepAlive=yes`
**Why**: Better connection monitoring, faster failure detection

#### 3. Stale Socket Cleanup
**File**: [`internal/ssh/master.go`](../internal/ssh/master.go)
**What**: Remove stale socket file before recreating ControlMaster
**Why**: Prevents leftover sockets from blocking recreation

#### 4. Faster Health Checks
**File**: [`internal/manager/manager.go`](../internal/manager/manager.go)
**Change**: Health check interval 30s → 15s
**Why**: Detects ControlMaster failures twice as fast

#### 5. Comprehensive Diagnostic Logging
**Files**: Multiple
**What**: Added stream timing, socket health, connectivity validation
**Why**: Track root cause patterns and validate fixes

### Infrastructure Change (Required)

#### 6. Enable SSH Server Keepalive
**Host**: `linux-docker-01` (192.168.1.84)
**File**: `/etc/ssh/sshd_config`
**Change**:
```bash
ClientAliveInterval 60
ClientAliveCountMax 3
```
**Why**: Maintains long-running connections from clients

---

## Deployment Steps

### Step 1: Build Updated Binary

On your development machine:

```bash
cd /home/tomek/work/remote-docker-host-port-forwarder

# Build for macOS (if deploying to macos-runner-01)
GOOS=darwin GOARCH=amd64 go build -o rdhpf-darwin ./cmd/rdhpf

# Or build locally if same OS
make build
```

### Step 2: Deploy to macos-runner-01

```bash
# Copy binary to runner
scp rdhpf-darwin runner@macos-runner-01:~/rdhpf-new

# SSH to runner
ssh runner@macos-runner-01

# Stop LaunchAgent
launchctl unload ~/Library/LaunchAgents/com.rdhpf.plist

# Wait for clean shutdown
sleep 5

# Backup old binary
sudo mv /usr/local/bin/rdhpf /usr/local/bin/rdhpf.backup

# Install new binary
sudo mv ~/rdhpf-new /usr/local/bin/rdhpf
sudo chmod +x /usr/local/bin/rdhpf

# Optional: Rotate logs for fresh start
cd ~/Library/Logs/rdhpf/
mv stdout.log stdout.log.$(date +%Y%m%d-%H%M%S)
mv stderr.log stderr.log.$(date +%Y%m%d-%H%M%S)
touch stdout.log stderr.log

# Start LaunchAgent
launchctl load ~/Library/LaunchAgents/com.rdhpf.plist

# Verify it started
launchctl list | grep rdhpf
```

### Step 3: Configure SSH Server (linux-docker-01)

```bash
# SSH to Docker host
ssh docker@linux-docker-01

# Edit SSH config
sudo nano /etc/ssh/sshd_config

# Add or uncomment these lines:
ClientAliveInterval 60
ClientAliveCountMax 3

# Validate config
sudo sshd -t

# Reload SSH (doesn't disconnect existing sessions)
sudo systemctl reload sshd

# Verify config applied
sudo sshd -T | grep -i clientalive
```

### Step 4: Verify Deployment

#### Check rdhpf is running:
```bash
ssh runner@macos-runner-01 'launchctl list | grep rdhpf'
# Should show PID and "0" exit code
```

#### Check logs for new diagnostic output:
```bash
ssh runner@macos-runner-01 'tail -50 ~/Library/Logs/rdhpf/stdout.log'
```

**Look for**:
- `Ensuring SSH ControlMaster is alive before starting event stream`
- `DIAGNOSTIC: SSH ControlMaster verified healthy`
- `DIAGNOSTIC: Docker daemon connectivity validated`
- `docker events stream started` with `startup_duration_ms`

#### Monitor SSH session count on Docker host:
```bash
ssh docker@linux-docker-01

# Watch active sessions (should stabilize at 1-2)
watch -n 5 'sudo journalctl -u ssh -n 20 --no-pager | grep "session opened.*192.168.1.88"'
```

**Expected after fix**:
- **Before**: New session every ~60 seconds
- **After**: One persistent session, minimal new sessions

### Step 5: Test With Real Workload

```bash
# On a test machine that uses the runner:
# 1. Start a MongoDB container
docker run -d --name test-mongo -p 27017:27017 mongo:latest

# 2. From macOS runner, verify port forward exists
ssh runner@macos-runner-01 'lsof -i :27017'
# Should show SSH process listening

# 3. Test connection from runner
ssh runner@macos-runner-01 'nc -zv localhost 27017'
# Should succeed

# 4. Stop container
docker stop test-mongo
docker rm test-mongo

# 5. Verify port forward removed
ssh runner@macos-runner-01 'lsof -i :27017'
# Should be empty
```

---

## Success Criteria

### Logs (macos-runner-01)

✅ **Healthy Operation**:
```
level=INFO msg="Ensuring SSH ControlMaster is alive before starting event stream" attempt=1
level=INFO msg="DIAGNOSTIC: SSH ControlMaster verified healthy before stream start"
level=INFO msg="DIAGNOSTIC: Docker daemon connectivity validated" docker_version=24.0.6
level=INFO msg="DIAGNOSTIC: Starting Docker events stream" attempt_number=1
level=INFO msg="docker events stream started" startup_duration_ms=34 pid=12345
level=INFO msg="DIAGNOSTIC: First Docker event received" time_since_start_ms=178
```

❌ **Failure Pattern (should NOT see)**:
```
Warning: Permanently added '192.168.1.84' (ED25519) to the list of known hosts.
Error: manager error: event stream failed after 10 consecutive failures
```

### SSH Sessions (linux-docker-01)

✅ **Success**:
- 1-2 persistent sessions from 192.168.1.88
- Minimal "session opened/closed" log entries (<5 per hour)
- `netstat` shows stable connection

❌ **Failure** (should NOT see):
- Session churn (>10 per hour)
- Rapid open/close cycles
- Growing session count

### Port Forwards

✅ **Success**:
- Forwards created within 2 seconds of container start
- Forwards persist for container lifetime
- Clean removal on container stop
- `rdhpf status` shows accurate state

---

## Rollback Procedure

If issues occur after deployment:

```bash
# SSH to runner
ssh runner@macos-runner-01

# Stop LaunchAgent
launchctl unload ~/Library/LaunchAgents/com.rdhpf.plist

# Restore old binary
sudo mv /usr/local/bin/rdhpf.backup /usr/local/bin/rdhpf

# Start LaunchAgent
launchctl load ~/Library/LaunchAgents/com.rdhpf.plist
```

For SSH server config rollback:

```bash
ssh docker@linux-docker-01

# Comment out the lines in /etc/ssh/sshd_config:
# ClientAliveInterval 60
# ClientAliveCountMax 3

sudo systemctl reload sshd
```

---

## Monitoring Post-Deployment

### Day 1-3: Active Monitoring

```bash
# Check logs every 4 hours
ssh runner@macos-runner-01 'tail -100 ~/Library/Logs/rdhpf/stdout.log | grep -E "(DIAGNOSTIC|consecutive_failures|event stream failed)"'

# Monitor SSH sessions on Docker host
ssh docker@linux-docker-01 'sudo journalctl -u ssh --since "1 hour ago" | grep "session.*192.168.1.88" | wc -l'
# Should be <10 per hour
```

### Week 1: Daily Check

```bash
# Check for any "consecutive_failures"
ssh runner@macos-runner-01 'grep "consecutive_failures" ~/Library/Logs/rdhpf/stdout.log | tail -20'

# Check log size growth (should be much slower now)
ssh runner@macos-runner-01 'ls -lh ~/Library/Logs/rdhpf/stdout.log'
```

### Alert Conditions

Set up alerts if monitoring shows:
- ❌ `consecutive_failures > 5` in logs
- ❌ `stdout.log` growing >100MB/day (was 4.2GB, should be <50MB/day)
- ❌ SSH session count >20/hour on Docker host
- ❌ "Permanently added" warnings in stderr.log

---

## Expected Improvements

| Metric | Before Fix | After Fix | Improvement |
|--------|-----------|-----------|-------------|
| SSH Sessions/hour | ~29 | <5 | **83% reduction** |
| Event stream failures | ~10 every 30s | <1/day | **>99% reduction** |
| Log growth rate | 4.2GB/unknown time | <50MB/day | **~98% reduction** |
| Test reliability | ~60% pass | >95% pass | **35% improvement** |
| ControlMaster stability | Dies frequently | Persistent | **Stable** |

---

## Troubleshooting

### Issue: "Failed to ensure SSH ControlMaster is alive"

**Cause**: Can't create/maintain ControlMaster

**Check**:
```bash
ssh runner@macos-runner-01 'ls -l /tmp/rdhpf-*.sock'
ssh runner@macos-runner-01 'ssh -S /tmp/rdhpf-*.sock -O check docker@linux-docker-01'
```

**Fix**: Check SSH key permissions, network connectivity

### Issue: Stream still failing after fix

**Check logs for**:
```bash
ssh runner@macos-runner-01 'grep "DIAGNOSTIC.*stream scanner finished" ~/Library/Logs/rdhpf/stdout.log | tail -5'
```

**Look for**:
- `total_duration_ms` < 1000 = stream dying too fast
- `events_received` = 0 = not receiving events
- `time_to_first_event_ms` > 5000 = slow startup

### Issue: SSH sessions still churning

**Verify**:
1. SSH server config applied: `ssh docker@linux-docker-01 'sudo sshd -T | grep ClientAlive'`
2. Binary is new version: `ssh runner@macos-runner-01 '/usr/local/bin/rdhpf --version'`
3. LaunchAgent restarted: `ssh runner@macos-runner-01 'launchctl list | grep rdhpf'`

---

## References

- Diagnostic Analysis: [`docs/event-stream-failure-diagnosis.md`](event-stream-failure-diagnosis.md)
- Code Changes: Git commit with these fixes
- SSH Logs: `ssh docker@linux-docker-01 'sudo journalctl -u ssh'`
- rdhpf Logs: `ssh runner@macos-runner-01 '~/Library/Logs/rdhpf/'`