package state

import (
	"sync"
)

// ContainerPorts represents the desired port forwards for a container
type ContainerPorts struct {
	ContainerID string
	Ports       []int
}

// ForwardState represents the current state of a port forward
type ForwardState struct {
	ContainerID string
	Port        int
	Status      string // "active", "conflict", "pending"
	Reason      string // explanation for conflict/pending status
}

// State manages the desired and actual state of port forwards
// It is thread-safe for concurrent access
type State struct {
	mu sync.RWMutex

	// desired maps containerID to its desired ports
	desired map[string][]int

	// actual maps containerID -> port -> ForwardState
	actual map[string]map[int]ForwardState
}

// NewState creates a new State instance with initialized maps.
//
// Example usage:
//
//	state := NewState()
//	state.SetDesired("container123", []int{8080, 9090})
func NewState() *State {
	return &State{
		desired: make(map[string][]int),
		actual:  make(map[string]map[int]ForwardState),
	}
}

// SetDesired sets the desired ports for a container.
// This represents what ports should be forwarded based on Docker state.
//
// Example usage:
//
//	state.SetDesired("container123", []int{8080, 9090})
func (s *State) SetDesired(containerID string, ports []int) {
	s.mu.Lock()
	defer s.mu.Unlock()

	// Store a copy to avoid external mutation
	portsCopy := make([]int, len(ports))
	copy(portsCopy, ports)
	s.desired[containerID] = portsCopy
}

// GetDesired returns the desired port forwards for all containers.
//
// Example usage:
//
//	desired := state.GetDesired()
//	for _, cp := range desired {
//	    fmt.Printf("Container %s wants ports %v\n", cp.ContainerID, cp.Ports)
//	}
func (s *State) GetDesired() []ContainerPorts {
	s.mu.RLock()
	defer s.mu.RUnlock()

	result := make([]ContainerPorts, 0, len(s.desired))
	for containerID, ports := range s.desired {
		// Return a copy to avoid external mutation
		portsCopy := make([]int, len(ports))
		copy(portsCopy, ports)
		result = append(result, ContainerPorts{
			ContainerID: containerID,
			Ports:       portsCopy,
		})
	}
	return result
}

// SetActual sets the actual state for a specific port forward.
//
// Example usage:
//
//	state.SetActual("container123", 8080, "active", "")
//	state.SetActual("container456", 9090, "conflict", "port already in use")
func (s *State) SetActual(containerID string, port int, status string, reason string) {
	s.mu.Lock()
	defer s.mu.Unlock()

	// Initialize container map if needed
	if s.actual[containerID] == nil {
		s.actual[containerID] = make(map[int]ForwardState)
	}

	s.actual[containerID][port] = ForwardState{
		ContainerID: containerID,
		Port:        port,
		Status:      status,
		Reason:      reason,
	}
}

// GetActual returns all actual port forward states.
//
// Example usage:
//
//	actual := state.GetActual()
//	for _, fs := range actual {
//	    fmt.Printf("%s:%d - %s\n", fs.ContainerID, fs.Port, fs.Status)
//	}
func (s *State) GetActual() []ForwardState {
	s.mu.RLock()
	defer s.mu.RUnlock()

	result := make([]ForwardState, 0)
	for _, portMap := range s.actual {
		for _, fs := range portMap {
			result = append(result, fs)
		}
	}
	return result
}

// MarkActive marks a port forward as active (successfully established).
//
// Example usage:
//
//	state.MarkActive("container123", 8080)
func (s *State) MarkActive(containerID string, port int) {
	s.SetActual(containerID, port, "active", "")
}

// MarkConflict marks a port forward as conflicted with a reason.
//
// Example usage:
//
//	state.MarkConflict("container123", 8080, "port already in use by container456")
func (s *State) MarkConflict(containerID string, port int, reason string) {
	s.SetActual(containerID, port, "conflict", reason)
}

// MarkPending marks a port forward as pending with a reason.
//
// Example usage:
//
//	state.MarkPending("container123", 8080, "waiting for SSH connection")
func (s *State) MarkPending(containerID string, port int, reason string) {
	s.SetActual(containerID, port, "pending", reason)
}

// Clear removes all port forwards for a container from both desired and actual state.
//
// Example usage:
//
//	state.Clear("container123") // removes all forwards for container123
func (s *State) Clear(containerID string) {
	s.mu.Lock()
	defer s.mu.Unlock()

	delete(s.desired, containerID)
	delete(s.actual, containerID)
}

// ClearPort removes a specific port forward from a container's actual state.
// This is used when removing individual forwards while keeping other ports active.
//
// Example usage:
//
//	state.ClearPort("container123", 8080) // removes only port 8080
func (s *State) ClearPort(containerID string, port int) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if portMap, exists := s.actual[containerID]; exists {
		delete(portMap, port)
		// If no ports remain, remove the container entry entirely
		if len(portMap) == 0 {
			delete(s.actual, containerID)
		}
	}
}

// GetByContainer returns all actual port forward states for a specific container.
//
// Example usage:
//
//	forwards := state.GetByContainer("container123")
//	for _, fs := range forwards {
//	    fmt.Printf("Port %d: %s\n", fs.Port, fs.Status)
//	}
func (s *State) GetByContainer(containerID string) []ForwardState {
	s.mu.RLock()
	defer s.mu.RUnlock()

	portMap, exists := s.actual[containerID]
	if !exists {
		return []ForwardState{}
	}

	result := make([]ForwardState, 0, len(portMap))
	for _, fs := range portMap {
		result = append(result, fs)
	}
	return result
}

// GetAllContainers returns all container IDs that have desired or actual state.
// Used for cleanup operations to ensure all forwards are removed.
//
// Example usage:
//
//	containers := state.GetAllContainers()
//	for _, containerID := range containers {
//	    state.SetDesired(containerID, []int{}) // clear all
//	}
func (s *State) GetAllContainers() []string {
	s.mu.RLock()
	defer s.mu.RUnlock()

	// Use a map to deduplicate container IDs
	containers := make(map[string]bool)

	// Add containers from desired state
	for containerID := range s.desired {
		containers[containerID] = true
	}

	// Add containers from actual state
	for containerID := range s.actual {
		containers[containerID] = true
	}

	// Convert to slice
	result := make([]string, 0, len(containers))
	for containerID := range containers {
		result = append(result, containerID)
	}

	return result
}
