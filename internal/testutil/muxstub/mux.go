// Package muxstub supplies a local, non-network OpenSSH stand-in for owned
// process tests. It never invokes SSH or forwards application traffic.
package muxstub

import (
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

type Server struct{ tuples map[string]bool }

// Serve handles the binary forwarding exchange after the fixture has selected
// it, independently of the production codec. Controls are private test files.
func (s *Server) Serve(reader io.Reader, writer io.Writer) (err error) {
	// The test parent explicitly selects its private fixture root. Do not fall
	// back to HOME: some Manager tests use a different publication HOME.
	directory := os.Getenv("S12_MUX_ROOT")
	if directory == "" || !filepath.IsAbs(directory) {
		return fmt.Errorf("missing owned mux fixture root")
	}
	root, err := os.OpenRoot(directory)
	if err != nil {
		return err
	}
	defer func() { err = errors.Join(err, root.Close()) }()
	info, err := root.Stat(".")
	if err != nil {
		return err
	}
	if !info.IsDir() || info.Mode().Perm() != 0700 {
		return fmt.Errorf("mux fixture root must be private")
	}
	controlName := func(path string) (string, error) {
		name, err := filepath.Rel(directory, path)
		if err != nil {
			return "", err
		}
		if !filepath.IsLocal(name) {
			return "", fmt.Errorf("mux control outside fixture root")
		}
		return name, nil
	}
	read := func() ([]byte, error) {
		var header [4]byte
		if _, err := io.ReadFull(reader, header[:]); err != nil {
			return nil, err
		}
		size := binary.BigEndian.Uint32(header[:])
		if size > 65536 || size < 4 {
			return nil, fmt.Errorf("invalid fixture frame")
		}
		body := make([]byte, int(size))
		_, err := io.ReadFull(reader, body)
		return body, err
	}
	write := func(body []byte) error {
		size := len(body)
		if size < 4 || size > 65536 {
			return fmt.Errorf("invalid outbound fixture frame")
		}
		frame := make([]byte, 4+size)
		binary.BigEndian.PutUint32(frame, uint32(size))
		copy(frame[4:], body)
		_, err := writer.Write(frame)
		return err
	}
	hello, err := read()
	if err != nil {
		return err
	}
	if len(hello) != 8 || binary.BigEndian.Uint32(hello) != 1 || binary.BigEndian.Uint32(hello[4:]) != 4 {
		return fmt.Errorf("invalid fixture hello")
	}
	if err = write(hello); err != nil {
		return err
	}
	request, err := read()
	if err != nil {
		return err
	}
	if len(request) < 12 {
		return fmt.Errorf("invalid fixture request")
	}
	operation, id, kind := binary.BigEndian.Uint32(request), binary.BigEndian.Uint32(request[4:]), binary.BigEndian.Uint32(request[8:])
	rest := request[12:]
	takeString := func() (string, error) {
		if len(rest) < 4 {
			return "", io.ErrUnexpectedEOF
		}
		size := binary.BigEndian.Uint32(rest)
		if size > 65536 {
			return "", io.ErrUnexpectedEOF
		}
		n := int(size)
		rest = rest[4:]
		if n > len(rest) {
			return "", io.ErrUnexpectedEOF
		}
		value := string(rest[:n])
		rest = rest[n:]
		return value, nil
	}
	bind, err := takeString()
	if err != nil {
		return err
	}
	if len(rest) < 4 {
		return io.ErrUnexpectedEOF
	}
	local := binary.BigEndian.Uint32(rest)
	rest = rest[4:]
	target, err := takeString()
	if err != nil {
		return err
	}
	if len(rest) != 4 {
		return io.ErrUnexpectedEOF
	}
	remote := binary.BigEndian.Uint32(rest)
	if kind != 1 || bind != "127.0.0.1" || target != "localhost" || local == 0 || remote == 0 || local > 65535 || remote > 65535 {
		return fmt.Errorf("unexpected fixture tuple")
	}
	name := "OPEN"
	if operation == 0x10000007 {
		name = "CLOSE"
	} else if operation != 0x10000006 {
		return fmt.Errorf("unexpected fixture operation")
	}
	if path := os.Getenv("S12_MUX_RECORD"); path != "" {
		recordName, err := controlName(path)
		if err != nil {
			return err
		}
		file, err := root.OpenFile(recordName, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0600)
		if err != nil {
			return err
		}
		_, err = fmt.Fprintf(file, "%s %d %d\n", name, local, remote)
		closeErr := file.Close()
		if err != nil {
			return err
		}
		if closeErr != nil {
			return closeErr
		}
	}
	if gate := os.Getenv("S12_MUX_OPEN_GATE"); name == "OPEN" && gate != "" {
		gate, err := controlName(gate)
		if err != nil {
			return err
		}
		if _, err := root.Stat(gate + ".armed"); err == nil {
			if err := root.WriteFile(gate+".entered", nil, 0600); err != nil {
				return err
			}
			for {
				if _, err := root.Stat(gate + ".release"); err == nil {
					break
				} else if !os.IsNotExist(err) {
					return err
				}
				time.Sleep(time.Millisecond)
			}
		} else if !os.IsNotExist(err) {
			return err
		}
	}
	reply := make([]byte, 8)
	binary.BigEndian.PutUint32(reply, 0x80000001)
	binary.BigEndian.PutUint32(reply[4:], id)
	reject := ""
	if path := os.Getenv("S12_MUX_" + name + "_FAIL"); path != "" {
		name, err := controlName(path)
		if err != nil {
			return err
		}
		file, err := root.Open(name)
		if err == nil {
			b, readErr := io.ReadAll(io.LimitReader(file, 65525))
			if err := errors.Join(readErr, file.Close()); err != nil {
				return err
			}
			if len(b) > 65524 {
				return fmt.Errorf("fixture rejection exceeds frame bound")
			}
			reject = strings.TrimSpace(string(b))
			if reject == "" {
				reject = "injected forwarding rejection"
			}
		} else if !os.IsNotExist(err) {
			return err
		}
	}
	if s.tuples == nil {
		s.tuples = make(map[string]bool)
	}
	key := strconv.FormatUint(uint64(local), 10) + ":" + strconv.FormatUint(uint64(remote), 10)
	if reject == "" {
		if name == "OPEN" {
			s.tuples[key] = true
		} else if !s.tuples[key] {
			reject = "port not forwarded"
		} else {
			delete(s.tuples, key)
		}
	}
	if reject != "" {
		binary.BigEndian.PutUint32(reply, 0x80000003)
		n := make([]byte, 4)
		size := len(reject)
		if size < 1 || size > 65524 {
			return fmt.Errorf("invalid fixture rejection length")
		}
		binary.BigEndian.PutUint32(n, uint32(size))
		reply = append(reply, n...)
		reply = append(reply, reject...)
	}
	if path := os.Getenv("S12_MUX_LOST_REPLY"); path != "" {
		name, err := controlName(path)
		if err != nil {
			return err
		}
		if _, err := root.Stat(name); err == nil {
			return nil
		} else if !os.IsNotExist(err) {
			return err
		}
	}
	return write(reply)
}
