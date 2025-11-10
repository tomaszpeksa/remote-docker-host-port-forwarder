package unit

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/tomaszpeksa/remote-docker-host-port-forwarder/internal/ssh"
)

// TestDeriveControlPath_Deterministic tests that same host produces same path
func TestDeriveControlPath_Deterministic(t *testing.T) {
	host := "ssh://user@example.com"

	path1, err1 := ssh.DeriveControlPath(host)
	require.NoError(t, err1)

	path2, err2 := ssh.DeriveControlPath(host)
	require.NoError(t, err2)

	assert.Equal(t, path1, path2, "Same host should produce same control path")
	assert.Contains(t, path1, "/tmp/rdhpf-", "Path should be in /tmp with rdhpf prefix")
	assert.Contains(t, path1, ".sock", "Path should end with .sock")
}

// TestDeriveControlPath_DifferentHosts tests that different hosts produce different paths
func TestDeriveControlPath_DifferentHosts(t *testing.T) {
	host1 := "ssh://user@example.com"
	host2 := "ssh://user@other.com"
	host3 := "ssh://admin@example.com"

	path1, err1 := ssh.DeriveControlPath(host1)
	require.NoError(t, err1)

	path2, err2 := ssh.DeriveControlPath(host2)
	require.NoError(t, err2)

	path3, err3 := ssh.DeriveControlPath(host3)
	require.NoError(t, err3)

	assert.NotEqual(t, path1, path2, "Different hosts should produce different paths")
	assert.NotEqual(t, path1, path3, "Different users should produce different paths")
	assert.NotEqual(t, path2, path3, "Different combinations should produce different paths")
}

// TestDeriveControlPath_InvalidFormat tests error handling for invalid host formats
func TestDeriveControlPath_InvalidFormat(t *testing.T) {
	invalidCases := []struct {
		name string
		host string
	}{
		{
			name: "missing ssh:// prefix",
			host: "user@example.com",
		},
		{
			name: "http:// prefix",
			host: "http://user@example.com",
		},
		{
			name: "empty after prefix",
			host: "ssh://",
		},
		{
			name: "just ssh",
			host: "ssh",
		},
		{
			name: "empty string",
			host: "",
		},
	}

	for _, tc := range invalidCases {
		t.Run(tc.name, func(t *testing.T) {
			path, err := ssh.DeriveControlPath(tc.host)
			assert.Error(t, err, "Invalid format should produce error")
			assert.Empty(t, path, "Path should be empty on error")
		})
	}
}

// TestDeriveControlPath_ValidFormats tests various valid host formats
func TestDeriveControlPath_ValidFormats(t *testing.T) {
	validCases := []struct {
		name string
		host string
	}{
		{
			name: "simple user@host",
			host: "ssh://user@example.com",
		},
		{
			name: "with port",
			host: "ssh://user@example.com:2222",
		},
		{
			name: "with IP address",
			host: "ssh://user@192.168.1.100",
		},
		{
			name: "with IP and port",
			host: "ssh://user@192.168.1.100:2222",
		},
		{
			name: "localhost",
			host: "ssh://user@localhost",
		},
		{
			name: "username with special chars",
			host: "ssh://user-name@example.com",
		},
	}

	for _, tc := range validCases {
		t.Run(tc.name, func(t *testing.T) {
			path, err := ssh.DeriveControlPath(tc.host)
			require.NoError(t, err, "Valid format should not produce error")
			assert.NotEmpty(t, path, "Path should not be empty")
			assert.Contains(t, path, "/tmp/rdhpf-", "Path should have correct prefix")
			assert.Contains(t, path, ".sock", "Path should have .sock extension")
		})
	}
}

// TestDeriveControlPath_HashLength tests that hash is appropriate length
func TestDeriveControlPath_HashLength(t *testing.T) {
	host := "ssh://user@example.com"

	path, err := ssh.DeriveControlPath(host)
	require.NoError(t, err)

	// Path format: /tmp/rdhpf-{16-char-hex}.sock
	// Expected length: 11 + 16 + 5 = 32 characters
	// /tmp/rdhpf- = 11 (including the dash)
	// hash = 16
	// .sock = 5
	assert.Len(t, path, 32, "Path should have expected length")
}

// TestDeriveControlPath_PortDoesNotAffectPath tests that same host:different port = different path
func TestDeriveControlPath_PortAffectsPath(t *testing.T) {
	host1 := "ssh://user@example.com:22"
	host2 := "ssh://user@example.com:2222"

	path1, err1 := ssh.DeriveControlPath(host1)
	require.NoError(t, err1)

	path2, err2 := ssh.DeriveControlPath(host2)
	require.NoError(t, err2)

	assert.NotEqual(t, path1, path2, "Different ports should produce different control paths")
}

// TestDeriveControlPath_Stability tests that path doesn't change between calls
func TestDeriveControlPath_Stability(t *testing.T) {
	host := "ssh://testuser@testhost.example.org:2222"

	// Call multiple times
	paths := make([]string, 10)
	for i := 0; i < 10; i++ {
		path, err := ssh.DeriveControlPath(host)
		require.NoError(t, err)
		paths[i] = path
	}

	// All paths should be identical
	firstPath := paths[0]
	for i, path := range paths {
		assert.Equal(t, firstPath, path, "Path at index %d should match first path", i)
	}
}
