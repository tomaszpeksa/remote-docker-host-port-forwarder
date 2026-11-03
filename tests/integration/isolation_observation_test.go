//go:build rdhpf_integration

package integration

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestIsolationMasterPathObservation(t *testing.T) {
	valid := []string{"/usr/bin/ssh", "-MN", "-o", "ControlMaster=yes", "-o", "ControlPath=/tmp/rdhpf-owned/mux", "--", "private-host", ""}
	for _, tc := range []struct {
		name      string
		parent    string
		args      []string
		wantError string
	}{
		{"owned-foreground", "42", valid, ""},
		{"wrong-parent-before-command-read", "99", nil, "not a child"},
		{"mux-command-is-not-master", "42", []string{"ssh", "-S", "/tmp/rdhpf-owned/mux", "-O", "check", "--", "private-host", ""}, "command shape"},
		{"missing-path", "42", []string{"ssh", "-MN", "-o", "ControlMaster=yes", "--", "private-host", ""}, "missing or unexpected"},
		{"ambiguous-path", "42", []string{"ssh", "-MN", "-o", "ControlPath=", "-o", "ControlPath=/tmp/rdhpf-owned/mux", "--", "private-host", ""}, "ambiguous"},
		{"unexpected-path", "42", []string{"ssh", "-MN", "-o", "ControlPath=/private-payload", "--", "private-host", ""}, "missing or unexpected"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			proc := t.TempDir() // Exact synthetic proc entry, no global process scan.
			require.NoError(t, os.WriteFile(filepath.Join(proc, "status"), []byte("Name:\tssh\nPPid:\t"+tc.parent+"\n"), 0600))
			if tc.args != nil {
				require.NoError(t, os.WriteFile(filepath.Join(proc, "cmdline"), []byte(strings.Join(tc.args, "\x00")), 0600))
			}
			path, err := isolationMasterPath(proc, 42)
			if tc.wantError == "" {
				require.NoError(t, err)
				require.Equal(t, "/tmp/rdhpf-owned/mux", path)
			} else {
				require.ErrorContains(t, err, tc.wantError)
				require.NotContains(t, err.Error(), "private-")
				require.Empty(t, path)
			}
		})
	}
}
