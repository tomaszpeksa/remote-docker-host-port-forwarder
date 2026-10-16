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
					Env   map[string]string `yaml:"env"`
					Steps []struct {
						Name  string `yaml:"name"`
						Shell string `yaml:"shell"`
						Run   string `yaml:"run"`
					} `yaml:"steps"`
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
				// runner context is unavailable in jobs.<id>.env. Export paths
				// from an ordinary shell step before mise bootstrap/installation.
				setup := ""
				for _, step := range job.Steps {
					if step.Name == "Isolate managed tool paths" {
						require.Equal(t, "bash", step.Shell, name)
						setup = step.Run
					}
					if step.Name == "Set up mise" {
						require.NotEmpty(t, setup, name)
					}
				}
				for _, key := range []string{"MISE_DATA_DIR", "MISE_CACHE_DIR", "MISE_STATE_DIR", "MISE_CONFIG_DIR", "MISE_GLOBAL_CONFIG_FILE", "MISE_SYSTEM_CONFIG_FILE", "GOCACHE", "GOMODCACHE"} {
					require.NotContains(t, job.Env, key, name)
					require.Contains(t, setup, key+"=$RUNNER_TEMP/s07-", name)
				}
				require.Contains(t, setup, `>> "$GITHUB_ENV"`, name)
			}
		})
	}
}
