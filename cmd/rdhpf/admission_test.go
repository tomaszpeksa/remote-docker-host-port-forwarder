package main

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
	"github.com/tomaszpeksa/remote-docker-host-port-forwarder/internal/config"
	"github.com/tomaszpeksa/remote-docker-host-port-forwarder/internal/instancepath"
)

func TestRunDuplicateDoesNotTouchIncumbent(t *testing.T) {
	for _, target := range []string{"ssh://first", "ssh://second"} {
		t.Run(target, func(t *testing.T) {
			dir, err := os.MkdirTemp("/tmp", "s06-admission-")
			require.NoError(t, err)
			defer os.RemoveAll(dir)
			t.Setenv("HOME", dir)
			t.Setenv("PATH", dir)
			owner, err := instancepath.Acquire("ssh://first")
			require.NoError(t, err)
			defer owner.Close()
			marker := filepath.Join(dir, "started")
			require.NoError(t, os.WriteFile(filepath.Join(dir, "ssh"), []byte(fmt.Sprintf("#!/bin/sh\nprintf started > %q\nexit 1\n", marker)), 0700))
			before := map[string]os.FileInfo{}
			for _, host := range []string{"ssh://first", "ssh://second"} {
				path, e := instancepath.StatusPath(host, ".state.json")
				require.NoError(t, e)
				require.NoError(t, os.WriteFile(path, []byte("untouched"), 0600))
				before[path], err = os.Stat(path)
				require.NoError(t, err)
				socketPath, e := instancepath.StatusPath(host, ".sock")
				require.NoError(t, e)
				listener, e := net.Listen("unix", socketPath)
				require.NoError(t, e)
				defer listener.Close()
				before[socketPath], err = os.Lstat(socketPath)
				require.NoError(t, err)
			}
			err = run(context.Background(), &config.Config{Host: target}, slog.New(slog.NewTextHandler(io.Discard, nil)))
			require.ErrorContains(t, err, "another rdhpf publisher")
			executable, err := os.Executable()
			require.NoError(t, err)
			child := exec.Command(executable, "-test.run=^TestRunDuplicateProcess$")
			child.Env = append(os.Environ(), "S06_DUPLICATE_PROCESS=1", "S06_DUPLICATE_TARGET="+target)
			output, err := child.CombinedOutput()
			require.NoError(t, err)
			require.Contains(t, string(output), "another rdhpf publisher")
			_, err = os.Stat(marker)
			require.True(t, os.IsNotExist(err), "loser must not start SSH")
			for path, original := range before {
				after, e := os.Lstat(path)
				require.NoError(t, e)
				require.True(t, os.SameFile(original, after))
				require.Equal(t, original.ModTime(), after.ModTime())
				if after.Mode().IsRegular() {
					data, e := os.ReadFile(path)
					require.NoError(t, e)
					require.Equal(t, "untouched", string(data))
				}
			}
		})
	}
}
