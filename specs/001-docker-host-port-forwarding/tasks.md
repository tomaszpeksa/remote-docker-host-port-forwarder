---
description: "Task list for Docker Host Port Forwarding implementation"
---

# Tasks: Docker Host Port Forwarding

**Input**: Design documents from `/specs/001-docker-host-port-forwarding/`
**Prerequisites**: plan.md (required), spec.md (required for user stories)

**Tests**: Tests are REQUIRED per the Constitution. Unit tests cover core logic; integration tests validate SSH/Docker event flow.

**Organization**: Tasks are grouped by user story to enable independent implementation and testing of each story.

## Format: `[ID] [P?] [Story] Description`

- **[P]**: Can run in parallel (different files, no dependencies)
- **[Story]**: Which user story this task belongs to (e.g., US1, US2, US3, US4)
- Include exact file paths in descriptions

## Path Conventions

- **Single project**: `cmd/rdhpf/`, `internal/`, `tests/` at repository root
- Paths are relative to repository root

---

## Phase 1: Setup (Shared Infrastructure)

**Purpose**: Project initialization and basic structure

- [ ] T001 Initialize Go module with go.mod and go.sum
- [ ] T002 [P] Create project directory structure per plan.md
- [ ] T003 [P] Add .gitignore for Go projects (bin/, vendor/, *.test, etc.)
- [ ] T004 [P] Add MIT LICENSE file
- [ ] T005 [P] Create CHANGELOG.md with v0.1.0 placeholder

---

## Phase 2: Foundational (Blocking Prerequisites)

**Purpose**: Core infrastructure that MUST be complete before ANY user story can be implemented

**⚠️ CRITICAL**: No user story work can begin until this phase is complete

- [ ] T006 [P] Implement structured logging with redaction in internal/logging/logger.go
- [ ] T007 [P] Implement config parsing (flags/env) in internal/config/config.go
- [ ] T008 [P] Implement ControlPath derivation (stable, collision-free) in internal/ssh/controlpath.go
- [ ] T009 Implement SSH ControlMaster wrapper in internal/ssh/master.go (open/close with keep-alives)
- [ ] T010 Implement SSH health check (-O check) with watchdog in internal/ssh/master.go
- [ ] T011 Implement SSH auto-recreate on failure in internal/ssh/master.go
- [ ] T012 [P] Implement Docker events JSON stream reader in internal/docker/events.go
- [ ] T013 [P] Implement Docker inspect wrapper (extract PortBindings) in internal/docker/inspect.go
- [ ] T014 [P] Implement state model (container→ports mapping) in internal/state/model.go
- [ ] T015 Implement forward status tracking (active/conflict/pending) in internal/state/model.go

**Checkpoint**: Foundation ready - user story implementation can now begin in parallel

---

## Phase 3: User Story 1 - Develop locally against services on a remote Docker host (Priority: P1) 🎯 MVP

**Goal**: Enable core developer workflow - forward published container ports to localhost

**Independent Test**: Start a container with published port 5432 on remote host; verify localhost:5432 accepts connections while tool runs; verify it fails when tool stops

### Tests for User Story 1 ⚠️

> **NOTE: Write these tests FIRST, ensure they FAIL before implementation**

- [ ] T016 [P] [US1] Unit test: Parse docker events JSON (start/die) in tests/unit/events_parse_test.go
- [ ] T017 [P] [US1] Unit test: Extract HostConfig.PortBindings in tests/unit/events_parse_test.go
- [ ] T018 [P] [US1] Unit test: ControlPath derivation stability in tests/unit/ssh_controlpath_test.go
- [ ] T019 [P] [US1] Integration test: ControlMaster lifecycle (open, check, exit) in tests/integration/ssh_master_test.go
- [ ] T020 [P] [US1] Integration test: Event→forward <2s for single port in tests/integration/end_to_end_test.go
- [ ] T021 [P] [US1] Integration test: Container stop→forward removed <2s in tests/integration/end_to_end_test.go
- [ ] T022 [P] [US1] Integration test: Container restart→forward re-established in tests/integration/end_to_end_test.go

### Implementation for User Story 1

- [ ] T023 [P] [US1] Implement SSH forward operations (-O forward) in internal/ssh/forward.go
- [ ] T024 [P] [US1] Implement SSH cancel operations (-O cancel) in internal/ssh/forward.go
- [ ] T025 [P] [US1] Implement TCP listener probe (validate forward active) in internal/util/tcpcheck.go
- [ ] T026 [US1] Implement reconciler diff logic (desired vs actual) in internal/reconcile/reconciler.go
- [ ] T027 [US1] Implement reconciler apply (add/remove forwards) in internal/reconcile/reconciler.go
- [ ] T028 [US1] Implement container-scoped batching (all ports together) in internal/reconcile/reconciler.go
- [ ] T029 [US1] Implement manager event subscription in internal/manager/manager.go
- [ ] T030 [US1] Implement manager event handler (start→inspect→reconcile) in internal/manager/manager.go
- [ ] T031 [US1] Implement manager event handler (die/stop→reconcile) in internal/manager/manager.go
- [ ] T032 [US1] Implement startup reconciliation (docker ps + inspect) in internal/manager/manager.go
- [ ] T033 [US1] Implement CLI skeleton (rdhpf run --host) in cmd/rdhpf/main.go
- [ ] T034 [US1] Wire manager with SSH master and Docker events in cmd/rdhpf/main.go

**Checkpoint**: At this point, User Story 1 should be fully functional - basic port forwarding works

---

## Phase 4: User Story 2 - Automatic convergence on container changes (Priority: P2)

**Goal**: Handle rapid container churn and keep forwards in sync without manual steps

**Independent Test**: Start/stop multiple containers rapidly; verify forwards converge to correct state within bounded time

### Tests for User Story 2 ⚠️

- [ ] T035 [P] [US2] Unit test: Idempotent reconciler decisions in tests/unit/reconciler_test.go
- [ ] T036 [P] [US2] Unit test: Batch operations for multiple containers in tests/unit/reconciler_test.go
- [ ] T037 [P] [US2] Integration test: Multiple containers (6379, 8080) → both forwards active in tests/integration/end_to_end_test.go
- [ ] T038 [P] [US2] Integration test: Remove one container → only that forward removed in tests/integration/end_to_end_test.go
- [ ] T039 [P] [US2] Integration test: Rapid start/stop churn → converges correctly in tests/integration/end_to_end_test.go

### Implementation for User Story 2

- [ ] T040 [P] [US2] Implement event stream watchdog (restart on error) in internal/docker/events.go
- [ ] T041 [P] [US2] Implement reconcile trigger on stream restart in internal/manager/manager.go
- [ ] T042 [US2] Implement "last event wins" logic in reconciler in internal/reconcile/reconciler.go
- [ ] T043 [US2] Add debouncing for rapid events in internal/manager/manager.go
- [ ] T044 [US2] Ensure idempotent operations (no duplicate forwards) in internal/reconcile/reconciler.go

**Checkpoint**: At this point, User Stories 1 AND 2 should both work - handles container churn

---

## Phase 5: User Story 3 - Clear behavior on local port conflicts (Priority: P3)

**Goal**: Surface conflicts clearly and continue operating for other ports

**Independent Test**: Occupy localhost:5432 locally; start remote container on 5432; verify conflict logged and other ports work

### Tests for User Story 3 ⚠️

- [ ] T045 [P] [US3] Unit test: Conflict marking and retry logic in tests/unit/forward_plan_test.go
- [ ] T046 [P] [US3] Integration test: Occupied port → conflict logged, others succeed in tests/integration/conflict_test.go
- [ ] T047 [P] [US3] Integration test: Port released → auto-retry succeeds in tests/integration/conflict_test.go

### Implementation for User Story 3

- [ ] T048 [P] [US3] Implement conflict detection (port in use) in internal/ssh/forward.go
- [ ] T049 [US3] Implement conflict logging with actionable guidance in internal/ssh/forward.go  
- [ ] T050 [US3] Implement retry with exponential backoff in internal/ssh/forward.go
- [ ] T051 [US3] Ensure other ports continue when one conflicts in internal/reconcile/reconciler.go

**Checkpoint**: All high-priority user stories complete - conflict handling works

---

## Phase 6: User Story 4 - Manage fixed or multiple target ports (Priority: P2)

**Goal**: Support fixed-ports mode for CI workflows with predetermined port lists

**Independent Test**: Provide fixed list [5432, 6379]; verify only those ports forwarded when containers match

### Tests for User Story 4 ⚠️

- [ ] T052 [P] [US4] Unit test: Fixed-ports mode filtering in tests/unit/reconciler_test.go
- [ ] T053 [P] [US4] Integration test: Fixed list → only matching ports forwarded in tests/integration/fixed_ports_test.go
- [ ] T054 [P] [US4] Integration test: No container for listed port → no forward in tests/integration/fixed_ports_test.go

### Implementation for User Story 4

- [ ] T055 [P] [US4] Implement fixed-ports mode flag parsing in internal/config/config.go
- [ ] T056 [US4] Implement port filtering (fixed vs all-published) in internal/reconcile/reconciler.go
- [ ] T057 [US4] Wire fixed-ports mode into manager in internal/manager/manager.go

**Checkpoint**: All user stories complete - fixed-ports mode works

---

## Phase 7: Robustness & Self-Healing

**Purpose**: Handle failures and edge cases across all user stories

- [ ] T058 [P] Unit test: SSH master failure→recreate in tests/unit/ssh_master_test.go
- [ ] T059 [P] Unit test: TIME_WAIT retry logic in tests/unit/forward_plan_test.go
- [ ] T060 [P] Integration test: Kill master→self-heal within 10s in tests/integration/self_heal_test.go
- [ ] T061 [P] Integration test: Event stream break→restart+reconcile in tests/integration/self_heal_test.go
- [ ] T062 [P] Integration test: Unclean disconnect→TIME_WAIT→eventual bind in tests/integration/self_heal_test.go
- [ ] T063 [P] Integration test: Exposed-only container→no forwards created in tests/integration/published_ports_test.go
- [ ] T064 [P] Integration test: Graceful shutdown (SIGTERM)→all ports released in tests/integration/shutdown_test.go
- [ ] T065 [P] Implement graceful shutdown handler (SIGTERM/SIGINT) in cmd/rdhpf/main.go
- [ ] T066 [P] Implement SSH master recreation on failure in internal/ssh/master.go
- [ ] T067 Implement full reconcile after master recreation in internal/manager/manager.go
- [ ] T068 [P] Implement graceful -O cancel for clean port release in internal/ssh/forward.go
- [ ] T069 Implement published-ports-only enforcement (ignore exposed-only) in internal/docker/inspect.go

---

## Phase 8: Observability & Diagnostics

**Purpose**: Production-grade logging and status command

- [ ] T070 [P] Add correlation IDs to logs (per container/forward) in internal/logging/logger.go
- [ ] T071 [P] Implement sensitive field redaction (hosts, keys) in internal/logging/logger.go
- [ ] T072 [P] Add --log-level flag (debug/info/warn/error) in internal/config/config.go
- [ ] T073 [P] Implement status command (rdhpf status) in cmd/rdhpf/main.go
- [ ] T074 Implement status snapshot output in internal/status/status.go
- [ ] T075 [P] Add master session health to status output in internal/status/status.go

---

## Phase 9: Test Harness & CI/CD

**Purpose**: Automated testing infrastructure

- [ ] T076 [P] Implement fake docker events generator in tests/integration/harness/fake_docker_events.go
- [ ] T077 [P] Implement SSH stub for testing in tests/integration/harness/ssh_stub.go
- [ ] T078 [P] Create GitHub Actions workflow for lint in .github/workflows/ci.yml
- [ ] T079 [P] Create GitHub Actions workflow for unit tests in .github/workflows/ci.yml
- [ ] T080 [P] Create GitHub Actions workflow for integration tests in .github/workflows/ci.yml
- [ ] T081 [P] Create GitHub Actions workflow for build (Linux + macOS) in .github/workflows/ci.yml
- [ ] T082 [P] Create GitHub Actions workflow for release on tag in .github/workflows/release.yml
- [ ] T083 Configure golangci-lint in .golangci.yml

---

## Phase 10: Documentation & Polish

**Purpose**: Production-ready release documentation

- [ ] T084 [P] Write README.md (install, quickstart, configuration)
- [ ] T085 [P] Write troubleshooting guide in docs/TROUBLESHOOTING.md
- [ ] T086 [P] Update CHANGELOG.md for v1.0.0
- [ ] T087 [P] Add code comments and package documentation
- [ ] T088 Run quickstart validation against real Docker host

---

## Dependencies & Execution Order

### Phase Dependencies

- **Setup (Phase 1)**: No dependencies - can start immediately
- **Foundational (Phase 2)**: Depends on Setup completion - BLOCKS all user stories
- **User Stories (Phase 3-6)**: All depend on Foundational phase completion
  - User stories can then proceed in parallel (if staffed)
  - Or sequentially in priority order (P1 → P2/P4 → P3)
- **Robustness (Phase 7)**: Depends on all user stories being implemented
- **Observability (Phase 8)**: Can start after US1 (P1) complete
- **Test Harness & CI (Phase 9)**: Can start after US1 (P1) complete
- **Documentation (Phase 10)**: Depends on all features complete

### User Story Dependencies

- **User Story 1 (P1)**: Depends on Foundational (Phase 2) - No dependencies on other stories
- **User Story 2 (P2)**: Depends on Foundational (Phase 2) and US1 core logic - Should still be independently testable
- **User Story 3 (P3)**: Depends on Foundational (Phase 2) - Independently testable
- **User Story 4 (P2)**: Depends on Foundational (Phase 2) and US1 core logic - Independently testable

### Within Each User Story

- Tests MUST be written and FAIL before implementation
- Core SSH/Docker wrappers before reconciler
- Reconciler before manager
- Manager before CLI wiring
- Story complete before moving to next priority

### Parallel Opportunities

- All Setup tasks marked [P] can run in parallel
- All Foundational tasks marked [P] can run in parallel (within Phase 2)
- Once Foundational phase completes, user stories can be worked on in parallel
- All tests for a user story marked [P] can run in parallel
- Within a story, tasks marked [P] can run in parallel
- Observability and Test Harness phases can proceed in parallel with User Stories 2-4

---

## Implementation Strategy

### MVP First (User Story 1 Only)

1. Complete Phase 1: Setup
2. Complete Phase 2: Foundational (CRITICAL - blocks all stories)
3. Complete Phase 3: User Story 1
4. **STOP and VALIDATE**: Test User Story 1 independently
5. Basic CI (lint + unit tests)
6. Deploy/demo if ready

### Incremental Delivery

1. Complete Setup + Foundational → Foundation ready
2. Add User Story 1 → Test independently → Deploy/Demo (MVP!)
3. Add User Story 2 → Test independently → Deploy/Demo
4. Add User Story 4 (fixed-ports mode) → Test independently → Deploy/Demo
5. Add User Story 3 → Test independently → Deploy/Demo
6. Add robustness layer → Full integration testing
7. Add observability → Production ready
8. Each increment adds value without breaking previous stories

### Parallel Team Strategy

With multiple developers:

1. Team completes Setup + Foundational together
2. Once Foundational is done:
   - Developer A: User Story 1 (P1)
   - Developer B: User Story 2 (P2)
   - Developer C: User Story 4 (P2) fixed-ports mode
   - Developer D: Test harness + CI setup
3. Stories complete and integrate independently

---

## Notes

- [P] tasks = different files, no dependencies
- [Story] label maps task to specific user story for traceability
- Each user story should be independently completable and testable
- Verify tests fail before implementing
- Commit after each task or logical group
- Stop at any checkpoint to validate story independently
- Performance target: p99 < 2s (target < 1s) for event→forward operations
- All forwards bind to 127.0.0.1 only (loopback); target remote 127.0.0.1:hostPublishedPort
- Published host ports only; exposed-only container ports are ignored