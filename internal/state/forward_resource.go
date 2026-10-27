package state

import "time"

// ForwardOwnership is a transport fact, not a public readiness status.
type ForwardOwnership uint8

const (
	ForwardUnowned ForwardOwnership = iota
	ForwardOwned
	ForwardUnknown
)

// ForwardResource describes the single outstanding operation/obligation for an
// exact owner and tuple. Unknown work is never replayed before proven retirement.
// The current supported tuple uses local 127.0.0.1 and remote localhost only.
type ForwardResource struct {
	ContainerID      string
	Port, RemotePort int
	Generation       uint64
	Ownership        ForwardOwnership
	Remove           bool
	RetryAt          time.Time
	EndReason        string // Original terminal-history reason, not superseding control intent.
	PreventRenewal   bool   // Sticky non-stop cleanup intent for this obligation's lifetime.
}

// GetResources returns copied transport facts, not a readiness snapshot.
func (s *State) GetResources() []ForwardResource {
	s.mu.RLock()
	defer s.mu.RUnlock()
	var result []ForwardResource
	for _, ports := range s.resources {
		for _, resource := range ports {
			result = append(result, resource)
		}
	}
	return result
}

// Resource returns a copy of the owner's resource under the State read lock.
func (s *State) Resource(owner string, port int) (ForwardResource, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	resource, ok := s.resources[owner][port]
	return resource, ok
}

// SetResource stores transport facts under the State lock. The caller must also
// serialize lifecycle decisions with the Master gate and retain cleanup intent.
func (s *State) SetResource(resource ForwardResource) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.resources[resource.ContainerID] == nil {
		s.resources[resource.ContainerID] = make(map[int]ForwardResource)
	}
	s.resources[resource.ContainerID][resource.Port] = resource
}

// RetireBefore consumes only resource facts belonging to proven older
// generations. The caller must hold the associated Master's lifecycle gate.
func (s *State) RetireBefore(generation uint64, at time.Time) []ForwardState {
	s.mu.Lock()
	defer s.mu.Unlock()
	var retired []ForwardState
	for id, ports := range s.resources {
		for port, resource := range ports {
			if resource.Generation >= generation {
				continue
			}
			delete(ports, port)
			desired := false
			for _, p := range s.desired[id] {
				desired = desired || p == port
			}
			forward, exists := s.actual[id][port]
			if !exists {
				continue
			}
			if !desired {
				retired = append(retired, forward)
				delete(s.actual[id], port)
			} else if forward.Status == "active" {
				forward.Status = "pending"
				forward.Reason = "SSH connection lost"
				forward.UpdatedAt = at
				s.actual[id][port] = forward
			}
		}
		if len(ports) == 0 {
			delete(s.resources, id)
		}
		if len(s.actual[id]) == 0 {
			delete(s.actual, id)
		}
	}
	return retired
}

// FinishResource consumes the matching terminal transition once. Its caller
// must supply an acknowledged CLOSE, known-unowned stop, or retirement proof.
func (s *State) FinishResource(resource ForwardResource) (ForwardState, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	current, ok := s.resources[resource.ContainerID][resource.Port]
	if !ok || current != resource {
		return ForwardState{}, false
	}
	delete(s.resources[resource.ContainerID], resource.Port)
	if len(s.resources[resource.ContainerID]) == 0 {
		delete(s.resources, resource.ContainerID)
	}
	forward, ok := s.actual[resource.ContainerID][resource.Port]
	delete(s.actual[resource.ContainerID], resource.Port)
	if len(s.actual[resource.ContainerID]) == 0 {
		delete(s.actual, resource.ContainerID)
	}
	return forward, ok
}
