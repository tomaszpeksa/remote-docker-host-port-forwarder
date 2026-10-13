package unit

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestMakeTestEntrypoints(t *testing.T) {
	makePath, err := exec.LookPath("make")
	require.NoError(t, err)
	makefile, err := filepath.Abs("../../Makefile")
	require.NoError(t, err)
	guard, err := os.ReadFile("../../scripts/check-tools.sh")
	require.NoError(t, err)
	for _, tc := range []struct {
		target, host, wantHost, key, wantArg string
		fail, called                         bool
	}{
		{target: "test-integration", fail: true},
		{target: "test-integration", host: "ssh://fixture:2222", wantHost: "ssh://fixture:2222", wantArg: "./tests/integration/", fail: true, called: true},
		{target: "itest", wantHost: "ssh://testuser@localhost:2222", key: "/.itests/home/.ssh/id_ed25519", wantArg: "./tests/integration/", fail: true, called: true},
		{target: "itest-stability", wantHost: "ssh://testuser@localhost:2222", key: "/.itests/home/.ssh/id_ed25519", wantArg: "TestManager_(LongRunning|TunnelStability)", fail: true, called: true},
		{target: "test-unit", host: "ssh://ambient", called: true},
		{target: "coverage", host: "ssh://ambient", called: true},
	} {
		t.Run(tc.target+"/"+tc.host, func(t *testing.T) {
			root := t.TempDir()
			bin := filepath.Join(root, "bin")
			require.NoError(t, os.Mkdir(bin, 0700))
			require.NoError(t, os.Mkdir(filepath.Join(root, "scripts"), 0700))
			require.NoError(t, os.WriteFile(filepath.Join(root, "scripts", "check-tools.sh"), guard, 0700))
			require.NoError(t, os.WriteFile(filepath.Join(bin, "mise"), []byte("#!/bin/sh\n[ \"$1\" = which ] || exit 2\nprintf '%s/%s\\n' \"$MANAGED_TOOLS\" \"$2\"\n"), 0700))
			require.NoError(t, os.WriteFile(filepath.Join(bin, "gofmt"), []byte("#!/bin/sh\nexit 0\n"), 0700))
			require.NoError(t, os.WriteFile(filepath.Join(bin, "jq"), []byte("#!/bin/sh\nexit 0\n"), 0700))
			for _, name := range []string{"mkdir", "grep", "awk", "date", "dirname", "basename"} {
				path, err := exec.LookPath(name)
				require.NoError(t, err)
				require.NoError(t, os.Symlink(path, filepath.Join(bin, name)))
			}
			log := filepath.Join(root, "go.log")
			body := "#!/bin/sh\nprintf 'HOST=%s\\nKEY=%s\\nHOME=%s\\n' \"$SSH_TEST_HOST\" \"$SSH_TEST_KEY_PATH\" \"$HOME\" >> '" + log + "'\nprintf '%s\\n' \"$@\" >> '" + log + "'\n"
			if tc.fail {
				body += "exit 17\n"
			} else {
				body += "if [ \"$1\" = test ]; then test -z \"$SSH_TEST_HOST$SSH_TEST_KEY_PATH\" || exit 99; fi\nprintf 'total: (statements) 0.0%%\\n'\n"
			}
			require.NoError(t, os.WriteFile(filepath.Join(bin, "go"), []byte(body), 0700))
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			cmd := exec.CommandContext(ctx, makePath, "--no-print-directory", "-f", makefile, tc.target,
				"VERSION=test", "COMMIT=test", "BUILD_DATE=test", "GOOS=linux", "GOARCH=amd64", "SHELL=/bin/sh", "COVERAGE_DIR="+filepath.Join(root, "coverage"))
			cmd.Dir = root
			cmd.Env = []string{"PATH=" + bin, "MANAGED_TOOLS=" + bin, "HOME=" + root, "PWD=" + root, "SSH_TEST_HOST=" + tc.host, "SSH_TEST_KEY_PATH=ambient-key"}
			output, err := cmd.CombinedOutput()
			require.Equal(t, tc.fail, err != nil, "output: %s", output)
			data, readErr := os.ReadFile(log)
			if !tc.called {
				require.Contains(t, string(output), "SSH_TEST_HOST is required")
				require.True(t, os.IsNotExist(readErr), "Go must not run without required host")
				return
			}
			require.NoError(t, readErr, "output: %s", output)
			require.Contains(t, string(data), "HOST="+tc.wantHost+"\n")
			require.Contains(t, string(data), tc.wantArg)
			if tc.key != "" {
				require.Contains(t, string(data), "KEY="+root+tc.key+"\n")
				require.Contains(t, string(data), "HOME="+root+"/.itests/home\n")
			}
			require.Contains(t, string(data), "-count=1\n", "uncached test execution")
			if !tc.fail {
				require.Contains(t, string(data), "KEY=\n")
			}
			if tc.target == "coverage" {
				require.Contains(t, string(data), "-coverpkg=./cmd/...,./internal/...\n")
			}
		})
	}
}
