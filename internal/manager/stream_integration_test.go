//go:build rdhpf_integration

package manager

import (
	"context"
	"fmt"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"github.com/tomaszpeksa/remote-docker-host-port-forwarder/internal/docker"
	"github.com/tomaszpeksa/remote-docker-host-port-forwarder/internal/testutil/sshfixture"
)

func TestManagerStreamOwnedTermination(t *testing.T) {
	for _, exitCode := range []int{0, 7} {
		t.Run(map[int]string{0: "EOF", 7: "Exit7"}[exitCode], func(t *testing.T) {
			w := sshfixture.NewWatchdog(t)
			h := newDiscoveryOwnedFixture(t, w.Fixture)
			t.Cleanup(func() {
				joined := false
				defer func() { w.SchedulePingAccounting(false, joined) }()
				h.close()
				w.Close()
				joined = true
			})
			a, survivor := h.backend(), h.backend()
			// Only initial inventory can establish these owners. Subsequent
			// measured repair must come from the real buffered Engine records.
			realList := h.m.discovery.list
			var lists atomic.Int64
			h.m.discovery.list = func(ctx context.Context) ([]string, error) {
				if lists.Add(1) == 1 {
					return realList(ctx)
				}
				<-ctx.Done()
				return nil, ctx.Err()
			}
			type receipt struct {
				attempt int
				at      time.Time
				result  docker.StreamResult
			}
			entered, observed := make(chan receipt, 3), make(chan receipt, 2)
			launch := []chan struct{}{make(chan struct{}), make(chan struct{})}
			drain := []chan struct{}{make(chan struct{}), make(chan struct{})}
			calls := 0
			h.m.discovery.openStream = func(ctx context.Context, deliver func(context.Context, docker.Event) error) <-chan docker.StreamResult {
				index := calls
				calls++
				entered <- receipt{attempt: index, at: time.Now()}
				if index >= 2 {
					return h.openStream(ctx, deliver)
				}
				select {
				case <-launch[index]:
				case <-ctx.Done():
					return h.openStream(ctx, deliver)
				}
				// Keep the Run consumer held while the real reader admits records
				// and joins. Forward its exact result, including the actual exit
				// error; never manufacture a successful EOF from cancellation.
				result := <-h.openStream(ctx, deliver)
				observed <- receipt{attempt: index, at: time.Now(), result: result}
				select {
				case <-drain[index]:
				case <-ctx.Done():
				}
				terminal := make(chan docker.StreamResult, 1)
				terminal <- result
				close(terminal)
				return terminal
			}
			h.start()
			await := func(ch <-chan receipt) receipt {
				t.Helper()
				select {
				case r := <-ch:
					return r
				case <-time.After(16 * time.Second):
					t.Fatal("owned finite stream boundary did not enter")
				}
				return receipt{}
			}
			require.Zero(t, await(entered).attempt)
			h.wait("STREAM_INITIAL_HTTP", func() error {
				if err := h.active(a); err != nil {
					return err
				}
				return h.active(survivor)
			})
			createdA := h.m.state.GetByContainer(a.id)[0].CreatedAt
			createdSurvivor := h.m.state.GetByContainer(survivor.id)[0].CreatedAt
			since := time.Now()
			h.stop(a)
			b := h.backend()
			until := time.Now()
			w.FiniteEvents(since, until, exitCode)
			lastHealth := lastWatchdogHealth(h.m.watchdog)
			close(launch[0])
			first := await(observed)
			require.False(t, first.result.Canceled)
			if exitCode == 0 {
				require.NoError(t, first.result.Err, "OWNED_FINITE_EOF")
			} else {
				require.ErrorContains(t, first.result.Err, "exit status 7", "OWNED_NONZERO_TERMINAL")
			}
			seenStart, seenStop := false, false
			for len(h.events) > 0 {
				event := <-h.events
				seenStart = seenStart || event.ContainerID == b.id && event.Type == "start"
				seenStop = seenStop || event.ContainerID == a.id && event.Type == "stop"
			}
			require.True(t, seenStart && seenStop, "OWNED_START_STOP_MUST_BE_ACCEPTED_BEFORE_TERMINAL")
			require.GreaterOrEqual(t, pendingWatchdogEvents(h.m), 2)
			require.Equal(t, lastHealth, lastWatchdogHealth(h.m.watchdog), "admission and EOF are not processing health")
			close(drain[0])
			secondEntry := await(entered)
			require.Equal(t, 1, secondEntry.attempt)
			require.GreaterOrEqual(t, secondEntry.at.Sub(first.at), time.Second, "OWNED_TERMINAL_MUST_PACE")
			require.Zero(t, pendingWatchdogEvents(h.m))
			h.wait("OWNED_BUFFERED_EVENTS_RESTORE_HTTP", func() error {
				if err := h.active(b); err != nil {
					return err
				}
				return h.absent(a)
			})
			h.owners(b, survivor)
			h.history(a, createdA)
			createdB := h.m.state.GetByContainer(b.id)[0].CreatedAt
			// A second real finite command demonstrates repeated replacement,
			// without repeating the measured owner transitions or inventing EOF.
			since = time.Now()
			h.f.Backend(sshfixture.Alpine, 0)
			t.Cleanup(h.close)
			w.FiniteEvents(since, time.Now(), exitCode)
			close(launch[1])
			second := await(observed)
			require.False(t, second.result.Canceled)
			if exitCode == 0 {
				require.NoError(t, second.result.Err)
			} else {
				require.ErrorContains(t, second.result.Err, "exit status 7")
			}
			close(drain[1])
			thirdEntry := await(entered)
			require.Equal(t, 2, thirdEntry.attempt)
			delay := time.Second
			if exitCode != 0 {
				delay = 2 * time.Second
			}
			require.GreaterOrEqual(t, thirdEntry.at.Sub(second.at), delay)
			require.Zero(t, pendingWatchdogEvents(h.m))
			h.owners(b, survivor)
			h.history(a, createdA)
			require.Equal(t, createdB, h.m.state.GetByContainer(b.id)[0].CreatedAt)
			require.Equal(t, createdSurvivor, h.m.state.GetByContainer(survivor.id)[0].CreatedAt)
			finiteCount := 0
			for _, attempt := range w.Snapshot() {
				if !attempt.FiniteEvents {
					continue
				}
				finiteCount++
				require.True(t, attempt.RealChildStarted)
				require.Equal(t, exitCode, attempt.ExitCode)
				require.False(t, attempt.JoinedAt.IsZero())
				require.False(t, attempt.CompletedAt.IsZero())
				for _, pid := range []int{attempt.ProxyPID, attempt.ChildPID} {
					var status syscall.WaitStatus
					_, err := syscall.Wait4(pid, &status, syscall.WNOHANG, nil)
					require.ErrorIs(t, err, syscall.ECHILD, "owned SSH/proxy must already be reaped")
				}
				t.Logf("STREAM_OWNED_WAIT %+v", attempt)
			}
			require.Equal(t, 2, finiteCount)
			t.Logf("STREAM_OWNED_PROOF exit=%d first_join=%s second_launch=%s second_join=%s third_launch=%s pending=0 HTTP/history/CreatedAt verified; finite Engine-history injection, not subscription readiness or remote-cleanup guarantee", exitCode, first.at, secondEntry.at, second.at, thirdEntry.at)
			h.close()
			w.Close()
			require.Empty(t, h.m.state.GetActual(), fmt.Sprintf("terminal cleanup exit=%d", exitCode))
		})
	}
}
