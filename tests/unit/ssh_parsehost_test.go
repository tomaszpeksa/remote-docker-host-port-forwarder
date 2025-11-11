package unit

import (
	"testing"

	"github.com/tomaszpeksa/remote-docker-host-port-forwarder/internal/ssh"
)

// TestParseHost_WithoutPort verifies parsing SSH URLs without port
func TestParseHost_WithoutPort(t *testing.T) {
	host, port, err := ssh.ParseHost("ssh://user@example.com")

	if err != nil {
		t.Fatalf("Unexpected error: %v", err)
	}
	if host != "user@example.com" {
		t.Errorf("Expected host 'user@example.com', got '%s'", host)
	}
	if port != "" {
		t.Errorf("Expected empty port, got '%s'", port)
	}
}

// TestParseHost_WithPort verifies parsing SSH URLs with port
func TestParseHost_WithPort(t *testing.T) {
	host, port, err := ssh.ParseHost("ssh://user@example.com:2222")

	if err != nil {
		t.Fatalf("Unexpected error: %v", err)
	}
	if host != "user@example.com" {
		t.Errorf("Expected host 'user@example.com', got '%s'", host)
	}
	if port != "2222" {
		t.Errorf("Expected port '2222', got '%s'", port)
	}
}

// TestParseHost_IPv4WithPort verifies parsing SSH URLs with IPv4 and port
func TestParseHost_IPv4WithPort(t *testing.T) {
	host, port, err := ssh.ParseHost("ssh://root@192.168.1.100:22")

	if err != nil {
		t.Fatalf("Unexpected error: %v", err)
	}
	if host != "root@192.168.1.100" {
		t.Errorf("Expected host 'root@192.168.1.100', got '%s'", host)
	}
	if port != "22" {
		t.Errorf("Expected port '22', got '%s'", port)
	}
}

// TestParseHost_Localhost verifies parsing localhost URLs
func TestParseHost_Localhost(t *testing.T) {
	host, port, err := ssh.ParseHost("ssh://testuser@localhost:2222")

	if err != nil {
		t.Fatalf("Unexpected error: %v", err)
	}
	if host != "testuser@localhost" {
		t.Errorf("Expected host 'testuser@localhost', got '%s'", host)
	}
	if port != "2222" {
		t.Errorf("Expected port '2222', got '%s'", port)
	}
}

// TestParseHost_AlreadyParsed verifies handling URLs without ssh:// prefix
func TestParseHost_AlreadyParsed(t *testing.T) {
	host, port, err := ssh.ParseHost("user@host:3333")

	if err != nil {
		t.Fatalf("Unexpected error: %v", err)
	}
	if host != "user@host" {
		t.Errorf("Expected host 'user@host', got '%s'", host)
	}
	if port != "3333" {
		t.Errorf("Expected port '3333', got '%s'", port)
	}
}

// TestParseHost_IPv6_Bracketed verifies IPv6 addresses with brackets
func TestParseHost_IPv6_Bracketed(t *testing.T) {
	tests := []struct {
		name         string
		input        string
		expectedHost string
		expectedPort string
		expectError  bool
	}{
		{
			name:         "IPv6 loopback with port",
			input:        "ssh://user@[::1]:2222",
			expectedHost: "user@[::1]",
			expectedPort: "2222",
			expectError:  false,
		},
		{
			name:         "IPv6 loopback without port",
			input:        "ssh://user@[::1]",
			expectedHost: "user@[::1]",
			expectedPort: "",
			expectError:  false,
		},
		{
			name:         "IPv6 full address with port",
			input:        "ssh://user@[2001:db8::1]:2222",
			expectedHost: "user@[2001:db8::1]",
			expectedPort: "2222",
			expectError:  false,
		},
		{
			name:         "IPv6 full address without port",
			input:        "ssh://user@[2001:db8::1]",
			expectedHost: "user@[2001:db8::1]",
			expectedPort: "",
			expectError:  false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			host, port, err := ssh.ParseHost(tt.input)

			if tt.expectError && err == nil {
				t.Errorf("Expected error but got none")
			}
			if !tt.expectError && err != nil {
				t.Errorf("Unexpected error: %v", err)
			}
			if !tt.expectError {
				if host != tt.expectedHost {
					t.Errorf("Expected host '%s', got '%s'", tt.expectedHost, host)
				}
				if port != tt.expectedPort {
					t.Errorf("Expected port '%s', got '%s'", tt.expectedPort, port)
				}
			}
		})
	}
}

// TestParseHost_IPv6_Unbracketed verifies that unbracketed IPv6 returns error
func TestParseHost_IPv6_Unbracketed(t *testing.T) {
	tests := []struct {
		name  string
		input string
	}{
		{
			name:  "IPv6 loopback unbracketed",
			input: "ssh://user@::1",
		},
		{
			name:  "IPv6 full address unbracketed",
			input: "ssh://user@2001:db8::1",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, _, err := ssh.ParseHost(tt.input)
			if err == nil {
				t.Errorf("Expected error for unbracketed IPv6, got none")
			}
		})
	}
}

// TestParseHost_EdgeCases verifies edge cases and error conditions
func TestParseHost_EdgeCases(t *testing.T) {
	tests := []struct {
		name         string
		input        string
		expectedHost string
		expectedPort string
		expectError  bool
	}{
		{
			name:         "just ssh prefix",
			input:        "ssh://",
			expectedHost: "",
			expectedPort: "",
			expectError:  true,
		},
		{
			name:         "just hostname",
			input:        "ssh://hostname",
			expectedHost: "hostname",
			expectedPort: "",
			expectError:  false,
		},
		{
			name:         "unclosed bracket",
			input:        "ssh://user@[::1",
			expectedHost: "",
			expectedPort: "",
			expectError:  true,
		},
		{
			name:         "invalid after bracket",
			input:        "ssh://user@[::1]x",
			expectedHost: "",
			expectedPort: "",
			expectError:  true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			host, port, err := ssh.ParseHost(tt.input)

			if tt.expectError && err == nil {
				t.Errorf("Expected error but got none")
			}
			if !tt.expectError && err != nil {
				t.Errorf("Unexpected error: %v", err)
			}
			if !tt.expectError {
				if host != tt.expectedHost {
					t.Errorf("Expected host '%s', got '%s'", tt.expectedHost, host)
				}
				if port != tt.expectedPort {
					t.Errorf("Expected port '%s', got '%s'", tt.expectedPort, port)
				}
			}
		})
	}
}
