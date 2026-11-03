package socket

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"time"

	"github.com/tomaszpeksa/remote-docker-host-port-forwarder/internal/statefile"
)

// Client connects to a socket server to retrieve status.
type Client struct {
	socketPath string
	host       string
}

// NewClient creates a new socket client for the given host.
func NewClient(host string) (*Client, error) {
	socketPath, err := GetSocketPath(host)
	if err != nil {
		return nil, err
	}
	return &Client{socketPath: socketPath, host: host}, nil
}

// GetStatus retrieves a snapshot with the ordinary one-second socket budget.
func (c *Client) GetStatus() (*statefile.StateFile, error) {
	return c.GetStatusContext(context.Background())
}

// GetStatusContext bounds both dialing and decoding to one second and the
// caller's remaining budget. Cancellation closes the owned connection; its
// callback is joined before returning.
func (c *Client) GetStatusContext(ctx context.Context) (*statefile.StateFile, error) {
	ctx, cancel := context.WithTimeout(ctx, time.Second)
	defer cancel()
	conn, err := (&net.Dialer{}).DialContext(ctx, "unix", c.socketPath)
	if err != nil {
		return nil, fmt.Errorf("failed to connect to socket: %w", err)
	}
	defer conn.Close()
	deadline, _ := ctx.Deadline()
	if err := conn.SetDeadline(deadline); err != nil {
		return nil, err
	}
	done := make(chan struct{})
	stop := context.AfterFunc(ctx, func() { _ = conn.Close(); close(done) })
	defer func() {
		if !stop() {
			<-done
		}
	}()
	var snapshot statefile.StateFile
	err = json.NewDecoder(conn).Decode(&snapshot)
	if ctx.Err() != nil {
		return nil, ctx.Err()
	}
	if err != nil {
		return nil, fmt.Errorf("failed to decode snapshot: %w", err)
	}
	if err := snapshot.Validate(c.host); err != nil {
		return nil, err
	}
	return &snapshot, nil
}
