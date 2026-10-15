package instancepath_test

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/tomaszpeksa/remote-docker-host-port-forwarder/internal/instancepath"
	"github.com/tomaszpeksa/remote-docker-host-port-forwarder/internal/socket"
	"github.com/tomaszpeksa/remote-docker-host-port-forwarder/internal/statefile"
)

var statusPaths = []struct {
	name   string
	suffix string
	path   func(string) (string, error)
}{
	{"socket", ".sock", socket.GetSocketPath},
	{"statefile", ".state.json", statefile.GetStateFilePath},
	{"helper-socket", ".sock", func(host string) (string, error) { return instancepath.StatusPath(host, ".sock") }},
	{"helper-statefile", ".state.json", func(host string) (string, error) { return instancepath.StatusPath(host, ".state.json") }},
}

func TestStatusPathGolden(t *testing.T) {
	// Literal outputs captured from the public wrappers before extraction.
	fixtures := []struct {
		host string
		hash string
	}{
		{"", "47DEQpj8HBSa"},
		{"example.com", "o3mm9u6vuaVe"},
		{"EXAMPLE.com", "s5Q9a1Gzj_s6"},
		{" example.com ", "UI9Afro-Uv_W"},
		{"user@example.com", "tMmiiTI7IaAc"},
		{"ssh://user@example.com", "WHhNu7djKMsZ"},
		{"ssh://user@example.com:22", "8Fzie8pwpmMW"},
		{"ssh://user@example.com:2222", "DZgbgjMf666U"},
		{"ssh://user@[::1]:2222", "SHi0ZRuW2xu1"},
		{"ssh://user@[0:0:0:0:0:0:0:1]:2222", "z-660t6bfCkt"},
	}
	for _, paths := range statusPaths {
		t.Run(paths.name, func(t *testing.T) {
			home := t.TempDir()
			t.Setenv("HOME", home)
			for _, fixture := range fixtures {
				t.Run(fixture.host, func(t *testing.T) {
					want := filepath.Join(home, ".rdhpf", fixture.hash+paths.suffix)
					for call := 0; call < 2; call++ {
						got, err := paths.path(fixture.host)
						if err != nil || got != want {
							t.Fatalf("call %d: got (%q, %v), want (%q, nil)", call, got, err, want)
						}
					}
				})
			}
		})
	}
}

func TestStatusPathCreatesDirectory(t *testing.T) {
	for _, paths := range statusPaths {
		t.Run(paths.name, func(t *testing.T) {
			home := filepath.Join(t.TempDir(), "new-home")
			t.Setenv("HOME", home)
			if _, err := paths.path("example.com"); err != nil {
				t.Fatal(err)
			}
			info, err := os.Stat(filepath.Join(home, ".rdhpf"))
			if err != nil {
				t.Fatal(err)
			}
			if !info.IsDir() || info.Mode().Perm() != 0700 {
				t.Fatalf("directory mode = %v, want directory with 0700", info.Mode())
			}
		})
	}
}

func TestStatusPathPreservesExistingDirectory(t *testing.T) {
	for _, paths := range statusPaths {
		t.Run(paths.name, func(t *testing.T) {
			home := t.TempDir()
			t.Setenv("HOME", home)
			dir := filepath.Join(home, ".rdhpf")
			if err := os.Mkdir(dir, 0700); err != nil {
				t.Fatal(err)
			}
			if err := os.Chmod(dir, 0751); err != nil {
				t.Fatal(err)
			}
			if _, err := paths.path("example.com"); err != nil {
				t.Fatal(err)
			}
			info, err := os.Stat(dir)
			if err != nil {
				t.Fatal(err)
			}
			if info.Mode().Perm() != 0751 {
				t.Fatalf("permissions = %o, want 0751", info.Mode().Perm())
			}
		})
	}
}

func TestStatusPathHomeUnavailable(t *testing.T) {
	for _, paths := range statusPaths {
		t.Run(paths.name, func(t *testing.T) {
			t.Setenv("HOME", "")
			if err := os.Unsetenv("HOME"); err != nil {
				t.Fatal(err)
			}
			got, err := paths.path("example.com")
			if got != "" || err == nil || err.Error() != "failed to get home directory: $HOME is not defined" {
				t.Fatalf("got (%q, %v), want empty path and wrapped missing HOME error", got, err)
			}
			if cause := errors.Unwrap(err); cause == nil || cause.Error() != "$HOME is not defined" {
				t.Fatalf("missing HOME cause = %v", cause)
			}
		})
	}
}

func TestStatusPathDirectoryFailure(t *testing.T) {
	for _, paths := range statusPaths {
		t.Run(paths.name, func(t *testing.T) {
			home := t.TempDir()
			t.Setenv("HOME", home)
			dir := filepath.Join(home, ".rdhpf")
			if err := os.WriteFile(dir, nil, 0600); err != nil {
				t.Fatal(err)
			}
			// Capture the platform's underlying error; assert the wrapper verbatim.
			mkdirErr := os.MkdirAll(dir, 0700)
			if mkdirErr == nil {
				t.Fatal("expected directory creation to fail on a regular file")
			}
			got, err := paths.path("example.com")
			if got != "" || err == nil || err.Error() != "failed to create .rdhpf directory: "+mkdirErr.Error() {
				t.Fatalf("got (%q, %v), want empty path and wrapped error %v", got, err, mkdirErr)
			}
			var cause *os.PathError
			if !errors.As(err, &cause) || cause.Path != dir || !errors.Is(err, mkdirErr.(*os.PathError).Err) {
				t.Fatalf("directory error does not preserve cause: %v", err)
			}
		})
	}
}

func TestStatusPathReadersCreateDirectory(t *testing.T) {
	readers := []struct {
		name string
		new  func(string) error
	}{
		{"socket", func(host string) error { _, err := socket.NewClient(host); return err }},
		{"statefile", func(host string) error { _, err := statefile.NewReader(host); return err }},
	}
	for _, reader := range readers {
		t.Run(reader.name, func(t *testing.T) {
			home := t.TempDir()
			t.Setenv("HOME", home)
			if err := reader.new("example.com"); err != nil {
				t.Fatal(err)
			}
			entries, err := os.ReadDir(filepath.Join(home, ".rdhpf"))
			if err != nil {
				t.Fatal(err)
			}
			if len(entries) != 0 {
				t.Fatalf("reader constructor created files: %v", entries)
			}
		})
	}
}
