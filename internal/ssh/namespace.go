package ssh

import (
	"context"
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"regexp"
	"syscall"
)

// Ownership relies on cooperating processes leaving this directory and its
// ancestors alone. Metadata/peer checks detect anomalies, not atomic unlink.
type masterNamespace struct {
	dir      string
	path     string
	identity os.FileInfo
	owned    bool
}

func newMasterNamespace() (*masterNamespace, error) {
	dir, err := os.MkdirTemp("/tmp", "rdhpf-")
	if err != nil {
		return nil, err
	}
	n := &masterNamespace{dir: dir, path: filepath.Join(dir, "mux"), owned: true}
	// OpenSSH binds a temporary name with a dot and 16 random characters.
	// Include that suffix and NUL in Darwin's smaller sockaddr_un budget.
	if len(n.path)+17+1 > 104 {
		return nil, errors.Join(fmt.Errorf("SSH control path too long"), os.Remove(dir))
	}
	n.identity, err = os.Lstat(dir)
	if err == nil && n.identity.Mode().Perm() != 0700 {
		err = &os.PathError{Op: "private namespace permissions", Path: dir, Err: os.ErrPermission}
	}
	if err != nil {
		return nil, errors.Join(err, os.Remove(dir))
	}
	return n, nil
}

func (n *masterNamespace) verify() error {
	info, err := os.Lstat(n.dir)
	if err != nil {
		return err
	}
	if !n.owned || !info.IsDir() || !os.SameFile(n.identity, info) || info.Mode().Perm() != 0700 {
		return fmt.Errorf("SSH namespace ownership lost: %s", n.dir)
	}
	return nil
}

func (n *masterNamespace) open() error {
	if n.owned {
		return n.verify()
	}
	// A previous Close released ownership. Never adopt even an empty directory.
	if err := os.Mkdir(n.dir, 0700); err != nil {
		return fmt.Errorf("exclusive SSH namespace recreation: %w", err)
	}
	info, err := os.Lstat(n.dir)
	if err == nil && info.Mode().Perm() != 0700 {
		err = &os.PathError{Op: "private namespace permissions", Path: n.dir, Err: os.ErrPermission}
	}
	if err != nil {
		return errors.Join(err, os.Remove(n.dir))
	}
	n.identity = info
	n.owned = true
	return nil
}

var temporaryMuxName = regexp.MustCompile(`^mux\.[a-zA-Z0-9]{16}$`)

// reclaim runs only after the retained child was reaped. This also covers
// startup before association and temporary-only or hard-linked mux artifacts.
func (n *masterNamespace) reclaim() error {
	if err := n.verify(); err != nil {
		return err
	}
	entries, err := os.ReadDir(n.dir)
	if err != nil {
		return err
	}
	for _, entry := range entries {
		if (entry.Name() != "mux" && !temporaryMuxName.MatchString(entry.Name())) || entry.Type()&os.ModeSocket == 0 {
			return fmt.Errorf("unexpected SSH namespace entry: %s", entry.Name())
		}
		ctx, cancel := context.WithTimeout(context.Background(), masterCheckTimeout)
		conn, dialErr := (&net.Dialer{}).DialContext(ctx, "unix", filepath.Join(n.dir, entry.Name()))
		cancel()
		if dialErr == nil {
			pid, peerErr := unixPeerPID(conn.(*net.UnixConn))
			return errors.Join(fmt.Errorf("live foreign SSH endpoint after child retirement (peer %d, identity error %v)", pid, peerErr), conn.Close())
		}
		if !errors.Is(dialErr, syscall.ECONNREFUSED) && !os.IsNotExist(dialErr) {
			return fmt.Errorf("uncertain SSH namespace entry: %w", dialErr)
		}
	}
	// Exclusive cooperative ownership, NOT the preceding checks, authorizes
	// these removals. Arbitrary external substitution is outside that contract.
	for _, entry := range entries {
		if err := os.Remove(filepath.Join(n.dir, entry.Name())); err != nil && !os.IsNotExist(err) {
			return err
		}
	}
	return nil
}

func (n *masterNamespace) close() error {
	if !n.owned {
		return nil
	}
	if err := n.verify(); err != nil {
		return err
	}
	// Never recursively delete, and never delete entries without a reaped child.
	if err := os.Remove(n.dir); err != nil {
		return err
	}
	n.owned = false
	return nil
}
