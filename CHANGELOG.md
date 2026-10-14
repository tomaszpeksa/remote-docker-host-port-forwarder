# Changelog

All notable changes to this project will be documented in this file.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.1.0/),
and this project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## [Unreleased]

- S07A: require Go 1.27.2 for source builds and macOS 13+ for new macOS binaries;
  pin repository development tools with mise and update application dependencies.
- Migrate Homebrew publishing to Linux/macOS binary casks. Existing formula users
  need the separately published tap transition; normal OS security remains enabled,
  so unsigned macOS downloads may be blocked on first launch. See the Homebrew guide.
- Prevent SSH destination options from being interpreted as local flags across
  production launches and the integration PID helper. Inherited SSH trust and
  BatchMode changes remain deferred to S07B; they are not part of this release.

- S06 correction: isolate each SSH Master in a short private namespace and reclaim
  owned stale sockets only after child retirement. Paths remain stable within a
  Master, not across independent instances; external namespace mutation is outside
  the cooperative ownership contract.
- Admit only one publisher per actual HOME/.rdhpf directory, regardless of target,
  with a persistent owner.lock held through joined status publication/cleanup.
  Requires local Linux/macOS storage and quiesced old-version publishers.
  Forced parent death can leave a child/private directory; no orphan sweep.


### Added

- Comprehensive debug logging for docker events SSH command execution
  - Logs full SSH command with arguments before execution
  - Captures and logs stderr separately (SSH warnings, error messages)
  - Logs last 50 lines of stdout on failures
  - Logs exit codes and signal information for better debugging

### Changed

- SSH masters run as directly owned foreground children; occupied or ambiguous
  control endpoints are refused rather than adopted or removed. Forced rdhpf
  termination (for example, SIGKILL) may leave an idle SSH child alive. Normal
  shutdown and failed startup still terminate and reap the owned child; no
  parent-death supervisor or global shutdown deadline is provided.

### Fixed

- Separate SSH connection replacement from monitor shutdown, serialize it with
  forwarding transactions, and invalidate retired connection state while
  preserving desired ports and logical-forward creation times.
- **CRITICAL**: Fix shell expansion bug in docker commands executed over SSH
  - Docker template syntax `{{json .}}` was being expanded by remote shell, causing "accepts no arguments" errors
  - Now wrap all docker commands in `sh -c 'command'` with proper quoting
  - Affects: docker events, docker inspect, docker ps commands (all fixed)
  - Added unit tests to prevent regression
- Fix GoReleaser Homebrew configuration schema (folder → directory) for v2 compatibility
- Pin GoReleaser version to v2 series in release workflow for stability
- Remove duplicate test execution in GoReleaser before.hooks (tests already run in workflow)
- Set Homebrew formula name to 'rdhpf' for easier installation (was defaulting to repo name)

## [0.1.0]

### Added

- Event‑driven port forwarding based on Docker events and inspect
- SSH ControlMaster management and idempotent reconciliation
- Conflict handling with retry; structured logging; integration tests (real Docker + harness)
- Graceful shutdown (SIGINT/SIGTERM) with clean tunnel teardown
