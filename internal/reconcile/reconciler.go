package reconcile

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/tomaszpeksa/remote-docker-host-port-forwarder/internal/ssh"
	"github.com/tomaszpeksa/remote-docker-host-port-forwarder/internal/state"
	"github.com/tomaszpeksa/remote-docker-host-port-forwarder/internal/util"
)

// Action represents a single port forward operation to perform
type Action struct {
	Type        string // "add" or "remove"
	ContainerID string
	Port        int
	RemotePort  int // For add operations, the remote port to forward from
}

// Reconciler compares desired and actual state to compute reconciliation actions
type Reconciler struct {
	state   *state.State
	history *state.History
	logger  *slog.Logger
}

// safeLogID returns a short version of containerID for logging.
// - For IDs >= 12 chars: returns first 12 chars (standard Docker format)
// - For IDs < 12 chars: returns full ID
// This prevents slice bounds panics with test fixtures.
func safeLogID(id string) string {
	if len(id) >= 12 {
		return id[:12]
	}
	return id
}

// NewReconciler creates a new Reconciler instance.
//
// Parameters:
//   - state: Shared state manager
//   - history: History manager for tracking removed forwards
//   - logger: Structured logger for operation logging
//
// Example usage:
//
//	reconciler := NewReconciler(state, history, logger)
//	toAdd, toRemove := reconciler.Diff()
func NewReconciler(state *state.State, history *state.History, logger *slog.Logger) *Reconciler {
	return &Reconciler{
		state:   state,
		history: history,
		logger:  logger,
	}
}

// Diff compares desired and actual state to determine what actions are needed.
//
// It implements container-scoped batching: all ports for a container are
// processed together.
//
// "Last Event Wins" Semantics:
// When a port conflict occurs (port claimed by different container), this
// reconciler implements "last event wins" conflict resolution:
//  1. If port P is currently forwarded for container A
//  2. But container B now wants port P
//  3. The reconciler will:
//     a) First remove the forward for container A (oldest)
//     b) Then add the forward for container B (newest)
//
// This ensures that the most recent desired state takes precedence, allowing
// containers to "steal" ports from each other if needed during rapid churn.
// The state tracks which container owns which port via the actualMap.
//
// Returns:
//   - toAdd: Actions to add port forwards
//   - toRemove: Actions to remove port forwards
//
// Example usage:
//
//	toAdd, toRemove := reconciler.Diff()
//	for _, action := range toAdd {
//	    fmt.Printf("Need to add: %s port %d\n", action.ContainerID, action.Port)
//	}
func (r *Reconciler) Diff() (toAdd, toRemove []Action) {
	desired := r.state.GetDesired()
	actual := r.state.GetActual()

	// Build maps for easier lookup
	desiredMap := make(map[string]map[int]bool) // containerID -> port -> exists
	for _, cp := range desired {
		if desiredMap[cp.ContainerID] == nil {
			desiredMap[cp.ContainerID] = make(map[int]bool)
		}
		for _, port := range cp.Ports {
			desiredMap[cp.ContainerID][port] = true
		}
	}

	actualMap := make(map[string]map[int]bool) // containerID -> port -> exists
	portOwner := make(map[int]string)          // port -> containerID (tracks ownership for conflict detection)
	for _, fs := range actual {
		// Only count "active" forwards in actual state
		// "pending" and "conflict" states don't count as ownership
		if fs.Status == "active" {
			if actualMap[fs.ContainerID] == nil {
				actualMap[fs.ContainerID] = make(map[int]bool)
			}
			actualMap[fs.ContainerID][fs.Port] = true
			portOwner[fs.Port] = fs.ContainerID // Track which container owns this port
		}
	}

	// Compute actions
	toAdd = make([]Action, 0)
	toRemove = make([]Action, 0)

	// Find ports to add (in desired but not in actual, or owned by different container)
	for containerID, ports := range desiredMap {
		for port := range ports {
			currentOwner, exists := portOwner[port]

			if !exists {
				// Port not currently forwarded by any container, add it
				toAdd = append(toAdd, Action{
					Type:        "add",
					ContainerID: containerID,
					Port:        port,
					RemotePort:  port, // For now, local and remote ports are the same
				})
			} else if currentOwner != containerID {
				// "Last event wins" conflict resolution:
				// Port is owned by a different container, so we transfer ownership
				// Remove from old owner (oldest)
				toRemove = append(toRemove, Action{
					Type:        "remove",
					ContainerID: currentOwner,
					Port:        port,
					RemotePort:  port,
				})
				// Add for new owner (newest wins)
				toAdd = append(toAdd, Action{
					Type:        "add",
					ContainerID: containerID,
					Port:        port,
					RemotePort:  port,
				})
			}
			// else: port is already active for this container, no action needed (idempotent)
		}
	}

	// Find ports to remove (in actual but not in desired)
	for containerID, ports := range actualMap {
		desiredPorts := desiredMap[containerID]
		for port := range ports {
			if !desiredPorts[port] {
				// Port is active but not desired anymore, remove it
				toRemove = append(toRemove, Action{
					Type:        "remove",
					ContainerID: containerID,
					Port:        port,
					RemotePort:  port,
				})
			}
		}
	}

	r.logger.Debug("reconciliation diff computed",
		"toAdd", len(toAdd),
		"toRemove", len(toRemove))

	return toAdd, toRemove
}

// Apply executes the provided actions using SSH forward operations.
//
// This method is designed to be idempotent:
//   - Calling it multiple times with the same actions has the same effect as once
//   - State is checked before each operation to avoid duplicate work
//   - State is updated after each operation so subsequent calls see current state
//   - Failed operations are marked in state to prevent infinite retries
//
// It processes actions in order:
//  1. First removes all forwards that need to be removed
//  2. Then adds all forwards that need to be added
//  3. For each add, validates with ProbePort after creating the forward
//
// Updates state for each operation (success or failure).
// Returns first error encountered but attempts all actions.
//
// Parameters:
//   - ctx: Context for cancellation
//   - sshMaster: SSH master connection to use
//   - host: SSH host string (ssh://user@host)
//   - actions: Combined list of add and remove actions
//
// Example usage:
//
//	actions := append(toRemove, toAdd...)
//	err := reconciler.Apply(ctx, sshMaster, "ssh://user@host", actions)
func (r *Reconciler) Apply(ctx context.Context, sshMaster *ssh.Master, host string, actions []Action) error {
	if len(actions) == 0 {
		r.logger.Debug("no actions to apply")
		return nil
	}

	// Get control path from master
	controlPath, err := ssh.DeriveControlPath(host)
	if err != nil {
		return fmt.Errorf("failed to derive control path: %w", err)
	}

	var firstError error

	// Separate actions by type for ordered processing
	var removeActions, addActions []Action
	for _, action := range actions {
		switch action.Type {
		case "remove":
			removeActions = append(removeActions, action)
		case "add":
			addActions = append(addActions, action)
		}
	}

	r.logger.Info("applying reconciliation actions",
		"removes", len(removeActions),
		"adds", len(addActions))

	// Process removals first
	for _, action := range removeActions {
		// Defensive check: skip if already removed
		// This makes Apply() idempotent even if called multiple times
		actualState := r.state.GetByContainer(action.ContainerID)
		alreadyRemoved := true
		var forwardToRemove *state.ForwardState
		for _, fs := range actualState {
			if fs.Port == action.Port && fs.Status == "active" {
				alreadyRemoved = false
				fsCopy := fs // Make a copy for history
				forwardToRemove = &fsCopy
				break
			}
		}

		if alreadyRemoved {
			r.logger.Debug("port forward already removed, skipping",
				"container", safeLogID(action.ContainerID),
				"port", action.Port)
			continue
		}

		r.logger.Debug("removing port forward",
			"container", safeLogID(action.ContainerID),
			"port", action.Port)

		err := ssh.CancelForward(ctx, controlPath, host, action.Port, action.RemotePort, r.logger)
		if err != nil {
			r.logger.Warn("failed to remove port forward",
				"container", safeLogID(action.ContainerID),
				"port", action.Port,
				"error", err.Error())
			if firstError == nil {
				firstError = err
			}
		}

		// Add to history before removing from state
		if forwardToRemove != nil {
			// Determine end reason based on context
			endReason := "container stopped"
			// Check if this is a port transfer (another container wants this port)
			for _, addAction := range addActions {
				if addAction.Port == action.Port && addAction.ContainerID != action.ContainerID {
					endReason = fmt.Sprintf("port claimed by %s", safeLogID(addAction.ContainerID))
					break
				}
			}

			r.history.Add(state.HistoryEntry{
				ContainerID: forwardToRemove.ContainerID,
				Port:        forwardToRemove.Port,
				StartedAt:   forwardToRemove.CreatedAt,
				EndedAt:     time.Now(),
				EndReason:   endReason,
				FinalStatus: forwardToRemove.Status,
			})
		}

		// Remove only this specific port from state (not all container ports)
		r.state.ClearPort(action.ContainerID, action.Port)
	}

	// Process additions with tracking for summary
	// T051: Ensure other ports continue working despite conflicts
	addedCount := 0
	conflictCount := 0
	pendingCount := 0

	for _, action := range addActions {
		// Defensive check: skip if already active
		// This makes Apply() idempotent even if called multiple times
		actualState := r.state.GetByContainer(action.ContainerID)
		alreadyActive := false
		for _, fs := range actualState {
			if fs.Port == action.Port && fs.Status == "active" {
				alreadyActive = true
				break
			}
		}

		if alreadyActive {
			r.logger.Debug("port forward already active, skipping",
				"container", safeLogID(action.ContainerID),
				"port", action.Port)
			addedCount++ // Count as added since it's active
			continue
		}

		r.logger.Debug("adding port forward",
			"container", safeLogID(action.ContainerID),
			"port", action.Port)

		// T050: Use retry logic with exponential backoff
		err := ssh.AddForwardWithRetry(ctx, controlPath, host, action.Port, action.RemotePort, r.logger)
		if err != nil {
			// T048/T049: Check if this is a port conflict
			var portErr *ssh.PortConflictError
			if errors.As(err, &portErr) {
				// Port conflict detected
				conflictCount++
				r.logger.Warn("port conflict - continuing with other ports",
					"container", safeLogID(action.ContainerID),
					"port", action.Port,
					"error", err.Error())
				r.state.MarkConflict(action.ContainerID, action.Port,
					fmt.Sprintf("port already in use after %d retry attempts", 5))
			} else {
				// Other error
				r.logger.Warn("failed to add port forward",
					"container", safeLogID(action.ContainerID),
					"port", action.Port,
					"error", err.Error())
				r.state.MarkConflict(action.ContainerID, action.Port, err.Error())
				conflictCount++
			}
			if firstError == nil {
				firstError = err
			}
			// T051: Continue processing other ports despite this failure
			continue
		}

		// Validate the forward is actually active
		if err := util.ProbePort(ctx, action.Port); err != nil {
			r.logger.Warn("port forward created but not responding",
				"container", safeLogID(action.ContainerID),
				"port", action.Port,
				"error", err.Error())
			r.state.MarkPending(action.ContainerID, action.Port, "port not responding")
			pendingCount++
			if firstError == nil {
				firstError = fmt.Errorf("port %d not responding after forward: %w", action.Port, err)
			}
			// T051: Continue processing other ports
			continue
		}

		// Success! Update state so subsequent calls see this forward as active
		r.state.MarkActive(action.ContainerID, action.Port)
		addedCount++
		r.logger.Info("port forward established",
			"container", safeLogID(action.ContainerID),
			"port", action.Port)
	}

	// T051: Provide summary of operations
	r.logger.Info("reconciliation completed",
		"removed", len(removeActions),
		"added", addedCount,
		"conflicts", conflictCount,
		"pending", pendingCount)

	if firstError != nil {
		return fmt.Errorf("reconciliation completed with errors: %w", firstError)
	}

	return nil
}
