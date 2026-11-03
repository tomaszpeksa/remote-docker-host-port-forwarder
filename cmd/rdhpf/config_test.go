package main

import (
	"fmt"
	"github.com/stretchr/testify/require"
	"os"
	"path/filepath"
	"testing"
)

func TestRunConfigurationRejectsBeforeAcquisition(t *testing.T) {
	for _, tc := range []struct {
		name, level, env, labels string
		trace                    bool
	}{
		{name: "flag", level: "private-payload"},
		{name: "empty flag", level: ""},
		{name: "whitespace", level: " info"},
		{name: "shadowed flag", level: "private-payload", env: "debug"},
		{name: "shadowed env", level: "info", env: "private-payload", trace: true},
		{name: "labels", level: "info", labels: "private-payload"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			home, err := os.MkdirTemp("/tmp", "s17-config-")
			require.NoError(t, err)
			t.Cleanup(func() { require.NoError(t, os.RemoveAll(home)) })
			t.Setenv("HOME", home)
			t.Setenv("PATH", home)
			t.Setenv("RDHPF_LOG_LEVEL", tc.env)
			t.Setenv("RDHPF_ENABLE_LABEL_PORTS", tc.labels)
			marker := filepath.Join(home, "started")
			require.NoError(t, os.WriteFile(filepath.Join(home, "ssh"), []byte(fmt.Sprintf("#!/bin/sh\nprintf started > %q\nexit 7\n", marker)), 0700))
			oldHost, oldLevel, oldTrace := flagHost, flagLogLevel, flagTrace
			oldChanged := runCmd.Flags().Lookup("log-level").Changed
			t.Cleanup(func() {
				flagHost, flagLogLevel, flagTrace = oldHost, oldLevel, oldTrace
				runCmd.Flags().Lookup("log-level").Changed = oldChanged
			})
			flagHost, flagLogLevel, flagTrace = "ssh://fixture.invalid", tc.level, tc.trace
			runCmd.Flags().Lookup("log-level").Changed = true
			err = runMain(runCmd, nil)
			require.Error(t, err)
			_, err = os.Stat(marker)
			require.True(t, os.IsNotExist(err), "invalid config launched SSH")
			_, err = os.Stat(filepath.Join(home, ".rdhpf"))
			require.True(t, os.IsNotExist(err), "invalid config acquired publisher")
		})
	}
}

func TestResolveRunConfig(t *testing.T) {
	for _, tc := range []struct {
		name, flag, env, labels string
		supplied, trace         bool
		want                    int
		bad                     string
	}{
		{name: "default", want: 0},
		{name: "flag", flag: "DEBUG", supplied: true, want: -4},
		{name: "environment override", flag: "error", supplied: true, env: "WaRn", want: 4},
		{name: "trace override", flag: "error", supplied: true, env: "warn", trace: true, want: -8},
		{name: "false trace", flag: "error", supplied: true, want: 8},
		{name: "empty environment", flag: "debug", supplied: true, env: "", want: -4},
		{name: "explicit empty", supplied: true, bad: "--log-level"},
		{name: "shadowed flag", flag: "private-payload", supplied: true, env: "info", trace: true, bad: "--log-level"},
		{name: "shadowed environment", env: "private-payload", trace: true, bad: "RDHPF_LOG_LEVEL"},
		{name: "environment whitespace", env: " info", bad: "RDHPF_LOG_LEVEL"},
		{name: "labels on", labels: "1", want: 0},
		{name: "labels off", labels: "0", want: 0},
		{name: "labels whitespace", labels: " 1", bad: "RDHPF_ENABLE_LABEL_PORTS"},
		{name: "labels invalid", labels: "true", bad: "RDHPF_ENABLE_LABEL_PORTS"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cfg, level, err := resolveRunConfig("ssh://fixture.invalid", tc.flag, tc.supplied, tc.trace, tc.env, tc.labels)
			if tc.bad != "" {
				require.ErrorContains(t, err, tc.bad)
				require.NotContains(t, err.Error(), "private-payload")
				require.Nil(t, cfg)
				return
			}
			require.NoError(t, err)
			require.Equal(t, tc.want, int(level))
			require.Equal(t, tc.labels == "1", cfg.EnableLabelPorts)
		})
	}
}
