<!--
Sync Impact Report
Version change: N/A → 1.0.0
Modified principles: Added I. Reliability & Self‑Healing; II. Simplicity & Determinism; III. Test‑First & CI Discipline; IV. Observability & Diagnostics; V. Security & Least Privilege
Added sections: Scope & Technology Constraints; Delivery & Release Standards
Removed sections: None
Templates requiring updates:
- .specify/templates/plan-template.md ✅ updated
- .specify/templates/tasks-template.md ✅ updated
- .specify/templates/spec-template.md ✅ no change needed
- .specify/templates/agent-file-template.md ✅ no change needed
- .specify/templates/checklist-template.md ✅ no change needed
- .specify/templates/commands/* ⚠ pending (no command files present)
Runtime docs and repository:
- README.md ⚠ pending (add install/usage, governance summary, MIT notice)
- LICENSE ⚠ pending (MIT license file)
Follow-up TODOs:
- TODO(README): Create or update README aligned with this constitution
- TODO(LICENSE): Add MIT LICENSE file at repository root
-->

# remote-docker-host-port-forwarder Constitution

## Core Principles

### I. Reliability & Self-Healing (NON-NEGOTIABLE)

- The system MUST continuously track the remote Docker host’s events and converge the
  actual set of SSH port forwards to the desired state derived from container state.
- On startup and periodically, perform a full reconcile: detect drifts, recover missing
  forwards, and remove stale ones. Operations MUST be idempotent.
- Connection failures (Docker or SSH) MUST trigger exponential backoff with jitter and
  MUST auto-retry until healthy, without requiring manual intervention.
- Conflicts such as “address already in use” MUST be handled deterministically
  (e.g., retry with backoff, rebind after cleanup, or emit actionable error).
- Graceful shutdown MUST tear down managed forwards; ungraceful exits MUST recover on
  restart without operator action.

Rationale: Remote networks and Docker daemons are unreliable. Self-healing behavior
prevents manual ops and reduces outage minutes.

### II. Simplicity & Determinism

- Keep dependencies minimal; avoid heavy frameworks. Prefer standard tooling and proven
  libraries. Default to a single small CLI binary.
- Clear, explicit CLI with discoverable help. Configuration via flags and environment
  variables; safe, documented defaults.
- Deterministic behavior: same inputs yield the same outputs. Concurrency is bounded and
  predictable. Disable “magic” implicit behaviors.
- Changes MUST be justified by user value. YAGNI applies—no speculative features.

Rationale: Smaller surface area lowers operational and security risk, improves
maintainability, and accelerates incident response.

### III. Test-First & CI Discipline

- Tests are REQUIRED. Unit tests cover core logic (event → desired forwards, parsing,
  reconciliation). Integration tests validate Docker event ingestion and SSH forwarding
  behavior (with mocks/sandboxes as needed).
- Follow Red–Green–Refactor. New work MUST land with tests. Failing tests block merge.
- Minimum coverage target: 80%. Deviations MUST include a written, time-bounded
  justification in the PR.
- GitHub Actions MUST run lint, unit, integration, build on PRs; release on tags with
  reproducible builds and checksums.

Rationale: Reliability emerges from repeatable tests and automated enforcement.

### IV. Observability & Diagnostics

- Structured logs with levels (info, warn, error, debug). Logs include correlation IDs
  per forward and container reference.
- Provide “doctor”/self-check and “--dry-run” diagnostics to show planned actions without
  side effects. Enable “--trace” to include verbose execution details.
- Expose health via process exit codes and optional minimal HTTP/CLI probe for container
  use-cases.

Rationale: When something breaks, operators need fast, precise insight to recover.

### V. Security & Least Privilege

- Default to 127.0.0.1 binds. Avoid wide exposure unless explicitly configured.
- Do not log secrets. Redact potentially sensitive values by default.
- Enforce StrictHostKeyChecking for SSH by default; allow explicit override with
  documented risk.
- Run as non-root where possible; container image uses read-only filesystem and drops
  unneeded capabilities.
- Docker connectivity MUST use TLS verification when remote and restrict to the minimum
  permissions needed (events/read).

Rationale: Port forwarding touches network boundaries; safe defaults prevent incidents.

## Scope & Technology Constraints

- Implementation language: Go (1.22+) or Rust (1.75+) producing a single static CLI
  binary named “rdhpf” (working name). Alternative choices require explicit approval
  with justification in the plan.
- SSH: Prefer the system OpenSSH client; do not reimplement cryptography. If embedding
  a library, choose a widely maintained, audited SSH library.
- Docker: Use the official API/SDK to subscribe to events; avoid shell-parsing of
  docker CLI output in core logic.
- Platforms: Linux x86_64 is REQUIRED; others are best-effort.
- Packaging: Provide a statically linked binary and a minimal container image.

## Delivery & Release Standards

- Versioning: Semantic Versioning (SemVer). Breaking governance or behavior changes
  require MAJOR release; features MINOR; fixes PATCH.
- Releases: Tag as vX.Y.Z. CI builds artifacts (binary + checksums) and publishes
  release notes. Builds MUST be reproducible with pinned dependencies.
- Licensing: MIT. Include a LICENSE file and reference in README.
- Documentation: README includes install, quickstart, configuration, observability,
  security notes, and support boundaries.

## Governance

- This constitution supersedes conflicting guidance in other docs.
- Amendments: Submit a PR updating this file. Include rationale, migration/operational
  impact, and proposed version bump (MAJOR/MINOR/PATCH) per the policy above.
- Ratification: Approval by maintainers is REQUIRED. Upon merge, update Version and
  Last Amended date.
- Compliance: PRs MUST include a “Constitution Check” section and pass CI gates. Plans
  and tasks generated via Speckit MUST include the constitution gates.
- Review cadence: Quarterly review to evaluate effectiveness and needed changes.
- Exceptions: Time-bounded, documented risk acceptance in PR with owner and expiry.

**Version**: 1.0.0 | **Ratified**: 2025-11-09 | **Last Amended**: 2025-11-09
