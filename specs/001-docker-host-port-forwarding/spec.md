# Feature Specification: Docker Host Port Forwarding

**Feature Branch**: `[001-docker-host-port-forwarding]`
**Created**: 2025-11-09  
**Status**: Draft  
**Input**: User description: "remote-docker-host-port-forwarder is a tool built to simplify working with remote DOCKER_HOST port forwarding - applications using Docker containers to run dependencies often assume ports exposed from Docker containers are available on localhsot and that is not the case when a remote Docker host is used (ports are forwarded to the host, not to the machine that uses Docker CLI). This tool automatically configures SSH port forwarding to remote DOCKER_HOST for all ports exposed there."

## Clarifications

### Session 2025-11-09
- Q: Remote endpoint for each SSH -L forward on the Docker host? → A: Always 127.0.0.1:hostPublishedPort
- Q: Collision policy and scope of ports to forward? → A: No forward on collision; only published host ports are forwarded; exposed-only ports are ignored.

## User Scenarios & Testing (mandatory)

### User Story 1 - Develop locally against services on a remote Docker host (Priority: P1)

A developer works on an app that expects dependencies on localhost (e.g., DB at localhost:5432). The team uses a remote DOCKER_HOST to run containers. With the tool running, the developer can connect to localhost and the traffic is transparently forwarded to the remote container’s exposed port.

Why this priority: Enables core developer workflow; without it, development is blocked or requires manual/fragile SSH tunnels.

Independent Test: Start a container exposing a well-known port on the remote host; verify that connecting to localhost:<port> on the developer machine succeeds while the tool is running and fails when the tool is stopped.

Acceptance Scenarios:

1. Given a remote DOCKER_HOST and a container exposing port 5432, When the tool is running, Then localhost:5432 accepts TCP connections and proxies to the container port.
2. Given the tool is running, When the container stops, Then localhost:5432 stops accepting connections within a short interval.
3. Given the container restarts, When the tool monitors events, Then localhost:5432 is re-established automatically without user action.

---

### User Story 2 - Automatic convergence on container changes (Priority: P2)

The developer frequently starts and stops multiple containers that expose various ports. The tool observes changes and keeps local forwards in sync without manual steps.

Why this priority: Reduces operational toil and eliminates error-prone manual tunnel management.

Independent Test: Start/stop containers that expose ports; observe that forwards are added/removed on localhost accordingly within a bounded time.

Acceptance Scenarios:

1. Given no containers, When two containers are started exposing ports 6379 and 8080, Then localhost:6379 and localhost:8080 become available within a bounded time.
2. Given forwards established, When one container is removed, Then the corresponding localhost port stops listening shortly after removal.
3. Given multiple changes, When containers are started and stopped in quick succession, Then the tool converges to the correct final set of forwards without duplications or stale listeners.

---

### User Story 3 - Clear behavior on local port conflicts (Priority: P3)

If a local port required for forwarding is already in use, the tool must surface the conflict clearly and provide guidance for resolution.

Why this priority: Prevents silent failures and helps developers quickly resolve issues.

Independent Test: Occupy localhost:5432 locally, then start a remote container exposing 5432; observe conflict handling and user-facing status output.

Acceptance Scenarios:

1. Given localhost:5432 is already in use, When a remote container exposing 5432 starts, Then the tool reports the conflict and marks that forward as “not established” with actionable guidance.
2. Given a conflict is cleared, When the local port becomes free, Then the tool detects it and establishes the forward automatically.

---

### User Story 4 - Manage fixed or multiple target ports (Priority: P2)

Some workflows require forwarding a predetermined set of ports (one or many). The tool manages each port independently, allowing multiple simultaneous tunnels without conflict.

Why this priority: Supports CI jobs and teams that standardize on fixed service ports.

Independent Test: Provide a fixed list of ports [e.g., 5432, 6379]; verify forwards establish when corresponding containers are running; verify no forwards for ports without matching containers.

Acceptance Scenarios:

1. Given a fixed list [5432, 6379], When containers exposing those ports start, Then localhost:5432 and localhost:6379 become available.
2. Given only one matching container is running, Then only that port is forwarded; others are not.
3. Given both containers stop, Then all corresponding forwards are removed.

---

### Edge Cases

- DOCKER_HOST not set or unreachable: tool reports status and retries; no local ports are bound until healthy.
- SSH authentication not available (e.g., missing key/agent): tool reports clear error with next steps.
- Privileged ports (<1024): behavior is documented; if binding is not permitted, the forward is skipped and reported.
- Multiple containers publishing the same host port: Docker-level collisions are prevented by Docker; the tool does not forward on collision and will log and retry when released. Exposed-only ports (no host publish) are ignored.
- Rapid event churn (start/stop/restart): tool debounces appropriately yet converges quickly.
- Missed events or helper restarts: on startup, perform full reconciliation so active containers are forwarded and stale forwards removed.
- Local port reuse timing (TIME_WAIT/CLOSE_WAIT): after unclean disconnects, the tool retries binding with backoff and cleans up gracefully to enable reuse.
- Workflow cancellation/failure: ensures graceful shutdown to avoid leaving SSH processes or bound ports behind.
- Concurrent triggers across steps: a single manager owns tunnel lifecycle to prevent duplicates and race conditions.
- Network drops: tunnel reconnects on recovery; forwards are re-established automatically.
- IPv4/IPv6 environments: localhost reachability is consistent and documented.

## Requirements (mandatory)

### Functional Requirements

- FR-001: While running, the tool MUST make ports exposed by containers on the remote DOCKER_HOST reachable on the developer’s local machine at localhost:[port].
- FR-002: The tool MUST discover published host ports (ports bound on the Docker host) and maintain a desired set of local forwards reflecting active containers; container-only exposed ports without a host binding are ignored.
- FR-003: The tool MUST react to container lifecycle events and converge the set of forwards within 1 second in 99% of cases.
- FR-004: When a container stops or is removed, the corresponding local forward MUST be removed within 1 second in 99% of cases.
- FR-005: On tool startup, a full reconcile MUST occur to correct any stale forwards and establish missing ones.
- FR-006: If a required local port is already in use, the tool MUST surface a clear conflict status (via logs) and continue operating for other ports.
- FR-007: The tool MUST provide a status/diagnostics output and/or logs that list current forwards, skipped/conflicted ports, and health.
- FR-008: The tool MUST operate without requiring changes to application code or docker-compose files.
- FR-009: The tool MUST not expose ports beyond localhost by default.
- FR-010: The tool MUST handle intermittent connectivity to the remote host by retrying with backoff and reconciling on recovery.
- FR-011: The tool MUST not forward the same port multiple times; operations are idempotent.
- FR-012: The tool MUST allow users to exclude specific containers or ports via configuration flags or environment variables.
- FR-013: The tool MUST provide clear exit codes indicating healthy/errored termination.
- FR-014: The tool MUST log actions at informative verbosity without leaking sensitive data.
- FR-015: The tool MUST start and run in the background without requiring interactive input once configured.
- FR-016: The tool MUST support a fixed-ports mode where one or more specified ports are managed independently, and an all-exposed-ports mode that forwards all published host ports. All-exposed MUST be the DEFAULT; fixed-ports is optional when explicitly specified.
- FR-017: The tool MUST operate as a background helper that can persist across multiple workflow steps in CI and provide a simple command or signal to cleanly stop and shut down all tunnels at job end.
- FR-018: The tool MUST ensure a single manager process owns tunnel lifecycle and is concurrency-safe to avoid duplicate or conflicting forwards when multiple workflow parts trigger container changes.
- FR-019: Tunnel shutdown MUST be graceful to release local sockets promptly and allow immediate reuse when feasible; after unclean disconnects the tool MUST retry binding with bounded backoff.
- FR-020: If an established tunnel drops while the target container remains running, the tool MUST detect loss and re-establish the tunnel automatically.
- FR-021: The tool MUST minimize setup overhead for multiple forwards by reusing a single secure session or an equivalent mechanism that avoids repeated handshakes for each additional port.
- FR-022: Detection latency from container port availability/unavailability to forward start/stop MUST be under 1 second in 99% of cases. Event-driven ingestion via Docker events is REQUIRED; polling fallbacks are out of scope.
- FR-023: Minimal configuration: users can provide the target host and a list of ports via flags or environment variables; the tool uses existing SSH credentials/agent by default.
- FR-024: For each container, the tool MUST group all its published ports into a single container-scoped tunnel lifecycle (add/remove all forwards for that container together).
- FR-025: The tool MUST be non-interactive: no prompts; all information is emitted as structured logs to stdout/stderr; a status/doctor command MAY be provided for diagnostics.
- FR-026: Connectivity to DOCKER_HOST MUST be via SSH (ssh://) only; Docker TLS API mode is out of scope.
- FR-027: The underlying SSH session MUST use keep-alives and health checks; if the master session fails, it MUST be recreated automatically and forwards re-established.
- FR-028: Each forward MUST target the remote endpoint 127.0.0.1:hostPublishedPort, irrespective of the container’s host bind IP. The local listener remains 127.0.0.1:[port].
- FR-029: Only published host ports (HostConfig.PortBindings) are eligible for forwarding; container-exposed ports without a host binding are ignored. The tool never attempts to forward unpublished container ports.

### Acceptance Criteria per Requirement

- FR-001: Within 1 second (p99) of container availability, the remote service is reachable
  via localhost:[port] without user interaction.
- FR-002: At any time, the set of active local forwards equals the set of exposed
  container ports for running containers (minus any user exclusions), with no extra
  forwards.
- FR-003: After a container start/stop event, local forwards converge to the correct
  state within 1 second in 99% of cases.
- FR-004: When a container stops or is removed, its corresponding localhost listener is
  removed within 1 second in 99% of cases.
- FR-005: On tool startup, missing forwards are created and stale forwards are removed
  so that the final state matches running containers (minus exclusions).
- FR-006: If a required local port is already in use, the tool logs a clear conflict
  for that port; other forwards continue operating.
- FR-007: A status/diagnostics output or logs list active forwards, skipped/conflicted
  ports with reasons, and overall health.
- FR-008: No application code or docker-compose changes are needed to use forwarded
  services; connectivity via localhost works without modifications.
- FR-009: By default, no listener binds on non-loopback interfaces.
- FR-010: After temporary loss of connectivity to the remote host, the tool
  automatically reconnects and fully reconciles within 10 seconds of recovery.
- FR-011: No duplicate localhost listeners exist for the same port.
- FR-012: When configured to exclude specific containers or ports, those items are not
  forwarded while others are unaffected.
- FR-013: Normal termination exits with code 0; unrecoverable error exits non-zero with
  a message.
- FR-014: Logs provide necessary context without exposing credentials or secrets.
- FR-015: After initial configuration, the tool runs unattended without interactive
  prompts.
- FR-016: With a fixed port list [e.g., 5432, 6379], only listed ports are forwarded; in all-ports mode, all currently exposed container ports are forwarded; both modes are verifiable via status output.
- FR-017: When started in the background at job start, a subsequent CI step can still access the forwarded port; a final stop command cleanly shuts down and releases ports.
- FR-018: Under concurrent container start events from separate workflow parts, only one localhost listener per port exists; no duplicate forwards or race-induced errors; final state matches desired.
- FR-019: After a container stops, the local port becomes free within 1 second (p99) and can be rebound upon container restart without undue delay beyond OS constraints.
- FR-020: If the underlying tunnel drops while a container continues running, the forward is re-established automatically upon connectivity recovery.
- FR-021: Adding all forwards for a container completes within 1 second in 99% of cases under normal conditions.
- FR-022: Time from container port availability/unavailability to corresponding forward start/stop is under 1 second in 99% of cases.
- FR-023: Running the tool with only host and ports provided (and existing SSH credentials) succeeds without additional manual setup steps.
- FR-028: Local 127.0.0.1:[port] forwards map to remote 127.0.0.1:hostPublishedPort for the corresponding container; validated via SSH control output or remote socket inspection; connections succeed via loopback.
- FR-029: A container with exposed-only ports (no HostConfig.PortBindings) results in no forward; when a published host port appears, the corresponding local forward is created; if the local port is occupied, a conflict is logged and retried.

### Key Entities (include if feature involves data)

- Container: a running workload that exposes one or more ports.
- Exposed Port: a port published by a container on the remote Docker host.
- Forward Rule: a mapping from localhost:[port] on the developer machine to the corresponding remote container port.
- Status Report: a user-visible summary of current forwards, conflicts, and health.

## Assumptions & Dependencies

- The remote Docker host is reachable from the developer machine/runner, and the team has
  valid credentials to access it. DOCKER_HOST is provided as ssh://user@host (SSH-only) and passwordless authentication is preconfigured.
- The developer machine/runner can open outbound connections and bind localhost ports; binding
  ports below 1024 may require elevated privileges and is not assumed.
- The user has permission to read container metadata and events on the remote Docker
  host.
- The remote host has Docker CLI (docker) available in PATH; docker events and docker inspect can be executed via SSH.
- CI environment (e.g., GitHub Actions) allows a background process to persist across multiple
  steps in a single job; a final cleanup step can be executed.
- SSH keys or agent are available to authenticate to the remote host; known_hosts trust is managed
  appropriately; no secrets are written to logs.
- Only one manager instance is expected to control tunnels per job/runner to avoid conflicts; cross-job
  concurrency on different runners is out of scope.
- Network connectivity may be intermittent; recovery is expected and covered by
  requirements.

## Success Criteria (mandatory)

### Measurable Outcomes

- SC-001: For a container start event, forwarded services become reachable via localhost within 1 second in 99% of cases.
- SC-002: Forwards are established or removed within 1 second of the corresponding container start/stop event in 99% of cases.
- SC-003: In case of connection loss to the remote host, recovery and full reconciliation occurs within 10 seconds after connectivity returns.
- SC-004: 0 unintended non-local binds (no port listens on non-loopback interfaces by default).
- SC-005: 0 silent failures: all conflicts and unrecoverable errors are visible in status output and logs.
- SC-006: Developers report successful local development against remote services without manual SSH tunnel management.
