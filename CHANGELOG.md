# Changelog

All notable changes to this project will be documented in this file.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.1.0/),
and this project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## [Unreleased]

### Added
- Comprehensive debug logging for docker events SSH command execution
  - Logs full SSH command with arguments before execution
  - Captures and logs stderr separately (SSH warnings, error messages)
  - Logs last 50 lines of stdout on failures
  - Logs exit codes and signal information for better debugging

### Fixed
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
