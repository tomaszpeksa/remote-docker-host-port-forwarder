# Status Improvement Implementation Tasks

Based on the design in [`status-improvement-design.md`](status-improvement-design.md).

---

## Phase 1: State File Implementation

### Task 1.1: Extend State Model with Timestamps
**Priority:** High  
**Estimated Effort:** 1-2 hours

**Changes:**
- [ ] Modify [`internal/state/model.go`](../internal/state/model.go):
  - Add `CreatedAt time.Time` field to `ForwardState`
  - Add `UpdatedAt time.Time` field to `ForwardState`
  - Update `SetActual()` to preserve `CreatedAt` on updates
  - Update `SetActual()` to set `UpdatedAt` to current time
  - Update `MarkActive()`, `MarkConflict()`, `MarkPending()` to use new signature

**Tests:**
- [ ] Unit test: Verify `CreatedAt` is preserved across status updates
- [ ] Unit test: Verify `UpdatedAt` changes on each update
- [ ] Unit test: Verify new forward gets current time as `CreatedAt`

**Example:**
```go
// Before
s.actual[containerID][port] = ForwardState{
    ContainerID: containerID,
    Port:        port,
    Status:      status,
    Reason:      reason,
}

// After
existing := s.actual[containerID][port]
now := time.Now()
createdAt := now
if !existing.CreatedAt.IsZero() {
    createdAt = existing.CreatedAt
}

s.actual[containerID][port] = ForwardState{
    ContainerID: containerID,
    Port:        port,
    Status:      status,
    Reason:      reason,
    CreatedAt:   createdAt,
    UpdatedAt:   now,
}
```

---

### Task 1.2: Create State File Package
**Priority:** High  
**Estimated Effort:** 3-4 hours

**New Package:** `internal/statefile/`

**Files to Create:**
- [ ] `internal/statefile/statefile.go` - Core types and interfaces
- [ ] `internal/statefile/writer.go` - State file writer with locking
- [ ] `internal/statefile/reader.go` - State file reader
- [ ] `internal/statefile/path.go` - Path resolution and host hashing

**Core Types:**
```go
// statefile.go
package statefile

import "time"

type StateFile struct {
    Version   string            `json:"version"`
    Host      string            `json:"host"`
    PID       int               `json:"pid"`
    StartedAt time.Time         `json:"started_at"`
    UpdatedAt time.Time         `json:"updated_at"`
    Forwards  []ForwardSnapshot `json:"forwards"`
}

type ForwardSnapshot struct {
    ContainerID string    `json:"container_id"`
    LocalPort   int       `json:"local_port"`
    RemotePort  int       `json:"remote_port"`
    Status      string    `json:"status"`
    Reason      string    `json:"reason"`
    CreatedAt   time.Time `json:"created_at"`
    UpdatedAt   time.Time `json:"updated_at"`
}

const (
    CurrentVersion = "1.0"
    MaxStateAge    = 10 * time.Second
)
```

**Writer Interface:**
```go
// writer.go
type Writer struct {
    path      string
    host      string
    pid       int
    startedAt time.Time
    mu        sync.Mutex
}

func NewWriter(host string) (*Writer, error)
func (w *Writer) Write(forwards []state.ForwardState) error
func (w *Writer) Close() error // Delete file
```

**Reader Interface:**
```go
// reader.go
type Reader struct {
    path string
}

func NewReader(host string) (*Reader, error)
func (r *Reader) Read() (*StateFile, error)
func (r *Reader) IsStale() bool
```

**Path Resolution:**
```go
// path.go
func GetStateFilePath(host string) (string, error) {
    homeDir, err := os.UserHomeDir()
    if err != nil {
        return "", err
    }
    
    rdhpfDir := filepath.Join(homeDir, ".rdhpf")
    if err := os.MkdirAll(rdhpfDir, 0700); err != nil {
        return "", err
    }
    
    hostHash := hashHost(host)
    return filepath.Join(rdhpfDir, hostHash+".state.json"), nil
}

func hashHost(host string) string {
    h := sha256.Sum256([]byte(host))
    encoded := base64.RawURLEncoding.EncodeToString(h[:])
    return encoded[:12]
}
```

**Tests:**
- [ ] Unit test: Path resolution with valid host
- [ ] Unit test: Host hashing produces consistent results
- [ ] Unit test: Write → Read roundtrip
- [ ] Unit test: File locking prevents concurrent corruption
- [ ] Unit test: Staleness detection works correctly
- [ ] Unit test: Missing file returns appropriate error

---

### Task 1.3: Integrate State Writer into Manager
**Priority:** High  
**Estimated Effort:** 2-3 hours

**Changes:**
- [ ] Modify [`internal/manager/manager.go`](../internal/manager/manager.go):
  - Add `stateWriter *statefile.Writer` field
  - Initialize writer in `NewManager()` or `Run()`
  - Start background writer goroutine
  - Write state on reconciliation events
  - Clean up writer on shutdown

**Background Writer:**
```go
func (m *Manager) startStateWriter(ctx context.Context) {
    ticker := time.NewTicker(2 * time.Second)
    defer ticker.Stop()
    
    for {
        select {
        case <-ticker.C:
            forwards := m.state.GetActual()
            if err := m.stateWriter.Write(forwards); err != nil {
                m.logger.Warn("failed to write state file", "error", err)
            }
        case <-ctx.Done():
            return
        }
    }
}
```

**Immediate Writes on Events:**
```go
// After reconciliation
forwards := m.state.GetActual()
if err := m.stateWriter.Write(forwards); err != nil {
    m.logger.Warn("failed to write state file", "error", err)
}
```

**Cleanup:**
```go
// In shutdown sequence
if err := m.stateWriter.Close(); err != nil {
    m.logger.Warn("failed to clean up state file", "error", err)
}
```

**Tests:**
- [ ] Integration test: State file created on startup
- [ ] Integration test: State updates within 2 seconds
- [ ] Integration test: State file deleted on graceful shutdown
- [ ] Integration test: State file remains after crash (manual kill)

---

### Task 1.4: Update Status Command to Read State File
**Priority:** High  
**Estimated Effort:** 2-3 hours

**Changes:**
- [ ] Modify [`cmd/rdhpf/main.go`](../cmd/rdhpf/main.go):
  - Replace `getActiveForwards()` implementation
  - Use `statefile.Reader` instead of `lsof` parsing
  - Add staleness warning
  - Keep fallback to `lsof` if state file unavailable

**New Implementation:**
```go
func getActiveForwards(ctx context.Context, host string) ([]status.Forward, error) {
    reader, err := statefile.NewReader(host)
    if err != nil {
        // Fallback to old lsof method
        return getActiveForwardsLegacy(ctx, host)
    }
    
    stateFile, err := reader.Read()
    if err != nil {
        if os.IsNotExist(err) {
            // No running instance
            return []status.Forward{}, nil
        }
        return nil, err
    }
    
    // Check staleness
    if reader.IsStale() {
        age := time.Since(stateFile.UpdatedAt)
        fmt.Fprintf(os.Stderr, "Warning: State is %v old (rdhpf may not be running)\n", age.Round(time.Second))
    }
    
    // Convert to status.Forward
    forwards := make([]status.Forward, len(stateFile.Forwards))
    for i, f := range stateFile.Forwards {
        forwards[i] = status.Forward{
            ContainerID: f.ContainerID,
            LocalPort:   f.LocalPort,
            RemotePort:  f.RemotePort,
            State:       f.Status,
            Duration:    time.Since(f.CreatedAt),
            Reason:      f.Reason,
        }
    }
    
    return forwards, nil
}
```

**Tests:**
- [ ] Integration test: Status reads from running instance
- [ ] Integration test: Status shows warning for stale state
- [ ] Integration test: Status shows "No running instance" if file missing
- [ ] Integration test: Container IDs displayed correctly
- [ ] Integration test: Durations calculated correctly
- [ ] Integration test: Reasons displayed for conflict/pending states

---

### Task 1.5: Update Status Display Format
**Priority:** Medium  
**Estimated Effort:** 1 hour

**Changes:**
- [ ] Modify [`internal/status/status.go`](../internal/status/status.go):
  - Update `Forward` struct to use `time.Duration` properly
  - Ensure JSON/YAML output includes duration as string
  - Improve table formatting

**Enhanced Display:**
```go
func FormatTable(forwards []Forward) string {
    if len(forwards) == 0 {
        return "No active forwards\n"
    }
    
    var sb strings.Builder
    
    // Header with better alignment
    sb.WriteString(fmt.Sprintf("%-16s %-20s %-12s %-10s %-12s %s\n",
        "CONTAINER",
        "LOCAL",
        "REMOTE",
        "STATE",
        "DURATION",
        "REASON"))
    sb.WriteString(strings.Repeat("-", 100))
    sb.WriteString("\n")
    
    for _, f := range forwards {
        containerID := f.ContainerID
        if len(containerID) > 16 {
            containerID = containerID[:12] + "..."
        }
        
        local := fmt.Sprintf("127.0.0.1:%d", f.LocalPort)
        remote := fmt.Sprintf("%d", f.RemotePort)
        duration := formatDuration(f.Duration)
        
        sb.WriteString(fmt.Sprintf("%-16s %-20s %-12s %-10s %-12s %s\n",
            containerID,
            local,
            remote,
            f.State,
            duration,
            f.Reason))
    }
    
    return sb.String()
}
```

**Tests:**
- [ ] Unit test: Table formatting with various durations
- [ ] Unit test: JSON output includes duration
- [ ] Unit test: YAML output includes duration
- [ ] Unit test: Long container IDs truncated properly
- [ ] Unit test: Long reasons don't break formatting

---

## Phase 2: Unix Socket Implementation

### Task 2.1: Create Socket Package
**Priority:** Medium  
**Estimated Effort:** 4-5 hours

**New Package:** `internal/socket/`

**Files to Create:**
- [ ] `internal/socket/socket.go` - Core types and protocol
- [ ] `internal/socket/server.go` - Socket server implementation
- [ ] `internal/socket/client.go` - Socket client implementation
- [ ] `internal/socket/path.go` - Socket path resolution

**Protocol Types:**
```go
// socket.go
package socket

type Request struct {
    Method string                 `json:"method"`
    Params map[string]interface{} `json:"params"`
}

type Response struct {
    Result interface{} `json:"result,omitempty"`
    Error  string      `json:"error,omitempty"`
}

type StatusResult struct {
    Host      string            `json:"host"`
    PID       int               `json:"pid"`
    StartedAt time.Time         `json:"started_at"`
    Forwards  []ForwardSnapshot `json:"forwards"`
}
```

**Server:**
```go
// server.go
type Server struct {
    listener   net.Listener
    socketPath string
    state      *state.State
    host       string
    pid        int
    startedAt  time.Time
    logger     *slog.Logger
}

func NewServer(host string, state *state.State, logger *slog.Logger) (*Server, error)
func (s *Server) Start(ctx context.Context) error
func (s *Server) handleConnection(conn net.Conn)
func (s *Server) Close() error
```

**Client:**
```go
// client.go
type Client struct {
    socketPath string
}

func NewClient(host string) (*Client, error)
func (c *Client) GetStatus() (*StatusResult, error)
```

**Tests:**
- [ ] Unit test: Request/response serialization
- [ ] Integration test: Server accepts connections
- [ ] Integration test: Client can query status
- [ ] Integration test: Socket cleanup on server exit
- [ ] Integration test: Multiple concurrent client connections

---

### Task 2.2: Integrate Socket Server into Manager
**Priority:** Medium  
**Estimated Effort:** 2-3 hours

**Changes:**
- [ ] Modify [`internal/manager/manager.go`](../internal/manager/manager.go):
  - Add `socketServer *socket.Server` field
  - Start socket server alongside state writer
  - Ensure socket shutdown in cleanup

**Implementation:**
```go
// In Run()
socketServer, err := socket.NewServer(cfg.Host, m.state, m.logger)
if err != nil {
    m.logger.Warn("failed to start socket server", "error", err)
    // Continue without socket (state file still works)
} else {
    go socketServer.Start(ctx)
    defer socketServer.Close()
}
```

**Tests:**
- [ ] Integration test: Socket available while rdhpf running
- [ ] Integration test: Socket removed on graceful shutdown
- [ ] Integration test: Socket works alongside state file

---

### Task 2.3: Add Socket Support to Status Command
**Priority:** Medium  
**Estimated Effort:** 2 hours

**Changes:**
- [ ] Modify [`cmd/rdhpf/main.go`](../cmd/rdhpf/main.go):
  - Try socket first in `getActiveForwards()`
  - Fallback to state file if socket unavailable
  - Log which method was used (debug mode)

**Implementation:**
```go
func getActiveForwards(ctx context.Context, host string) ([]status.Forward, error) {
    // Try socket first (real-time)
    client, err := socket.NewClient(host)
    if err == nil {
        result, err := client.GetStatus()
        if err == nil {
            return convertToForwards(result.Forwards), nil
        }
    }
    
    // Fallback to state file
    reader, err := statefile.NewReader(host)
    if err != nil {
        return getActiveForwardsLegacy(ctx, host)
    }
    
    stateFile, err := reader.Read()
    if err != nil {
        if os.IsNotExist(err) {
            return []status.Forward{}, nil
        }
        return nil, err
    }
    
    if reader.IsStale() {
        age := time.Since(stateFile.UpdatedAt)
        fmt.Fprintf(os.Stderr, "Warning: State is %v old\n", age.Round(time.Second))
    }
    
    return convertToForwards(stateFile.Forwards), nil
}
```

**Tests:**
- [ ] Integration test: Socket used when available
- [ ] Integration test: Falls back to file if socket unavailable
- [ ] Integration test: Falls back to lsof if no state at all

---

## Testing Strategy

### Unit Tests
- [ ] All new packages have 80%+ coverage
- [ ] State model timestamp logic fully tested
- [ ] Path resolution tested on different platforms
- [ ] File locking tested with concurrent access

### Integration Tests
- [ ] End-to-end: `rdhpf run` → `rdhpf status` shows correct state
- [ ] State file updates within 2 seconds of changes
- [ ] Graceful shutdown cleans up files
- [ ] Crash leaves state file for debugging
- [ ] Status command handles all scenarios:
  - Running instance (socket available)
  - Running instance (socket unavailable, file fresh)
  - Crashed instance (file stale)
  - No instance (no file)

### Manual Testing Scenarios
1. Start rdhpf, check status shows accurate info
2. Start container, verify status updates within 2s
3. Create port conflict, verify reason shown
4. Stop rdhpf gracefully, verify state file removed
5. Kill rdhpf with SIGKILL, verify state file remains
6. Run status against stale state, verify warning shown

---

## Documentation Updates

### User-Facing Documentation
- [ ] Update [`docs/user-guide.md`](user-guide.md):
  - Document what Reason field means for each status
  - Add examples of status output with duration/reason
  - Explain staleness warnings

- [ ] Update [`README.md`](../README.md):
  - Update status command examples
  - Show new output format

### Developer Documentation
- [ ] Create [`docs/state-persistence.md`](state-persistence.md):
  - Explain state file format
  - Document socket protocol
  - Describe fallback chain

- [ ] Update [`docs/architecture.md`](architecture.md):
  - Add state persistence component
  - Add diagram showing IPC flow

---

## Rollout Plan

### Phase 1 Milestone (State File)
**Goal:** Basic persistence working, status shows real data

**Definition of Done:**
- ✅ State file created and updated by running instance
- ✅ Status command reads state file
- ✅ Container IDs, duration, reasons all displayed correctly
- ✅ All tests passing
- ✅ Documentation updated

### Phase 2 Milestone (Unix Socket)
**Goal:** Real-time queries via socket, state file as fallback

**Definition of Done:**
- ✅ Socket server running alongside rdhpf
- ✅ Status command tries socket first
- ✅ Graceful fallback working
- ✅ All tests passing
- ✅ Documentation updated

---

## Technical Debt & Future Work

### Known Limitations
1. **Windows support**: Phase 2 socket uses Unix sockets
   - Future: Implement named pipes for Windows
   
2. **Multiple instances**: Still limited by SSH ControlMaster
   - Not a regression, existing limitation
   
3. **State file size**: Unbounded growth if many containers
   - Future: Implement rotation/cleanup

### Future Enhancements
Enabled by socket protocol:

1. **Live reload**: `rdhpf reload` to re-read config
2. **Pause/resume**: Temporarily disable forwarding
3. **Health endpoint**: `/health` for monitoring
4. **Metrics**: Export Prometheus metrics
5. **Web UI**: Simple status dashboard

---

## Risk Mitigation

### Risks
1. **File corruption** during concurrent writes
   - Mitigation: Atomic writes, file locking, write validation
   
2. **Performance impact** of background writer
   - Mitigation: 2s interval, async writes, skip if unchanged
   
3. **Disk space** from state files
   - Mitigation: Small files (~1KB per 10 forwards), cleanup on exit

4. **Breaking changes** for existing users
   - Mitigation: Graceful fallback to legacy method, version field

### Rollback Plan
If critical issues found:
1. State file is opt-in via feature flag
2. Default to legacy lsof method
3. Fix bugs, re-enable in next release