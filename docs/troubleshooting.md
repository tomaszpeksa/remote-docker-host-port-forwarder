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

# Inspect only the exact control_path recorded in this instance's logs.
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
   Identify the owner from your process/service records and stop that instance
   gracefully. Do not kill every process matching the program name; other status
   directories/users can have independent owners.

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
- Health checks every 15 seconds
- Automatic reconnection on failure
- Circuit breaker prevents retry storms (opens after 5 failed connection recreations)

An internal health check has a two-second deadline. If that check times out,
rdhpf can retire and replace its SSH master even while a separate probe still
reaches the master. A slow check subprocess can therefore interrupt the existing
connection; a successful replacement restores forwarding through reconciliation.
This is the current timeout policy, not a measured outage duration or a guarantee
of recovery within two seconds.

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

4. **Inspect Owned Control Sockets**: use the exact private `control_path` in
   this instance's logs. Do not use wildcard removal or broad process matching.
   A live parent automatically recovers its own crashed SSH child.

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
user  12345  ssh -MN -o ControlMaster=yes -o ControlPersist=no ...
```

### Prevention

rdhpf automatically cleans up SSH processes on shutdown:
1. Fences recovery and signals its retained foreground SSH child
2. Waits for retirement, escalating to Kill if needed
3. Reaps that child before reclaiming its private socket artifacts
4. Removes the empty owned directory; uncertainty is reported, not adopted

### Forced termination and cleanup refusal

Forced termination (for example, `kill -9` on rdhpf) may leave an idle SSH child,
its forwards and its private directory behind indefinitely. A new instance uses a
fresh SSH directory; it does not adopt or sweep the old resources, and old
forwards may still occupy local ports.

Retain the logs identifying the former instance, its child and its exact private
path when investigating. A pathname or a PID alone is not proof of current
ownership. Do not remove active directories, their ancestors, publication paths
or `~/.rdhpf/owner.lock`; never use wildcard `rm` or `pkill` recipes.

A live foreign endpoint or an unknown directory entry causes cleanup refusal and
may leave the private directory behind. This is distinct from uncertain child
retirement: the CLI logs unrelated cleanup errors rather than retrying them as
retirement uncertainty. Neither refusal authorizes deleting the occupant.

### Shutdown awaiting owned-child retirement

If shutdown reports `waiting for owned SSH retirement; retaining publisher ownership`,
the CLI has not yet established completion of its retained SSH child. It keeps the
publisher admission lock and retries cleanup rather than admitting a successor or
abandoning the child. Individual termination attempts are bounded, but there is
**no total shutdown deadline**. Repeated Ctrl+C is not a forced-exit mechanism;
forced parent termination still has the orphan-child limitation described above.
Do not delete the lock to bypass admission.

### Preventing Orphans

- Use Ctrl+C (SIGINT) or `kill <pid>` (SIGTERM) instead of `kill -9`
- rdhpf registers signal handlers for graceful shutdown
- Avoid force-killing the process

---

## State Persistence on Crash

### Limitation

**Live forwarding ownership is maintained in memory.** Status snapshots are
published to disk, but they are not a restart journal and do not authorize
adopting an old SSH child. After a crash (for example, `kill -9`, panic or OOM),
established forwards may remain orphaned; idle-child expiry is not guaranteed.

### Status snapshots are not recovery state

A new run discovers containers and creates a new SSH connection. The publisher
lock excludes cooperating new-version runs in the same status directory, but it
does not reclaim a previous process's orphaned SSH resources.

### Impact

On unclean shutdown (crash, `kill -9`):
- Port forwards may remain active (orphaned)
- SSH ControlMaster may remain running
- Manual cleanup may be needed

### Restart limitations

Review the [forced-termination limitations](#forced-termination-and-cleanup-refusal)
before restarting. A new run uses a fresh SSH directory; an old orphan can still
hold a local forwarding port.
Duplicate admission protects cooperating publishers in the same status directory,
not arbitrary external or same-UID namespace replacement.

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
1. Checks the connection every 15 seconds
2. Attempts replacement on failure, subject to the circuit breaker's cooldown
3. Re-establishes all forwards after reconnection
4. Logs the recovery process

### Q: Can I forward ports from containers on different Docker hosts?

**A:** Only one `rdhpf run` may publish in the same actual `HOME/.rdhpf`
directory at a time, even for different SSH targets. To change targets, stop the
current run gracefully and wait for its shutdown to complete before starting the
next one. Before upgrading, also stop old-version publishers using that directory:
they do not honor the admission lock. Do not delete or replace `owner.lock` to
bypass this restriction.

This is normally one publisher per local user, not a machine-wide cross-user
lock. See [one active publisher per status directory](user-guide.md#one-active-publisher-per-status-directory)
for the supported ownership and migration boundaries.

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
