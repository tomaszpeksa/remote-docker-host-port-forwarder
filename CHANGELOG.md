# Changelog

All notable changes to this project will be documented in this file.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.1.0/),
and this project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## [Unreleased]

- Repair missed Docker discovery with independent bounded inventory audits,
  fenced partial progress and fair timer retries; unknown observations preserve
  current desired ports instead of authorizing removal.
- Join Docker event-stream commands/readers before terminal completion, reject
  malformed short IDs safely, bound diagnostics and retain unapplied desired
  changes across EOF and unavailable transport without perpetual operation retries.

- S07A: require Go 1.27.2 for source builds and macOS 13+ for new macOS binaries;
  pin repository development tools with mise and update application dependencies.
- Migrate Homebrew publishing to Linux/macOS binary casks. Existing formula users
  need the separately published tap transition; normal OS security remains enabled,
  so unsigned macOS downloads may be blocked on first launch. See the Homebrew guide.
- Prevent SSH destination options from being interpreted as local flags across
  production launches and the integration PID helper.
- S07B: inherit configured SSH trust sources/checking policy instead of forcing
  accept-new and discarding user pins. Force BatchMode on all application SSH paths;
  default unknown hosts and unavailable noninteractive credentials now fail rather
  than prompting. Explicit accept-new enrollment remains configurable.
- Pin owned SSH/backend fixtures, provision verified owned public host keys and
  exercise real trust, prompts, missing-mux commands and recovery without weakening
  production policy.

### Added

- Comprehensive debug logging for docker events SSH command execution
  - Logs full SSH command with arguments before execution
  - Captures and logs stderr separately (SSH warnings, error messages)
  - Logs last 50 lines of stdout on failures
  - Logs exit codes and signal information for better debugging

### Changed

- **BREAKING CHANGE:** Event-health pings use the application's SSH target and
  noninteractive trust policy, not local Docker host/context/TLS settings. The
  ping command, image, tick intervals and error/cancellation policy are unchanged.
- **BREAKING CHANGE:** The application ignores `SSH_TEST_KEY_PATH`; configure
  OpenSSH `IdentityFile` or an agent instead. Test-only fixture usage remains.
- Quote container operands through both remote shell layers and place Docker
  options before `--`; reject NUL without restricting names or short IDs.

- **BREAKING CHANGE:** Only one `rdhpf run` may publish in the same actual
  `HOME/.rdhpf` directory, even for different SSH targets. Stop all old-version
  publishers before upgrading; they do not honor the new admission lock. This is
  normally one instance per local user, not a machine-wide cross-user lock.
  The persistent `owner.lock` is held through status publication and cleanup;
  do not delete or replace it. Requires local Linux/macOS storage.
- Each SSH master uses a private directory and a socket path stable across its
  connection replacements, not across independent instances. Owned stale sockets
  are reclaimed only after child retirement. Other instances and cleanup tools
  must leave active namespaces and their ancestors alone; this is not protection
  against arbitrary same-UID path replacement.
- SSH masters run as directly owned foreground children; occupied or ambiguous
  control endpoints are refused rather than adopted or removed. Forced rdhpf
  termination (for example, SIGKILL) may leave an idle SSH child and its private
  directory behind; a new instance does not adopt or sweep them. Normal shutdown
  and failed startup retain responsibility for terminating and reaping the child.
  If retirement cannot be proved, shutdown keeps publisher ownership and retries
  without a total deadline. There is no two-second shutdown guarantee.

### Fixed

- Snapshot performance-summary totals and averages under the same metrics lock,
  preventing races with concurrent event and reconciliation recording.

- Request full container IDs during startup inventory so stopping a preexisting
  container removes its forwards and restarting the same container restores one
  consistent owner. Inventory/event handoff and resynchronization are unchanged.
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
