# User Guide: Remote Docker Host Port Forwarder (rdhpf)

This guide helps you install, configure, and use rdhpf for remote Docker development and CI.

- [Getting Started](#getting-started)
- [Core Concepts](#core-concepts)
- [Common Workflows](#common-workflows)
- [Configuration Reference](#configuration-reference)
- [Advanced Usage](#advanced-usage)
- [Best Practices](#best-practices)
- [See Also](#see-also)

## Getting Started

### Prerequisites

- Local machine with OpenSSH client (ssh) on PATH
- Remote Docker host reachable over SSH (ssh://user@host) with key-based auth
- Docker installed on the remote host
- Optional: Go 1.23+ to build from source

### Install

#### Homebrew (macOS/Linux) - Recommended

```bash
# Add the tap (first time only)
brew tap tomaszpeksa/tap

# Install rdhpf
brew install rdhpf

# Or in one command
brew install tomaszpeksa/tap/rdhpf

# Verify installation
rdhpf version
```

#### Pre-built Binaries

Download from [GitHub Releases](https://github.com/tomaszpeksa/remote-docker-host-port-forwarder/releases):

```bash
# Linux/macOS
tar xzf rdhpf_*.tar.gz
sudo mv rdhpf /usr/local/bin/

# Windows - extract zip and add to PATH
```

#### From Source (Go 1.23+)

```bash
go install github.com/tomaszpeksa/remote-docker-host-port-forwarder/cmd/rdhpf@latest
```

### First run

```bash
rdhpf run --host ssh://user@remote-host
```
- Keep this terminal running; press Ctrl+C to stop gracefully
- In another terminal you can connect to forwarded services on 127.0.0.1:PORT

Verify:
```bash
rdhpf status --host ssh://user@remote-host
```

### Stopping the tool

Press Ctrl+C to stop gracefully. The tool responds to SIGINT and SIGTERM signals.

```bash
# Ctrl+C or:
kill <rdhpf_pid>
```

Shutdown stops automatic recovery and retires the owned SSH child before releasing
publisher ownership. Individual checks and termination attempts have time limits,
but shutdown has **no total deadline** and is not guaranteed to finish within two
seconds. If child retirement cannot be proved, rdhpf retains the admission lock
and retries cleanup; see [shutdown awaiting owned-child retirement](troubleshooting.md#shutdown-awaiting-owned-child-retirement).
Containers continue running (rdhpf never affects container lifecycle).

**Important:** Do NOT use `kill -9` as a cleanup shortcut. It bypasses cleanup and
may leave the SSH child, its forwards and its private directory behind. A new
instance does not adopt or sweep those resources.

### Configuration basics

- Host is required: `--host ssh://user@host`
- Mode: Auto-discovery forwards all published container ports
- Logging: `--log-level info|debug|trace` or `RDHPF_LOG_LEVEL`

## Core Concepts

### Port forwarding model

- Local bind: `127.0.0.1:PORT`
- Remote target: `127.0.0.1:PORT` on the remote Docker host
- Only published host ports are forwarded; exposed-only ports are ignored

### Event-driven forwarding

- Listens to Docker container events over SSH and reconciles forwards automatically
- Automatically establishes and tears down SSH tunnels as containers start and stop

### SSH ControlMaster multiplexing

- A foreground SSH child is directly owned (`ControlMaster=yes`, `ForkAfterAuthentication=no`, `ControlPersist=no`)
- Keep-alives maintain connectivity; health monitor recreates session on failure

### State and reconciliation

- Desired vs actual: the reconciler computes add/remove actions to converge state
- Container-batched operations: all of a container's ports are added/removed together
- Last event wins: newer events supersede previous bindings during churn

## Common Workflows

### Remote development with Docker Compose

docker-compose.yml example:
```yaml
services:
  web:
    image: nginx
    ports:
      - "8080:80"
  db:
    image: postgres
    environment:
      - POSTGRES_PASSWORD=dev
    ports:
      - "5432:5432"
```
Run rdhpf:
```bash
rdhpf run --host ssh://user@remote-host
```
Access services:
- http://127.0.0.1:8080
- `psql -h 127.0.0.1 -p 5432 -U postgres postgres`

### Database containers

Access databases running in Docker containers with published ports:
```bash
# PostgreSQL container
rdhpf run --host ssh://db.example.com
psql -h 127.0.0.1 -p 5432 -U myuser mydb
```

### One active publisher per status directory

**Breaking change for concurrent runs:** Only one `rdhpf run` may use the same
actual `HOME/.rdhpf` directory at a time, even for different SSH targets. A duplicate
fails before SSH startup or status changes. Directory aliases share admission. This is normally one instance per
local user, not machine-wide cross-user exclusion. Distinct directories do not
share admission, but their local forwarding ports can still conflict.

The persistent `HOME/.rdhpf/owner.lock` must not be deleted or replaced. Ownership
requires local Linux/macOS storage; network/shared/FUSE filesystems are outside
this contract. **Before upgrading, stop all old-version publishers using that
directory and wait for their shutdown to complete**: they do not honor the lock
and cannot be fenced by the new version. Status filenames, target selection and
output schemas are unchanged.

SSH uses a short private directory per Master, with one stable socket path across
connection recovery. Other instances and cleanup tools must leave active
namespaces (including ancestor paths and lock files) alone. Permissions and
random names do not protect against malicious same-UID namespace replacement.
Normal cleanup removes the owned SSH directory; forced parent termination may
leave it and an SSH child behind. Recovery from an SSH-child crash while the
parent remains alive is separate and automatic.

## Configuration Reference

### CLI flags (rdhpf run)

- `--host` string (required): SSH host in format `ssh://user@host`
- `--log-level` string (default: `info`): `trace`, `debug`, `info`, `warn`, `error`
- `--trace` (boolean): enable maximum verbosity (equivalent to `--log-level trace`)

### CLI flags (rdhpf status)

- `--host` string (required): SSH host in format `ssh://user@host`
- `--format` string (default: `table`): `table`, `json`, `yaml`

### Environment variables

- `RDHPF_LOG_LEVEL=debug`

### Exit codes

- `0`: normal termination
- `non-zero`: unrecoverable error reported in logs

### SSH configuration requirements

- Key-based authentication via ssh-agent or private key
- Known host key present; StrictHostKeyChecking is enabled by default
- OpenSSH client available on PATH

## Advanced Usage

### Debug and trace logging

```bash
rdhpf run --host ssh://user@host --log-level debug
rdhpf run --host ssh://user@host --trace
```
Logs are structured; sensitive values are redacted. Use debug to diagnose port conflicts and reconciling actions.

### Performance tuning

Ensure local ports are free; conflicts cause backoff retries.

### Run as a systemd service

Create `/etc/systemd/system/rdhpf.service`:
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

Enable and start:
```bash
sudo systemctl daemon-reload
sudo systemctl enable rdhpf
sudo systemctl start rdhpf
```

### CI integration example (GitHub Actions)

```yaml
- name: Start port forwarder
  run: nohup rdhpf run --host ssh://user@host & echo $! > rdhpf.pid
- name: Run tests
  run: make test
- name: Show status
  run: rdhpf status --host ssh://user@host --format json
- name: Stop port forwarder
  if: always()
  run: kill $(cat rdhpf.pid) || true
```

## Best Practices

### Security

- rdhpf binds only on `127.0.0.1` by design
- Do not weaken SSH host key checking in production
- Logs are structured with redaction, but review before sharing

### Monitoring

- Use `rdhpf status` to verify active forwards
- On failures, run with `--log-level debug` or `--trace`

## See Also

- Main README: ../README.md
- Troubleshooting: ./troubleshooting.md
- CI/CD: ./ci-cd.md
- Architecture: ./architecture.md