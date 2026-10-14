package unit

import (
	"os"
	"testing"

	"github.com/stretchr/testify/require"
	"go.yaml.in/yaml/v3"
)

func TestGoVetHookUsesModuleScope(t *testing.T) {
	data, err := os.ReadFile("../../.pre-commit-config.yaml")
	require.NoError(t, err)
	var config struct {
		Repos []struct {
			Hooks []struct {
				ID            string   `yaml:"id"`
				Entry         string   `yaml:"entry"`
				Language      string   `yaml:"language"`
				Args          []string `yaml:"args"`
				PassFilenames *bool    `yaml:"pass_filenames"`
			} `yaml:"hooks"`
		} `yaml:"repos"`
	}
	require.NoError(t, yaml.Unmarshal(data, &config))
	found := 0
	for _, repo := range config.Repos {
		for _, hook := range repo.Hooks {
			if hook.ID == "go-vet" {
				found++
				require.Equal(t, "go vet", hook.Entry)
				require.Equal(t, "system", hook.Language)
				require.Equal(t, []string{"./..."}, hook.Args)
				require.NotNil(t, hook.PassFilenames)
				require.False(t, *hook.PassFilenames)
			}
		}
	}
	require.Equal(t, 1, found)
}
