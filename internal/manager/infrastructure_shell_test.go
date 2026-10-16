package manager

import (
	"context"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"github.com/tomaszpeksa/remote-docker-host-port-forwarder/internal/config"
)

// The owned fake SSH executes OpenSSH's remote command string through a real
// harmless /bin/sh; a second shell records whether expansion already occurred.
// The scripts are immutable and shared within this serial test only. Each
// case still has private outputs and environment, avoiding repeated executable
// setup without dropping either real shell layer or any operand case.
func inspectShellExecutables(t *testing.T) string {
	t.Helper()
	bin := t.TempDir()
	for name, script := range map[string]string{
		"ssh":    "#!/bin/sh\nprintf launched > \"$SHELL_LAUNCH\"\nfor last do :; done\nexec /bin/sh -c \"$last\"\n",
		"sh":     "#!/bin/sh\nif [ -e \"$SHELL_MARKER\" ]; then printf outer > \"$SHELL_PHASE\"; else printf absent > \"$SHELL_PHASE\"; fi\nexec /bin/sh \"$@\"\n",
		"docker": "#!/bin/sh\nn=0\nif [ -f \"$SHELL_ARGV.count\" ]; then read -r n < \"$SHELL_ARGV.count\"; fi\nn=$((n+1))\nprintf '%s\\n' \"$n\" > \"$SHELL_ARGV.count\"\nprintf '%s\\000' \"$@\" > \"$SHELL_ARGV.$n\"\nif [ \"$SHELL_REPLY\" = fallback ] && [ \"$n\" = 1 ]; then printf '{}'; elif [ \"$SHELL_REPLY\" = fallback ]; then printf '{\"rdhpf.forward.18081\":\"80\"}'; else printf '%s' \"$SHELL_REPLY\"; fi\n",
	} {
		require.NoError(t, os.WriteFile(filepath.Join(bin, name), []byte(script), 0700))
	}
	return bin
}

func inspectShellFixture(t *testing.T, response, bin string) (string, string, string) {
	t.Helper()
	dir := t.TempDir()
	marker, argv, launch := filepath.Join(dir, "marker"), filepath.Join(dir, "argv"), filepath.Join(dir, "launch")

	t.Setenv("PATH", bin)
	t.Setenv("HOME", dir)
	t.Setenv("SHELL_MARKER", marker)
	t.Setenv("SHELL_PHASE", filepath.Join(dir, "phase"))
	t.Setenv("SHELL_ARGV", argv)
	t.Setenv("SHELL_LAUNCH", launch)
	t.Setenv("SHELL_REPLY", response)
	return marker, argv, launch
}

func inspectShellOperands() []struct{ name, id string } {
	return []struct{ name, id string }{
		{"short", "abc123def456"}, {"full", strings.Repeat("a", 64)}, {"name", "service-api_1"},
		{"substitution", `abc123def456$( : > $SHELL_MARKER )`},
		{"backticks", "abc123def456` : > $SHELL_MARKER `"},
		{"separator", `abc123def456; : > "$SHELL_MARKER"; #`},
		{"quotes", `abc123def456'"quote`}, {"whitespace", "abc123def456 space\tline\nend"},
		{"leading_dash", "--format=unexpected"}, {"nul", "abc123def456\x00suffix"},
	}
}

func TestInfrastructureLabelShellOperands(t *testing.T) {
	bin := inspectShellExecutables(t)
	for _, tc := range inspectShellOperands() {
		t.Run(tc.name, func(t *testing.T) {
			marker, argv, launch := inspectShellFixture(t, "true", bin)
			m := &Manager{cfg: &config.Config{Host: "ssh://fixture.invalid:2222"}, logger: slog.New(slog.NewTextHandler(io.Discard, nil))}
			ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
			defer cancel()
			got := m.hasTestInfrastructureLabel(ctx, "/unused-control", tc.id)
			if tc.name == "nul" {
				require.False(t, got)
				require.NoFileExists(t, launch)
				return
			}
			require.True(t, got)
			require.NoError(t, ctx.Err())
			require.NoFileExists(t, marker, "container operand executed shell code")
			phase, err := os.ReadFile(os.Getenv("SHELL_PHASE"))
			require.NoError(t, err)
			require.Equal(t, "absent", string(phase))
			data, err := os.ReadFile(argv + ".1")
			require.NoError(t, err)
			require.Equal(t, []string{"inspect", "--format", `{{index .Config.Labels "rdhpf.test-infrastructure"}}`, "--", tc.id}, strings.Split(strings.TrimSuffix(string(data), "\x00"), "\x00"))
		})
	}
}
