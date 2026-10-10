package ssh

import (
	"context"
	"encoding/json"
	"net"
	"os"
	"path/filepath"
	"runtime"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// DIAGNOSTIC OVERLAY ONLY. These observations are not a product correction.
type socketDiagnosticIdentity struct {
	Device  uint64 `json:"device"`
	Inode   uint64 `json:"inode"`
	CTimeNS int64  `json:"ctime_ns"`
	MTimeNS int64  `json:"mtime_ns"`
	Links   uint64 `json:"links"`
	Mode    string `json:"mode"`
}

func diagnosticRecord(t *testing.T, phase string, old, current os.FileInfo, child, peer int, err error) {
	t.Helper()
	record := map[string]any{"phase": phase, "pid": os.Getpid(), "owned_child_pid": child, "kernel_peer_pid": peer, "error": nil}
	if err != nil {
		record["error"] = err.Error()
	}
	if old != nil {
		record["old"] = diagnosticIdentity(old)
	}
	if current != nil {
		record["current"] = diagnosticIdentity(current)
	}
	if old != nil && current != nil {
		record["same_file"] = os.SameFile(old, current)
	}
	data, e := json.Marshal(record)
	if e != nil {
		t.Errorf("marshal diagnostic: %v", e)
		return
	}
	t.Log("SOCKET_DIAGNOSTIC " + string(data))
}

func diagnosticTrace(t *testing.T, m *Master) func(string, *masterConnection, os.FileInfo, error) {
	t.Helper()
	return func(phase string, c *masterConnection, snapshot os.FileInfo, err error) {
		diagnosticRecord(t, "retirement-"+phase, c.socket, snapshot, c.cmd.Process.Pid, 0, err)
		actual, e := os.Lstat(m.ControlPath())
		diagnosticRecord(t, "pathname-at-"+phase, c.socket, actual, c.cmd.Process.Pid, 0, e)
	}
}

func diagnosticPeer(t *testing.T, path string) int {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	conn, err := (&net.Dialer{}).DialContext(ctx, "unix", path)
	require.NoError(t, err)
	defer conn.Close()
	pid, err := unixPeerPID(conn.(*net.UnixConn))
	require.NoError(t, err)
	return pid
}

func diagnosticPin(t *testing.T, path string) *os.File {
	t.Helper()
	pin, err := diagnosticOpenPin(path)
	if err != nil && runtime.GOOS == "darwin" {
		t.Skipf("Darwin identity pin unavailable: %v", err)
	}
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, pin.Close()) })
	return pin
}

func TestSocketDiagnosticIdentityPin(t *testing.T) {
	dir, err := os.MkdirTemp("/tmp", "s06-pin-")
	require.NoError(t, err)
	defer os.RemoveAll(dir)
	path := filepath.Join(dir, "socket")
	old, err := net.ListenUnix("unix", &net.UnixAddr{Name: path, Net: "unix"})
	require.NoError(t, err)
	old.SetUnlinkOnClose(false)
	defer old.Close()
	before, err := os.Lstat(path)
	require.NoError(t, err)
	pin := diagnosticPin(t, path)
	pinned, err := pin.Stat()
	require.NoError(t, err)
	diagnosticRecord(t, "pin-open", before, pinned, 0, diagnosticPeer(t, path), nil)
	require.True(t, os.SameFile(before, pinned))
	require.NoError(t, old.Close())
	require.NoError(t, os.Remove(path))
	replacement, err := net.ListenUnix("unix", &net.UnixAddr{Name: path, Net: "unix"})
	require.NoError(t, err)
	defer replacement.Close()
	after, err := os.Lstat(path)
	require.NoError(t, err)
	pinnedAfter, err := pin.Stat()
	require.NoError(t, err)
	diagnosticRecord(t, "pin-after-replacement", pinnedAfter, after, 0, diagnosticPeer(t, path), nil)
	require.True(t, os.SameFile(pinned, pinnedAfter), "pin still references old socket inode")
	require.False(t, os.SameFile(pinnedAfter, after), "pinned old inode must not be recycled")
	t.Log("PIN_OBSERVATION: one owned unlink/rebind preserved old file identity; not conditional unlink proof")
}

// Even a pinned inode does not make the pathname check/removal atomic. The
// actor here explicitly removes the old path after the real Lstat, then binds
// another owned listener. This models namespace substitution, not a claim that
// a cooperating new Master.Open would unlink an occupied endpoint.
func TestSocketDiagnosticStatToUnlinkReplacement(t *testing.T) {
	m, _ := masterFixture(t)
	require.NoError(t, m.Open(context.Background()))
	owned := m.connection
	pin := diagnosticPin(t, m.ControlPath())
	pinned, err := pin.Stat()
	require.NoError(t, err)
	require.NoError(t, owned.cmd.Process.Kill()) // retained owned handle only
	<-owned.done
	atStat, proceed := make(chan struct{}), make(chan struct{})
	var releaseOnce sync.Once
	release := func() { releaseOnce.Do(func() { close(proceed) }) }
	defer release()
	trace := diagnosticTrace(t, m)
	m.retirementTrace = func(phase string, c *masterConnection, info os.FileInfo, err error) {
		trace(phase, c, info, err)
		if phase == "after-stat" {
			close(atStat)
			<-proceed
		}
	}
	result := make(chan error, 1)
	go func() { result <- m.Close() }()
	select {
	case <-atStat:
	case <-time.After(5 * time.Second):
		t.Fatal("production retirement did not reach stat barrier")
	}
	// Both namespace entries are owned exclusively by this test's private dir.
	require.NoError(t, os.Remove(m.ControlPath()))
	foreign, err := net.Listen("unix", m.ControlPath())
	require.NoError(t, err)
	defer foreign.Close()
	foreignInfo, err := os.Lstat(m.ControlPath())
	require.NoError(t, err)
	diagnosticRecord(t, "barrier-foreign-installed", pinned, foreignInfo, owned.cmd.Process.Pid, diagnosticPeer(t, m.ControlPath()), nil)
	require.False(t, os.SameFile(pinned, foreignInfo), "pin removes inode reuse from this separate experiment")
	release()
	select {
	case err = <-result:
		require.NoError(t, err)
	case <-time.After(5 * time.Second):
		t.Fatal("retirement did not finish after barrier release")
	}
	after, statErr := os.Lstat(m.ControlPath())
	diagnosticRecord(t, "barrier-after-retirement", foreignInfo, after, owned.cmd.Process.Pid, 0, statErr)
	require.NoError(t, statErr, "foreign replacement must survive; pinning alone does not make stat/unlink atomic")
}
