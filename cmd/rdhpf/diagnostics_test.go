package main

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"github.com/stretchr/testify/require"
	"github.com/tomaszpeksa/remote-docker-host-port-forwarder/internal/instancepath"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"
)

func TestCLISafeErrorProcess(t *testing.T) {
	if os.Getenv("S17_CLI_PROCESS") != "1" {
		return
	}
	for i, arg := range os.Args {
		if arg == "--" {
			os.Args = append([]string{"rdhpf"}, os.Args[i+1:]...)
			break
		}
	}
	main()
}

func TestCLISafeErrors(t *testing.T) {
	for _, args := range [][]string{
		{"private-payload"},
		{"run", "--host=ssh://fixture.invalid", "--trace=private-payload"},
		{"run", "--host=private-payload"},
		{"status", "--host=ssh://fixture.invalid", "--format=private-payload"},
	} {
		t.Run(args[0]+args[len(args)-1], func(t *testing.T) {
			exe, err := os.Executable()
			require.NoError(t, err)
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			cmd := exec.CommandContext(ctx, exe, append([]string{"-test.run=^TestCLISafeErrorProcess$", "--"}, args...)...)
			cmd.Env = append(os.Environ(), "S17_CLI_PROCESS=1")
			var out, stderr bytes.Buffer
			cmd.Stdout = &out
			cmd.Stderr = &stderr
			require.Error(t, cmd.Run())
			require.NoError(t, ctx.Err())
			require.NotContains(t, out.String()+stderr.String(), "private-payload")
			require.Contains(t, stderr.String(), "Error:")
		})
	}
}

func TestCLIForegroundStderrPrivate(t *testing.T) {
	binary := filepath.Join(t.TempDir(), "rdhpf")
	build := exec.Command("go", "build", "-o", binary, ".")
	output, err := build.CombinedOutput()
	require.NoError(t, err, string(output))
	for _, level := range []string{"trace", "debug", "info", "warn", "error"} {
		t.Run(level, func(t *testing.T) {
			home, err := os.MkdirTemp("/tmp", "s17-cli-")
			require.NoError(t, err)
			defer os.RemoveAll(home)
			t.Setenv("HOME", home)
			require.NoError(t, os.WriteFile(filepath.Join(home, "ssh"), []byte("#!/bin/sh\ni=0; while [ \"$i\" -lt 100 ]; do printf '%1000s' private-child-payload >&2; i=$((i+1)); done\nprintf private-split >&2; printf -- -unterminated >&2\nexit 7\n"), 0700))
			ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
			defer cancel()
			cmd := exec.CommandContext(ctx, binary, "run", "--host=ssh://private-user@192.0.2.77", "--log-level="+level)
			cmd.Env = append(os.Environ(), "HOME="+home, "PATH="+home, "RDHPF_LOG_LEVEL=", "RDHPF_ENABLE_LABEL_PORTS=")
			var stdout, stderr bytes.Buffer
			cmd.Stdout = &stdout
			cmd.Stderr = &stderr
			err = cmd.Run()
			require.NoError(t, ctx.Err())
			var exit *exec.ExitError
			require.ErrorAs(t, err, &exit)
			require.Equal(t, 1, exit.ExitCode())
			for _, marker := range []string{"private-child", "private-split", "private-user", "192.0.2.77", home} {
				require.NotContains(t, stdout.String()+stderr.String(), marker)
			}
			require.Contains(t, stdout.String(), "fatal error")
			require.Contains(t, stderr.String(), "operation failed")
			owner, err := instancepath.Acquire("ssh://successor.invalid")
			require.NoError(t, err)
			require.NoError(t, owner.Close())
		})
	}
}

func TestCLIErrorPresentationPreservesCauses(t *testing.T) {
	cause := errors.New("private-cause")
	wrapped := fmt.Errorf("private-wrapper: %w", cause)
	require.NotContains(t, cliErrorMessage(wrapped), "private-")
	require.ErrorIs(t, wrapped, cause)
	cleanup := &cleanupFailure{cause: context.Canceled}
	require.False(t, cancellationOnly(cleanup))
	require.False(t, cancellationOnly(errors.Join(context.Canceled, wrapped)))
	require.True(t, cancellationOnly(fmt.Errorf("private-wrapper: %w", context.Canceled)))
	require.NotContains(t, cliErrorMessage(cleanup), "private-")
}
