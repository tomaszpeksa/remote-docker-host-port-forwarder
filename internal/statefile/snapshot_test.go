package statefile

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"github.com/tomaszpeksa/remote-docker-host-port-forwarder/internal/state"
)

func TestNewSnapshotPreservesMetadata(t *testing.T) {
	forwards, history, want := snapshotFixture()
	got := NewSnapshot(want.Host, want.PID, want.StartedAt, want.UpdatedAt, forwards[:1], history[:1])
	want.Forwards, want.History = want.Forwards[:1], want.History[:1]
	require.Equal(t, want, got)
	data, err := json.Marshal(got)
	require.NoError(t, err)
	require.JSONEq(t, `{
		"version":"2.0", "host":"ssh://user@host:2222", "pid":1234,
		"started_at":"2025-02-03T04:05:06.000000123Z", "updated_at":"2025-02-03T04:06:06.000000123Z",
		"forwards":[{"container_id":"z", "port":8080, "status":"conflict", "reason":"occupied",
			"created_at":"2025-02-03T04:05:06.000000123Z", "updated_at":"2025-02-03T04:06:06.000000123Z"}],
		"history":[{"container_id":"y", "port":9000, "started_at":"2025-02-03T04:05:06.000000123Z",
			"ended_at":"2025-02-03T04:06:06.000000123Z", "end_reason":"stopped", "final_status":"active"}]
	}`, string(data))
}

func TestNewSnapshotEmptyArrays(t *testing.T) {
	for _, name := range []string{"nil", "empty"} {
		t.Run(name, func(t *testing.T) {
			var forwards []state.ForwardState
			var history []state.HistoryEntry
			if name == "empty" {
				forwards, history = []state.ForwardState{}, []state.HistoryEntry{}
			}
			got := NewSnapshot("", 0, time.Time{}, time.Time{}, forwards, history)
			require.NotNil(t, got.Forwards)
			require.NotNil(t, got.History)
			data, err := json.Marshal(got)
			require.NoError(t, err)
			require.JSONEq(t, `{"version":"2.0", "host":"", "pid":0,
				"started_at":"0001-01-01T00:00:00Z", "updated_at":"0001-01-01T00:00:00Z",
				"forwards":[], "history":[]}`, string(data))
		})
	}
}

func TestNewSnapshotPreservesInputOrder(t *testing.T) {
	forwards, history, want := snapshotFixture()
	got := NewSnapshot(want.Host, want.PID, want.StartedAt, want.UpdatedAt, forwards, history)
	require.Equal(t, want, got)
}

func TestNewSnapshotCopiesInputs(t *testing.T) {
	forwards, history, want := snapshotFixture()
	got := NewSnapshot(want.Host, want.PID, want.StartedAt, want.UpdatedAt, forwards, history)
	forwards[0], forwards[1] = state.ForwardState{}, state.ForwardState{}
	history[0], history[1] = state.HistoryEntry{}, state.HistoryEntry{}
	require.Equal(t, want, got)
}
