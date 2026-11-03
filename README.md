# Remote Docker Host Port Forwarder (rdhpf)

[![CI](https://github.com/tomaszpeksa/remote-docker-host-port-forwarder/actions/workflows/ci.yml/badge.svg)](https://github.com/tomaszpeksa/remote-docker-host-port-forwarder/actions/workflows/ci.yml) [![License: MIT](https://img.shields.io/badge/License-MIT-yellow.svg)](LICENSE) [![Go](https://img.shields.io/badge/go-1.27.3%2B-00ADD8?logo=go)](go.mod) [![Coverage](https://img.shields.io/badge/coverage-%E2%80%94-informational.svg)](docs/ci-cd.md)

A lightweight CLI that automatically forwards published container ports from a remote Docker host (ssh://) to your local machine via SSH. When containers on the remote host publish ports, rdhpf makes those services available on your localhost so apps that assume "localhost" keep working unchanged.

rdhpf is designed for developers and CI environments using a remote DOCKER_HOST (over SSH). It discovers published ports, maintains idempotent SSH tunnels, self-heals on failures, and provides a status command with structured output.


## Features

- Auto-discovery of published container ports (event-driven via Docker events)
- Automatic reconnection and self-healing (ControlMaster health checks, circuit breaker)
- Clear conflict handling when local ports are in use (actionable messages and backoff)
- Status reporting via `rdhpf status` (table/json/yaml)
- Structured logging with debug and trace modes with redaction


## Quick Start

```bash
# Installation (macOS/Linux via Homebrew, after the cask tap migration)
brew install --cask tomaszpeksa/tap/rdhpf

# Basic usage
rdhpf run --host ssh://user@remote-docker-host

# Check status
rdhpf status --host ssh://user@example.com
```


## Installation

### Homebrew (macOS/Linux) - Recommended

```bash
# Add the tap (first time only)
brew tap tomaszpeksa/tap

# Install rdhpf (after the cask is published to the tap)
brew install --cask rdhpf

# Or install in one command
brew install --cask tomaszpeksa/tap/rdhpf

# Verify installation
rdhpf version

# Upgrade to latest version
brew upgrade rdhpf
```

Use a current Homebrew with Linux/macOS binary-cask support. The separate tap
transition must be published before these cask commands work. Existing formula
users should follow the [migration guide](docs/homebrew.md#migrating-from-the-formula).
Normal OS security checks remain enabled; unsigned macOS binaries may be blocked
on first launch. No quarantine bypass is installed. Older Homebrew clients and
installed migration/first-launch behavior have not been validated.

### Pre-built Binaries

Download from [GitHub Releases](https://github.com/tomaszpeksa/remote-docker-host-port-forwarder/releases):

```bash
# Linux/macOS
tar xzf rdhpf_*.tar.gz
sudo mv rdhpf /usr/local/bin/

# Windows
# Extract the zip file and add rdhpf.exe to your PATH
```

### From Source (Go 1.27.3+)

New macOS binaries require macOS 13+. Repository builds use the pinned
[project tools](CONTRIBUTING.md#initial-setup); no global mise settings change is needed.

```bash
# Using go install
go install github.com/tomaszpeksa/remote-docker-host-port-forwarder/cmd/rdhpf@latest

# Or clone and build with Make
git clone https://github.com/tomaszpeksa/remote-docker-host-port-forwarder.git
cd remote-docker-host-port-forwarder
# Install the pinned base tool set as described in CONTRIBUTING.md.
MISE_LOCKED_SCOPES=project mise install --locked core:go aqua:golangci/golangci-lint aqua:jqlang/jq
mise exec -- make build
sudo mv build/rdhpf /usr/local/bin/
```


## Usage Examples

- Basic auto-discovery (all published ports)
  ```bash
  rdhpf run --host ssh://user@host
  ```

- With Docker Compose (remote host)
  ```yaml
  # docker-compose.yml (publishes ports on the remote host)
  services:
    web:
      ports:
        - "8080:80"
    db:
      ports:
        - "5432:5432"
  ```
  ```bash
  # Forward all published ports from the remote host
  rdhpf run --host ssh://user@remote.host
  ```

- Debug mode
  ```bash
  rdhpf run --host ssh://user@host --log-level debug
  # or maximum verbosity:
  rdhpf run --host ssh://user@host --trace
  ```

- Status checking
  ```bash
  rdhpf status --host ssh://user@host
  rdhpf status --host ssh://user@host --format json
  rdhpf status --host ssh://user@host --format yaml
  ```


## Configuration

rdhpf is primarily configured via CLI flags with a small set of optional environment variables.

- CLI flags (`rdhpf run`):
  - `--host` string: SSH host in format `ssh://user@host` (required)
  - `--log-level` string: Log level: `trace`, `debug`, `info`, `warn`, `error` (default: `info`)
  - `--trace`: Shortcut to maximum verbosity (equivalent to `--log-level trace`)

- CLI flags (`rdhpf status`):
  - `--host` string: SSH host in format `ssh://user@host` (required)
  - `--format` string: Output format: `table`, `json`, `yaml` (default: `table`)

- Environment variables:
  - `RDHPF_LOG_LEVEL`: One of `trace`, `debug`, `info`, `warn`, `error`

- Config file:
  - Not implemented in v1.0.0


## Documentation

- User Guide: [docs/user-guide.md](docs/user-guide.md)
- Homebrew Tap Guide: [docs/homebrew.md](docs/homebrew.md)
- Troubleshooting Guide: [docs/troubleshooting.md](docs/troubleshooting.md)
- Integration Testing Guide: [docs/integration-testing.md](docs/integration-testing.md)
- CI/CD Documentation: [docs/ci-cd.md](docs/ci-cd.md)
- Contributing Guide: [CONTRIBUTING.md](CONTRIBUTING.md)


## Requirements

- Go 1.27.3+ (to build from source)
- OpenSSH client available on PATH
- SSH access to the remote Docker host (`ssh://user@host`), with keys/agent configured and host key known
- Docker installed on the remote host (for auto-discovery mode)


## SSH trust

rdhpf inherits your OpenSSH trust sources and checking policy, and forces
`BatchMode=yes` on every application SSH launch. The default policy rejects unknown
hosts without prompting; provision verified host keys and usable keys/agent first.
Explicit `accept-new` can persist enrollment in your configured known-host file.
See [SSH trust and authentication](docs/user-guide.md#ssh-trust-and-noninteractive-authentication)
for alternate authorities, weaker policies and external-helper limits.

## Why rdhpf?

Many developer tools assume dependencies are reachable on localhost. With a remote DOCKER_HOST, published ports live on the remote machine, not on your local workstation. rdhpf bridges that gap by automatically establishing SSH tunnels for published ports so your normal workflow continues to function without code or compose changes.

The system is event-driven, idempotent, and resilient: it uses a single SSH ControlMaster session, listens to Docker events, reconciles desired vs actual forwards, and self-heals on connection loss. Conflicts are surfaced clearly and other ports continue operating.


## License

MIT — see [LICENSE](LICENSE)


## Acknowledgements

Built with Go and OpenSSH. Inspired by the pain of manual SSH tunneling on CI and remote dev machines.
