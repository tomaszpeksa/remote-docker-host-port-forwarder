package config

import (
	"fmt"
	"strings"
)

// Config represents the application configuration
type Config struct {
	// Host is the SSH connection string in ssh://user@host format (required)
	Host string

	// LogLevel controls logging verbosity: debug, info, warn, error (default: "info")
	LogLevel string

	// ControlPath is the SSH control socket path
	ControlPath string
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

	return nil
}
