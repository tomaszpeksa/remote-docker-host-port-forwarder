package ssh

import (
	"fmt"
	"strings"
)

// ParseHost extracts the host and port from an SSH connection string with full IPv6 support.
// Input formats supported:
//   - ssh://user@host
//   - ssh://user@host:port
//   - ssh://user@[::1]:port (IPv6 with brackets)
//   - ssh://user@[2001:db8::1]:2222 (IPv6 with brackets and port)
//
// Returns:
//   - host: The SSH host in user@host format
//   - port: The port number as a string, or empty string if not specified
//   - error: If the URL format is invalid
//
// Examples:
//   - "ssh://user@example.com" -> ("user@example.com", "", nil)
//   - "ssh://user@localhost:2222" -> ("user@localhost", "2222", nil)
//   - "ssh://user@[::1]:2222" -> ("user@[::1]", "2222", nil)
func ParseHost(sshURL string) (host string, port string, error error) {
	// Remove ssh:// prefix
	hostPart := strings.TrimPrefix(sshURL, "ssh://")

	if hostPart == "" {
		return "", "", fmt.Errorf("empty host in SSH URL")
	}

	// Check for IPv6 with brackets: user@[::1]:port or user@[::1]
	if idx := strings.Index(hostPart, "["); idx != -1 {
		// Find closing bracket
		closeBracketIdx := strings.Index(hostPart, "]")
		if closeBracketIdx == -1 {
			return "", "", fmt.Errorf("unclosed bracket in IPv6 address: %s", hostPart)
		}
		if closeBracketIdx < idx {
			return "", "", fmt.Errorf("invalid bracket order in IPv6 address: %s", hostPart)
		}

		// Check if there's a port after the closing bracket
		if closeBracketIdx+1 < len(hostPart) {
			if hostPart[closeBracketIdx+1] != ':' {
				return "", "", fmt.Errorf("invalid format after IPv6 address: expected ':' but got '%c'", hostPart[closeBracketIdx+1])
			}
			// Extract port
			port = hostPart[closeBracketIdx+2:]
			host = hostPart[:closeBracketIdx+1]
		} else {
			// No port specified
			host = hostPart
		}

		return host, port, nil
	}

	// Not IPv6 with brackets - check for regular host:port
	// Use LastIndex to handle cases like user@host:port
	if idx := strings.LastIndex(hostPart, ":"); idx != -1 {
		// Make sure this isn't an unbracketed IPv6 address (multiple colons)
		colonCount := strings.Count(hostPart, ":")
		if colonCount > 1 {
			return "", "", fmt.Errorf("IPv6 addresses must use bracket notation: ssh://user@[::1]:port (got: %s)", sshURL)
		}

		port = hostPart[idx+1:]
		host = hostPart[:idx]
	} else {
		// No port specified
		host = hostPart
	}

	return host, port, nil
}
