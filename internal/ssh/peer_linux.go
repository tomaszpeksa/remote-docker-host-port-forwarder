package ssh

import (
	"net"

	"golang.org/x/sys/unix"
)

// unixPeerPID obtains kernel identity, not the PID claimed in mux output.
func unixPeerPID(conn *net.UnixConn) (int, error) {
	raw, err := conn.SyscallConn()
	if err != nil {
		return 0, err
	}
	var pid int
	var socketErr error
	err = raw.Control(func(fd uintptr) {
		credentials, e := unix.GetsockoptUcred(int(fd), unix.SOL_SOCKET, unix.SO_PEERCRED)
		socketErr = e
		if e == nil {
			pid = int(credentials.Pid)
		}
	})
	if err != nil {
		return 0, err
	}
	return pid, socketErr
}
