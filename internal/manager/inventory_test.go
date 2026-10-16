package manager

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"github.com/tomaszpeksa/remote-docker-host-port-forwarder/internal/state"
)

var discoveryA = strings.Repeat("a", 64)
var discoveryB = strings.Repeat("b", 64)
var discoveryC = strings.Repeat("c", 64)

func inventoryManager(t *testing.T) *Manager {
	m, _, _ := recoveryManager(t)
	m.discovery.now = time.Now
	m.discovery.after = time.After
	m.discovery.wake = make(chan struct{}, 1)
	m.discovery.reconcileWake = make(chan struct{}, 1)
	return m
}

func TestManagerDiscoveryInventory(t *testing.T) {
	t.Run("PartialABC", func(t *testing.T) {
		m := inventoryManager(t)
		m.state.SetDesired(discoveryA, []int{1001})
		m.state.SetDesired(discoveryB, []int{2001})
		m.discovery.list = func(context.Context) ([]string, error) { return []string{discoveryB, discoveryC}, nil }
		fail := true
		m.discovery.observe = func(ctx context.Context, id string) ([]int, error) {
			if id == discoveryB && fail {
				return nil, errors.New("B unknown")
			}
			return []int{3001}, nil
		}
		c := m.inventoryPass(context.Background(), inventoryFence{}, "")
		// Change current B after observation, without copying it into the candidate.
		m.state.SetDesired(discoveryB, []int{2002})
		committed, changed, err := m.commitInventory(c)
		require.NoError(t, err)
		require.True(t, committed, "PARTIAL_AUTHORITY")
		require.True(t, changed)
		require.ElementsMatch(t, []state.ContainerPorts{{ContainerID: discoveryA, Ports: []int{}}, {ContainerID: discoveryB, Ports: []int{2002}}, {ContainerID: discoveryC, Ports: []int{3001}}}, m.state.GetDesired(), "PARTIAL_PRESERVE")
		require.True(t, c.incomplete)
		fail = false
		c = m.inventoryPass(context.Background(), inventoryFence{}, c.cursor)
		committed, changed, err = m.commitInventory(c)
		require.NoError(t, err)
		require.True(t, committed)
		require.True(t, changed)
		require.Contains(t, m.state.GetDesired(), state.ContainerPorts{ContainerID: discoveryB, Ports: []int{3001}})
	})
	t.Run("FailedList", func(t *testing.T) {
		m := inventoryManager(t)
		m.state.SetDesired(discoveryA, []int{1001})
		m.discovery.list = func(context.Context) ([]string, error) { return nil, errors.New("list failed") }
		c := m.inventoryPass(context.Background(), inventoryFence{}, "")
		require.Error(t, c.problem)
		committed, _, _ := m.commitInventory(c)
		require.False(t, committed)
		require.Contains(t, m.state.GetDesired(), state.ContainerPorts{ContainerID: discoveryA, Ports: []int{1001}})
	})
	t.Run("FenceBeforeAbsence", func(t *testing.T) {
		m := inventoryManager(t)
		m.state.SetDesired(discoveryA, []int{1001})
		m.discovery.list = func(context.Context) ([]string, error) { m.discovery.revision++; return nil, nil }
		c := m.inventoryPass(context.Background(), inventoryFence{}, "")
		committed, _, _ := m.commitInventory(c)
		require.False(t, committed, "STALE_ABSENCE_FENCE")
		require.Contains(t, m.state.GetDesired(), state.ContainerPorts{ContainerID: discoveryA, Ports: []int{1001}})
	})
	t.Run("FairBudget", func(t *testing.T) {
		m := inventoryManager(t)
		now := time.Unix(0, 0)
		m.discovery.now = func() time.Time { return now }
		ids := []string{discoveryA, discoveryB, discoveryC, strings.Repeat("d", 64), strings.Repeat("e", 64), strings.Repeat("f", 64)}
		m.discovery.list = func(context.Context) ([]string, error) { return ids, nil }
		counts := map[string]int{}
		m.discovery.observe = func(ctx context.Context, id string) ([]int, error) {
			counts[id]++
			now = now.Add(5 * time.Second)
			if id == discoveryB {
				return nil, errors.New("persistent B")
			}
			return []int{1001}, nil
		}
		c := m.inventoryPass(context.Background(), inventoryFence{}, "")
		require.True(t, c.incomplete, "BUDGET_PARTIAL_PROGRESS")
		committed, _, err := m.commitInventory(c)
		require.NoError(t, err)
		require.True(t, committed)
		m.inventoryPass(context.Background(), inventoryFence{}, c.cursor)
		for _, id := range ids {
			require.Positive(t, counts[id], "FAIR_ROTATION: %s", id)
		}
		require.Contains(t, m.state.GetDesired(), state.ContainerPorts{ContainerID: discoveryC, Ports: []int{1001}})
	})
	t.Run("UnknownStartup", func(t *testing.T) {
		m := inventoryManager(t)
		m.discovery.list = func(context.Context) ([]string, error) { return []string{discoveryB}, nil }
		m.discovery.observe = func(context.Context, string) ([]int, error) { return nil, errors.New("unknown") }
		c := m.inventoryPass(context.Background(), inventoryFence{}, "")
		committed, changed, err := m.commitInventory(c)
		require.NoError(t, err)
		require.True(t, committed)
		require.False(t, changed)
		require.Empty(t, m.state.GetDesired())
	})
	t.Run("InvalidSubset", func(t *testing.T) {
		m := inventoryManager(t)
		_, _, err := m.commitInventory(inventoryCandidate{listedComplete: true, listed: []string{discoveryA}, observed: []state.ContainerPorts{{ContainerID: discoveryB, Ports: []int{1001}}}})
		require.Error(t, err, "INVALID_SUBSET_MUST_NOT_COMMIT")
		require.Empty(t, m.state.GetDesired())
	})
}

func TestManagerDiscoveryKnownEmptyStartup(t *testing.T) {
	m := inventoryManager(t)
	m.discovery.list = func(context.Context) ([]string, error) { return []string{discoveryA}, nil }
	m.discovery.observe = func(context.Context, string) ([]int, error) { return []int{}, nil }
	candidate := m.inventoryPass(context.Background(), inventoryFence{}, "")
	committed, changed, err := m.commitInventory(candidate)
	require.NoError(t, err)
	require.True(t, committed)
	require.False(t, changed, "PORTLESS_INVENTORY_MUST_NOT_INVENT_OWNER")
	require.Empty(t, m.state.GetDesired())
	require.Zero(t, m.discovery.request)
}

func TestManagerDiscoveryCandidateLimits(t *testing.T) {
	tooMany := make([]string, 10001)
	for i := range tooMany {
		tooMany[i] = discoveryA
	}
	cases := []struct {
		name      string
		candidate inventoryCandidate
		message   string
	}{
		{"IDs", inventoryCandidate{listedComplete: true, listed: tooMany}, "container list limit"},
		{"ports", inventoryCandidate{listedComplete: true, listed: []string{discoveryA}, observed: []state.ContainerPorts{{ContainerID: discoveryA, Ports: make([]int, 100001)}}}, "inventory candidate limit"},
		{"bytes", inventoryCandidate{listedComplete: true, listed: []string{strings.Repeat("a", 16<<20)}}, "inventory candidate limit"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			m := inventoryManager(t)
			committed, changed, err := m.commitInventory(tc.candidate)
			require.ErrorContains(t, err, tc.message, "CANDIDATE_CAPACITY_REJECTED_BEFORE_MERGE")
			require.False(t, committed)
			require.False(t, changed)
			require.Empty(t, m.state.GetDesired())
		})
	}
}

func TestManagerDiscoveryCandidateCapacityProgress(t *testing.T) {
	m := inventoryManager(t)
	ids := []string{}
	for _, digit := range []string{"1", "2", "3", "4", "5", "6", "7"} {
		ids = append(ids, strings.Repeat(digit, 64))
	}
	ports := make([]int, 20000)
	for i := range ports {
		ports[i] = i + 1
	}
	m.state.SetDesired(ids[5], []int{2002})
	m.discovery.list = func(context.Context) ([]string, error) { return ids, nil }
	m.discovery.observe = func(context.Context, string) ([]int, error) { return ports, nil }
	cursor := ""
	for pass := 0; pass < 3; pass++ {
		c := m.inventoryPass(context.Background(), inventoryFence{}, cursor)
		require.True(t, c.incomplete, "CAPACITY_MUST_STOP_ENRICHMENT")
		require.Len(t, c.observed, 5, "PASS_PORT_CAP")
		require.ErrorContains(t, c.problem, "inventory candidate limit")
		committed, _, err := m.commitInventory(c)
		require.NoError(t, err)
		require.True(t, committed)
		if pass == 0 {
			require.Contains(t, m.state.GetDesired(), state.ContainerPorts{ContainerID: ids[5], Ports: []int{2002}}, "CAPACITY_PRESERVES_UNKNOWN")
		}
		cursor = c.cursor
	}
	require.Len(t, m.state.GetDesired(), 7, "capacity-limited passes must rotate, not starve later owners")
	for _, cp := range m.state.GetDesired() {
		require.Len(t, cp.Ports, 20000)
	}
	require.Empty(t, m.state.GetActual(), "bounded candidates never invent actual ownership")
}

func TestManagerDiscoveryCandidateFenceDimensions(t *testing.T) {
	for _, dimension := range []string{"generation", "attempt", "pending"} {
		t.Run(dimension, func(t *testing.T) {
			m := inventoryManager(t)
			m.state.SetDesired(discoveryA, []int{1001})
			m.discovery.list = func(context.Context) ([]string, error) {
				switch dimension {
				case "generation":
					m.discovery.generation++
				case "attempt":
					m.discovery.attempt++
				case "pending":
					m.discovery.pending++
				}
				return nil, nil
			}
			c := m.inventoryPass(context.Background(), inventoryFence{}, "")
			committed, changed, err := m.commitInventory(c)
			require.NoError(t, err)
			require.False(t, committed, "STALE_FENCE_DIMENSION")
			require.False(t, changed)
			require.Contains(t, m.state.GetDesired(), state.ContainerPorts{ContainerID: discoveryA, Ports: []int{1001}})
		})
	}
}
