# CI/CD Pipeline Documentation

This document describes the continuous integration and continuous delivery (CI/CD) infrastructure for the Remote Docker Host Port Forwarder project.

## Overview

The project uses GitHub Actions for automated testing, code quality checks, and releases. The CI/CD pipeline ensures code quality, catches bugs early, and streamlines the release process.

## Workflows

### 1. CI Workflow (`.github/workflows/ci.yml`)

**Triggers**: Push to `main`, Pull Requests

The primary continuous integration workflow that runs on every code change.

**Jobs**:

- **lint**: Code quality checks
  - `go vet` - Static analysis
  - `gofmt -s` - Code formatting
  - `golangci-lint` - Comprehensive linting

- **test**: Infrastructure-independent tests
  - Runs on pinned Go 1.27.2 with race detection and uncached execution
  - Clears integration host/key variables; remote scenarios self-skip
  - Instruments production packages (`cmd/...`, `internal/...`) for coverage
  - Retains JSON results, stderr, exits, inventory and coverage even after failure

Real SSH/Docker scenarios run in the separate Integration Tests workflow.

- **build**: Verify build
  - Builds for `linux/amd64`
  - Uploads binary artifact

**Duration**: ~5 minutes

### 2. Release Workflow (`.github/workflows/release.yml`)

**Triggers**: Git tags matching `v*` (e.g., `v1.0.0`)

Automatically builds and publishes releases when a version tag is pushed.

**Jobs**:

- **goreleaser**: Automated release with GoReleaser
  - Builds for all target platforms
  - Creates compressed archives (tar.gz/zip)
  - Generates SHA256 checksums
  - Creates GitHub Release with notes

- **build-manual**: Fallback manual build
  - Matrix builds for multiple OS/arch combinations
  - Platforms: linux, darwin, windows
  - Architectures: amd64, arm64

**Output**: GitHub Release with binaries for all platforms

### 3. Code Quality Workflow (`.github/workflows/quality.yml`)

**Triggers**: Push to `main`, pull requests targeting `main`, manual dispatch

Advanced code quality and security checks.

**Jobs**:

- **security**: Security scanning
  - All gosec rules/SSA through the pinned official golangci-lint release; hosted config preserves medium/medium thresholds (local Make uses low/low)
  - Native golangci JSON, stderr, scanner exit, selected config and package scope; incomplete analysis fails
  - Attempts report upload even if scanning fails; scanner failure remains a failure

- **coverage**: Test coverage analysis
  - Generates HTML coverage report
  - Calculates coverage percentage
  - (Optional) Uploads to Codecov
  - Comments coverage on PRs

- **dependencies**: Dependency checks
  - Checks for outdated dependencies
  - Runs `govulncheck` for vulnerabilities
  - Verifies `go.mod` is tidy

### 4. Documentation Workflow (`.github/workflows/docs.yml`)

**Triggers**: Push to `main`, PRs affecting `docs/` or `*.md` files

Ensures documentation quality and validity.

**Jobs**:

- **markdown-lint**: Markdown style checking
- **link-check**: Validates all links in docs
- **spell-check**: Spell checking (informational)
- **validate-structure**: Checks required files exist

### 5. Integration Test Workflow (`.github/workflows/integration-test.yml`)

**Triggers**: Matching Go/module/workflow changes in pull requests targeting `main`,
manual dispatch, nightly schedule (2 AM UTC).

The `integration-test` job starts an SSH container on the isolated hosted runner,
mounts its Docker socket, and runs SSH, stream, one-port, full-suite and private
command trust phases. The fixture bootstrap pins image digests/package revisions,
uses fresh container-owned host keys, trusts only its public key obtained through
the retained container, and restores disposable-account configuration exactly.
Private keys/config backups are never uploaded. Native build/APK/runtime/API
provenance accompanies the public test evidence.

`.github/required-trust-tests.txt` names every mandatory trust subcase. The workflow
requires actual PASS records across full-suite/internal-package JSON, in addition
to both recovery guards. Native askpass positive controls must fire with
BatchMode=no; application refusal must complete before deadlines without prompting.
The conditional control-check test mixes an owned fake-child association with a
real OpenSSH command; only separate Master/HTTP scenarios establish real lifecycle
and recovery behavior. Local/preconfigured-host runs do not execute this disruptive
hosted matrix. Missing evidence or skips cannot satisfy these mandatory scenarios.

The production launch inventory is explicit (all invoke SSH with BatchMode=yes):

| Source | Actual launch methods |
| --- | --- |
| `internal/ssh/master.go` | `openConnection`, `checkConnection` |
| `internal/ssh/forward.go` | `AddForward`, `CancelForward` |
| `internal/docker/events.go` | `EventReader.stream` (owned by `Stream`) |
| `internal/docker/inspect.go` | `InspectPorts`, `inspectPortsFromLabels` |
| `internal/manager/manager.go` | `listRunning`, `hasTestInfrastructureLabel`, `validateDockerConnectivity`, `remoteDockerPingRunner.Ping` |

The eleven launch sites have actual argv and individual native BatchMode-removal
controls. A generic AST launch guard is deferred; this inventory is not a guarantee
about arbitrary future launch sites. The ping cases also require a real event on
the application's remote stream and no residual ping container, despite hostile
local Docker endpoint settings. The unchanged production `alpine:latest` ping
image is warmed on the owned daemon after the mandatory watchdog cold-miss phase,
and its actual digest is recorded separately from the pinned fixture images. Normal real inspection and label fallback cover
full IDs, short IDs and names with Docker options before `--`.

Actual-method shell regressions execute both remote shell layers using owned fake
SSH/Docker executables and real `/bin/sh`. Container operands and enclosing scripts
are independently quoted; NUL is refused before launch. Marker-only mutations
prove both boundaries are sensitive. This does not impose a strict hex-ID policy
or repair separate malformed-event/short-ID display behavior.

A separate bounded diagnostic step preserves source diffs/hashes and expected red
outcomes for the accepted-A Master overlay and each individual BatchMode removal.
These are not frozen-candidate passes: each must fail its named scenario for the
expected reason, not a compile error or deadline kill. The script restores exact
candidate source after every control. Cleanup results are retained alongside the
scenario artifacts, without private keys or account configuration backups. The
same disposable runner also exercises the documented Make fixture lifecycle:
up, a named Manager test in its per-invocation runtime HOME, down, a second up,
both real stability cases, and down. Named PASS guards reject skips; uncertain
publisher failure retains runtime storage rather than deleting its lock inode.

It does not run a nested Docker daemon or a separate remote-host job. The legacy
manual `ssh_host` input does not change the fixed localhost harness.

### Reading test evidence

Coverage measures production packages, not only external test packages; there is
no percentage gate. Coverage is not proof of traffic, recovery or a skipped scenario.
Each CI artifact includes raw Go JSON, stderr, process exits, a listing and an
inventory. Integration profiles/results are separate per phase; do not add repeated
phase executions as if they were unique tests.

- `results`: actual pass/fail/self-skip events, with top-level/subtest labels and skip output.
- `excluded`: listed top-level tests deliberately omitted by the phase selector.
- `unrun`: selected listed tests with no start event, including phases blocked by earlier failure.
- `unfinished`: tests that started but have no completion event.
- `process_exit`: Go/tee exits, or `unrun` when the command never ran.

Listing is not execution. Dynamic subtests cannot be enumerated beforehand;
missing listing/artifact data is a limit, not a pass. Empty execution files represent
no observed tests. Unit checks intentionally leave real-host scenarios self-skipped.
The one-port test separately waits for its owned nginx backend before asserting
forwarded traffic; successful preflight or local TCP acceptance alone is insufficient.

## Local Development

### Using the Makefile

The project includes a comprehensive Makefile for local development:

```bash
# Show all available targets
make help

# Build for current platform
make build

# Run unit tests
make test

# Run integration tests (requires SSH_TEST_HOST)
make test-integration

# Run linters
make lint

# Format code
make fmt

# Generate coverage report
make coverage

# Build for all platforms
make release

# Install to GOPATH/bin
make install

# Run all checks (lint + test)
make check

# Full CI pipeline locally
make ci

# Check development environment
make doctor
```

### Environment Variables

For integration tests:

```bash
# Explicitly request tests against an isolated, configured SSH/Docker host
export SSH_TEST_HOST=ssh://your-username@your-ssh-host:22

# Optional fixture-command identity only; the application ignores this variable.
# Configure OpenSSH IdentityFile (or an agent) for application authentication.
export SSH_TEST_KEY_PATH=/path/to/test-key
```

### Pre-commit Hooks

The project supports pre-commit hooks to catch issues before committing:

**Installation**: install the optional hook/documentation sets from
[CONTRIBUTING](../CONTRIBUTING.md#installing-development-tools), then:

```bash
mise exec -- pre-commit run --all-files
mise exec -- pre-commit install
```

The first hook rejects missing or ambient tool providers before any Go/Markdown
hook runs. Hook registration is explicit and optional. The manual
`scripts/pre-commit.sh` entrypoint still operates on staged Go files and checks
its required providers before running them.

**What the hooks check**:
- Code formatting (gofmt)
- Static analysis (go vet)
- Fast unit tests
- No hardcoded credentials
- No debug statements (warning only)

All local Make/Go examples assume the base tool set is installed and commands
are run through `mise exec --` or an activated mise shell.

## Running Tests Locally

### Unit Tests

```bash
# All unit tests
make test

# Specific package
go test ./internal/ssh/...

# With coverage
make coverage

# Verbose output
make test VERBOSE=1
```

### Integration Tests

```bash
# Use the configured isolated host (required by this make target)
export SSH_TEST_HOST=ssh://testuser@localhost:2222
make test-integration

# Direct with go test; a configured but unavailable host fails preflight
go test -v -count=1 -timeout=15m ./tests/integration/...

# Single test; package selection alone does not request infrastructure
go test -v -count=1 -timeout=3m -run '^TestManager_ContainerWithOnePort$' ./tests/integration/
```

### Benchmarks

```bash
# Run benchmarks
go test -bench=. -benchmem ./...

# Specific benchmark
go test -bench=BenchmarkReconcile ./internal/reconcile/
```

## Release Process

### Creating a Release

1. **Update CHANGELOG.md**:
   ```markdown
   ## [1.0.0] - 2024-01-15

   ### Added
   - New feature

   ### Fixed
   - Bug fix
   ```

2. **Create and push tag**:
   ```bash
   git tag -a v1.0.0 -m "Release v1.0.0"
   git push origin v1.0.0
   ```

3. **Automated steps**:
   - GitHub Actions builds all platform binaries
   - Creates GitHub Release
   - Attaches binaries and checksums
   - Publishes release notes

### Version Numbering

Follow [Semantic Versioning](https://semver.org/):

- **MAJOR** (v2.0.0): Breaking changes
- **MINOR** (v1.1.0): New features, backward compatible
- **PATCH** (v1.0.1): Bug fixes, backward compatible

Pre-release versions:
- `v1.0.0-rc.1` - Release candidate
- `v1.0.0-beta.1` - Beta version
- `v1.0.0-alpha.1` - Alpha version

## Adding New Workflows

To add a new workflow:

1. Create `.github/workflows/your-workflow.yml`
2. Define trigger conditions
3. Add jobs and steps
4. Test with `workflow_dispatch` trigger
5. Update this documentation

Use the existing CI workflow as the bootstrap example: SHA-pinned mise-action,
exact bootstrap checksum, automatic install/environment/PATH export and caching
disabled, then named project-scoped locked installs plus provider checks. Run
existing commands through `mise exec --`; keep Make/scripts as logic owners.
Actions and hook revisions remain in their native files, not in mise tasks.

## Troubleshooting

### CI Failures

**Lint failures**:
```bash
# Fix formatting locally
make fmt

# Run linters
make lint
```

**Test failures**:
```bash
# Run specific failing test
go test -v -run TestName ./path/to/package

# With race detector
go test -race ./...
```

**Integration test failures**:
- Check SSH_TEST_HOST is accessible
- Verify Docker is running
- Check network connectivity
- Review workflow logs for specific errors

### Build Failures

**GoReleaser issues**:
```bash
# Test release locally
mise exec -- goreleaser release --snapshot --clean --skip=publish

# Validate configuration
mise exec -- goreleaser check
```

**Platform-specific build issues**:
```bash
# Build for specific platform
GOOS=linux GOARCH=amd64 go build ./cmd/rdhpf
```

## Performance Optimization

### Caching

Managed CI tools use isolated runner-temporary config/data/cache directories.
Mise action caching is deliberately disabled during locked adoption; installs
verify the selected artifacts. This does not claim installed caches are
continuously byte-reverified. `GOTOOLCHAIN=local` prevents automatic compiler
substitution. No token persistence or additional workflow permissions are added.

### Parallel Execution

- Jobs share the pinned Go version; independent lint/test/build jobs run concurrently
- Lint and test jobs run concurrently
- Build matrix parallelizes cross-platform builds

### Fast Feedback

- Fail fast: Stops on first failure
- Unit tests complete in <1 minute
- Full CI pipeline in ~5 minutes

## Badges

Add these badges to README.md:

```markdown
[![CI](https://github.com/your-org/rdhpf/actions/workflows/ci.yml/badge.svg)](https://github.com/your-org/rdhpf/actions/workflows/ci.yml)
[![Go Report Card](https://goreportcard.com/badge/github.com/your-org/rdhpf)](https://goreportcard.com/report/github.com/your-org/rdhpf)
[![codecov](https://codecov.io/gh/your-org/rdhpf/branch/main/graph/badge.svg)](https://codecov.io/gh/your-org/rdhpf)
[![License](https://img.shields.io/github/license/your-org/rdhpf)](LICENSE)
```

## Security

### Secrets Management

Required secrets (configure in repository settings):

- `CODECOV_TOKEN` - (Optional) For coverage uploads
- `SSH_TEST_PRIVATE_KEY` - (Optional) For remote integration tests
- `SSH_TEST_USER` - (Optional) For remote integration tests

Never commit:
- API keys
- Passwords
- SSH private keys
- Access tokens

The pre-commit hooks check for hardcoded credentials.

## Future Enhancements

Planned improvements:

- [ ] Docker image builds and publishing to registry
- [ ] Snap/deb/rpm package generation
- [ ] Automated dependency updates (Dependabot)
- [ ] Performance regression testing
- [ ] Canary deployments
- [ ] Multi-architecture Docker images

## Resources

- [GitHub Actions Documentation](https://docs.github.com/en/actions)
- [GoReleaser Documentation](https://goreleaser.com/)
- [Pre-commit Framework](https://pre-commit.com/)
- [Semantic Versioning](https://semver.org/)

### Mandatory discovery repair evidence

The additive `discovery-owned` phase executes actual Manager.Run, Docker
commands/parser, State and gated Diff/Apply against the same pinned owned fixture.
`.github/required-discovery-tests.txt` requires exact passes for:

- `TestManagerDiscoveryOwnedStartupGap`
- `TestManagerDiscoveryOwnedInventoryOverlap`
- `TestManagerDiscoveryOwnedDowntime`
- `TestManagerDiscoveryOwnedInspectRetry`

Gates surround real command dispatch/results, not simulated inventories or event
replay. Each case records full IDs, unique HTTP200 bodies, once-only target
transitions, stopped-listener ECONNREFUSED, history/CreatedAt and joined cleanup.
CLI/Engine/API negotiation and source/tree identities accompany the artifact.
The workflow records Go/tee exits and fails on missing/skipped names or malformed
JSON/jq errors; native unit tests execute that actual shell block negatively.

`scripts/itest-discovery-mutations.sh` records seven diagnostic source overlays:
`disable-fence`, `disable-retry`, `disable-absence`, `disable-partial-preserve`,
`disable-unapplied`, `disable-size-error`, and `advance-deferred-cursor`.
The last restores the capacity-starvation behavior and must fail a desired-merge
assertion for six individually admissible owners. Named assertion reds, exact original
bytes/hashes/diffs and restored greens distinguish these from candidate evidence.
The first three controls exercise all four owned cases; the remaining controls
exercise focused production-method tests. Compile failures, test timeouts and
unjoined cleanup are not accepted as regression reds.

All inherited 89 mandatory trust names, fourteen trust controls, S08 full-ID
control, both two-loss/monitor proofs, both full three-minute Make stability
cases and cleanup remain mandatory. Discovery adds evidence rather than replacing
those requirements. Native compilation is not owned runtime proof, nor does this
workflow imply release, cask/Gatekeeper or every-platform runtime clearance.

The owned fixture pins Alpine's `docker-cli=29.8.2-r1`, a same-source rebuild of
Docker CLI 29.8.2 against Alpine Go 1.26.9 (previous r0 used Go 1.26.8). The
[official Alpine recipe change](https://gitlab.alpinelinux.org/alpine/aports/-/commit/c02e0c037b032adc0301e2027ad0f3a1c0b98a7a)
changes only this recipe's package revision; the Docker upstream source commits
are unchanged. This is the fixture CLI's distribution build, not the application's
Go toolchain. Exact package/version assertions and normal apk signature checks
remain enabled; no package ranges, mirror replacement or trust bypass is used.

### Mandatory processing-watchdog evidence

The owned hosted workflow adds six exact leaf PASS requirements in
`.github/required-watchdog-tests.txt`:

- `TestWatchdogOwnedColdPull`: prove an actual `alpine:latest` image miss before
  warm-up, then exercise the unchanged probe through SSH/Engine and record its
  outcome, digest when available, and exact-name cleanup. A cached image blocks
  cold proof; the test does not delete an unowned tag or silently skip.
- `TestWatchdogOwnedWarmProcessing`: retain real processing and forwarded HTTP
  for at least 130 seconds with correlated ping Engine IDs.
- `TestWatchdogOwnedCLISilentStream`: run the built shipped CLI, hold only its
  owned stream output, and require the processing-based fatal diagnostic and
  exit 1 after ordinary cleanup, without user cancellation as the oracle.
- `TestWatchdogOwnedBlockedWork/Probe` and `/Inspect`: separately hold concrete
  owned real-SSH output/completion boundaries and record fatal/cancel/Wait times.
  The inspect case requires cancellation before its unchanged five-second budget;
  its separately recorded pre-dispatch probe hold prevents probe-generated health
  from shifting the horizon. A suppressed probe has no SSH child or Engine ID and
  is not real blocked-probe evidence.
- `TestWatchdogOwnedPullAllowance`: an owned real-probe completion delay exceeds
  its remaining allowance without delaying fatal initiation. This controlled
  delay is not a second cold-download claim.

The controller records application proxy, retained real-child Wait and controller
completion separately. A held output copier can delay Wait even after the remote
command exits; no claim that such a child was still executing is inferred. Native
unit tests separately prove Run cannot abandon its own watchdog/probe join. Local
joins and remote `--rm` removal are distinct facts; interrupted attempts enumerate
exact generated names/observed IDs and only verified owned leftovers are removed.

`scripts/itest-watchdog-mutations.sh` requires eight named assertion reds and
byte-exact restored greens: cancellation, probe independence, failed-handler
health, retained cause, one-time arming, remaining allowance, owner join and
inherited-output completion. Compile errors, outer timeouts, races and cleanup
leaks are not valid reds. Native guard tests execute the workflow's actual jq
predicate against missing, skipped, wrong-package/name, malformed and unfinished
records. Native unselected hosted tests self-skip and do not supply this proof.

The former 35-second log-only
`TestEventWatchdog_PingRunsAndDoesNotKillManagerWhenHealthy` and
`TestEventWatchdog_ExecutesPingLocally` scenarios are replaced by the warm and
CLI cases above. Production 10/30/60 timings are unchanged; the existing 35-minute
job allowance gains 25 minutes for additive phase envelopes (60 total). All 89
trust names, 14 trust controls, four discovery cases/seven controls, S08 identity
control, both two-loss HTTP/monitor scenarios and both full three-minute Make
stability cases plus cleanup remain required. A workflow's green summary alone
is not scenario, remote-cleanup or release clearance.
