package socket

import (
	"encoding/json"
	"fmt"
	"net"

	"github.com/tomaszpeksa/remote-docker-host-port-forwarder/internal/statefile"
)

// Client connects to a socket server to retrieve status
type Client struct {
	socketPath string
}

// NewClient creates a new socket client for the given host
func NewClient(host string) (*Client, error) {
	socketPath, err := GetSocketPath(host)
	if err != nil {
		return nil, err
	}

	return &Client{
		socketPath: socketPath,
	}, nil
}

// GetStatus connects to the socket and retrieves status snapshot
func (c *Client) GetStatus() (*statefile.StateFile, error) {
	conn, err := net.Dial("unix", c.socketPath)
	if err != nil {
		return nil, fmt.Errorf("failed to connect to socket: %w", err)
	}
	defer func() {
		_ = conn.Close()
	}()

	var snapshot statefile.StateFile
	if err := json.NewDecoder(conn).Decode(&snapshot); err != nil {
		return nil, fmt.Errorf("failed to decode snapshot: %w", err)
	}

	return &snapshot, nil
}
