package main

import (
	"bufio"
	"bytes"
	"context"
	"fmt"
	"log/slog"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"github.com/tomaszpeksa/remote-docker-host-port-forwarder/internal/config"
)

// The only executable on the test PATH is this owned foreground helper. It
// acknowledges Start but never creates a mux endpoint, then awaits retirement.
func TestCLIStartupProcess(t *testing.T) {
	if os.Getenv("S06_CLI_HELPER") != "1" {
		return
	}
	conn, err := net.Dial("unix", os.Getenv("S06_CLI_GATE"))
	if err != nil {
		os.Exit(2)
	}
	var controlPath string
	for _, arg := range os.Args {
		if strings.HasPrefix(arg, "ControlPath=") {
			controlPath = strings.TrimPrefix(arg, "ControlPath=")
		}
	}
	_, _ = fmt.Fprintln(conn, controlPath)
	_, _ = bufio.NewReader(conn).ReadByte()
	os.Exit(3)
}

func TestRunFailedStartupOwnsCleanup(t *testing.T) {
	dir, err := os.MkdirTemp("/tmp", "s06-cli-")
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, os.RemoveAll(dir)) })
	gate, err := net.Listen("unix", filepath.Join(dir, "gate"))
	require.NoError(t, err)
	defer gate.Close()
	executable, err := os.Executable()
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(filepath.Join(dir, "ssh"), []byte(fmt.Sprintf("#!/bin/sh\nexec %q -test.run=^TestCLIStartupProcess$ -- \"$@\"\n", executable)), 0700))
	t.Setenv("PATH", dir)
	t.Setenv("HOME", dir)
	t.Setenv("S06_CLI_HELPER", "1")
	t.Setenv("S06_CLI_GATE", gate.Addr().String())
	t.Setenv("GORACE", os.Getenv("GORACE")+" atexit_sleep_ms=0")
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	var logs bytes.Buffer
	host := "ssh://" + filepath.Base(dir) + ".invalid"
	result := make(chan error, 1)
	go func() { result <- run(ctx, &config.Config{Host: host}, slog.New(slog.NewTextHandler(&logs, nil))) }()
	require.NoError(t, gate.(*net.UnixListener).SetDeadline(time.Now().Add(5*time.Second)))
	child, err := gate.Accept()
	require.NoError(t, err)
	defer child.Close()
	require.NoError(t, child.SetDeadline(time.Now().Add(5*time.Second)))
	line, err := bufio.NewReader(child).ReadString('\n')
	require.NoError(t, err)
	path := strings.TrimSpace(line)
	require.NotEmpty(t, path)
	_, err = os.Lstat(filepath.Dir(path))
	require.NoError(t, err, "constructor owns its actual private directory")
	cancel()
	select {
	case err = <-result:
	case <-time.After(5 * time.Second):
		t.Fatal("failed startup did not retire its child")
	}
	require.ErrorIs(t, err, context.Canceled)
	_, err = bufio.NewReader(child).ReadByte()
	require.Error(t, err, "child must release owned pipe before Run returns")
	require.Contains(t, logs.String(), "closing SSH ControlMaster connection", "CLI cleanup owner must exist even when Open fails")
	_, err = os.Lstat(filepath.Dir(path))
	require.True(t, os.IsNotExist(err), "failed startup must release the allocated directory")
}
