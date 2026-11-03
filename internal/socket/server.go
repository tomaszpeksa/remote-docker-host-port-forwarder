package socket

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"os"
	"sync"
	"time"

	"github.com/tomaszpeksa/remote-docker-host-port-forwarder/internal/state"
	"github.com/tomaszpeksa/remote-docker-host-port-forwarder/internal/statefile"
)

// Server serves status information over a Unix socket
type Server struct {
	listener    net.Listener
	socketPath  string
	state       *state.State
	history     *state.History
	host        string
	pid         int
	startedAt   time.Time
	logger      *slog.Logger
	closeOnce   sync.Once
	closeErr    error
	stopOnce    sync.Once
	stopErr     error
	mu          sync.Mutex
	stopping    bool
	started     bool
	startDone   chan struct{}
	connections map[net.Conn]struct{}
	handlers    sync.WaitGroup
}

// NewServer resolves the legacy address wrapper. The caller must own publisher
// admission (or an isolated test namespace) through Start completion and Close.
func NewServer(host string, stateManager *state.State, history *state.History, startedAt time.Time, logger *slog.Logger) (*Server, error) {
	socketPath, err := GetSocketPath(host)
	if err != nil {
		return nil, err
	}

	return NewServerAtPath(host, socketPath, stateManager, history, startedAt, logger)
}

// NewServerAtPath requires publisher admission for this frozen path through
// Start completion and Close. Only Close owns pathname removal after binding.
func NewServerAtPath(host, socketPath string, stateManager *state.State, history *state.History, startedAt time.Time, logger *slog.Logger) (*Server, error) {
	if info, err := os.Lstat(socketPath); err == nil {
		if info.Mode()&os.ModeSocket == 0 {
			return nil, fmt.Errorf("unexpected status socket entry")
		}
		if err := os.Remove(socketPath); err != nil {
			return nil, err
		}
	} else if !os.IsNotExist(err) {
		return nil, err
	}
	listener, err := net.Listen("unix", socketPath)
	if err != nil {
		return nil, fmt.Errorf("failed to create socket: %w", err)
	}

	listener.(*net.UnixListener).SetUnlinkOnClose(false)
	return &Server{
		listener:    listener,
		socketPath:  socketPath,
		state:       stateManager,
		history:     history,
		host:        host,
		pid:         os.Getpid(),
		startedAt:   startedAt,
		logger:      logger,
		connections: make(map[net.Conn]struct{}),
		startDone:   make(chan struct{}),
	}, nil
}

// Start begins accepting connections on the socket
func (s *Server) Start(ctx context.Context) error {
	s.mu.Lock()
	if s.stopping {
		s.mu.Unlock()
		return nil
	}
	if s.started {
		s.mu.Unlock()
		return fmt.Errorf("socket server already started")
	}
	s.started = true
	s.mu.Unlock()
	defer close(s.startDone)
	s.logger.Debug("socket server listening", "path", s.socketPath)

	stop, stopped := make(chan struct{}), make(chan struct{})
	go func() {
		defer close(stopped)
		select {
		case <-ctx.Done():
			s.stop()
		case <-stop:
		}
	}()
	defer func() {
		s.stop()
		close(stop)
		<-stopped
		s.handlers.Wait()
	}()

	for {
		conn, err := s.listener.Accept()
		if err != nil {
			if errors.Is(err, net.ErrClosed) {
				return nil
			}
			// Check if we're shutting down
			select {
			case <-ctx.Done():
				return nil
			default:
				s.logger.Warn("failed to accept connection", "error", err)
				continue
			}
		}

		s.mu.Lock()
		if s.stopping {
			s.mu.Unlock()
			s.recordCloseError(conn.Close())
			continue
		}
		s.connections[conn] = struct{}{}
		s.handlers.Add(1)
		s.mu.Unlock()
		go func() {
			defer s.handlers.Done()
			defer func() {
				s.mu.Lock()
				delete(s.connections, conn)
				s.mu.Unlock()
			}()
			s.handleConnection(conn)
		}()
	}
}

// handleConnection handles a single client connection
func (s *Server) handleConnection(conn net.Conn) {
	defer func() { s.recordCloseError(conn.Close()) }()

	// Get current state
	forwards := s.state.GetActual()
	history := s.history.GetAll()

	snapshot := statefile.NewSnapshot(s.host, s.pid, s.startedAt, time.Time{}, forwards, history)
	// Observe the update time after conversion, before publishing the snapshot.
	snapshot.UpdatedAt = time.Now()

	// Write JSON and close
	encoder := json.NewEncoder(conn)
	encoder.SetIndent("", "  ")
	if err := encoder.Encode(snapshot); err != nil {
		s.logger.Warn("failed to write snapshot to socket", "error", err)
	}
}

func (s *Server) recordCloseError(err error) {
	if err != nil && !errors.Is(err, net.ErrClosed) {
		s.mu.Lock()
		s.stopErr = errors.Join(s.stopErr, err)
		s.mu.Unlock()
	}
}

// stop fences registration before any Wait and interrupts accepted socket I/O.
func (s *Server) stop() {
	s.stopOnce.Do(func() {
		s.mu.Lock()
		s.stopping = true
		if err := s.listener.Close(); err != nil && !errors.Is(err, net.ErrClosed) {
			s.stopErr = errors.Join(s.stopErr, err)
		}
		for conn := range s.connections {
			if err := conn.Close(); err != nil && !errors.Is(err, net.ErrClosed) {
				s.stopErr = errors.Join(s.stopErr, err)
			}
		}
		s.mu.Unlock()
	})
}

// Close joins accepted handlers before removing the socket file.
func (s *Server) Close() error {
	s.closeOnce.Do(func() {
		s.stop()
		s.mu.Lock()
		started := s.started
		s.mu.Unlock()
		if started {
			<-s.startDone
		}
		s.handlers.Wait()
		if s.stopErr != nil {
			s.closeErr = s.stopErr
			return
		}
		if err := os.Remove(s.socketPath); err != nil && !os.IsNotExist(err) {
			s.closeErr = fmt.Errorf("failed to remove socket: %w", err)
		}
	})
	return s.closeErr
}
