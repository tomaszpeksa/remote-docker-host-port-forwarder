# Changelog

All notable changes to this project are documented here.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.1.0/).
Versions follow [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## [Unreleased]

## [0.2.0] - 2026-11-12

### Upgrade notes

- **Before starting the upgraded application, stop older instances that use the
  same actual `HOME/.rdhpf` directory.** Only one `rdhpf run` may publish there,
  even for different SSH targets. Older versions do not honor the new lock.
  Do not delete or replace `~/.rdhpf/owner.lock` to bypass it.
- **Check SSH access before starting the application.** Configured host-key pins
  and checking policies are respected. All application SSH commands run without
  interactive prompts; unknown hosts under the default policy and credentials
  requiring a prompt fail. Enroll host keys and configure noninteractive
  credentials through OpenSSH first.
- **Configure SSH identities through OpenSSH.** The application no longer uses
  `SSH_TEST_KEY_PATH`; use `IdentityFile` or an SSH agent. Test fixtures still
  support the test-only variable.
- **Event-health checks use the application's SSH target.** They no longer use
  local Docker host, context or TLS settings.
- Source builds require **Go 1.27.4 or newer**. New macOS binaries require
  **macOS 13 or newer**. Linux and macOS, on amd64 and arm64, remain the supported
  release targets.
- Homebrew distribution changes from a formula to a binary cask. See the
  [Homebrew guide](docs/homebrew.md) for migration and installation checks.
  Normal OS security remains enabled; unsigned macOS binaries may be blocked on
  first launch. Do not disable quarantine or Gatekeeper to bypass a block.

### Changed

- Manage only application-created local TCP forwards, ignoring SSH-configured
  forwards and tunnels while preserving host aliases, authentication and trust.
- Give each SSH master a private control socket and own its child process
  directly. Refuse occupied or ambiguous control endpoints instead of adopting
  them. A forced kill may leave a child or private directory behind; new
  instances do not adopt or sweep those resources.
- Apply a shared ten-second shutdown budget across signals, startup failures,
  worker joins and status cleanup. Cleanup errors or overruns produce a failing
  exit status. Shutdown can exceed the budget while waiting to prove owned
  resources have retired; it is not a forced-exit deadline.
- Use safe diagnostic summaries instead of logging raw SSH arguments, remote
  output or configuration inputs. Trace uses the same safe content as debug.
- Resolve logging and label-port enablement before startup. Validate all supplied
  log levels; trace takes precedence over a nonempty environment setting, then
  the flag or INFO default. Label values remain same-port localhost hints.

### Fixed

- Restore desired forwards after SSH connection replacement without losing their
  logical creation times or confusing recovery with monitor shutdown.
- Use full container IDs during startup discovery, so stopping and restarting a
  preexisting container updates the same forwarding owner.
- Repair missed container discovery across startup, event-stream gaps and
  temporary inspection failures with bounded inventory audits and retries.
  Failed observations preserve known desired ports rather than removing them.
- Retain ownership of failed or uncertain forwarding operations. Retry known
  conflicts after the port is released, without requiring another Docker event.
  Use verified SSH mux replies rather than command output to confirm changes.
- Make the event watchdog cancel monitoring after 60 seconds without a
  successfully handled Docker event, checked every ten seconds. Silent streams
  and blocked work no longer prevent cancellation; cleanup can delay exit.
- Pace clean Docker event-stream restarts with a cancelable one-second delay,
  drain accepted events and join stream workers before completion.
- Bound status reads by one shared five-second budget, including at most one
  second for the socket attempt. Report invalid snapshots and failed reads
  instead of presenting empty success. Mark stale file results as unverified.
- Expire idle history after one hour and show each entry's original start time,
  retaining the 100-entry limit and existing table, JSON and YAML formats.
- Snapshot performance totals and averages under the same lock to avoid races.
- Restore clean integration-fixture builds with available, exact OpenSSH and
  Docker CLI package revisions; retain runtime version checks.

### Security

- Reject SSH destinations that could be interpreted as local command options.
- Quote Docker container operands across remote shell boundaries and reject NUL
  inputs without restricting valid names or short IDs.
- Preserve configured SSH trust sources and host-key pins on normal, recovery
  and missing-control-socket paths; never silently enroll a replacement key.
- Build with Go 1.27.4, including the fix for **GO-2026-6859** in `net/http`.

### Development

- Pin repository tools with mise and refresh application dependencies.
- Exercise production Docker parsing, SSH arguments, recovery, forwarding,
  shutdown and process isolation with owned fixtures. Integration CI treats
  unavailable prerequisites as failures and retains execution evidence.

## Earlier releases (through 0.1.9)

The following changes were already shipped before 0.2.0. Earlier changelogs kept
these entries under Unreleased without assigning individual release versions.

- Added Docker-event SSH diagnostics. The safe diagnostic summaries in 0.2.0
  replace the earlier raw command and output logging.
- Fixed remote shell expansion of Docker template arguments.
- Updated GoReleaser configuration and moved test execution to the release
  workflow.
- Published the Homebrew formula as `rdhpf`; 0.2.0 moves publishing to a cask.

## [0.1.0]

### Added

- Event-driven port forwarding based on Docker events and inspect.
- SSH ControlMaster management and idempotent reconciliation.
- Conflict handling with retry, structured logging and integration tests.
- Graceful shutdown with tunnel cleanup on SIGINT and SIGTERM.

[0.1.0]: https://github.com/tomaszpeksa/remote-docker-host-port-forwarder/releases/tag/v0.1.0
