package socket

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net"
	"os"
	"time"

	"github.com/tomaszpeksa/remote-docker-host-port-forwarder/internal/state"
	"github.com/tomaszpeksa/remote-docker-host-port-forwarder/internal/statefile"
)

// Server serves status information over a Unix socket
type Server struct {
	listener   net.Listener
	socketPath string
	state      *state.State
	history    *state.History
	host       string
	pid        int
	startedAt  time.Time
	logger     *slog.Logger
}

// NewServer creates a new socket server for the given host
func NewServer(host string, stateManager *state.State, history *state.History, startedAt time.Time, logger *slog.Logger) (*Server, error) {
	socketPath, err := GetSocketPath(host)
	if err != nil {
		return nil, err
	}

	// Clean up any stale socket
	os.Remove(socketPath)

	listener, err := net.Listen("unix", socketPath)
	if err != nil {
		return nil, fmt.Errorf("failed to create socket: %w", err)
	}

	return &Server{
		listener:   listener,
		socketPath: socketPath,
		state:      stateManager,
		history:    history,
		host:       host,
		pid:        os.Getpid(),
		startedAt:  startedAt,
		logger:     logger,
	}, nil
}

// Start begins accepting connections on the socket
func (s *Server) Start(ctx context.Context) error {
	s.logger.Debug("socket server listening", "path", s.socketPath)

	// Close listener when context is cancelled
	go func() {
		<-ctx.Done()
		s.listener.Close()
	}()

	for {
		conn, err := s.listener.Accept()
		if err != nil {
			// Check if we're shutting down
			select {
			case <-ctx.Done():
				return nil
			default:
				s.logger.Warn("failed to accept connection", "error", err)
				continue
			}
		}

		// Handle connection in goroutine
		go s.handleConnection(conn)
	}
}

// handleConnection handles a single client connection
func (s *Server) handleConnection(conn net.Conn) {
	defer conn.Close()

	// Get current state
	forwards := s.state.GetActual()
	history := s.history.GetAll()

	// Convert to snapshot format
	forwardSnapshots := make([]statefile.ForwardSnapshot, len(forwards))
	for i, f := range forwards {
		forwardSnapshots[i] = statefile.FromForwardState(f)
	}

	historySnapshots := make([]statefile.HistorySnapshot, len(history))
	for i, h := range history {
		historySnapshots[i] = statefile.FromHistoryEntry(h)
	}

	// Create snapshot
	snapshot := statefile.StateFile{
		Version:   statefile.CurrentVersion,
		Host:      s.host,
		PID:       s.pid,
		StartedAt: s.startedAt,
		UpdatedAt: time.Now(),
		Forwards:  forwardSnapshots,
		History:   historySnapshots,
	}

	// Write JSON and close
	encoder := json.NewEncoder(conn)
	encoder.SetIndent("", "  ")
	if err := encoder.Encode(snapshot); err != nil {
		s.logger.Warn("failed to write snapshot to socket", "error", err)
	}
}

// Close stops the server and removes the socket file
func (s *Server) Close() error {
	if err := s.listener.Close(); err != nil {
		return err
	}

	if err := os.Remove(s.socketPath); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("failed to remove socket: %w", err)
	}

	return nil
}