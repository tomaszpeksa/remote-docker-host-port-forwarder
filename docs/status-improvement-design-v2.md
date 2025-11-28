# Status Improvement Design V2: Simplified with History

## Design Changes from V1

**Simplified based on feedback:**
1. ✅ **No JSON-RPC** - Socket just dumps JSON and closes connection
2. ✅ **No backward compatibility** - Clean break, no legacy fallback
3. ✅ **History tracking** - Keep last 100 entries or 1 hour, whichever is less
4. ✅ **Unified output** - Current + history in single list, sorted by most recent activity

---

## Problem Statement

Current `rdhpf status` shows:
- ❌ Container ID: `"unknown"`
- ❌ Duration: `"0s"` (hardcoded)
- ❌ No history of recently ended forwards
- ❌ Stale information (parses lsof, not running process)

---

## Status Values & Semantics

### For Current Forwards (Active)
| Status | Meaning | Reason Field |
|--------|---------|-------------|
| `active` | Forward working correctly | Empty |
| `conflict` | Port conflict, cannot establish | `"port already in use after 5 retry attempts"` |
| `pending` | Forward created but not responding | `"port not responding"` |

### For History Entries (Ended)
| Status | Meaning | End Reason |
|--------|---------|-----------|
| `stopped` | Container stopped normally | `"container stopped"` |
| `removed` | Container removed | `"container removed"` |
| `shutdown` | rdhpf shut down gracefully | `"rdhpf shutdown"` |
| `conflict` | Never successfully established | `"port conflict"` |
| `replaced` | Port stolen by another container | `"port claimed by {container}"` |

---

## State Model

### Current Forwards (In-Memory)

```go
// internal/state/model.go
type ForwardState struct {
    ContainerID string
    Port        int
    Status      string        // "active", "conflict", "pending"
    Reason      string
    CreatedAt   time.Time
    UpdatedAt   time.Time
}
```

### History Entries (Ring Buffer)

```go
// internal/state/history.go
type HistoryEntry struct {
    ContainerID string
    Port        int
    StartedAt   time.Time
    EndedAt     time.Time
    EndReason   string        // Why it ended
    FinalStatus string        // Status before removal ("active", "conflict", etc.)
}

type History struct {
    entries []HistoryEntry
    maxSize int              // 100
    maxAge  time.Duration    // 1 hour
    mu      sync.RWMutex
}

func NewHistory() *History {
    return &History{
        entries: make([]HistoryEntry, 0, 100),
        maxSize: 100,
        maxAge:  time.Hour,
    }
}

func (h *History) Add(entry HistoryEntry) {
    h.mu.Lock()
    defer h.mu.Unlock()
    
    // Add to end
    h.entries = append(h.entries, entry)
    
    // Trim old entries (older than 1 hour)
    now := time.Now()
    cutoff := now.Add(-h.maxAge)
    
    filtered := make([]HistoryEntry, 0, len(h.entries))
    for _, e := range h.entries {
        if e.EndedAt.After(cutoff) {
            filtered = append(filtered, e)
        }
    }
    h.entries = filtered
    
    // Trim if exceeds max size (keep most recent 100)
    if len(h.entries) > h.maxSize {
        h.entries = h.entries[len(h.entries)-h.maxSize:]
    }
}

func (h *History) GetAll() []HistoryEntry {
    h.mu.RLock()
    defer h.mu.RUnlock()
    
    result := make([]HistoryEntry, len(h.entries))
    copy(result, h.entries)
    return result
}
```

---

## State File Format

### Location
```
~/.rdhpf/{host-hash}.state.json
```

Where `{host-hash} = base64(sha256(host))[:12]`

### Format

```json
{
  "version": "2.0",
  "host": "ssh://user@example.com",
  "pid": 12345,
  "started_at": "2025-11-27T15:00:00Z",
  "updated_at": "2025-11-27T15:30:45Z",
  "forwards": [
    {
      "container_id": "abc123def456",
      "port": 8080,
      "status": "active",
      "reason": "",
      "created_at": "2025-11-27T15:01:23Z",
      "updated_at": "2025-11-27T15:01:24Z"
    }
  ],
  "history": [
    {
      "container_id": "xyz789abc123",
      "port": 5432,
      "started_at": "2025-11-27T14:20:00Z",
      "ended_at": "2025-11-27T14:45:00Z",
      "end_reason": "container stopped",
      "final_status": "active"
    }
  ]
}
```

---

## Unix Socket Protocol (Simplified)

### No RPC - Just Stream JSON

**Client connects → Server writes JSON → Server closes**

```go
// Server side (in rdhpf run)
func handleConnection(conn net.Conn, state *state.State, history *state.History) {
    defer conn.Close()
    
    response := StatusSnapshot{
        Version:   "2.0",
        Host:      host,
        PID:       os.Getpid(),
        StartedAt: startTime,
        UpdatedAt: time.Now(),
        Forwards:  state.GetActual(),
        History:   history.GetAll(),
    }
    
    json.NewEncoder(conn).Encode(response)
}

// Client side (in rdhpf status)
func getStatusViaSocket(socketPath string) (*StatusSnapshot, error) {
    conn, err := net.Dial("unix", socketPath)
    if err != nil {
        return nil, err
    }
    defer conn.Close()
    
    var snapshot StatusSnapshot
    if err := json.NewDecoder(conn).Decode(&snapshot); err != nil {
        return nil, err
    }
    
    return &snapshot, nil
}
```

**No request/response protocol needed** - connection itself is the request.

---

## History Lifecycle

### When to Add History Entries

```go
// In reconciler.Apply() - when removing a forward
func (r *Reconciler) removeForward(containerID string, port int, reason string) {
    // Get current state before removal
    currentState := r.state.GetByContainer(containerID)
    var finalStatus string
    for _, fs := range currentState {
        if fs.Port == port {
            finalStatus = fs.Status
            break
        }
    }
    
    // Create history entry
    entry := state.HistoryEntry{
        ContainerID: containerID,
        Port:        port,
        StartedAt:   fs.CreatedAt,
        EndedAt:     time.Now(),
        EndReason:   reason,
        FinalStatus: finalStatus,
    }
    
    r.history.Add(entry)
    
    // Remove from state
    r.state.ClearPort(containerID, port)
}
```

### Removal Reasons

```go
const (
    ReasonContainerStopped = "container stopped"
    ReasonContainerRemoved = "container removed"
    ReasonRdhpfShutdown    = "rdhpf shutdown"
    ReasonPortConflict     = "port conflict"
    ReasonReplacedBy       = "port claimed by %s"  // format with container ID
)
```

### Example Scenarios

**Container stops:**
```go
// Docker event: container die/stop
endReason = "container stopped"
```

**Port stolen by another container:**
```go
// Reconciler removes old forward before adding new one
endReason = fmt.Sprintf("port claimed by %s", newContainerID[:12])
```

**rdhpf shutdown:**
```go
// In cleanup()
for _, forward := range allForwards {
    addToHistory(forward, "rdhpf shutdown")
}
```

---

## Unified Output Format

### Single List, Sorted by Most Recent Activity

```
CONTAINER        PORT    STATUS      STARTED         ENDED           REASON
-----------------------------------------------------------------------------------
abc123def456     8080    active      2m ago          -               
xyz789abc123     5432    conflict    5m ago          -               port already in use
def456ghi789     3000    stopped     20m ago         10m ago         container stopped
ghi789jkl012     27017   stopped     45m ago         30m ago         container removed
jkl012mno345     6379    shutdown    1h ago          58m ago         rdhpf shutdown
```

**Sort order:** Most recent activity first
- Activity = MAX(created_at, updated_at, ended_at)
- Current forwards have no ended_at, so use updated_at
- History entries use ended_at

### Status Column Logic

```go
func getDisplayStatus(forward Forward, isHistory bool) string {
    if isHistory {
        // Map final_status to display status
        switch forward.FinalStatus {
        case "active":
            return "stopped"  // Was active when it ended
        case "conflict":
            return "conflict" // Never worked
        default:
            return "ended"
        }
    }
    return forward.Status // "active", "conflict", "pending"
}
```

---

## Implementation Architecture

```
┌─────────────────┐
│  rdhpf run      │
├─────────────────┤
│ State Manager   │───┐
│ History Manager │   │
│                 │   │ Write every 2s
│ State Writer    │◄──┤ + on events
│ Socket Server   │   │
└─────────────────┘   │
         │            │
         │ writes     │
         ▼            ▼
   ~/.rdhpf/         ~/.rdhpf/
   host.sock         host.state.json


┌─────────────────┐
│ rdhpf status    │
├─────────────────┤
│ 1. Try socket   │──► Connect to host.sock
│ 2. Fallback to  │──► Read host.state.json
│    state file   │
│ 3. Merge & sort │
│ 4. Display      │
└─────────────────┘
```

### State Writer (Background)

```go
func (m *Manager) startStateWriter(ctx context.Context) {
    ticker := time.NewTicker(2 * time.Second)
    defer ticker.Stop()
    
    for {
        select {
        case <-ticker.C:
            snapshot := StateSnapshot{
                Version:   "2.0",
                Host:      m.cfg.Host,
                PID:       os.Getpid(),
                StartedAt: m.startTime,
                UpdatedAt: time.Now(),
                Forwards:  m.state.GetActual(),
                History:   m.history.GetAll(),
            }
            
            if err := m.stateWriter.Write(snapshot); err != nil {
                m.logger.Warn("failed to write state", "error", err)
            }
            
        case <-ctx.Done():
            // Final write before exit
            snapshot := m.createSnapshot()
            m.stateWriter.Write(snapshot)
            return
        }
    }
}
```

---

## Status Command Flow

```go
func runStatus(cmd *cobra.Command, args []string) error {
    socketPath := getSocketPath(flagHost)
    
    // Try socket (real-time)
    snapshot, err := getStatusViaSocket(socketPath)
    if err != nil {
        // Fallback to state file
        snapshot, err = getStatusFromFile(flagHost)
        if err != nil {
            return fmt.Errorf("no running rdhpf instance found")
        }
        
        // Warn if stale
        if time.Since(snapshot.UpdatedAt) > 10*time.Second {
            fmt.Fprintf(os.Stderr, "Warning: State is %v old\n", 
                time.Since(snapshot.UpdatedAt).Round(time.Second))
        }
    }
    
    // Merge current + history
    entries := mergeForDisplay(snapshot)
    
    // Sort by most recent activity
    sort.Slice(entries, func(i, j int) bool {
        return entries[i].MostRecentActivity().After(
            entries[j].MostRecentActivity())
    })
    
    // Display
    switch flagFormat {
    case "json":
        fmt.Print(formatJSON(entries))
    case "yaml":
        fmt.Print(formatYAML(entries))
    default:
        fmt.Print(formatTable(entries))
    }
    
    return nil
}
```

### Merged Display Entry

```go
type DisplayEntry struct {
    ContainerID string
    Port        int
    Status      string        // "active", "conflict", "pending", "stopped", "shutdown"
    StartedAt   time.Time
    EndedAt     *time.Time    // nil for current forwards
    Reason      string
    IsHistory   bool
}

func (d DisplayEntry) MostRecentActivity() time.Time {
    if d.EndedAt != nil {
        return *d.EndedAt
    }
    return d.StartedAt
}
```

---

## Table Formatting

```go
func formatTable(entries []DisplayEntry) string {
    var sb strings.Builder
    
    // Header
    sb.WriteString(fmt.Sprintf("%-16s %-8s %-10s %-16s %-16s %s\n",
        "CONTAINER", "PORT", "STATUS", "STARTED", "ENDED", "REASON"))
    sb.WriteString(strings.Repeat("-", 100) + "\n")
    
    // Rows
    for _, e := range entries {
        container := truncate(e.ContainerID, 16)
        port := fmt.Sprintf("%d", e.Port)
        status := e.Status
        started := formatTimeAgo(e.StartedAt)
        ended := "-"
        if e.EndedAt != nil {
            ended = formatTimeAgo(*e.EndedAt)
        }
        reason := e.Reason
        
        sb.WriteString(fmt.Sprintf("%-16s %-8s %-10s %-16s %-16s %s\n",
            container, port, status, started, ended, reason))
    }
    
    return sb.String()
}

func formatTimeAgo(t time.Time) string {
    d := time.Since(t)
    
    if d < time.Minute {
        return fmt.Sprintf("%ds ago", int(d.Seconds()))
    }
    if d < time.Hour {
        return fmt.Sprintf("%dm ago", int(d.Minutes()))
    }
    if d < 24*time.Hour {
        return fmt.Sprintf("%dh ago", int(d.Hours()))
    }
    // Shouldn't happen (max 1 hour history)
    return t.Format("15:04:05")
}
```

### Example Output

```
CONTAINER        PORT     STATUS      STARTED         ENDED           REASON
-----------------------------------------------------------------------------------
abc123def456     8080     active      2m ago          -               
xyz789abc123     5432     conflict    5m ago          -               port already in use
def456ghi789     3000     stopped     20m ago         10m ago         container stopped
ghi789jkl012     27017    stopped     45m ago         30m ago         container removed
```

---

## JSON/YAML Output

Same merged list, but with full data:

```json
{
  "forwards": [
    {
      "container_id": "abc123def456",
      "port": 8080,
      "status": "active",
      "started_at": "2025-11-27T15:28:00Z",
      "ended_at": null,
      "reason": "",
      "is_history": false
    },
    {
      "container_id": "def456ghi789",
      "port": 3000,
      "status": "stopped",
      "started_at": "2025-11-27T15:10:00Z",
      "ended_at": "2025-11-27T15:20:00Z",
      "reason": "container stopped",
      "is_history": true
    }
  ]
}
```

---

## State Management Integration

### Manager Changes

```go
type Manager struct {
    // ... existing fields ...
    history      *state.History
    stateWriter  *statefile.Writer
    socketServer *socket.Server
}

func NewManager(...) *Manager {
    return &Manager{
        // ... existing ...
        history: state.NewHistory(),
    }
}
```

### Reconciler Changes

```go
// When removing a forward, add to history
func (r *Reconciler) Apply(ctx context.Context, ..., actions []Action) error {
    // ... existing remove logic ...
    
    for _, action := range removeActions {
        // Get current state before removal
        currentState := r.state.GetByContainer(action.ContainerID)
        for _, fs := range currentState {
            if fs.Port == action.Port {
                // Add to history
                r.history.Add(state.HistoryEntry{
                    ContainerID: action.ContainerID,
                    Port:        action.Port,
                    StartedAt:   fs.CreatedAt,
                    EndedAt:     time.Now(),
                    EndReason:   determineEndReason(action),
                    FinalStatus: fs.Status,
                })
                break
            }
        }
        
        // ... proceed with removal ...
    }
}
```

---

## File & Socket Paths

```go
func getStateFilePath(host string) string {
    homeDir, _ := os.UserHomeDir()
    rdhpfDir := filepath.Join(homeDir, ".rdhpf")
    os.MkdirAll(rdhpfDir, 0700)
    
    hostHash := hashHost(host)
    return filepath.Join(rdhpfDir, hostHash+".state.json")
}

func getSocketPath(host string) string {
    homeDir, _ := os.UserHomeDir()
    rdhpfDir := filepath.Join(homeDir, ".rdhpf")
    os.MkdirAll(rdhpfDir, 0700)
    
    hostHash := hashHost(host)
    return filepath.Join(rdhpfDir, hostHash+".sock")
}

func hashHost(host string) string {
    h := sha256.Sum256([]byte(host))
    return base64.RawURLEncoding.EncodeToString(h[:])[:12]
}
```

---

## Cleanup Strategy

### Graceful Shutdown

```go
func cleanup(...) error {
    // Add all current forwards to history
    for _, forward := range stateManager.GetActual() {
        history.Add(state.HistoryEntry{
            ContainerID: forward.ContainerID,
            Port:        forward.Port,
            StartedAt:   forward.CreatedAt,
            EndedAt:     time.Now(),
            EndReason:   "rdhpf shutdown",
            FinalStatus: forward.Status,
        })
    }
    
    // Write final state
    stateWriter.Write(createSnapshot())
    
    // Remove state file
    stateWriter.Delete()
    
    // Close socket
    socketServer.Close() // Also removes socket file
    
    return nil
}
```

### Crash/Kill

State file remains with last written state + history. Socket auto-removed by OS.

---

## Testing Strategy

### Unit Tests
- History ring buffer (add, trim by age, trim by size)
- Time ago formatting
- Display entry sorting
- State file serialization
- Socket connect/read/close

### Integration Tests
- End-to-end: run → status shows current forwards
- History: start container → stop container → verify in history
- Socket: run → status via socket → verify real-time data
- File fallback: kill rdhpf → status via file → verify works
- Mixed output: active + history shown together, sorted correctly

---

## Implementation Summary

**Breaking Changes (intentional):**
1. State file format version 2.0 (incompatible with v1)
2. Socket protocol simplified (just stream JSON)
3. Remove lsof fallback (require state file or socket)
4. New output format with history

**New Features:**
1. History tracking (100 entries / 1 hour)
2. Unified display (current + history)
3. Sorted by most recent activity
4. Clear end reasons for historical forwards

**File Structure:**
```
~/.rdhpf/
  AbCdEfGh1234.state.json  # State for ssh://user@host1
  AbCdEfGh1234.sock        # Socket for ssh://user@host1
  XyZ9876543Ab.state.json  # State for ssh://user@host2
  XyZ9876543Ab.sock        # Socket for ssh://user@host2
```

---

## Success Criteria

After implementation:

```bash
$ rdhpf status --host ssh://user@host
CONTAINER        PORT     STATUS      STARTED         ENDED           REASON
-----------------------------------------------------------------------------------
abc123def456     8080     active      2m ago          -               
xyz789abc123     5432     conflict    5m ago          -               port already in use
def456ghi789     3000     stopped     20m ago         10m ago         container stopped
ghi789jkl012     27017    stopped     45m ago         30m ago         container removed
jkl012mno345     6379     shutdown    58m ago         56m ago         rdhpf shutdown
```

✅ Real container IDs  
✅ Real durations  
✅ Current + history in one view  
✅ Clear reasons for ended forwards  
✅ Real-time via socket, fallback to file  