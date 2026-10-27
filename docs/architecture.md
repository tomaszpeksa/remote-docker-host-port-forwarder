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
  - Debounces event bursts; independently audits inventory and retains unapplied work
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
5. Apply add/remove actions through the gate-scoped, same-FD mux forwarding helper.

Properties:
- Idempotent: repeated application yields no changes after convergence
- Batched per container: all ports added/removed together for lifecycle consistency

Related code: internal/reconcile/reconciler.go, internal/ssh/forward.go

### App-only forwarding obligations

Every app SSH launch supplies `ClearAllForwardings=yes` and `Tunnel=no`, including
Master startup/checks, Docker reads/events and ordinary missing-mux read fallbacks.
Host aliases, configured authentication/trust and forced BatchMode remain in use;
external SSH commands are unchanged. There is no startup configuration parser.

`Master.WithForwarding` supplies a concrete capability valid only inside the
existing lifecycle gate. Each OPEN/CLOSE uses the same kernel-peer-verified Unix
FD for bounded mux-v4 HELLO and one correlated request for the exact tuple
`127.0.0.1:local:localhost:remote`. No mutation subprocess or SSH fallback exists.
Not-dispatched, rejected, acknowledged and uncertain effects are distinct.
An OPEN_OK reply can be idempotent: it does not prove exclusive creation. Ownership
relies on the cooperative private-Master/sole-producer boundary and resource ledger,
not protection from a same-user process deliberately issuing its own mux requests.

The ledger stores owner, tuple and generation separately from public readiness.
Acknowledged OPEN is recorded before probing; owned-pending retry probes rather
than repeating OPEN, and stopping owned-pending work still requires CLOSE. Failed
CLOSE retains the obligation and produces no terminal history. Unknown/partial/lost
replies are never replayed or treated as absence; affected replacement waits for
proven natural generation retirement, without retiring a shared Master to clear
one forward. Unrelated ports can progress. Never-owned conflicts can be forgotten
on stop without CLOSE. Confirmed terminal removal/retirement consumes history once.
If the same owner restarts while a container-stop CLOSE is still owed, the old
CLOSE must be acknowledged before a fresh unowned creation obligation is retained.
That new obligation uses the existing one-second resource retry, not immediate
reopening. Claimant-transfer and shutdown removals do not enable this renewal.
A superseding non-stop cleanup restriction persists across deferred or failed
CLOSE attempts independently of the original terminal-history reason.

The existing one-second reconciliation timer also schedules eligible resource
work after a discovery request has been acknowledged. This permits eventless
conflict-release and known failed-cancel retries without changing S09 request
capture/acknowledgement or the existing candidate-selection policy. Public status
remains readiness information, not an authoritative cleanup ledger.

Related code: internal/ssh/mux_forward.go, internal/state/forward_resource.go,
internal/reconcile/reconciler.go, internal/manager/discovery.go.

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

- A rejected OPEN that specifically identifies a bind conflict uses the
  five-attempt AddForward burst: base delay 100ms, doubling between attempts,
  with a 10s delay cap.
- A generic rejection such as `Port forwarding failed` is not address-in-use
  evidence. It returns without the conflict burst; the retained unowned resource
  becomes eligible for the ordinary one-second reconciliation retry.
- Unknown effects are not replayed. Eligible resource retries progress without a
  new event or changed inventory, and unrelated ports continue independently.

Related code: internal/ssh/forward.go (AddForwardWithRetry)

## Data Flow

### Startup (auto-discovery mode)

1. CLI parses flags/env, builds config and acquires exclusive publisher admission
   in the actual `HOME/.rdhpf` directory before SSH or publication changes.
2. A directly owned foreground SSH child opens with keep-alives in a private
   namespace. Inherited backgrounding and ControlPersist are explicitly disabled.
3. Manager:
   - Starts Docker event stream over SSH (start/die/stop)
   - Starts independent bounded startup inventory/audits (partial desired merge)
4. Reconciler computes add actions and applies via the owned mux forwarding capability
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
   current generation inside `WithForwarding`, invalidates retired actual state,
   then runs Diff/Apply only if the connection is ready. Invalidation records a
   restoration request even while unavailable; a later ready attempt at the same
   generation consumes that request. Desired ports and logical creation times
   survive recovery; notification ordering is not state authority.

### Shutdown and cleanup

1. SIGINT/SIGTERM cancels the run context. Manager fences and joins its recovery
   worker/health monitor; forwarding cleanup uses the lifecycle gate and sends
   correlated mux CLOSE only when the transport is ready; failed/unknown effects
   remain obligations rather than being announced as removed.
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

### Discovery repair and stream ownership

Manager starts a Run-owned inventory worker before connection maintenance can
block. Startup, stream replacement and failed observation retain repair work;
normal audits run every 10 seconds. Failed/incomplete passes retain one retry
eligibility deadline (1, 2, 4, 8, 16, then 30 seconds), rather than postponing it
on every event. A sorted circular full-ID cursor advances even after failures.
An individually admissible observation that exceeds remaining aggregate capacity
gets first opportunity on the next pass. An observation too large even for an
empty candidate remains unknown while the pass advances to other owners.
One pass has a 30-second observation slice and reserves a whole five-second
observation plus two-second cleanup allowance before starting another ID.

A complete running-container list authorizes absence removals and independently
successful observations. Listed but unknown containers retain their **current**
desired ports; failed/truncated lists authorize no removals. New zero-port
observations do not invent desired owners; existing event/absence tombstones
remain, and an authoritative empty observation still clears an existing owner.
Candidates are
bounded to 16 MiB/100,000 ports and validated as unique subsets of the list.
Before list dispatch and at commit, Manager checks stream generation, acceptance
revision, attempt and zero pending events. Acceptance reserves before enqueue;
processing or canceled enqueue retires exactly once. Inspections run outside the
discovery mutex, with generation/cancellation checked before desired mutation.

The event callback may backpressure through ten buffered records plus one
producer-held record. `StreamResult` is emitted once after the owned command,
readers and cancellation machinery join. Clean EOF is `Err == nil && !Canceled`,
not readiness or replay. Malformed/short-ID records are rejected before slicing;
records and retained diagnostic tails are capped at 64 KiB. Accepted queued
records are drained before stream replacement. Cancellation retires queued
reservations without manufacturing new forwards or removal history. Deadlines
initiate cancellation/pipe closure/kill; OS-uninterruptible children do not have
a guaranteed bounded join.

After joined completion, accepted-event drain and generation retirement, clean
EOF clears prior restart failures and waits one cancelable second before another
connection check/subscription. Nonclean stream and EnsureAlive failures retain
shared 1/2/4/8/16/30-second backoff and termination on the tenth failure; successful
event handling does not forgive that restart history. Parent or watchdog
cancellation suppresses replacement and retains the selected terminal cause.
This pacing floor neither bounds lifetime EOF count nor establishes healthy HTTP
traffic or synchronous reconciliation/history completion.

Desired changes retain a request revision independently of debounce and EOF.
Only a ready-gated Diff/Apply attempt acknowledges its captured revision; a newer
request remains owed. Unavailable transport retains timer work, while a failed
resource operation discharges the attempted revision. Unchanged audits therefore
do not introduce perpetual operation retries (S12 remains separate).

This is eventual repair under quiescence and finite observation latency, not an
atomic Docker snapshot or complete event history. A delayed start for a stopped
but inspectable container can transiently restore desired ports until an audit.
Continuous churn, permanent observation failure and capacity limits can delay
convergence. Bounded discovery does not bound existing State tombstones.

### Processing watchdog and terminal cause

The watchdog arms once at Run monitoring start, before connection maintenance.
Construction and earlier CLI SSH startup are outside that clock. On each ordinary
10-second check, elapsed time below 30 seconds is healthy; from 30 to below 60
seconds one probe is eligible; at 60 seconds or more the watchdog commits a fatal
result and cancels active Run work. Retries, generations and clean EOF do not
re-arm it. Only successful start/die/stop handler completion updates health;
queue admission, failed inspections, inventory and probe success do not.

Timing decisions never execute or wait for Ping. A single owned asynchronous
probe has a fixed deadline at the launch-time last-success timestamp plus 60
seconds, further shortened by its parent. Later health does not extend an active
attempt; completion permits another only on a later eligible tick. Ping retains
64 KiB tails per output stream and a two-second inherited-descriptor WaitDelay.
Its direct local command and copy workers join before Ping returns. The remote
`alpine:latest` stimulus, possible implicit pull, configured target and inherited
SSH trust remain unchanged. These bounds do not prove remote container removal.

Health and terminal selection share a short lock. A committed result is immutable;
parent cancellation already observable at selection wins over a new watchdog
contender. A watchdog cause is committed before cancellation, logging or joining,
so induced context cancellation and late events cannot erase it. Cancellation
reaches the active handler's existing five-second inspection context, stream,
discovery and reconciliation. Run retires accepted reservations exactly once,
joins its active stream and new watchdog/probe owner, and invokes existing terminal
forward cleanup once for watchdog termination, including preflight/backoff exits.
Publisher admission remains held through those obligations.

The guarantee is bounded **initiation**, not a global shutdown deadline. Existing
SSH lifecycle-gate waits, background cleanup and uncertain child retirement can
delay completion; no worker is detached to manufacture an exit deadline. Successful
processing is the health signal, not proof that delivery stopped or that every
forward has working application traffic. Stream restart pacing and global shutdown
policy are separate concerns.
