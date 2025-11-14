package config

import (
	"fmt"
	"os"
	"strings"

	"github.com/tomaszpeksa/remote-docker-host-port-forwarder/internal/ssh"
)

// Config represents the application configuration
type Config struct {
	// Host is the SSH connection string in ssh://user@host format (required)
	Host string

	// LogLevel controls logging verbosity: debug, info, warn, error (default: "info")
	LogLevel string

	// ControlPath is the SSH control socket path
	ControlPath string

	// EnableLabelPorts enables port discovery from rdhpf.forward.* labels
	// This is primarily for testing scenarios where containers don't publish ports
	// Set via RDHPF_ENABLE_LABEL_PORTS=1 environment variable
	EnableLabelPorts bool
}

// Validate checks that the configuration is valid
func (c *Config) Validate() error {
	// Host is required and must be ssh:// format
	if c.Host == "" {
		return fmt.Errorf("host is required (set via --host flag or RDHPF_HOST env var)")
	}
	if !strings.HasPrefix(c.Host, "ssh://") {
		return fmt.Errorf("host must be in ssh://user@host format, got: %s", c.Host)
	}

	// Validate SSH URL format and check for IPv6 support
	host, _, err := ssh.ParseHost(c.Host)
	if err != nil {
		return fmt.Errorf("invalid SSH_HOST format: %w", err)
	}

	// Warn about unbracketed IPv6 (already caught by ParseHost but good to document)
	if strings.Count(host, ":") > 1 && !strings.Contains(host, "[") {
		return fmt.Errorf("IPv6 addresses must use bracket notation: ssh://user@[::1]:port")
	}

	// Read label ports flag from environment
	c.EnableLabelPorts = os.Getenv("RDHPF_ENABLE_LABEL_PORTS") == "1"

	return nil
}
