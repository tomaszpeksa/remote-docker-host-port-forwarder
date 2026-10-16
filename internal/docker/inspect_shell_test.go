package docker

import (
	"context"
	"github.com/stretchr/testify/require"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// The owned fake SSH executes OpenSSH's remote command string through a real
// harmless /bin/sh; a second shell records whether expansion already occurred.
func inspectShellFixture(t *testing.T, response string) (string, string, string) {
	t.Helper()
	dir := t.TempDir()
	marker, argv, launch := filepath.Join(dir, "marker"), filepath.Join(dir, "argv"), filepath.Join(dir, "launch")
	for name, script := range map[string]string{
		"ssh":    "#!/bin/sh\nprintf launched > \"$SHELL_LAUNCH\"\nfor last do :; done\nexec /bin/sh -c \"$last\"\n",
		"sh":     "#!/bin/sh\nif [ -e \"$SHELL_MARKER\" ]; then printf outer > \"$SHELL_PHASE\"; else printf absent > \"$SHELL_PHASE\"; fi\nexec /bin/sh \"$@\"\n",
		"docker": "#!/bin/sh\nn=0\nif [ -f \"$SHELL_ARGV.count\" ]; then read -r n < \"$SHELL_ARGV.count\"; fi\nn=$((n+1))\nprintf '%s\\n' \"$n\" > \"$SHELL_ARGV.count\"\nprintf '%s\\000' \"$@\" > \"$SHELL_ARGV.$n\"\nif [ \"$SHELL_REPLY\" = fallback ] && [ \"$n\" = 1 ]; then printf '{}'; elif [ \"$SHELL_REPLY\" = fallback ]; then printf '{\"rdhpf.forward.18081\":\"80\"}'; else printf '%s' \"$SHELL_REPLY\"; fi\n",
	} {
		require.NoError(t, os.WriteFile(filepath.Join(dir, name), []byte(script), 0700))
	}
	t.Setenv("PATH", dir)
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

func TestInspectShellOperands(t *testing.T) {
	for _, method := range []struct{ name, reply, format string }{
		{"InspectPorts", `{"80/tcp":[{"HostPort":"18081"}]}`, "{{json .HostConfig.PortBindings}}"},
		{"inspectPortsFromLabels", `{"rdhpf.forward.18081":"80"}`, "{{json .Config.Labels}}"},
		{"InspectPortsLabelFallback", "fallback", "{{json .HostConfig.PortBindings}}"},
	} {
		for _, tc := range inspectShellOperands() {
			t.Run(method.name+"/"+tc.name, func(t *testing.T) {
				marker, argv, launch := inspectShellFixture(t, method.reply)
				t.Setenv("RDHPF_ENABLE_LABEL_PORTS", "1")
				call := InspectPorts
				if method.name == "inspectPortsFromLabels" {
					call = inspectPortsFromLabels
				}
				ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
				defer cancel()
				ports, err := call(ctx, "ssh://fixture.invalid:2222", "/unused-control", tc.id)
				if tc.name == "nul" {
					require.ErrorContains(t, err, "NUL")
					require.NoFileExists(t, launch)
					return
				}
				require.NoError(t, err)
				require.NoError(t, ctx.Err())
				require.Equal(t, []int{18081}, ports)
				require.NoFileExists(t, marker, "container operand executed shell code")
				phase, err := os.ReadFile(os.Getenv("SHELL_PHASE"))
				require.NoError(t, err)
				require.Equal(t, "absent", string(phase))
				data, err := os.ReadFile(argv + ".1")
				require.NoError(t, err)
				require.Equal(t, []string{"inspect", "--format", method.format, "--", tc.id}, strings.Split(strings.TrimSuffix(string(data), "\x00"), "\x00"))
				if method.reply == "fallback" {
					data, err = os.ReadFile(argv + ".2")
					require.NoError(t, err)
					require.Equal(t, []string{"inspect", "--format", "{{json .Config.Labels}}", "--", tc.id}, strings.Split(strings.TrimSuffix(string(data), "\x00"), "\x00"))
				}
			})
		}
	}
}
