package util

import (
	"context"
	"fmt"
	"net"
	"time"
)

// ProbePort attempts to connect to a TCP port on localhost to verify it's listening.
//
// This is used to validate that an SSH port forward is truly active and accepting connections.
// It performs a TCP dial with a 1 second timeout, and immediately closes the connection if successful.
// It does not send or receive any data - it only checks if the port accepts connections.
//
// Parameters:
//   - ctx: Context for cancellation (additional to the built-in 1s timeout)
//   - port: Port number to probe (will connect to 127.0.0.1:port)
//
// Returns:
//   - nil if connection succeeds (port is listening)
//   - error if port is unreachable or connection fails
//
// Example usage:
//
//	ctx := context.Background()
//	if err := ProbePort(ctx, 5432); err != nil {
//	    log.Printf("Port 5432 is not listening: %v", err)
//	} else {
//	    log.Printf("Port 5432 is active")
//	}
func ProbePort(ctx context.Context, port int) error {
	// Create a dialer with 1 second timeout
	dialer := &net.Dialer{
		Timeout: 1 * time.Second,
	}

	// Attempt to connect
	address := fmt.Sprintf("127.0.0.1:%d", port)
	conn, err := dialer.DialContext(ctx, "tcp", address)
	if err != nil {
		return fmt.Errorf("port %d unreachable: %w", port, err)
	}

	// Close connection immediately - we only wanted to verify it's listening
	_ = conn.Close()

	return nil
}
