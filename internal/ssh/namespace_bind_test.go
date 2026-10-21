//go:build linux || darwin

package ssh

import (
	"io"
	"log/slog"
	"net"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
	"golang.org/x/sys/unix"
)

// This exercises the generated pathname and kernel, not a copied path builder.
// OpenSSH muxserver_listen (mux.c) appends a dot and 16 random characters before
// binding and linking its final address. Both addresses must fit, including NUL.
func TestMasterPrivatePathNativeBind(t *testing.T) {
	t.Setenv("HOME", filepath.Join(t.TempDir(), strings.Repeat("長", 80)))
	t.Setenv("TMPDIR", filepath.Join(t.TempDir(), strings.Repeat("tmp", 80)))
	for _, host := range []string{"ssh://same.invalid", "ssh://same.invalid", "ssh://user@other.invalid:2222"} {
		m, err := NewMaster(host, slog.New(slog.NewTextHandler(io.Discard, nil)))
		require.NoError(t, err, "ISOLATION_PRIVATE_PATH_CONSTRUCTION")
		t.Cleanup(func() { require.NoError(t, m.Close()) })
		path := m.ControlPath()
		info, err := os.Stat(filepath.Dir(path))
		require.NoError(t, err)
		require.Equal(t, os.FileMode(0700), info.Mode().Perm())
		require.True(t, strings.HasPrefix(path, "/tmp/rdhpf-"), "ISOLATION_PRIVATE_PATH_ROOT")
		require.LessOrEqual(t, len(path)+18, 104, "ISOLATION_PRIVATE_PATH_BUDGET")
		for _, address := range []string{path, path + ".0123456789abcdef"} {
			require.Less(t, len(address), len(unix.RawSockaddrUnix{}.Path), "ISOLATION_NATIVE_SOCKADDR_BUDGET")
			listener, err := net.ListenUnix("unix", &net.UnixAddr{Name: address, Net: "unix"})
			require.NoError(t, err, "ISOLATION_NATIVE_PRIVATE_BIND")
			require.NoError(t, listener.Close())
			_, err = os.Lstat(address)
			require.True(t, os.IsNotExist(err))
			t.Logf("native=%s/%s sockaddr_path_bytes=%d address_bytes=%d permissions=%04o bound_and_removed=%s", runtime.GOOS, runtime.GOARCH, len(unix.RawSockaddrUnix{}.Path), len(address), info.Mode().Perm(), address)
		}
		require.NoError(t, m.Close())
	}
}
