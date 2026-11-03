// Package pipehold retains a copy of a short command's output descriptor in
// its test owner. It creates no descendant process and no second Wait owner.
package pipehold

import (
	"fmt"
	"net"
	"os"

	"golang.org/x/sys/unix"
)

// Send transfers stdout before the test command exits normally.
func Send(path string) error {
	conn, err := net.DialUnix("unix", nil, &net.UnixAddr{Name: path, Net: "unix"})
	if err != nil {
		return err
	}
	defer conn.Close()
	_, _, err = conn.WriteMsgUnix([]byte{1}, unix.UnixRights(int(os.Stdout.Fd())), nil)
	return err
}

// Receive obtains the output FD from one owned test command.
func Receive(listener *net.UnixListener) (*os.File, error) {
	conn, err := listener.AcceptUnix()
	if err != nil {
		return nil, err
	}
	defer conn.Close()
	data, control := make([]byte, 1), make([]byte, unix.CmsgSpace(4))
	_, n, _, _, err := conn.ReadMsgUnix(data, control)
	if err != nil {
		return nil, err
	}
	messages, err := unix.ParseSocketControlMessage(control[:n])
	if err != nil {
		return nil, err
	}
	if len(messages) != 1 {
		return nil, fmt.Errorf("expected one descriptor message")
	}
	fds, err := unix.ParseUnixRights(&messages[0])
	if err != nil {
		return nil, err
	}
	if len(fds) != 1 {
		for _, fd := range fds {
			_ = unix.Close(fd)
		}
		return nil, fmt.Errorf("expected one output descriptor")
	}
	unix.CloseOnExec(fds[0])
	return os.NewFile(uintptr(fds[0]), "owned-command-output"), nil
}
