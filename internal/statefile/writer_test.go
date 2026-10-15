package statefile

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"github.com/tomaszpeksa/remote-docker-host-port-forwarder/internal/state"
)

func snapshotFixture() ([]state.ForwardState, []state.HistoryEntry, StateFile) {
	start := time.Date(2025, 2, 3, 4, 5, 6, 123, time.UTC)
	end := start.Add(time.Minute)
	return []state.ForwardState{
			{ContainerID: "z", Port: 8080, Status: "conflict", Reason: "occupied", CreatedAt: start, UpdatedAt: end},
			{ContainerID: "a", Port: 80, Status: "pending", Reason: "waiting", CreatedAt: end, UpdatedAt: start},
		}, []state.HistoryEntry{
			{ContainerID: "y", Port: 9000, StartedAt: start, EndedAt: end, EndReason: "stopped", FinalStatus: "active"},
			{ContainerID: "b", Port: 90, StartedAt: end, EndedAt: start, EndReason: "removed", FinalStatus: "conflict"},
		}, StateFile{
			Version: "2.0", Host: "ssh://user@host:2222", PID: 1234, StartedAt: start, UpdatedAt: end,
			Forwards: []ForwardSnapshot{
				{ContainerID: "z", Port: 8080, Status: "conflict", Reason: "occupied", CreatedAt: start, UpdatedAt: end},
				{ContainerID: "a", Port: 80, Status: "pending", Reason: "waiting", CreatedAt: end, UpdatedAt: start},
			},
			History: []HistorySnapshot{
				{ContainerID: "y", Port: 9000, StartedAt: start, EndedAt: end, EndReason: "stopped", FinalStatus: "active"},
				{ContainerID: "b", Port: 90, StartedAt: end, EndedAt: start, EndReason: "removed", FinalStatus: "conflict"},
			},
		}
}

func TestWriterSnapshotEncoding(t *testing.T) {
	for _, name := range []string{"nil", "empty", "one", "multiple"} {
		t.Run(name, func(t *testing.T) {
			forwards, history, want := snapshotFixture()
			switch name {
			case "nil":
				forwards, history = nil, nil
				want.Forwards, want.History = []ForwardSnapshot{}, []HistorySnapshot{}
			case "empty":
				forwards, history = []state.ForwardState{}, []state.HistoryEntry{}
				want.Forwards, want.History = []ForwardSnapshot{}, []HistorySnapshot{}
			case "one":
				forwards, history = forwards[:1], history[:1]
				want.Forwards, want.History = want.Forwards[:1], want.History[:1]
			}
			w := Writer{path: filepath.Join(t.TempDir(), "state.json"), host: want.Host, pid: want.PID, startedAt: want.StartedAt}
			before := time.Now()
			require.NoError(t, w.Write(forwards, history))
			after := time.Now()
			data, err := os.ReadFile(w.path)
			require.NoError(t, err)
			var got StateFile
			require.NoError(t, json.Unmarshal(data, &got))
			require.False(t, got.UpdatedAt.Before(before))
			require.False(t, got.UpdatedAt.After(after))
			want.UpdatedAt = got.UpdatedAt // The writer owns the wall-clock read.
			require.Equal(t, want, got)
			encoded, err := json.MarshalIndent(want, "", "  ")
			require.NoError(t, err)
			require.Equal(t, string(encoded)+"\n", string(data))
		})
	}
}
