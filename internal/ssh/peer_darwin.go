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
	err = raw.Control(func(fd uintptr) { pid, socketErr = unix.GetsockoptInt(int(fd), unix.SOL_LOCAL, unix.LOCAL_PEERPID) })
	if err != nil {
		return 0, err
	}
	return pid, socketErr
}
