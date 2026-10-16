package ssh

import (
	"context"
	"os"
	"os/exec"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestLateRetirementProcess(t *testing.T) {
	if os.Getenv("S06_LATE_RETIREMENT") != "1" {
		return
	}
	time.Sleep(10 * time.Second)
	os.Exit(0)
}

func TestCloseUncertainRetainsLateWaitResponsibility(t *testing.T) {
	m, _ := masterFixture(t)
	executable, err := os.Executable()
	require.NoError(t, err)
	cmd := exec.Command(executable, "-test.run=^TestLateRetirementProcess$")
	cmd.Env = append(os.Environ(), "S06_LATE_RETIREMENT=1")
	require.NoError(t, cmd.Start())
	c := &masterConnection{cmd: cmd, done: make(chan struct{})}
	m.connection = c
	m.cmd = cmd
	release := make(chan struct{})
	var once sync.Once
	complete := func() { once.Do(func() { close(release) }) }
	defer func() { complete(); <-c.done }()
	// Model delayed completion of the single retained Wait, without production hooks.
	go func() { <-release; c.waitErr = cmd.Wait(); close(c.done) }()
	require.ErrorIs(t, m.Close(), ErrRetirementUncertain)
	require.Same(t, c, m.connection)
	require.Zero(t, m.generation)
	require.ErrorContains(t, m.Open(context.Background()), "close still in progress")
	complete()
	<-c.done
	require.NotNil(t, cmd.ProcessState)
	require.NoError(t, m.Close())
	require.Nil(t, m.connection)
	require.Equal(t, uint64(1), m.generation)
}
