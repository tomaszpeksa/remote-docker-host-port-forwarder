package socket

import (
	"encoding/json"
	"io"
	"log/slog"
	"net"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"github.com/tomaszpeksa/remote-docker-host-port-forwarder/internal/state"
	"github.com/tomaszpeksa/remote-docker-host-port-forwarder/internal/statefile"
)

func TestServerSnapshotEncoding(t *testing.T) {
	for _, count := range []int{0, 1, 2} {
		name := []string{"empty", "one", "multiple"}[count]
		t.Run(name, func(t *testing.T) {
			start := time.Date(2025, 2, 3, 4, 5, 6, 123, time.UTC)
			// Future fixed end times survive History.Add's age-based pruning.
			end := time.Date(9999, 2, 3, 4, 5, 6, 123, time.UTC)
			st, hist := state.NewState(), state.NewHistory()
			want := statefile.StateFile{
				Version: "2.0", Host: "ssh://user@host:2222", PID: 1234, StartedAt: start,
				Forwards: []statefile.ForwardSnapshot{}, History: []statefile.HistorySnapshot{},
			}
			for i, id := range []string{"z", "a"}[:count] {
				st.SetActual(id, 8080+i, "conflict", "occupied")
				f := st.GetByContainer(id)[0] // State supplies both timestamps.
				want.Forwards = append(want.Forwards, statefile.ForwardSnapshot{
					ContainerID: id, Port: 8080 + i, Status: "conflict", Reason: "occupied", CreatedAt: f.CreatedAt, UpdatedAt: f.UpdatedAt,
				})
				hist.Add(state.HistoryEntry{ContainerID: id, Port: 9000 + i, StartedAt: start, EndedAt: end, EndReason: "stopped", FinalStatus: "active"})
				want.History = append(want.History, statefile.HistorySnapshot{
					ContainerID: id, Port: 9000 + i, StartedAt: start, EndedAt: end, EndReason: "stopped", FinalStatus: "active",
				})
			}
			s := Server{state: st, history: hist, host: want.Host, pid: want.PID, startedAt: start, logger: slog.New(slog.NewTextHandler(io.Discard, nil))}
			server, client := net.Pipe()
			defer client.Close()
			require.NoError(t, client.SetDeadline(time.Now().Add(5*time.Second)))
			before := time.Now()
			done := make(chan struct{})
			go func() {
				s.handleConnection(server)
				close(done)
			}()
			data, err := io.ReadAll(client)
			require.NoError(t, err)
			<-done
			after := time.Now()
			var got statefile.StateFile
			require.NoError(t, json.Unmarshal(data, &got))
			require.False(t, got.UpdatedAt.Before(before))
			require.False(t, got.UpdatedAt.After(after))
			want.UpdatedAt = got.UpdatedAt
			// GetActual traverses maps: only the supplied slice's order is a contract,
			// not order across separate acquisitions. History order is deterministic.
			// Compare wire values (including offsets), not time.Time's Location representation.
			wantForwards := make([]json.RawMessage, len(want.Forwards))
			gotForwards := make([]json.RawMessage, len(got.Forwards))
			for i, forward := range want.Forwards {
				wantForwards[i], err = json.Marshal(forward)
				require.NoError(t, err)
			}
			for i, forward := range got.Forwards {
				gotForwards[i], err = json.Marshal(forward)
				require.NoError(t, err)
			}
			require.ElementsMatch(t, wantForwards, gotForwards)
			want.Forwards = got.Forwards
			require.NotNil(t, got.Forwards)
			require.Equal(t, want, got)
			encoded, err := json.MarshalIndent(want, "", "  ")
			require.NoError(t, err)
			require.Equal(t, string(encoded)+"\n", string(data))
		})
	}
}
