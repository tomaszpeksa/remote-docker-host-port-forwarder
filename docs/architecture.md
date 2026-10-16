# Architecture: Remote Docker Host Port Forwarder (rdhpf)

This document explains the system architecture for contributors and maintainers. It covers the high-level design, core components, key algorithms, data flow, testing strategy, and planned enhancements.

- [System Overview](#system-overview)
- [Components](#components)
- [Key Algorithms](#key-algorithms)
- [Data Flow](#data-flow)
- [Testing Strategy](#testing-strategy)
- [Future Enhancements](#future-enhancements)

## System Overview

rdhpf is an event-driven helper that makes services running on a remote Docker host reachable on localhost by maintaining SSH local port forwards. It favors simplicity and resilience by using the system OpenSSH client in ControlMaster mode and reacting to Docker events streamed over SSH.

Design principles:
- Single responsibility per module; clear orchestration in the Manager
- Deterministic/idempotent operations (reconciliation model)
- Favor OS/battle-tested tools over custom protocols
- Non-interactive operation; structured logging; safe redaction
- Self-healing on failures; bounded backoff; circuit breaker

### High-level architecture (ASCII)

```
+-----------------------+       +----------------------+       +----------------------+
|        CLI (Cobra)    |       |      Manager         |       |        State         |
|  rdhpf run / status   | ----> |  Orchestrates flow  | <---> | Desired / Actual     |
+-----------+-----------+       +----------+-----------+       +----------+-----------+
            |                              |                              |
            |                              v                              |
            |                     +--------+--------+                     |
            |                     |     Reconciler  | --------------------+
            |                     | Diff + Apply    |
            |                     +--------+--------+
            |                              |
            v                              v
+-----------+-----------+         +--------+--------+         +----------------------+
|  Docker Events over   |         |   SSH Control    |         |      Status          |
|    SSH (EventReader)  |         |  Master/Forwards |         |  Format (table/json) |
+-----------+-----------+         +--------+--------+         +----------+-----------+
            |                               ^                            |
            v                               |                            |
      docker inspect                        |                            |
            |                               |                            |
            +-------------------------------+----------------------------+
```

## Components

This section maps responsibilities to packages and files in the repository. Links point to implementation entrypoints or representative files.

- SSH Module
  - ControlMaster management: start/close/check, keep-alives, circuit breaker, health monitor
  - Forward lifecycle: add/cancel with retry on conflicts
  - Files:
    - internal/ssh/master.go — ControlMaster, health monitor, circuit breaker (open/half-open/closed)
    - internal/ssh/forward.go — AddForward, CancelForward, AddForwardWithRetry (exponential backoff)
    - internal/ssh/namespace.go — exclusively created private namespace, stable per-Master ControlPath and owned stale-socket cleanup
    - internal/instancepath/owner.go — nonblocking single-publisher admission per actual status directory, held through joined publication cleanup

- Docker Module
  - Event streaming over SSH: `docker events --format '{{json .}}'`
  - Container inspect to extract HostConfig.PortBindings (published host ports only)
  - Files:
    - internal/docker/events.go — event reader
    - internal/docker/inspect.go — inspect and flatten published ports

- State Module
  - In-memory store of desired vs actual, and mapping from container → ports
  - Tracks forward status (active/conflict/pending)
  - Files:
    - internal/state/model.go — minimal types and getters/setters

- Reconciler
  - Computes diff between desired and actual; outputs add/remove operations
  - Enforces "last event wins" ownership per port
  - Container-batched operations; idempotent apply
  - Files:
    - internal/reconcile/reconciler.go — diff and apply logic

- Manager
  - Wires config, event reader, reconciler, SSH master, and state
  - Debounces event bursts; reconciles on startup and after recoveries
  - Files:
    - internal/manager/manager.go — orchestration and event loop

- Status
  - Formats current forwards for CLI output (table/json/yaml)
  - Files:
    - internal/status/status.go — formatting and output types
    - cmd/rdhpf/main.go — `status` command wiring

- Logging
  - Structured logs with optional TRACE level; redact sensitive info
  - Correlation ID helpers for consistent tracing
  - Files:
    - internal/logging/logger.go — logger setup, redaction, correlation ID utilities

- Configuration and CLI
  - CLI flags via Cobra; validation and env fallback
  - Files:
    - cmd/rdhpf/main.go — commands, flags, run/status; signal handling and shutdown
    - internal/config/config.go — config struct, fixed ports parsing, validation

## Key Algorithms

### Reconciliation loop (Desired vs Actual)

Goal: converge actual SSH forwards to match desired ports for running containers.

Inputs:
- Desired: state derived from Docker events/inspect
- Actual: implicit via SSH master state; treated conservatively

Process:
1. Build desired Map[ContainerID]→Set[Port].
2. Infer current ownership (portOwner[Port] = ContainerID).
3. For each container and port in desired:
   - If port unowned → add
   - If owned by others → remove from current owner, then add for new owner ("last event wins")
   - If already owned by same container → no-op (idempotent)
4. For any port no longer in desired → remove
5. Apply add/remove actions through SSH `-O forward` / `-O cancel`

Properties:
- Idempotent: repeated application yields no changes after convergence
- Batched per container: all ports added/removed together for lifecycle consistency

Related code: internal/reconcile/reconciler.go, internal/ssh/forward.go

### Debouncing (200ms window)

Purpose: absorb rapid event churn (start/stop/restart) while keeping convergence latency low.

Strategy:
- Manager collects events and schedules reconcile after a short delay (default 200ms).
- Additional events within the window reset the timer (“coalesce then apply”).
- Guarantees final state matches the last sequence of events with minimal redundant SSH ops.

Related code: internal/manager/manager.go

### "Last event wins" conflict resolution

- When two containers publish the same host port over time, the reconciler transfers ownership:
  - Remove forward for old owner
  - Add forward for new owner (most recent event)
- Ensures final state reflects the latest Docker state.

Related code: internal/reconcile/reconciler.go

### Circuit breaker (SSH ControlMaster)

States:
- Closed: healthy
- Open: fast-fail after five consecutive failed connection recreations
- Half-open: single trial after cooldown (60s) to probe recovery

Triggers:
- Health checks verify the retained child's socket ownership and run SSH `-O check`,
  with a local two-second deadline.
- A failed internal check, including that deadline, can retire the child and
  attempt recreation. Caller/monitor cancellation does not itself retire a ready
  connection.
- Failed recreation increments the breaker counter; successful recreation resets
  it. Connection changes notify reconciliation, not successful routine checks.

Related code: internal/ssh/master.go

### Exponential backoff for port conflicts

- Problem: local “address already in use” during forward creation
- Strategy: retry AddForward with exponential backoff
  - Base delay: 100ms; doubles each retry; max delay 10s; up to 5 attempts
- Non-retryable errors fail fast; conflicts log guidance and continue with other ports

Related code: internal/ssh/forward.go (AddForwardWithRetry)

## Data Flow

### Startup (auto-discovery mode)

1. CLI parses flags/env, builds config and acquires exclusive publisher admission
   in the actual `HOME/.rdhpf` directory before SSH or publication changes.
2. A directly owned foreground SSH child opens with keep-alives in a private
   namespace. Inherited backgrounding and ControlPersist are explicitly disabled.
3. Manager:
   - Starts Docker event stream over SSH (start/die/stop)
   - Performs startup reconciliation (inspect existing containers, build desired)
4. Reconciler computes add actions and applies via SSH -O forward
5. State reflects active forwards; logs emitted with correlation IDs

### Event processing

1. Docker emits container event (start/die/stop)
2. Manager ingests event; for start, perform docker inspect to get published ports
3. Update desired state; debounce; then reconcile
4. Reconciler computes diff and applies add/remove via SSH
5. Logs indicate results; conflicts are surfaced with guidance

### Health check and recovery

1. The health monitor calls `EnsureAlive` every 15 seconds. Retirement and
   replacement use the same lifecycle gate as whole forwarding transactions;
   replacement does not call `Close` or stop the monitor.
2. A failed check leads to retained-child retirement before a successor can start.
   Proven retirement advances the generation; uncertain retirement blocks
   replacement and retains cleanup responsibility.
3. `Changes()` coalesces connection-change notifications. Manager observes the
   current generation inside `WithConnection`, invalidates retired actual state,
   then runs Diff/Apply only if the connection is ready. Desired ports and logical
   creation times survive recovery; notification ordering is not state authority.

### Shutdown and cleanup

1. SIGINT/SIGTERM cancels the run context. Manager fences and joins its recovery
   worker/health monitor; forwarding cleanup uses the lifecycle gate and sends
   SSH `-O cancel` only when the transport is ready.
2. Final status publication and socket pathname cleanup are joined before
   publisher admission is released. This is not a join of every socket handler.
3. CLI cleanup calls Master `Close`, which signals the retained child with SIGTERM,
   escalates to Kill if needed and observes its single Wait completion before
   reclaiming owned socket artifacts and removing the empty private directory.
4. If retirement is uncertain, CLI cleanup retains admission and retries without
   a total deadline. Other cleanup errors are logged, not retried as uncertainty.
   Forced parent death may leave a child and directory; no orphan sweep is provided.

## Testing Strategy

rdhpf uses a layered test strategy to validate correctness, performance, and resilience.

### Unit tests (fast, deterministic)

- Event parsing and inspect flattening:
  - tests/unit/events_parse_test.go
  - tests/unit/published_ports_test.go
- Reconciler diff, idempotency, batching, last event wins:
  - tests/unit/reconciler_test.go
- Forward planner and conflict handling/backoff:
  - tests/unit/forward_plan_test.go
- SSH utilities and health:
  - internal/ssh/master_test.go
  - internal/ssh/check_cancellation_test.go
  - internal/ssh/retirement_test.go
- Status formatting:
  - tests/unit/status_format_test.go

### Integration tests (SSH + Docker scenarios)

- SSH ControlMaster lifecycle:
  - tests/integration/ssh_master_test.go
- End-to-end event → forward establishment/removal:
  - tests/integration/end_to_end_test.go
- Conflict handling scenarios:
  - tests/integration/conflict_test.go
- Recovery and graceful shutdown:
  - tests/integration/connection_recovery_test.go
  - tests/integration/graceful_shutdown_test.go
- Status command behavior:
  - tests/integration/status_test.go

Scenario assertions cover published ports, idempotent operations, recovery and
normal shutdown. Compilation or a skipped scenario does not prove runtime
behavior. The 15-second health interval, check/startup deadlines and breaker
cooldown do not constitute a ten-second recovery SLA or a total shutdown bound.

## Future Enhancements

- Restart recovery beyond the existing published status snapshots
- Metrics and Prometheus endpoint for observability
- Docker socket forwarding (opt-in) for advanced workflows
- Multi-host support (multiple remote hosts with isolated managers)
- Dynamic port management API (add/remove without restart)
- Port range syntax and per-port configuration (bind address, annotations)

---
References:
- CLI/flags: cmd/rdhpf/main.go
- Config: internal/config/config.go
- SSH: internal/ssh/master.go, internal/ssh/forward.go, internal/ssh/namespace.go
- Docker: internal/docker/events.go, internal/docker/inspect.go
- Reconciler: internal/reconcile/reconciler.go
- State: internal/state/model.go
- Status: internal/status/status.go
- Logging: internal/logging/logger.go

### Discovery observation errors

A required infrastructure-label or enabled forwarding-label inspection failure is
unknown information, not an empty port set. Discovery commands cap captured
output and use cancellation deadlines; overflow cannot authorize inventory
absence. These deadlines do not promise bounded OS-level process termination.
Desired-only inventory merging preserves actual forward ownership and history.
