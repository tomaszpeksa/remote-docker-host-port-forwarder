package muxstub

import (
	"bufio"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Install selects only this test executable; the caller must register Master
// cleanup after this returns so its Wait precedes directory/environment cleanup.
func Install(t *testing.T) string {
	t.Helper()
	dir, err := os.MkdirTemp("/tmp", "s12-mux-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := os.RemoveAll(dir); err != nil {
			t.Error(err)
		}
	})
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	script := fmt.Sprintf("#!/bin/sh\nexec %q -test.run '^TestMuxProcess$' -- \"$@\"\n", executable)
	// #nosec G306 -- Owner-only launcher in a new private directory selects only
	// this executable's TestMuxProcess; callers join Master before fixture cleanup.
	if err := os.WriteFile(filepath.Join(dir, "ssh"), []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir)
	t.Setenv("HOME", dir)
	t.Setenv("S12_MUX_PROCESS", "1")
	t.Setenv("S12_MUX_ROOT", dir)
	t.Setenv("GORACE", os.Getenv("GORACE")+" atexit_sleep_ms=0")
	t.Setenv("S12_MUX_RECORD", filepath.Join(dir, "calls"))
	t.Setenv("S12_MUX_OPEN_FAIL", filepath.Join(dir, "fail-OPEN"))
	t.Setenv("S12_MUX_CLOSE_FAIL", filepath.Join(dir, "fail-CLOSE"))
	t.Setenv("S12_MUX_LOST_REPLY", filepath.Join(dir, "lost-reply"))
	return dir
}

// Process is called only by the explicitly selected test helper process.
func Process(t *testing.T) {
	if os.Getenv("S12_MUX_PROCESS") != "1" {
		return
	}
	args := os.Args
	for len(args) > 0 && args[0] != "--" {
		args = args[1:]
	}
	if len(args) == 0 {
		os.Exit(90)
	}
	args = args[1:]
	path, operation := "", ""
	for i, arg := range args {
		if strings.HasPrefix(arg, "ControlPath=") {
			path = strings.TrimPrefix(arg, "ControlPath=")
		}
		if arg == "-S" && i+1 < len(args) {
			path = args[i+1]
		}
		if arg == "-O" && i+1 < len(args) {
			operation = args[i+1]
		}
	}
	if len(args) > 0 && args[0] == "-MN" {
		if !filepath.IsAbs(path) || filepath.Base(path) != "mux" {
			t.Fatal("unexpected owned mux pathname")
		}
		root, err := os.OpenRoot(filepath.Dir(path))
		if err != nil {
			t.Fatal(err)
		}
		defer root.Close()
		const temporaryName = "mux.0123456789abcdef"
		temporary := filepath.Join(filepath.Dir(path), temporaryName)
		listener, err := net.ListenUnix("unix", &net.UnixAddr{Name: temporary, Net: "unix"})
		if err != nil {
			t.Fatal(err)
		}
		listener.SetUnlinkOnClose(false)
		if err := root.Link(temporaryName, "mux"); err != nil {
			t.Fatal(err)
		}
		if err := root.Remove(temporaryName); err != nil {
			t.Fatal(err)
		}
		var server Server
		for {
			conn, err := listener.Accept()
			if err != nil {
				t.Fatal(err)
			}
			reader := bufio.NewReader(conn)
			first, err := reader.Peek(1)
			if err == nil {
				if first[0] == 0 {
					_ = server.Serve(reader, conn)
				} else if first[0] == 'c' {
					_, _ = fmt.Fprintf(conn, "Master running (pid=%d)\n", os.Getpid())
				}
			}
			_ = conn.Close()
		}
	}
	if operation == "check" {
		conn, err := net.DialUnix("unix", nil, &net.UnixAddr{Name: path, Net: "unix"})
		if err != nil {
			os.Exit(91)
		}
		if _, err := conn.Write([]byte{'c'}); err != nil {
			os.Exit(92)
		}
		if _, err := io.Copy(os.Stdout, conn); err != nil {
			os.Exit(93)
		}
		if err := conn.Close(); err != nil {
			os.Exit(94)
		}
	} else {
		_, _ = fmt.Fprintln(os.Stdout, "{}")
	}
	os.Exit(0)
}
