package unit

import (
	"os"
	"testing"

	"github.com/stretchr/testify/require"
	"go.yaml.in/yaml/v3"
)

func TestGoReleaserHomebrewCask(t *testing.T) {
	data, err := os.ReadFile("../../.goreleaser.yml")
	require.NoError(t, err)
	var config map[string]any
	require.NoError(t, yaml.Unmarshal(data, &config))
	require.NotContains(t, config, "brews")
	casks, ok := config["homebrew_casks"].([]any)
	require.True(t, ok, "supported homebrew_casks section required")
	require.Len(t, casks, 1)
	cask := casks[0].(map[string]any)
	require.Equal(t, "rdhpf", cask["name"])
	require.Equal(t, "Casks", cask["directory"])
	require.Equal(t, []any{"rdhpf"}, cask["binaries"])
	require.Equal(t, map[string]any{
		"owner": "tomaszpeksa", "name": "homebrew-tap", "token": "{{ .Env.HOMEBREW_TAP_TOKEN }}",
	}, cask["repository"])
	require.Equal(t, false, cask["skip_upload"])
	for _, field := range []string{"install", "test", "hooks", "custom_block", "license"} {
		require.NotContains(t, cask, field, "no formula DSL or OS security overrides")
	}
	for _, field := range []string{"signs", "notarize", "kos"} {
		require.NotContains(t, config, field, "no new signing or publication paths")
	}
	builds := config["builds"].([]any)
	require.Len(t, builds, 1)
	build := builds[0].(map[string]any)
	require.Equal(t, []any{"linux", "darwin"}, build["goos"])
	require.Equal(t, []any{"amd64", "arm64"}, build["goarch"])
	require.Equal(t, "rdhpf", build["binary"])
	archives := config["archives"].([]any)
	require.Len(t, archives, 1)
	require.Equal(t, []any{"tar.gz"}, archives[0].(map[string]any)["formats"])
}
