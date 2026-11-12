package unit

import (
	"os"
	"testing"

	"github.com/stretchr/testify/require"
	"go.yaml.in/yaml/v3"
)

func TestGoReleaserBinaryRelease(t *testing.T) {
	data, err := os.ReadFile("../../.goreleaser.yml")
	require.NoError(t, err)
	var config map[string]any
	require.NoError(t, yaml.Unmarshal(data, &config))
	require.NotContains(t, config, "brews", "retired binary-formula generator is not supported")
	require.NotContains(t, config, "homebrew_casks", "the binary formula is maintained separately in the tap")
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
