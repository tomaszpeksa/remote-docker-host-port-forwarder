# Testing & Documentation Plan for Status Improvement

## Unit Tests

### 1. History Ring Buffer Tests (`tests/unit/history_test.go`)

**Test Coverage:**

```go
// TestHistoryAdd_BasicFunctionality
// - Add single entry
// - Verify entry is retrievable
// - Verify Count() returns 1

// TestHistoryAdd_AgeTrimming
// - Add entry with EndedAt = now - 2 hours
// - Add entry with EndedAt = now - 30 minutes
// - Verify only recent entry remains
// - Verify old entry was trimmed

// TestHistoryAdd_SizeTrimming
// - Add 150 entries
// - Verify only most recent 100 remain
// - Verify oldest 50 were removed

// TestHistoryAdd_CombinedTrimming
// - Add 50 old entries (>1 hour)
// - Add 60 recent entries
// - Verify old entries removed by age
// - Verify total doesn't exceed 100

// TestHistoryGetAll
// - Add multiple entries
// - Verify GetAll returns copy (modifying result doesn't affect internal state)
// - Verify correct order

// TestHistoryClear
// - Add entries
// - Call Clear()
// - Verify Count() returns 0

// TestHistoryConcurrency
// - Goroutine 1: Add entries
// - Goroutine 2: GetAll entries
// - Goroutine 3: Add entries
// - Verify no race conditions (run with -race flag)
```

**Example Test:**
```go
func TestHistoryAdd_AgeTrimming(t *testing.T) {
    h := state.NewHistory()
    
    // Add old entry (should be trimmed)
    oldEntry := state.HistoryEntry{
        ContainerID: "old-container",
        Port:        8080,
        StartedAt:   time.Now().Add(-2 * time.Hour),
        EndedAt:     time.Now().Add(-2 * time.Hour),
        EndReason:   "test",
        FinalStatus: "active",
    }
    h.Add(oldEntry)
    
    // Add recent entry (should remain)
    recentEntry := state.HistoryEntry{
        ContainerID: "recent-container",
        Port:        8080,
        StartedAt:   time.Now().Add(-30 * time.Minute),
        EndedAt:     time.Now().Add(-30 * time.Minute),
        EndReason:   "test",
        FinalStatus: "active",
    }
    h.Add(recentEntry)
    
    entries := h.GetAll()
    assert.Equal(t, 1, len(entries), "Should only have recent entry")
    assert.Equal(t, "recent-container", entries[0].ContainerID)
}
```

---

### 2. State File Tests (`tests/unit/statefile_test.go`)

**Test Coverage:**

```go
// TestPathResolution
// - Verify path includes ~/.rdhpf/
// - Verify host hash is consistent
// - Verify hash is 12 characters

// TestWriteRead_Roundtrip
// - Write state with forwards and history
// - Read it back
// - Verify data matches

// TestStateFile_IsStale
// - Create state with old UpdatedAt
// - Verify IsStale() returns true
// - Create state with recent UpdatedAt
// - Verify IsStale() returns false

// TestWriter_AtomicWrite
// - Write state
// - Verify temp file is cleaned up
// - Verify final file exists

// TestWriter_FileLocking
// - Writer 1: Start writing large state
// - Writer 2: Try to write simultaneously
// - Verify no corruption

// TestReader_MissingFile
// - Try to read non-existent file
// - Verify appropriate error
```

---

### 3. Socket Tests (`tests/unit/socket_test.go`)

**Test Coverage:**

```go
// TestSocketPath
// - Verify path generation
// - Verify consistency

// TestServer_StartStop
// - Start server
// - Verify socket file created
// - Stop server
// - Verify socket file removed

// TestClient_Connect
// - Start server with test data
// - Connect client
// - Verify received correct data

// TestClient_ServerNotRunning
// - Try to connect without server
// - Verify appropriate error

// TestServer_MultipleClients
// - Start server
// - Multiple clients connect simultaneously
// - Verify all get correct data
```

---

## Integration Tests

### 1. Status with History (`tests/integration/status_history_test.go`)

**Test Scenarios:**

```go
// TestStatusHistory_ContainerLifecycle
// Setup:
// - Start rdhpf
// - Start container with port 9090
// - Wait for forward to establish
// - Check status (should show active)
// - Stop container
// - Wait for removal
// - Check status again (should show in history)
//
// Verify:
// - Active forward has no EndedAt
// - History entry has EndedAt
// - History entry has reason "container stopped"
// - Duration is calculated correctly

// TestStatusHistory_PortConflict
// Setup:
// - Bind port 9090 externally (nc -l 9090)
// - Start container wanting port 9090
// - Wait for conflict
// - Check status
//
// Verify:
// - Status shows "conflict"
// - Reason mentions port already in use
// - Duration shows time since creation

// TestStatusHistory_ShutdownTracking
// Setup:
// - Start rdhpf
// - Start container
// - Wait for forward
// - Stop rdhpf with SIGTERM
// - Check state file (should persist)
// - Read state file
//
// Verify:
// - History shows "rdhpf shutdown"
// - All active forwards moved to history

// TestStatusHistory_MaxEntries
// Setup:
// - Start/stop 150 containers sequentially
// - Check status
//
// Verify:
// - History has maximum 100 entries
// - Most recent 100 are kept

// TestStatusHistory_AgeLimit
// Setup:
// - Mock old history entries (>1 hour)
// - Add recent entries
// - Check status
//
// Verify:
// - Old entries not shown
// - Only recent entries within 1 hour
```

**Example Test:**
```go
func TestStatusHistory_ContainerLifecycle(t *testing.T) {
    host := getTestDockerHost(t)
    
    // Start rdhpf in background
    ctx, cancel := context.WithCancel(context.Background())
    defer cancel()
    
    rdhpfCmd := exec.CommandContext(ctx, "./rdhpf", "run", "--host", host)
    require.NoError(t, rdhpfCmd.Start())
    defer rdhpfCmd.Process.Kill()
    
    time.Sleep(2 * time.Second) // startup
    
    // Start container
    containerID := startTestContainer(t, host, 9090)
    time.Sleep(3 * time.Second) // wait for forward
    
    // Check status - should be active
    status1 := getStatus(t, host)
    require.Len(t, status1, 1)
    assert.Equal(t, "active", status1[0].State)
    assert.False(t, status1[0].IsHistory)
    assert.Nil(t, status1[0].EndedAt)
    
    // Stop container
    stopTestContainer(t, host, containerID)
    time.Sleep(3 * time.Second) // wait for removal
    
    // Check status - should be in history
    status2 := getStatus(t, host)
    require.Len(t, status2, 1)
    assert.Equal(t, "stopped", status2[0].State)
    assert.True(t, status2[0].IsHistory)
    assert.NotNil(t, status2[0].EndedAt)
    assert.Contains(t, status2[0].Reason, "container stopped")
}
```

---

### 2. Socket → File Fallback (`tests/integration/status_fallback_test.go`)

**Test Scenarios:**

```go
// TestStatusFallback_SocketFirst
// Setup:
// - Start rdhpf (socket + file running)
// - Check status
//
// Verify:
// - Status comes from socket (real-time)
// - No staleness warning

// TestStatusFallback_SocketUnavailable
// Setup:
// - Start rdhpf
// - Stop rdhpf with SIGKILL (socket removed but file remains)
// - Check status immediately
//
// Verify:
// - Status falls back to file
// - Shows staleness warning if >10s

// TestStatusFallback_NoState
// Setup:
// - No rdhpf running
// - No state file
// - Check status
//
// Verify:
// - Returns empty list or appropriate error
// - No crash

// TestStatusFallback_StaleState
// Setup:
// - Create old state file (UpdatedAt = now - 2 minutes)
// - Check status
//
// Verify:
// - Reads from file
// - Shows staleness warning

// TestStatusFallback_CorruptFile
// Setup:
// - Create corrupt state file (invalid JSON)
// - Check status
//
// Verify:
// - Appropriate error message
// - No crash
```

**Example Test:**
```go
func TestStatusFallback_SocketFirst(t *testing.T) {
    host := getTestDockerHost(t)
    
    // Start rdhpf
    ctx, cancel := context.WithCancel(context.Background())
    defer cancel()
    
    rdhpfCmd := exec.CommandContext(ctx, "./rdhpf", "run", "--host", host)
    require.NoError(t, rdhpfCmd.Start())
    defer rdhpfCmd.Process.Kill()
    
    time.Sleep(2 * time.Second)
    
    // Start container
    startTestContainer(t, host, 9191)
    time.Sleep(3 * time.Second)
    
    // Capture status output
    statusCmd := exec.Command("./rdhpf", "status", "--host", host)
    output, err := statusCmd.CombinedOutput()
    require.NoError(t, err)
    
    // Should NOT have staleness warning (socket is real-time)
    assert.NotContains(t, string(output), "Warning")
    assert.Contains(t, string(output), "9191")
    assert.Contains(t, string(output), "active")
}
```

---

## Documentation Updates

### 1. README.md Updates

**Section: Usage Examples → Status checking**

Replace:
```markdown
- Status checking
  ```bash
  rdhpf status --host ssh://user@host
  rdhpf status --host ssh://user@host --format json
  rdhpf status --host ssh://user@host --format yaml
  ```
```

With:
```markdown
- Status checking (shows current forwards + last hour of history)
  ```bash
  # Table format (default)
  rdhpf status --host ssh://user@host
  
  # Example output:
  # CONTAINER        PORT     STATUS      STARTED         ENDED           REASON
  # --------------------------------------------------------------------------------
  # abc123def456     8080     active      2m ago          -               
  # xyz789abc123     5432     conflict    5m ago          -               port already in use
  # def456ghi789     3000     stopped     20m ago         10m ago         container stopped
  
  # JSON format
  rdhpf status --host ssh://user@host --format json
  
  # YAML format
  rdhpf status --host ssh://user@host --format yaml
  ```
```

**Section: Configuration**

Update status flags:
```markdown
- CLI flags (`rdhpf status`):
  - `--host` string: SSH host in format `ssh://user@host` (required)
  - `--format` string: Output format: `table`, `json`, `yaml` (default: `table`)
  
  Status shows:
  - Current active/pending/conflict forwards
  - History of forwards from the last hour (max 100 entries)
  - Reasons for conflicts or stopped forwards
```

---

### 2. docs/user-guide.md Updates

**Section: Status Command**

Add new section:
```markdown
### Understanding Status Output

The `rdhpf status` command shows both **current forwards** and **recent history**:

#### Status Values

| Status | Meaning | Action Needed |
|--------|---------|---------------|
| `active` | Forward working correctly | None |
| `conflict` | Port already in use | Free the port or stop conflicting process |
| `pending` | Forward created but not responding | Check if remote service is running | 
| `stopped` | Container was stopped | None (historical record) |

#### Reading the Output

```
CONTAINER        PORT     STATUS      STARTED         ENDED           REASON
-----------------------------------------------------------------------------------
abc123def456     8080     active      2m ago          -               
xyz789abc123     5432     conflict    5m ago          -               port already in use
def456ghi789     3000     stopped     20m ago         10m ago         container stopped
```

- **STARTED**: How long ago the forward was created
- **ENDED**: When it stopped (only for history entries)
- **REASON**: Why a forward is in conflict/pending/stopped state

#### Current vs History

- Forwards with **ENDED = "-"** are currently active
- Forwards with an **ENDED time** are historical (stopped within last hour)
- History is limited to 100 most recent entries or 1 hour, whichever is less
```

---

### 3. docs/troubleshooting.md Updates

**Add new section:**

```markdown
## State Files

### Overview

`rdhpf` maintains state in two locations for each host:

1. **Socket**: `~/.rdhpf/{host-hash}.sock` - Real-time IPC (deleted on exit)
2. **State file**: `~/.rdhpf/{host-hash}.state.json` - Persistent snapshot (deleted on graceful exit)

### State File Location

Find your state files:
```bash
ls -la ~/.rdhpf/
```

The `{host-hash}` is a 12-character hash derived from your SSH host URL.

### Viewing State Manually

```bash
cat ~/.rdhpf/*.state.json | jq .
```

### Cleaning Up Stale State

If `rdhpf` crashes (SIGKILL, OOM, etc.), state files may remain:

```bash
# Remove all state files
rm -rf ~/.rdhpf/*.state.json ~/.rdhpf/*.sock

# Or remove for specific host
rm ~/.rdhpf/AbCdEfGh1234.*
```

### Staleness Warning

If you see:
```
Warning: State is 45s old (rdhpf may not be running)
```

This means:
- The state file is more than 10 seconds old
- `rdhpf run` is likely not running or crashed
- The socket is unavailable (status fell back to reading the file)

**Resolution**: Restart `rdhpf run`
```

---

## Test Execution Plan

### Order of Implementation

1. **Unit Tests** (fastest, foundation)
   - History ring buffer tests
   - State file tests
   - Socket tests

2. **Integration Tests** (slower, end-to-end validation)
   - Status with history tests
   - Socket → file fallback tests

3. **Documentation** (parallel to testing)
   - Update examples as tests confirm behavior
   - Add troubleshooting based on test learnings

### Running Tests

```bash
# Unit tests only
go test -v ./tests/unit/...

# Integration tests only (requires Docker)
TEST_SSH_HOST=ssh://docker@linux-docker-01 go test -v ./tests/integration/...

# All tests with race detection
go test -race -v ./...

# Coverage
go test -coverprofile=coverage.out ./...
go tool cover -html=coverage.out
```

### Success Criteria

✅ **Unit Tests:**
- 80%+ coverage for new packages
- All edge cases covered (age trimming, size limits, concurrency)
- No flaky tests

✅ **Integration Tests:**
- All happy path scenarios pass
- Failure scenarios handled gracefully
- Tests run reliably in CI

✅ **Documentation:**
- Examples match actual output
- All new features documented
- Troubleshooting covers common issues

---

## Timeline Estimate

- Unit tests: 2-3 hours
- Integration tests: 3-4 hours
- Documentation: 1-2 hours
- **Total**: 6-9 hours of development time