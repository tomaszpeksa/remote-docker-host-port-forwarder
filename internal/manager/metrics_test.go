package manager

import (
	"context"
	"encoding/json"
	"log/slog"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"github.com/tomaszpeksa/remote-docker-host-port-forwarder/internal/state"
)

// Capture actual JSONHandler output without blocking the production logger.
type metricsLogCapture chan []byte

func (c metricsLogCapture) Write(p []byte) (int, error) {
	select {
	case c <- append([]byte(nil), p...):
	default:
	}
	return len(p), nil
}

func (c metricsLogCapture) next(t *testing.T) map[string]any {
	t.Helper()
	select {
	case data := <-c:
		var record map[string]any
		require.NoError(t, json.Unmarshal(data, &record))
		return record
	case <-time.After(5 * time.Second):
		t.Fatal("production performance logger did not emit a summary")
		return nil
	}
}

func startMetricsLogger(t *testing.T, m *Manager) metricsLogCapture {
	t.Helper()
	capture := make(metricsLogCapture, 1)
	m.logger = slog.New(slog.NewJSONHandler(capture, nil))
	ctx, cancel := context.WithCancel(context.Background())
	var wg sync.WaitGroup
	wg.Add(1)
	t.Cleanup(func() { cancel(); wg.Wait() })
	go func() {
		defer wg.Done()
		m.logPerformanceMetrics(ctx, time.Microsecond)
	}()
	return capture
}

func TestMetricsStats(t *testing.T) {
	for _, tc := range []struct {
		name                   string
		samples                int
		avgEvent, avgReconcile time.Duration
		avgSSH                 time.Duration
	}{
		{name: "empty"},
		{name: "known integer averages", samples: 2, avgEvent: 1, avgReconcile: 3, avgSSH: 4},
		{name: "rolling last 100", samples: 150, avgEvent: 100, avgReconcile: 201, avgSSH: 301},
	} {
		t.Run(tc.name, func(t *testing.T) {
			m := &Manager{state: state.NewState(), metrics: performanceMetrics{startTime: time.Now().Add(-time.Minute)}}
			for i := 1; i <= tc.samples; i++ {
				m.metrics.recordEventProcessing(time.Duration(i))
				m.metrics.recordReconciliation(2 * time.Duration(i))
				m.metrics.recordSSHCommand(3 * time.Duration(i))
			}
			before := time.Since(m.metrics.startTime)
			stats := m.metrics.getStats()
			after := time.Since(m.metrics.startTime)
			require.Equal(t, tc.avgEvent, stats.avgEventTime)
			require.Equal(t, tc.avgReconcile, stats.avgReconcileTime)
			require.Equal(t, tc.avgSSH, stats.avgSSHTime)
			require.Equal(t, int64(tc.samples), stats.totalEventsProcessed)
			require.Equal(t, int64(tc.samples), stats.totalReconciliations)
			require.GreaterOrEqual(t, stats.uptime, before)
			require.LessOrEqual(t, stats.uptime, after)
			record := startMetricsLogger(t, m).next(t)
			require.Equal(t, float64(tc.samples), record["total_events"])
			require.Equal(t, float64(tc.samples), record["total_reconciliations"])
		})
	}
}

func TestMetricsSummaryLog(t *testing.T) {
	m := &Manager{state: state.NewState(), metrics: performanceMetrics{startTime: time.Now().Add(-time.Minute)}}
	m.metrics.recordEventProcessing(1500 * time.Microsecond)
	m.metrics.recordEventProcessing(3500 * time.Microsecond)
	m.metrics.recordReconciliation(7500 * time.Microsecond)
	m.metrics.recordSSHCommand(9500 * time.Microsecond)
	m.state.MarkActive("first", 80)
	m.state.MarkActive("second", 81)
	m.state.MarkConflict("third", 82, "occupied")
	m.state.MarkPending("fourth", 83, "waiting")
	before := time.Since(m.metrics.startTime).Round(time.Second)
	record := startMetricsLogger(t, m).next(t)
	after := time.Since(m.metrics.startTime).Round(time.Second)
	keys := make([]string, 0, len(record))
	for key := range record {
		keys = append(keys, key)
	}
	require.ElementsMatch(t, []string{"time", "level", "msg", "avg_event_latency_ms", "avg_reconcile_ms", "avg_ssh_cmd_ms", "active_forwards", "conflicts", "total_events", "total_reconciliations", "uptime"}, keys)
	require.Equal(t, "INFO", record["level"])
	require.Equal(t, "Performance summary", record["msg"])
	_, err := time.Parse(time.RFC3339Nano, record["time"].(string))
	require.NoError(t, err)
	for key, want := range map[string]float64{
		"avg_event_latency_ms": 2, "avg_reconcile_ms": 7, "avg_ssh_cmd_ms": 9,
		"active_forwards": 2, "conflicts": 1, "total_events": 2, "total_reconciliations": 1,
	} {
		require.Equal(t, want, record[key], key)
	}
	uptime, err := time.ParseDuration(record["uptime"].(string))
	require.NoError(t, err)
	require.Equal(t, uptime.Round(time.Second), uptime)
	require.GreaterOrEqual(t, uptime, before)
	require.LessOrEqual(t, uptime, after)
}

func TestMetricsSnapshotConcurrent(t *testing.T) {
	m := &Manager{state: state.NewState(), metrics: performanceMetrics{startTime: time.Now()}}
	ctx, cancel := context.WithCancel(context.Background())
	var wg sync.WaitGroup
	// Register before launching any worker, so even a failed log assertion joins all workers.
	t.Cleanup(func() { cancel(); wg.Wait() })
	for _, record := range []func(time.Duration){m.metrics.recordEventProcessing, m.metrics.recordReconciliation, m.metrics.recordSSHCommand} {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for ctx.Err() == nil {
				record(time.Millisecond)
			}
		}()
	}
	capture := startMetricsLogger(t, m)
	for i := 0; i < 100; i++ {
		record := capture.next(t)
		require.Equal(t, "Performance summary", record["msg"])
	}
}
