package integration

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestIntegrationPreflightCommand(t *testing.T) {
	t.Run("argv", func(t *testing.T) {
		argv := filepath.Join(t.TempDir(), "argv")
		fakeIntegrationSSH(t, "printf '%s\\n' \"$@\" > '"+argv+"'")
		require.NoError(t, integrationPreflight(context.Background(), "ssh://user@fixture:2222", "/fixture/key"))
		got, err := os.ReadFile(argv)
		require.NoError(t, err)
		require.Equal(t, "-o\nStrictHostKeyChecking=no\n-o\nUserKnownHostsFile=/dev/null\n-i\n/fixture/key\n-p\n2222\nuser@fixture\ndocker\nps\n", string(got))
	})
	t.Run("deadline", func(t *testing.T) {
		fakeIntegrationSSH(t, "exec /bin/sleep 30")
		ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
		defer cancel()
		require.ErrorIs(t, integrationPreflight(ctx, "ssh://fixture:2222", ""), context.DeadlineExceeded)
	})
}
