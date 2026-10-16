package ssh

import (
	"bufio"
	"bytes"
	"context"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestMasterProcessPublishesReadySocket(t *testing.T) {
	for _, operation := range []string{"exit", "crash", "occupied"} {
		t.Run(operation, func(t *testing.T) {
			dir, err := os.MkdirTemp("/tmp", "s07-publish-")
			require.NoError(t, err)
			t.Cleanup(func() { require.NoError(t, os.RemoveAll(dir)) })
			path := filepath.Join(dir, "mux")
			gate, err := net.ListenUnix("unix", &net.UnixAddr{Name: filepath.Join(dir, "gate"), Net: "unix"})
			require.NoError(t, err)
			defer gate.Close()
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			cmd := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestMasterProcess$", "--", "-MN", "-o", "ControlPath="+path, "--", "owned-fixture.invalid")
			cmd.Env = append(os.Environ(), "S06_MASTER_PROCESS=1", "S07_PUBLISH_GATE="+gate.Addr().String())
			var output bytes.Buffer
			cmd.Stdout, cmd.Stderr = &output, &output
			require.NoError(t, cmd.Start())
			done := make(chan struct{})
			var waitErr error
			go func() { waitErr = cmd.Wait(); close(done) }()
			t.Cleanup(func() { cancel(); <-done })
			require.NoError(t, gate.SetDeadline(time.Now().Add(5*time.Second)))
			control, err := gate.Accept()
			require.NoError(t, err)
			defer control.Close()
			require.NoError(t, control.SetDeadline(time.Now().Add(5*time.Second)))
			reader := bufio.NewReader(control)
			line, err := reader.ReadString('\n')
			require.NoError(t, err)
			require.Equal(t, "listening\n", line)
			_, err = os.Lstat(path)
			require.True(t, os.IsNotExist(err), "final name must remain absent while publication is held")
			if operation == "occupied" {
				require.NoError(t, os.WriteFile(path, []byte("foreign occupant"), 0600))
			}
			_, err = control.Write([]byte{1})
			require.NoError(t, err)
			if operation == "occupied" {
				<-done
				require.Error(t, waitErr)
				require.Equal(t, 2, cmd.ProcessState.ExitCode())
				data, err := os.ReadFile(path)
				require.NoError(t, err)
				require.Equal(t, "foreign occupant", string(data))
			} else {
				line, err = reader.ReadString('\n')
				require.NoError(t, err)
				require.Equal(t, "published\n", line)
				_, err = os.Lstat(path + ".0123456789abcdef")
				require.True(t, os.IsNotExist(err), "publication must remove the temporary link")
				conn, err := net.Dial("unix", path)
				require.NoError(t, err, "published listener must already accept connections")
				require.NoError(t, conn.SetDeadline(time.Now().Add(5*time.Second)))
				_, err = fmt.Fprintln(conn, "check")
				require.NoError(t, err)
				body, err := io.ReadAll(conn)
				require.NoError(t, err)
				require.NoError(t, conn.Close())
				require.Equal(t, fmt.Sprintf("Master running (pid=%d)\n", cmd.Process.Pid), string(body))
				conn, err = net.Dial("unix", path)
				require.NoError(t, err)
				require.NoError(t, conn.SetDeadline(time.Now().Add(5*time.Second)))
				_, err = fmt.Fprintln(conn, operation)
				require.NoError(t, err)
				_, err = io.ReadAll(conn)
				require.NoError(t, err)
				require.NoError(t, conn.Close())
				if operation == "crash" {
					_, err = os.Lstat(path)
					require.True(t, os.IsNotExist(err), "crash must unlink before EOF acknowledgement")
				}
				<-done
				if operation == "exit" {
					require.NoError(t, waitErr, "%s", output.String())
				} else {
					require.Equal(t, 9, cmd.ProcessState.ExitCode(), "%s", output.String())
				}
				_, err = os.Lstat(path)
				require.True(t, os.IsNotExist(err), "helper shutdown must remove the final link")
			}
			_, err = os.Lstat(path + ".0123456789abcdef")
			require.True(t, os.IsNotExist(err), "temporary link must not remain after publication or refusal")
		})
	}
}
