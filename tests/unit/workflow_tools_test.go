package unit

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
	"go.yaml.in/yaml/v3"
)

func TestWorkflowManagedToolEnvironment(t *testing.T) {
	files, err := filepath.Glob("../../.github/workflows/*.yml")
	require.NoError(t, err)
	require.NotEmpty(t, files)
	for _, file := range files {
		t.Run(filepath.Base(file), func(t *testing.T) {
			data, err := os.ReadFile(file)
			require.NoError(t, err)
			var workflow struct {
				Jobs map[string]struct {
					Env      map[string]string `yaml:"env"`
					Defaults struct {
						Run struct {
							Shell string `yaml:"shell"`
						} `yaml:"run"`
					} `yaml:"defaults"`
				} `yaml:"jobs"`
			}
			require.NoError(t, yaml.Unmarshal(data, &workflow))
			for name, job := range workflow.Jobs {
				if !strings.HasPrefix(job.Defaults.Run.Shell, "mise exec -- ") {
					continue
				}
				require.Equal(t, "local", job.Env["GOTOOLCHAIN"], name)
				require.Equal(t, "0", job.Env["MISE_AUTO_INSTALL"], name)
				for _, key := range []string{"MISE_DATA_DIR", "MISE_CACHE_DIR", "MISE_STATE_DIR", "MISE_CONFIG_DIR", "MISE_GLOBAL_CONFIG_FILE", "MISE_SYSTEM_CONFIG_FILE", "GOCACHE", "GOMODCACHE"} {
					require.True(t, strings.HasPrefix(job.Env[key], "${{ runner.temp }}/s07-"), "%s: %s must apply to setup and every managed step", name, key)
				}
			}
		})
	}
}
