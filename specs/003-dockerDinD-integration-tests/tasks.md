# DinD Integration Tests - Task Plan

## Task 1: Create DinD Container Infrastructure

**Goal:** Build Docker-in-Docker container that runs SSH server and real Docker daemon

**Deliverables:**
- `docker/sshd-stub-dind/Dockerfile` - DinD-based container definition
- `docker/sshd-stub-dind/entrypoint.sh` - Startup script for dockerd + sshd

**Acceptance Criteria:**
- [ ] Container builds successfully from docker:dind-alpine
- [ ] Dockerfile installs openssh-server and configures key-based auth
- [ ] Entrypoint script starts dockerd and waits for it to be ready
- [ ] Entrypoint script starts sshd in foreground
- [ ] testuser exists and is in docker group
- [ ] Container can be started with `--privileged` flag

**Testing:**
```bash
# Build
docker build -t rdhpf-sshd-stub-dind -f docker/sshd-stub-dind/Dockerfile docker/sshd-stub-dind/

# Start
docker run -d --name test-dind --privileged -p 2222:22 rdhpf-sshd-stub-dind

# Verify Docker daemon running
docker exec test-dind docker info

# Verify SSH server running
docker exec test-dind netstat -tlnp | grep :22

# Cleanup
docker stop test-dind && docker rm test-dind
```

---

## Task 2: Configure SSH Authentication in Container

**Goal:** Set up SSH key-based authentication for testuser

**Deliverables:**
- Working SSH key authentication method
- Verification script

**Acceptance Criteria:**
- [ ] Can generate SSH key locally
- [ ] Can copy public key to container
- [ ] Can SSH into container as testuser
- [ ] testuser can run `docker` commands
- [ ] No password required for SSH

**Testing:**
```bash
# Generate key
ssh-keygen -t ed25519 -f ~/.ssh/id_test_dind -N ""

# Start container with socket mount
docker run -d --name test-ssh \
  -p 2222:22 \
  -v /var/run/docker.sock:/var/run/docker.sock \
  rdhpf-sshd-stub

# Configure key
docker cp ~/.ssh/id_test_dind.pub test-dind:/tmp/pubkey
docker exec test-dind sh -c "
  mv /tmp/pubkey /home/testuser/.ssh/authorized_keys &&
  chmod 600 /home/testuser/.ssh/authorized_keys &&
  chown testuser:testuser /home/testuser/.ssh/authorized_keys
"

# Test SSH access
ssh -i ~/.ssh/id_test_dind -p 2222 -o StrictHostKeyChecking=no testuser@localhost echo "SSH works"

# Test Docker access over SSH
ssh -i ~/.ssh/id_test_dind -p 2222 -o StrictHostKeyChecking=no testuser@localhost docker info

# Cleanup
docker stop test-ssh && docker rm test-ssh
rm ~/.ssh/id_test_dind ~/.ssh/id_test_dind.pub
```

---

## Task 3: Create Stream Persistence Test

**Goal:** Add test that verifies docker events stream stays open

**Deliverables:**
- `tests/integration/docker_events_stream_test.go` - Stream persistence test

**Acceptance Criteria:**
- [ ] Test uses getTestSSHHost(t) helper
- [ ] Test creates SSH ControlMaster connection
- [ ] Test starts docker events stream
- [ ] Test fails if stream closes before 5 seconds
- [ ] Test passes if stream stays open for 5+ seconds
- [ ] Test verifies clean cancellation
- [ ] Test has clear error messages indicating bug detection

**Testing:**
```bash
# With DinD container running and SSH configured
SSH_TEST_HOST=ssh://testuser@localhost:2222 \
  go test -v -run TestDockerEventsStreamPersistence ./tests/integration/

# Expected output:
# - If stream closes early: FAIL with "BUG DETECTED: Event stream closed after XXms"
# - If stream persists: PASS with "Stream stayed open for 5+ seconds"
```

---

## Task 4: Update GitHub Actions Workflow

**Goal:** Use SSH container with Docker socket mount in CI

**Deliverables:**
- Updated `.github/workflows/integration-test.yml`

**Acceptance Criteria:**
- [x] Build step uses `docker/sshd-stub-dind/Dockerfile`
- [x] Container started with Docker socket mount
- [x] Workflow configures SSH key authentication
- [x] Workflow tests SSH + Docker access before running tests
- [x] All test phases run successfully in phased approach
- [x] Cleanup step removes SSH container

**Testing:**
```bash
# Create a test branch and push
git checkout -b test/dind-integration
git add .
git commit -m "test: DinD integration tests"
git push origin test/dind-integration

# Create PR and watch CI run
# Verify all steps pass
```

---

## Task 5: Clean Up Legacy Infrastructure

**Goal:** Remove old shim/harness infrastructure and consolidate to socket mount approach

**Deliverables:**
- Removed `docker/sshd-stub/` directory
- Removed `tests/integration/harness/` directory (docker-shim and scenarios)
- Removed `logs/` directory and added to `.gitignore`
- Removed legacy CI job from `.github/workflows/ci.yml`
- Updated Makefile descriptions
- Updated documentation

**Acceptance Criteria:**
- [x] Legacy shim/harness directories removed
- [x] logs/ added to .gitignore
- [x] No duplicate CI jobs
- [x] Makefile descriptions updated
- [x] Documentation reflects socket mount approach
- [x] All integration tests still pass

**Testing:**
```bash
# Verify cleanup
git status
git diff

# Run local integration tests
./scripts/itest-up.sh
make itest
./scripts/itest-down.sh
```

---

## Task 6: Update Documentation

**Goal:** Document the DinD approach and how it catches bugs

**Deliverables:**
- Updated `docs/ci-integration-tests.md`
- Updated `CONTRIBUTING.md`
- Updated `CHANGELOG.md`

**Acceptance Criteria:**
- [x] Documentation explains socket mount approach
- [x] Documentation shows how to test locally
- [x] Documentation explains what bugs this catches
- [x] CONTRIBUTING.md has correct integration test instructions
- [x] All links and code examples work

**Testing:**
```bash
# Follow documentation to run tests locally
# Verify all commands work as documented
```

---

## Task 7: End-to-End Verification

**Goal:** Verify complete solution works and would catch the bug

**Deliverables:**
- Working CI pipeline
- Evidence that stream persistence test works
- Performance metrics

**Acceptance Criteria:**
- [x] PR CI passes all integration tests
- [x] Stream persistence test runs in CI
- [x] Test catches stream closing bugs
- [x] CI runtime acceptable (<10 minutes total)
- [x] Tests are stable and non-flaky

**Testing:**
```bash
# Run full test suite locally
SSH_TEST_HOST=ssh://testuser@localhost:2222 go test -v ./tests/integration/...

# Introduce artificial bug to verify test catches it
# (temporarily modify events.go to close stream early)

# Run test - should FAIL
SSH_TEST_HOST=ssh://testuser@localhost:2222 \
  go test -v -run TestDockerEventsStreamPersistence ./tests/integration/

# Revert artificial bug - should PASS
```

---

## Overall Acceptance Criteria

**Completed:**
- ✅ SSH container with Docker socket mount
- ✅ SSH authentication works
- ✅ Real Docker daemon accessible via socket
- ✅ Stream persistence test catches early-closing streams
- ✅ All existing integration tests pass
- ✅ CI pipeline executes successfully in phases
- ✅ Clear documentation
- ✅ Fast CI execution (~5-7 minutes)
- ✅ Easy local testing setup with scripts
- ✅ Legacy shim/harness infrastructure removed
- ✅ Single source of truth for CI integration tests

---

## Task Dependencies

```
Task 1 (SSH Container)
  ↓
Task 2 (SSH Auth)
  ↓
Task 3 (Stream Test)
  ↓
Task 4 (CI Workflow)
  ↓
Task 5 (Cleanup Legacy) + Task 6 (Documentation)
  ↓
Task 7 (Verification)
```

**Status:** All tasks completed ✅

---

## Time Estimates (Reference Only)

- Task 1: ~30 minutes (Dockerfile + entrypoint)
- Task 2: ~15 minutes (SSH setup)
- Task 3: ~30 minutes (Test code)
- Task 4: ~20 minutes (Workflow update)
- Task 5: ~15 minutes (Cleanup)
- Task 6: ~20 minutes (Documentation)
- Task 7: ~30 minutes (Verification)

**Total: ~2.5 hours** (for implementation + testing)

---

## Rollback Plan

If issues arise at any task:

**Task 1-2 Issues:** Fix Dockerfile/entrypoint locally before committing
**Task 3 Issues:** Test can be disabled temporarily with `t.Skip()`
**Task 4 Issues:** Revert workflow to previous shim-based version
**Task 5-7 Issues:** Non-blocking, can be fixed in follow-up PR

**Emergency Rollback:**
```bash
git revert <commit-sha>
git push origin main