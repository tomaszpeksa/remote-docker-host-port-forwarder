package reconcile

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"github.com/tomaszpeksa/remote-docker-host-port-forwarder/internal/ssh"
	"github.com/tomaszpeksa/remote-docker-host-port-forwarder/internal/state"
	"github.com/tomaszpeksa/remote-docker-host-port-forwarder/internal/util"
)

type Action struct {
	Type        string
	ContainerID string
	Port        int
	RemotePort  int
	EndReason   string
}

type Reconciler struct {
	state         *state.State
	history       *state.History
	logger        *slog.Logger
	now           func() time.Time
	probe         func(context.Context, int) error
	cancelForward func(context.Context, *ssh.Forwarding, int, int, *slog.Logger) (ssh.ForwardEffect, error)
}

func safeLogID(id string) string {
	if len(id) >= 12 {
		return id[:12]
	}
	return id
}
func NewReconciler(st *state.State, history *state.History, logger *slog.Logger) *Reconciler {
	return &Reconciler{state: st, history: history, logger: logger, now: time.Now, probe: util.ProbePort, cancelForward: ssh.CancelForward}
}
func desiredPorts(desired []state.ContainerPorts) map[string]map[int]bool {
	result := make(map[string]map[int]bool)
	for _, owner := range desired {
		result[owner.ContainerID] = make(map[int]bool)
		for _, port := range owner.Ports {
			result[owner.ContainerID][port] = true
		}
	}
	return result
}

// Diff retains the existing desired-change transfer boundary. Transport ownership
// is independent of readiness: a pending/unknown listener still blocks reuse.
func (r *Reconciler) Diff() (toAdd, toRemove []Action) {
	desired := desiredPorts(r.state.GetDesired())
	actual := r.state.GetActual()
	resources := r.state.GetResources()
	owner := make(map[int]string)
	active := make(map[string]map[int]bool)
	for _, resource := range resources {
		if resource.Ownership != state.ForwardUnowned {
			owner[resource.Port] = resource.ContainerID
		}
	}
	for _, forward := range actual {
		if active[forward.ContainerID] == nil {
			active[forward.ContainerID] = make(map[int]bool)
		}
		active[forward.ContainerID][forward.Port] = forward.Status == "active"
	}
	removed := make(map[string]map[int]bool)
	remove := func(id string, port, remote int) {
		if removed[id] == nil {
			removed[id] = make(map[int]bool)
		}
		if !removed[id][port] {
			removed[id][port] = true
			toRemove = append(toRemove, Action{Type: "remove", ContainerID: id, Port: port, RemotePort: remote})
		}
	}
	for id, ports := range desired {
		for port := range ports {
			current := owner[port]
			if current != "" && current != id {
				remove(current, port, port)
			}
			resource, exists := r.state.Resource(id, port)
			if exists && resource.Ownership == state.ForwardUnknown {
				continue
			}
			if current != id || !active[id][port] || (exists && resource.Remove) {
				toAdd = append(toAdd, Action{Type: "add", ContainerID: id, Port: port, RemotePort: port})
			}
		}
	}
	for _, forward := range actual {
		if !desired[forward.ContainerID][forward.Port] {
			remove(forward.ContainerID, forward.Port, forward.Port)
		}
	}
	return toAdd, toRemove
}

// NextRetry reports only eligible kinds of resource work, not discovery requests.
func (r *Reconciler) NextRetry() (time.Time, bool) {
	var next time.Time
	for _, resource := range r.state.GetResources() {
		if resource.Ownership != state.ForwardUnknown && !resource.RetryAt.IsZero() && (next.IsZero() || resource.RetryAt.Before(next)) {
			next = resource.RetryAt
		}
	}
	return next, !next.IsZero()
}

// RetryActions never re-runs claimant arbitration or invents new desired work.
func (r *Reconciler) RetryActions() []Action {
	desired := desiredPorts(r.state.GetDesired())
	var actions []Action
	now := r.now()
	for _, resource := range r.state.GetResources() {
		if resource.Ownership == state.ForwardUnknown || resource.RetryAt.IsZero() || resource.RetryAt.After(now) {
			continue
		}
		kind := "add"
		if resource.Remove || !desired[resource.ContainerID][resource.Port] {
			kind = "remove"
		}
		actions = append(actions, Action{Type: kind, ContainerID: resource.ContainerID, Port: resource.Port, RemotePort: resource.RemotePort, EndReason: resource.EndReason})
	}
	return actions
}

// Apply runs inside Master's forwarding gate. Positive replies discharge only
// the exact current request; unknown effects await natural generation retirement.
func (r *Reconciler) Apply(ctx context.Context, connection *ssh.Forwarding, actions []Action) error {
	if connection == nil || !connection.Ready() {
		return fmt.Errorf("SSH connection unavailable")
	}
	var first error
	for _, kind := range []string{"remove", "add"} {
		for _, action := range actions {
			if action.Type != kind {
				continue
			}
			if err := ctx.Err(); err != nil {
				if first == nil {
					first = err
				}
				return first
			}
			if kind == "remove" && action.EndReason == "" {
				action.EndReason = "container stopped"
				for _, add := range actions {
					if add.Type == "add" && add.Port == action.Port && add.ContainerID != action.ContainerID {
						action.EndReason = fmt.Sprintf("port claimed by %s", safeLogID(add.ContainerID))
						break
					}
				}
			}
			if err := r.apply(ctx, connection, action); err != nil && first == nil {
				first = err
			}
		}
	}
	if first != nil {
		return fmt.Errorf("reconciliation completed with errors: %w", first)
	}
	return nil
}

func (r *Reconciler) apply(ctx context.Context, connection *ssh.Forwarding, action Action) error {
	resource, exists := r.state.Resource(action.ContainerID, action.Port)
	if !exists {
		resource = state.ForwardResource{ContainerID: action.ContainerID, Port: action.Port, RemotePort: action.RemotePort, Generation: connection.Generation()}
	}
	if resource.Generation != connection.Generation() {
		return fmt.Errorf("forward resource generation not retired")
	}
	if action.Type == "remove" {
		resource.Remove = true
		if resource.EndReason == "" {
			resource.EndReason = action.EndReason
		}
		r.state.SetResource(resource)
		if resource.Ownership == state.ForwardUnowned {
			r.finish(resource)
			return nil
		}
	} else {
		desired := desiredPorts(r.state.GetDesired())
		if !desired[action.ContainerID][action.Port] {
			return nil
		}
	}
	if resource.Ownership == state.ForwardUnknown {
		r.state.MarkPending(resource.ContainerID, resource.Port, "forward effect uncertain; awaiting SSH generation retirement")
		return fmt.Errorf("forward effect uncertain")
	}
	// New intent can request cleanup immediately, but repeated notifications
	// cannot accelerate a returned failure's existing resource retry deadline.
	if !resource.RetryAt.IsZero() && resource.RetryAt.After(r.now()) {
		return nil
	}
	if resource.Remove {
		effect, err := r.cancelForward(ctx, connection, resource.Port, resource.RemotePort, r.logger)
		if effect == ssh.ForwardAcknowledged {
			r.finish(resource)
			return err
		}
		if effect == ssh.ForwardUncertain {
			resource.Ownership = state.ForwardUnknown
		}
		return r.failed(resource, "forward cancellation failed; cleanup remains owed", err)
	}
	if resource.Ownership == state.ForwardUnowned {
		for _, other := range r.state.GetResources() {
			if other.Port == resource.Port && other.ContainerID != resource.ContainerID && other.Ownership != state.ForwardUnowned {
				return r.failed(resource, "port replacement waiting for owned cleanup", fmt.Errorf("port replacement blocked by cleanup"))
			}
		}
		effect, err := ssh.AddForwardWithRetry(ctx, connection, resource.Port, resource.RemotePort, r.logger)
		switch effect {
		case ssh.ForwardAcknowledged:
			resource.Ownership = state.ForwardOwned
			resource.RetryAt = time.Time{}
			r.state.SetResource(resource) // Must precede the readiness probe.
		case ssh.ForwardUncertain:
			resource.Ownership = state.ForwardUnknown
			return r.failed(resource, "forward creation effect uncertain", err)
		default:
			r.state.MarkConflict(resource.ContainerID, resource.Port, fmt.Sprint(err))
			resource.RetryAt = r.now().Add(time.Second)
			r.state.SetResource(resource)
			return err
		}
	}
	if err := r.probe(ctx, resource.Port); err != nil {
		return r.failed(resource, "port not responding", err)
	}
	resource.RetryAt = time.Time{}
	r.state.SetResource(resource)
	r.state.MarkActive(resource.ContainerID, resource.Port)
	return nil
}
func (r *Reconciler) failed(resource state.ForwardResource, reason string, err error) error {
	resource.RetryAt = time.Time{}
	if resource.Ownership != state.ForwardUnknown {
		resource.RetryAt = r.now().Add(time.Second)
	}
	r.state.SetResource(resource)
	r.state.MarkPending(resource.ContainerID, resource.Port, reason)
	if err == nil {
		return fmt.Errorf("%s", reason)
	}
	return err
}

// RetireBefore is called only with proven retirement from the Master gate.
func (r *Reconciler) RetireBefore(generation uint64) {
	at := r.now()
	for _, forward := range r.state.RetireBefore(generation, at) {
		r.history.Add(state.HistoryEntry{ContainerID: forward.ContainerID, Port: forward.Port, StartedAt: forward.CreatedAt, EndedAt: at, EndReason: "container stopped", FinalStatus: forward.Status})
	}
}

func (r *Reconciler) finish(resource state.ForwardResource) {
	if forward, ok := r.state.FinishResource(resource); ok {
		reason := resource.EndReason
		if reason == "" {
			reason = "container stopped"
		}
		r.history.Add(state.HistoryEntry{ContainerID: forward.ContainerID, Port: forward.Port, StartedAt: forward.CreatedAt, EndedAt: r.now(), EndReason: reason, FinalStatus: forward.Status})
	}
}
