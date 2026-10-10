package instancepath

import (
	"bufio"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// Owned IPC controls both fixture processes. The child has a bounded read
// deadline and exits on EOF; no PID discovered from output is signaled.
func TestOwnerCrashProcess(t *testing.T) {
	role := os.Getenv("RDHPF_OWNER_CRASH_ROLE")
	if role == "" {
		return
	}
	if role == "parent" {
		owner, err := Acquire("ssh://parent")
		require.NoError(t, err)
		defer owner.Close()
		executable, err := os.Executable()
		require.NoError(t, err)
		child := exec.Command(executable, "-test.run=^TestOwnerCrashProcess$")
		child.Env = append(os.Environ(), "RDHPF_OWNER_CRASH_ROLE=child")
		require.NoError(t, child.Start())
		defer func() { _ = child.Process.Kill(); _ = child.Wait() }()
	}
	conn, err := net.DialTimeout("unix", os.Getenv("RDHPF_OWNER_CRASH_GATE"), time.Second)
	require.NoError(t, err)
	defer conn.Close()
	require.NoError(t, conn.SetDeadline(time.Now().Add(10*time.Second)))
	_, err = fmt.Fprintln(conn, role)
	require.NoError(t, err)
	reader := bufio.NewReader(conn)
	for {
		command, err := reader.ReadString('\n')
		if err != nil {
			return
		}
		if command == "stop\n" {
			return
		}
		if command == "ping\n" {
			_, err = fmt.Fprintln(conn, "alive")
			require.NoError(t, err)
		}
	}
}

func TestOwnerReleasedAfterParentSIGKILLWithLiveExecChild(t *testing.T) {
	dir, err := os.MkdirTemp("/tmp", "s06-owner-death-")
	require.NoError(t, err)
	defer os.RemoveAll(dir)
	t.Setenv("HOME", dir)
	gate, err := net.ListenUnix("unix", &net.UnixAddr{Name: filepath.Join(dir, "gate"), Net: "unix"})
	require.NoError(t, err)
	defer gate.Close()
	require.NoError(t, gate.SetDeadline(time.Now().Add(5*time.Second)))
	executable, err := os.Executable()
	require.NoError(t, err)
	parent := exec.Command(executable, "-test.run=^TestOwnerCrashProcess$")
	parent.Env = append(os.Environ(), "RDHPF_OWNER_CRASH_ROLE=parent", "RDHPF_OWNER_CRASH_GATE="+gate.Addr().String(), "GORACE=atexit_sleep_ms=0")
	require.NoError(t, parent.Start())
	waited := make(chan struct{})
	var waitErr error
	go func() { waitErr = parent.Wait(); close(waited) }()
	defer func() { _ = parent.Process.Kill(); <-waited }()
	peers := map[string]net.Conn{}
	defer func() {
		for _, conn := range peers {
			conn.Close()
		}
	}()
	for i := 0; i < 2; i++ {
		conn, e := gate.Accept()
		require.NoError(t, e)
		require.NoError(t, conn.SetDeadline(time.Now().Add(5*time.Second)))
		role, e := bufio.NewReader(conn).ReadString('\n')
		require.NoError(t, e)
		peers[role] = conn
	}
	require.NotNil(t, peers["parent\n"])
	child := peers["child\n"]
	require.NotNil(t, child)
	_, err = Acquire("ssh://contender")
	require.Error(t, err, "parent has admission")
	require.NoError(t, parent.Process.Kill())
	<-waited
	require.Error(t, waitErr)
	require.NotNil(t, parent.ProcessState)
	// Child is still responsive AFTER the actual owner process was killed/reaped.
	_, err = fmt.Fprintln(child, "ping")
	require.NoError(t, err)
	reader := bufio.NewReader(child)
	line, err := reader.ReadString('\n')
	require.NoError(t, err)
	require.Equal(t, "alive\n", line)
	next, err := Acquire("ssh://successor")
	require.NoError(t, err)
	require.NoError(t, next.Close())
	_, err = fmt.Fprintln(child, "stop")
	require.NoError(t, err)
	_, err = reader.ReadByte()
	require.Error(t, err, "owned orphan closes IPC on exit; OS reaps it")
}
