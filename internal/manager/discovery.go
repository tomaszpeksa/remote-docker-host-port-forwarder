package manager

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/tomaszpeksa/remote-docker-host-port-forwarder/internal/docker"
	"github.com/tomaszpeksa/remote-docker-host-port-forwarder/internal/state"
)

type inventoryFence struct{ generation, revision, attempt uint64 }
type inventoryCandidate struct {
	fence          inventoryFence
	listed         []string
	observed       []state.ContainerPorts
	listedComplete bool
	cursor         string
	incomplete     bool
	problem        error
}
type eventReservation struct {
	generation uint64
	retired    bool
}
type acceptedEvent struct {
	event docker.Event
	token *eventReservation
}
type discoveryState struct {
	mu                            sync.Mutex
	generation, revision, attempt uint64
	pending                       int
	cursor                        string
	repair                        bool
	request, attempted            uint64
	wake, reconcileWake           chan struct{}
	events                        chan acceptedEvent
	list                          func(context.Context) ([]string, error)
	observe                       func(context.Context, string) ([]int, error)
	openStream                    func(context.Context, func(context.Context, docker.Event) error) <-chan docker.StreamResult
	now                           func() time.Time
	after                         func(time.Duration) <-chan time.Time
}

func (m *Manager) inventoryPass(ctx context.Context, f inventoryFence, cursor string) inventoryCandidate {
	c := inventoryCandidate{fence: f, cursor: cursor}
	listCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	listed, err := m.discovery.list(listCtx)
	cancel()
	if err != nil {
		c.problem = err
		return c
	}
	c.listed = listed
	if err := validateInventory(c); err != nil {
		c.problem = err
		c.listed = nil
		return c
	}
	c.listed = append([]string{}, listed...)
	sort.Strings(c.listed)
	c.listedComplete = true
	start := sort.SearchStrings(c.listed, cursor)
	if start < len(c.listed) && c.listed[start] == cursor {
		start++
	}
	deadline := m.discovery.now().Add(30 * time.Second)
	bytes, portsCount := 0, 0
	for _, id := range c.listed {
		bytes += len(id) + 256
	}
	for offset := 0; offset < len(c.listed); offset++ {
		if ctx.Err() != nil {
			c.problem = ctx.Err()
			c.incomplete = true
			break
		}
		if deadline.Sub(m.discovery.now()) < 7*time.Second {
			c.incomplete = true
			break
		}
		id := c.listed[(start+offset)%len(c.listed)]
		c.cursor = id
		observeCtx, finish := context.WithTimeout(ctx, 5*time.Second)
		ports, err := m.discovery.observe(observeCtx, id)
		finish()
		if err != nil {
			c.problem = err
			c.incomplete = true
			continue
		}
		if len(ports) > 100000-portsCount || len(ports) > (16<<20-bytes)/16 {
			c.problem = fmt.Errorf("inventory candidate limit exceeded")
			c.incomplete = true
			break
		}
		portsCount += len(ports)
		bytes += len(ports) * 16
		c.observed = append(c.observed, state.ContainerPorts{ContainerID: id, Ports: append([]int{}, ports...)})
	}
	return c
}

// validateInventory checks bounded data before any desired-state authority.
func validateInventory(c inventoryCandidate) error {
	if len(c.listed) > 10000 {
		return fmt.Errorf("container list limit exceeded")
	}
	present := make(map[string]bool, len(c.listed))
	bytes := 0
	for _, id := range c.listed {
		bytes += len(id) + 256
		if len(id) < 12 || len(strings.Fields(id)) != 1 || present[id] {
			return fmt.Errorf("invalid inventory ID set")
		}
		present[id] = true
	}
	if bytes > 16<<20 {
		return fmt.Errorf("inventory candidate limit exceeded")
	}
	ports := 0
	if len(c.observed) > len(c.listed) {
		return fmt.Errorf("invalid inventory observation subset")
	}
	observed := make(map[string]bool, len(c.observed))
	for _, cp := range c.observed {
		if !present[cp.ContainerID] || observed[cp.ContainerID] {
			return fmt.Errorf("invalid inventory observation subset")
		}
		observed[cp.ContainerID] = true
		if len(cp.Ports) > 100000-ports || len(cp.Ports) > (16<<20-bytes)/16 {
			return fmt.Errorf("inventory candidate limit exceeded")
		}
		ports += len(cp.Ports)
		bytes += len(cp.Ports) * 16
	}
	return nil
}

func (m *Manager) commitInventory(c inventoryCandidate) (bool, bool, error) {
	if !c.listedComplete {
		return false, false, c.problem
	}
	if err := validateInventory(c); err != nil {
		return false, false, err
	}
	d := &m.discovery
	d.mu.Lock()
	defer d.mu.Unlock()
	if m.stopping.Load() || d.pending != 0 || c.fence != (inventoryFence{d.generation, d.revision, d.attempt}) {
		return false, false, nil
	}
	changed := m.state.MergeInventory(c.listed, c.observed)
	if changed {
		m.requestReconcile()
	}
	return true, changed, nil
}

func (m *Manager) observeContainer(ctx context.Context, id string) ([]int, error) {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	infrastructure, err := m.hasTestInfrastructureLabel(ctx, m.sshMaster.ControlPath(), id)
	if err != nil {
		return nil, err
	}
	if infrastructure {
		return []int{}, nil
	}
	return docker.InspectPorts(ctx, m.cfg.Host, m.sshMaster.ControlPath(), id)
}

func (m *Manager) runDiscovery(ctx context.Context) {
	d := &m.discovery
	auditAt := d.now()
	var retryAt time.Time
	backoff := time.Second
	for ctx.Err() == nil {
		now := d.now()
		d.mu.Lock()
		due := d.repair || !now.Before(auditAt)
		eligible := retryAt.IsZero() || !now.Before(retryAt)
		pending := d.pending
		if due && eligible && pending == 0 {
			d.attempt++
			fence := inventoryFence{d.generation, d.revision, d.attempt}
			cursor := d.cursor
			d.mu.Unlock()
			candidate := m.inventoryPass(ctx, fence, cursor)
			if ctx.Err() != nil {
				return
			}
			committed, changed, err := m.commitInventory(candidate)
			if err != nil || candidate.problem != nil {
				m.logger.Warn("inventory observation incomplete", "commit_error", err, "observation_error", candidate.problem)
			}
			d.mu.Lock()
			d.cursor = candidate.cursor
			d.repair = !committed || candidate.incomplete || candidate.fence != (inventoryFence{d.generation, d.revision, d.attempt})
			if d.repair {
				retryAt = d.now().Add(backoff)
				backoff = min(backoff*2, 30*time.Second)
			} else {
				retryAt = time.Time{}
				backoff = time.Second
				auditAt = d.now().Add(10 * time.Second)
			}
			d.mu.Unlock()
			if changed {
				m.wakeReconciliation()
			}
			continue
		}
		target := auditAt
		if due {
			target = retryAt
		}
		d.mu.Unlock()
		var tick <-chan time.Time
		if pending == 0 {
			tick = d.after(max(target.Sub(now), time.Duration(0)))
		}
		select {
		case <-ctx.Done():
			return
		case <-d.wake:
		case <-tick:
		}
	}
}
func (m *Manager) markRepair() {
	m.discovery.mu.Lock()
	m.discovery.repair = true
	m.discovery.mu.Unlock()
	select {
	case m.discovery.wake <- struct{}{}:
	default:
	}
}
func (m *Manager) deliverEvent(ctx context.Context, generation uint64, event docker.Event) error {
	d := &m.discovery
	d.mu.Lock()
	if generation != d.generation {
		d.mu.Unlock()
		return fmt.Errorf("retired event generation")
	}
	token := &eventReservation{generation: generation}
	d.revision++
	d.pending++
	d.mu.Unlock()
	select {
	case d.events <- acceptedEvent{event: event, token: token}:
		return nil
	case <-ctx.Done():
		m.retireEvent(token)
		return ctx.Err()
	}
}
func (m *Manager) retireEvent(token *eventReservation) {
	if token == nil {
		return
	}
	d := &m.discovery
	d.mu.Lock()
	if !token.retired {
		token.retired = true
		if token.generation == d.generation {
			d.pending--
		}
	}
	d.mu.Unlock()
	select {
	case d.wake <- struct{}{}:
	default:
	}
}

func (m *Manager) initDiscovery() {
	d := &m.discovery
	d.wake = make(chan struct{}, 1)
	d.reconcileWake = make(chan struct{}, 1)
	d.events = make(chan acceptedEvent, 10)
	d.now = time.Now
	d.after = time.After
	d.list = m.listRunning
	d.observe = m.observeContainer
	if m.eventReader != nil {
		d.openStream = m.eventReader.Stream
	}
}

func (m *Manager) finishStream(generation uint64) error {
	d := &m.discovery
	d.mu.Lock()
	if d.pending != 0 || generation != d.generation {
		d.mu.Unlock()
		return fmt.Errorf("stream ended with unretired event reservations")
	}
	d.generation++
	d.repair = true
	d.mu.Unlock()
	select {
	case d.wake <- struct{}{}:
	default:
	}
	return nil
}

// Inspection is performed by the handler before entering this short section.
func (m *Manager) setEventDesired(ctx context.Context, generation uint64, id string, ports []int) error {
	d := &m.discovery
	d.mu.Lock()
	defer d.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return err
	}
	if generation != d.generation {
		return fmt.Errorf("retired event generation")
	}
	m.state.SetDesired(id, ports)
	m.requestReconcile()
	return nil
}

// requestReconcile records desired mutation while discovery.mu is held. Waking
// is separate so event batching can coalesce work without owning its lifetime.
func (m *Manager) requestReconcile() { m.discovery.request++ }

func (m *Manager) hasUnapplied() bool {
	m.discovery.mu.Lock()
	defer m.discovery.mu.Unlock()
	return m.discovery.request > m.discovery.attempted
}

func (m *Manager) wakeReconciliation() {
	select {
	case m.discovery.reconcileWake <- struct{}{}:
	default:
	}
}

func (m *Manager) runReconciliation(ctx context.Context) {
	var retry <-chan time.Time
	for {
		select {
		case <-ctx.Done():
			return
		case <-m.sshMaster.Changes(): // Proven retirement is independent of desired changes.
		case <-m.discovery.reconcileWake:
			if !m.hasUnapplied() {
				continue
			}
		case <-retry:
			retry = nil
			if !m.hasUnapplied() {
				continue
			}
		}
		if err := m.triggerReconcile(ctx); err != nil && ctx.Err() == nil {
			m.logger.Warn("reconciliation unavailable or failed", "error", err)
		}
		if m.hasUnapplied() {
			retry = m.discovery.after(time.Second)
		} else {
			retry = nil
		}
	}
}
