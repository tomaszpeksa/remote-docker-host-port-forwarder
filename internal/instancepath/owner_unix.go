//go:build linux || darwin

package instancepath

import (
	"errors"
	"fmt"
	"os"

	"golang.org/x/sys/unix"
)

func openOwnerFile(root *os.Root) (*os.File, error) {
	// Reject known nonregular entries before opening them. Root.OpenFile follows
	// in-root symlinks, so retain kernel O_NOFOLLOW via a constant-leaf openat.
	if info, err := root.Lstat("owner.lock"); err == nil {
		if !info.Mode().IsRegular() {
			return nil, fmt.Errorf("owner.lock is not a regular file")
		}
	} else if !os.IsNotExist(err) {
		return nil, err
	}
	directory, err := root.Open(".")
	if err != nil {
		return nil, err
	}
	defer directory.Close()
	fd, err := unix.Openat(int(directory.Fd()), "owner.lock", unix.O_CREAT|unix.O_RDWR|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0600)
	if err != nil {
		return nil, err
	}
	if fd < 0 {
		return nil, fmt.Errorf("invalid owner.lock descriptor")
	}
	file := os.NewFile(uintptr(fd), "owner.lock")
	info, err := file.Stat()
	if err != nil || !info.Mode().IsRegular() {
		file.Close()
		return nil, fmt.Errorf("owner.lock is not a regular file: %v", err)
	}
	if err = unix.Flock(int(file.Fd()), unix.LOCK_EX|unix.LOCK_NB); err != nil {
		file.Close()
		if errors.Is(err, unix.EWOULDBLOCK) {
			return nil, fmt.Errorf("another rdhpf publisher already owns this status directory")
		}
		return nil, err
	}
	return file, nil
}
