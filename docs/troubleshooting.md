# Troubleshooting Guide

This document covers common issues and their solutions when using rdhpf.

## Table of Contents

- [Graceful Shutdown](#graceful-shutdown)
- [TIME_WAIT State After Port Forward Removal](#time_wait-state-after-port-forward-removal)
- [Port Already in Use](#port-already-in-use)
- [SSH Connection Issues](#ssh-connection-issues)
- [Container Ports Not Forwarded](#container-ports-not-forwarded)
- [Orphaned SSH Processes](#orphaned-ssh-processes)
- [State Persistence on Crash](#state-persistence-on-crash)

## Graceful Shutdown

### Overview

rdhpf implements graceful shutdown to ensure clean termination and resource cleanup when you stop the tool.

### How to Stop rdhpf

**Recommended Methods (Graceful):**

```bash
# Method 1: Press Ctrl+C in the terminal
# - Sends SIGINT signal
# - Triggers graceful shutdown
# - Cleans up all resources

# Method 2: Send SIGTERM signal
kill <rdhpf_pid>
```

**NOT Recommended:**

```bash
# ❌ DO NOT USE kill -9 (SIGKILL)
kill -9 <rdhpf_pid>  # Bypasses cleanup, may orphan SSH processes
```

### What Happens During Shutdown

When you press Ctrl+C or send SIGTERM:

1. **Signal Received** (< 1ms)
   - Tool logs: `received signal, shutting down`
   - Context cancellation triggered

2. **Event Stream Stops** (< 100ms)
   - Docker events stream gracefully closed
   - SSH process for event monitoring terminated

3. **Forwards Removed** (< 1s)
   - Tool logs: `shutdown initiated, cleaning up forwards`
   - All active port forwards are removed via SSH
   - Local ports are released

4. **SSH Master Closed** (< 500ms)
   - Tool logs: `closing SSH ControlMaster connection`
   - ControlMaster receives `ssh -O exit` command
   - Control socket file removed

5. **Clean Exit** (< 2s total)
   - Tool logs: `shutdown complete, all forwards removed`
   - Tool logs: `rdhpf stopped`
   - Process exits with code 0 (success)

### Verification

After shutdown, verify clean state:

```bash
# Check no rdhpf processes remain
ps aux | grep rdhpf
# Should show nothing (except the grep command itself)

# Check no orphaned SSH processes
ps aux | grep "ssh.*ControlMaster"
# Should show nothing

# Check no orphaned control sockets
ls /tmp/rdhpf-*.sock 2>/dev/null
# Should show nothing

# Verify exit code was 0
echo $?
# Should be 0
```

### Timing Guarantees

- **Signal → Context canceled**: Immediate (< 1ms)
- **Event loop exit**: < 100ms after context cancellation
- **All forwards removed**: < 1s total
- **SSH ControlMaster closed**: < 500ms
- **Complete shutdown**: < 2s total

### Troubleshooting

**If shutdown seems to hang:**

1. Check logs for errors (run with `--log-level debug`)
2. Give it 10 seconds (cleanup has a 10-second timeout)
3. If still hanging after 10s, there may be a bug - please report it

**If orphaned processes remain:**

This should NOT happen with graceful shutdown (Ctrl+C or `kill`). If it does:

1. Clean up manually (see [Orphaned SSH Processes](#orphaned-ssh-processes))
2. Report the issue with logs and reproduction steps

**Expected vs Actual:**

| Scenario | Expected | Actual (if different = bug) |
|----------|----------|----------------------------|
| Ctrl+C (SIGINT) | Exit code 0, clean logs, no orphans | Report if different |
| kill (SIGTERM) | Exit code 0, clean logs, no orphans | Report if different |
| kill -9 (SIGKILL) | May leave orphans (expected) | Use manual cleanup |
| Crash/panic | May leave orphans (expected) | Use manual cleanup |

---

## TIME_WAIT State After Port Forward Removal

### Symptom

After stopping a container or shutting down rdhpf, you see "port already in use" errors when trying to quickly restart, even though the forward has been removed.

```
ERROR: failed to establish forward: bind: address already in use
```

### Explanation

This is **normal TCP behavior**, not a bug. When a TCP connection is closed, the local port enters a `TIME_WAIT` state for approximately 60 seconds. This is part of the TCP protocol specification (RFC 793) to ensure proper connection termination and prevent packet confusion.

You can verify this state with:
```bash
netstat -an | grep TIME_WAIT | grep :8080
# or on macOS:
lsof -i :8080 | grep TIME_WAIT
```

### Why This Happens

1. **Forward Removal**: When rdhpf removes a port forward, SSH closes the listening socket
2. **TCP Cleanup**: The OS moves the port to TIME_WAIT state (60-120 seconds)
3. **Quick Restart**: Trying to bind the same port immediately fails
4. **Automatic Resolution**: After ~60s, the port becomes available again

### Solutions

#### Option 1: Wait (Recommended)

Simply wait 60 seconds before restarting. The TIME_WAIT period ensures clean connection termination.

#### Option 2: Use Different Ports Temporarily

If you need immediate access:
```bash
# Instead of restarting on the same port:
docker run -p 8081:80 myapp  # Use 8081 instead of 8080
```

#### Option 3: SO_REUSEADDR (Automatic)

rdhpf's SSH implementation already uses `SO_REUSEADDR` through SSH's port forwarding, which allows binding to a port in TIME_WAIT state. However, the TIME_WAIT period still applies at the OS level.

### Not a Bug

This behavior is by design and provides important TCP reliability guarantees:
- Ensures all packets from the old connection are received or expired
- Prevents delayed packets from interfering with new connections
- Standard across all TCP implementations (Linux, macOS, Windows)

### Prevention

If frequent restarts are needed (e.g., during development), consider:
1. **Longer-lived Containers**: Restart app inside container instead of container itself
2. **Development Workflow**: Accept the 60s delay as part of clean shutdown
3. **Manual SSH Tunneling**: For persistent static tunnels, use `ssh -L` directly

---

## Port Already in Use

### Symptom

```
ERROR: Port 8080 conflict: address already in use
```

### Diagnosis

Check what's using the port:
```bash
# Linux:
sudo lsof -i :8080
sudo netstat -tlnp | grep :8080

# macOS:
lsof -i :8080
netstat -an | grep LISTEN | grep :8080
```

### Solutions

1. **Stop the Conflicting Process**:
   ```bash
   # Find the PID
   lsof -i :8080
   # Kill it
   kill <PID>
   ```

2. **Use a Different Port**:
   ```bash
   # Start container on different host port
   docker run -p 8081:80 myapp
   ```

3. **Check for Previous rdhpf Instance**:
   ```bash
   ps aux | grep rdhpf
   # Kill any orphaned instances
   pkill rdhpf
   ```

### Automatic Recovery

rdhpf logs conflicts and continues forwarding other ports. Once the conflicting process releases the port, rdhpf will automatically retry (with exponential backoff).

---

## SSH Connection Issues

### Symptom

```
ERROR: SSH ControlMaster check failed
ERROR: failed to recreate SSH ControlMaster
```

### Auto-Recovery

rdhpf includes automatic recovery:
- Health checks every 30 seconds
- Automatic reconnection on failure
- Circuit breaker prevents retry storms (opens after 5 failures)

### Manual Troubleshooting

1. **Test SSH Connection Manually**:
   ```bash
   ssh user@remotehost docker ps
   ```

2. **Check SSH Agent**:
   ```bash
   ssh-add -l  # List keys
   ssh-add ~/.ssh/id_rsa  # Add key if needed
   ```

3. **Verify Host Key**:
   ```bash
   ssh-keyscan remotehost >> ~/.ssh/known_hosts
   ```

4. **Clean Up Stale Control Sockets**:
   ```bash
   rm -f /tmp/rdhpf-*.sock
   pkill -f "ssh.*ControlMaster"
   ```

### Circuit Breaker Behavior

After 5 consecutive connection failures:
- Circuit "opens" (fast-fail mode)
- Waits 60 seconds (cooldown period)
- Attempts one "half-open" retry
- On success: resumes normal operation
- On failure: re-opens circuit

This prevents resource exhaustion from endless retry loops.

---

## Container Ports Not Forwarded

### Symptom

Container is running but its ports aren't forwarded to localhost.

### Check: Published vs. Exposed Ports

**rdhpf only forwards PUBLISHED ports (with `-p` flag), not EXPOSED ports:**

```bash
# ✅ This WILL be forwarded (published):
docker run -p 8080:80 nginx

# ❌ This will NOT be forwarded (exposed only):
docker run nginx  # nginx EXPOSES port 80 but doesn't publish it
```

**Why?** Exposed-only ports aren't accessible on the Docker host, so there's nothing to forward.

### Verify Published Ports

```bash
# Check what ports Docker is actually publishing:
docker ps
# Look for "0.0.0.0:8080->80/tcp" in the PORTS column

# Or inspect a specific container:
docker inspect <container_id> --format '{{json .HostConfig.PortBindings}}'
```

### Solutions

1. **Publish the Port**:
   ```bash
   docker run -p 8080:80 myapp
   ```

2. **Check rdhpf Logs**:
   ```bash
   rdhpf run --host ssh://user@host --log-level debug
   ```
   Look for messages like:
   ```
   container ports discovered: containerID=abc123 ports=[8080, 9090]
   ```

---

## Orphaned SSH Processes

### Symptom

SSH processes remain after stopping rdhpf:
```bash
$ ps aux | grep ssh | grep ControlMaster
user  12345  ssh -MNf -o ControlMaster=auto ...
```

### Prevention

rdhpf automatically cleans up SSH processes on shutdown:
1. Sends `ssh -O exit` to ControlMaster
2. Waits 1 second for graceful termination
3. Sends SIGTERM if process still exists
4. Removes control socket file

### Manual Cleanup

If cleanup fails (e.g., after `kill -9` on rdhpf):

```bash
# Find SSH master processes
ps aux | grep ssh | grep ControlMaster

# Kill them
pkill -f "ssh.*ControlMaster"

# Clean up control sockets
rm -f /tmp/rdhpf-*.sock
```

### Preventing Orphans

- Use Ctrl+C (SIGINT) or `kill <pid>` (SIGTERM) instead of `kill -9`
- rdhpf registers signal handlers for graceful shutdown
- Avoid force-killing the process

---

## State Persistence on Crash

### Limitation

**rdhpf maintains state in memory only.** If the process crashes (e.g., `kill -9`, panic, OOM killer), established port forwards may remain orphaned until they timeout.

### Why In-Memory Only?

Version 1.0 prioritizes simplicity and reliability:
- No complex state file management
- No file locking or corruption issues
- No sync/recovery logic needed
- Process restarts are fast and clean

### Impact

On unclean shutdown (crash, `kill -9`):
- Port forwards may remain active (orphaned)
- SSH ControlMaster may remain running
- Manual cleanup may be needed

### Manual Recovery

```bash
# 1. Kill any orphaned SSH processes
pkill -f "ssh.*ControlMaster.*rdhpf"

# 2. Remove control sockets
rm -f /tmp/rdhpf-*.sock

# 3. Check for orphaned port forwards (rare, but possible)
netstat -tlnp | grep ssh  # Look for SSH forwarding processes

# 4. Restart rdhpf
rdhpf run --host ssh://user@remotehost
```

### Production Deployment

For resilience in production:

1. **Use a Process Supervisor** (systemd, supervisord):
   ```ini
   [Unit]
   Description=rdhpf - Remote Docker Host Port Forwarder
   After=network.target

   [Service]
   Type=simple
   User=youruser
   ExecStart=/usr/local/bin/rdhpf run --host ssh://user@dockerhost
   Restart=always
   RestartSec=10

   [Install]
   WantedBy=multi-user.target
   ```

2. **Health Monitoring**: Monitor rdhpf process and restart if it stops

3. **Accept Limitations**: For v1.0, in-memory state is acceptable. Future versions may add state persistence if needed.

### Future Enhancement

State persistence to disk is deferred to a future version because:
- Adds complexity (file locking, corruption handling, recovery logic)
- Most use cases don't require it (development workflows)
- Process supervisors (systemd) provide automatic restart
- Current design prioritizes reliability and simplicity

---

## FAQ

### Q: Can I forward the same port from multiple containers?

**A:** No. SSH can only bind a local port once. If multiple containers publish the same host port, only the first one will be forwarded. Others will show "conflict" status.

**Solution:** Use different host ports:
```bash
docker run -p 8080:80 app1
docker run -p 8081:80 app2
```

### Q: Does rdhpf work with Docker Compose?

**A:** Yes! rdhpf forwards any published ports from any container:
```yaml
# docker-compose.yml
services:
  web:
    ports:
      - "8080:80"  # Will be forwarded
  db:
    ports:
      - "5432:5432"  # Will be forwarded
```

### Q: Can I use rdhpf with Kubernetes?

**A:** Not directly. rdhpf is designed for Docker hosts. For Kubernetes, use `kubectl port-forward` instead.

### Q: What happens if my SSH connection drops?

**A:** rdhpf automatically:
1. Detects the failure (health check every 30s)
2. Attempts to reconnect (with exponential backoff)
3. Re-establishes all forwards after reconnection
4. Logs the recovery process

### Q: Can I forward ports from containers on different Docker hosts?

**A:** You'll need separate rdhpf instances:
```bash
# Terminal 1: Forward from host1
rdhpf run --host ssh://user@host1

# Terminal 2: Forward from host2 (will fail if ports conflict)
rdhpf run --host ssh://user@host2
```

But be careful about port conflicts!

---

## Getting Help

If you encounter an issue not covered here:

1. **Check Logs**: Run with `--log-level debug` for verbose output
2. **Search Issues**: Check [GitHub Issues](https://github.com/youruser/rdhpf/issues)
3. **File a Bug**: Include logs, Docker version, SSH version, and steps to reproduce
4. **Ask for Help**: Open a discussion or issue on GitHub

---

## Common Error Messages

| Error | Meaning | Solution |
|-------|---------|----------|
| `address already in use` | Port conflict or TIME_WAIT | Wait 60s or see [Port Already in Use](#port-already-in-use) |
| `SSH ControlMaster check failed` | SSH connection dropped | Auto-recovers; check SSH connectivity |
| `circuit breaker open` | Too many SSH failures | Wait 60s cooldown; check SSH config |
| `context canceled` | Normal shutdown | None needed (expected during Ctrl+C) |
| `container not found` | Container stopped/removed | None needed (rdhpf removes forward automatically) |
| `no published ports` | Container has no `-p` flags | Add `-p` flags to container |

---

**Last Updated**: 2024-01-15
**Version**: 1.0.0