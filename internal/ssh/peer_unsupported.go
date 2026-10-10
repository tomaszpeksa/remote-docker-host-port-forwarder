//go:build !linux && !darwin

package ssh

import (
	"fmt"
	"net"
	"runtime"
)

// Unsupported kernels cannot establish ownership; never fall back to a mux PID.
func unixPeerPID(_ *net.UnixConn) (int, error) {
	return 0, fmt.Errorf("SSH peer identity is unsupported on %s", runtime.GOOS)
}
