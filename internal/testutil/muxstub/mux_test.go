package muxstub

import (
	"bytes"
	"encoding/binary"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// Literal wire fields keep the fixture check independent of the app codec.
func fixtureOpen() io.Reader {
	var wire bytes.Buffer
	for _, word := range []uint32{8, 1, 4, 46, 0x10000006, 1, 1, 9} {
		requireWrite(&wire, word)
	}
	wire.WriteString("127.0.0.1")
	for _, word := range []uint32{15432, 9} {
		requireWrite(&wire, word)
	}
	wire.WriteString("localhost")
	requireWrite(&wire, 5432)
	return &wire
}
func requireWrite(writer io.Writer, word uint32) {
	var b [4]byte
	binary.BigEndian.PutUint32(b[:], word)
	_, _ = writer.Write(b[:])
}
func fixtureRoot(t *testing.T) string {
	t.Helper()
	root := filepath.Join(t.TempDir(), "mux")
	require.NoError(t, os.Mkdir(root, 0700))
	info, err := os.Lstat(root)
	require.NoError(t, err)
	require.True(t, info.IsDir())
	require.Equal(t, os.FileMode(0700), info.Mode().Perm())
	t.Setenv("S12_MUX_ROOT", root)
	for _, name := range []string{"RECORD", "OPEN_FAIL", "CLOSE_FAIL", "OPEN_GATE", "LOST_REPLY"} {
		t.Setenv("S12_MUX_"+name, "")
	}
	return root
}
func TestServerRecordsWireOperationNotFilename(t *testing.T) {
	root := fixtureRoot(t)
	record := filepath.Join(root, "observed-control.log")
	t.Setenv("S12_MUX_RECORD", record)
	var server Server
	for _, operation := range []uint32{0x10000006, 0x10000007} {
		wire, err := io.ReadAll(fixtureOpen())
		require.NoError(t, err)
		binary.BigEndian.PutUint32(wire[16:20], operation)
		var response bytes.Buffer
		require.NoError(t, server.Serve(bytes.NewReader(wire), &response))
		require.Len(t, response.Bytes(), 24)
		require.EqualValues(t, 0x80000001, binary.BigEndian.Uint32(response.Bytes()[16:20]))
		require.EqualValues(t, 1, binary.BigEndian.Uint32(response.Bytes()[20:24]))
	}
	recorded, err := os.ReadFile(record)
	require.NoError(t, err)
	require.Equal(t, "OPEN 15432 5432\nCLOSE 15432 5432\n", string(recorded), "MUX_RECORD_MUST_REPORT_OPERATION_NOT_FILENAME")
}

func TestServerControlContainment(t *testing.T) {
	for _, symlink := range []bool{false, true} {
		t.Run(map[bool]string{false: "escape", true: "outward_symlink"}[symlink], func(t *testing.T) {
			root, outside := fixtureRoot(t), t.TempDir()
			path := filepath.Join(outside, "record")
			if symlink {
				require.NoError(t, os.Symlink(outside, filepath.Join(root, "link")))
				path = filepath.Join(root, "link", "record")
			}
			var server Server
			require.NoError(t, server.Serve(fixtureOpen(), io.Discard), "valid private-root exchange before containment control")
			t.Setenv("S12_MUX_RECORD", path)
			err := server.Serve(fixtureOpen(), io.Discard)
			_, statErr := os.Stat(filepath.Join(outside, "record"))
			t.Logf("owned outside record stat=%v", statErr)
			require.Error(t, err, "MUX_CONTROL_MUST_STAY_IN_ROOT")
			require.True(t, os.IsNotExist(statErr))
		})
	}
}
func TestServerControlReadContainment(t *testing.T) {
	for _, control := range []string{"OPEN_FAIL", "OPEN_GATE", "LOST_REPLY"} {
		for _, symlink := range []bool{false, true} {
			t.Run(control+map[bool]string{false: "/escape", true: "/outward_symlink"}[symlink], func(t *testing.T) {
				root, outside := fixtureRoot(t), t.TempDir()
				path := filepath.Join(outside, "control")
				if symlink {
					require.NoError(t, os.Symlink(outside, filepath.Join(root, "link")))
					path = filepath.Join(root, "link", "control")
				}
				var server Server
				require.NoError(t, server.Serve(fixtureOpen(), io.Discard), "valid private-root exchange before containment control")
				t.Setenv("S12_MUX_"+control, path)
				require.Error(t, server.Serve(fixtureOpen(), io.Discard), "fixture read controls cannot escape selected root")
			})
		}
	}
}
func TestServerRequiresExplicitPrivateRoot(t *testing.T) {
	root := fixtureRoot(t)
	t.Setenv("HOME", root)
	t.Setenv("S12_MUX_ROOT", "")
	var server Server
	require.ErrorContains(t, server.Serve(fixtureOpen(), io.Discard), "missing owned mux fixture root")
	t.Setenv("S12_MUX_ROOT", root)
	require.NoError(t, os.Chmod(root, 0755))
	t.Cleanup(func() { require.NoError(t, os.Chmod(root, 0700)) })
	require.ErrorContains(t, server.Serve(fixtureOpen(), io.Discard), "must be private")
}

func TestServerOversizedRejection(t *testing.T) {
	root := fixtureRoot(t)
	path := filepath.Join(root, "reject")
	var server Server
	require.NoError(t, server.Serve(fixtureOpen(), io.Discard), "valid private-root exchange before rejection bound control")
	require.NoError(t, os.WriteFile(path, []byte(strings.Repeat("x", 65525)), 0600))
	t.Setenv("S12_MUX_OPEN_FAIL", path)
	require.ErrorContains(t, server.Serve(fixtureOpen(), io.Discard), "fixture rejection exceeds frame bound", "MUX_REJECTION_MUST_BE_BOUNDED")
}
func TestInstallPrivateLauncher(t *testing.T) {
	root := Install(t)
	require.Equal(t, root, os.Getenv("S12_MUX_ROOT"))
	info, err := os.Lstat(root)
	require.NoError(t, err)
	require.True(t, info.IsDir())
	require.Equal(t, os.FileMode(0700), info.Mode().Perm())
	launcher := filepath.Join(root, "ssh")
	info, err = os.Lstat(launcher)
	require.NoError(t, err)
	require.True(t, info.Mode().IsRegular())
	require.Equal(t, os.FileMode(0700), info.Mode().Perm())
	script, err := os.ReadFile(launcher)
	require.NoError(t, err)
	executable, err := os.Executable()
	require.NoError(t, err)
	require.Contains(t, string(script), executable)
	require.Contains(t, string(script), "-test.run '^TestMuxProcess$' -- \"$@\"")
}
