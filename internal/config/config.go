package config

import (
	"fmt"
	"strings"

	"github.com/tomaszpeksa/remote-docker-host-port-forwarder/internal/logging"
	"github.com/tomaszpeksa/remote-docker-host-port-forwarder/internal/ssh"
)

// Config represents the application configuration
type Config struct {
	// Host is the SSH connection string in ssh://user@host format (required)
	Host string

	// LogLevel controls verbosity: trace, debug, info, warn, error; empty means INFO.
	LogLevel string

	// EnableLabelPorts enables same-number SSH-side localhost hints from label
	// suffixes. Values are ignored; the hinted service may be unrelated.
	EnableLabelPorts bool
}

// Validate checks that the configuration is valid
func (c *Config) Validate() error {
	// Host is required and must be ssh:// format
	if c.Host == "" {
		return fmt.Errorf("host is required (set via --host flag)")
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

	if c.LogLevel != "" {
		if _, err := logging.ParseLevel(c.LogLevel); err != nil {
			return err
		}
	}

	return nil
}
