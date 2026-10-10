package ssh

import (
	"os"
	"syscall"

	"golang.org/x/sys/unix"
)

func diagnosticIdentity(info os.FileInfo) socketDiagnosticIdentity {
	stat := info.Sys().(*syscall.Stat_t)
	return socketDiagnosticIdentity{Device: uint64(stat.Dev), Inode: uint64(stat.Ino), CTimeNS: stat.Ctimespec.Sec*1e9 + stat.Ctimespec.Nsec, MTimeNS: stat.Mtimespec.Sec*1e9 + stat.Mtimespec.Nsec, Links: uint64(stat.Nlink), Mode: info.Mode().String()}
}

func diagnosticOpenPin(path string) (*os.File, error) {
	fd, err := unix.Open(path, unix.O_EVTONLY|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0)
	if err != nil {
		return nil, err
	}
	return os.NewFile(uintptr(fd), path), nil
}
