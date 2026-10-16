//go:build linux || darwin

package instancepath

import (
	"errors"
	"fmt"
	"os"

	"golang.org/x/sys/unix"
)

// Select the actual directory once, following permitted directory aliases.
// The caller closes this anchor after acquiring (or failing to acquire) the lock.
func openOwnerDirectory(path string) (*os.File, error) {
	var fd int
	var err error
	for {
		fd, err = unix.Open(path, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_CLOEXEC, 0)
		if !errors.Is(err, unix.EINTR) {
			break
		}
	}
	if err != nil {
		return nil, &os.PathError{Op: "open", Path: path, Err: err}
	}
	if fd < 0 {
		return nil, fmt.Errorf("invalid publisher directory descriptor")
	}
	return os.NewFile(uintptr(fd), path), nil
}

func openOwnerFile(directory *os.File) (*os.File, error) {
	// Both operations use the selected directory FD and a constant leaf. Never
	// re-resolve directory.Name(): a renamed/rebound path is not this anchor.
	// Reject known nonregular entries before opening, then verify the opened FD.
	var stat unix.Stat_t
	var err error
	for {
		err = unix.Fstatat(int(directory.Fd()), "owner.lock", &stat, unix.AT_SYMLINK_NOFOLLOW)
		if !errors.Is(err, unix.EINTR) {
			break
		}
	}
	if err == nil {
		if stat.Mode&unix.S_IFMT != unix.S_IFREG {
			return nil, fmt.Errorf("owner.lock is not a regular file")
		}
	} else if !errors.Is(err, unix.ENOENT) {
		return nil, &os.PathError{Op: "statat", Path: "owner.lock", Err: err}
	}
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
		return nil, errors.Join(fmt.Errorf("owner.lock is not a regular file: %v", err), file.Close())
	}
	if err = unix.Flock(int(file.Fd()), unix.LOCK_EX|unix.LOCK_NB); err != nil {
		if errors.Is(err, unix.EWOULDBLOCK) {
			err = fmt.Errorf("another rdhpf publisher already owns this status directory")
		}
		return nil, errors.Join(err, file.Close())
	}
	return file, nil
}
