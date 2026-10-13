package unit

import (
	"os"
	"testing"

	"github.com/stretchr/testify/require"
	"go.yaml.in/yaml/v3"
)

func TestSecurityThresholdConfigs(t *testing.T) {
	var configs []map[string]any
	for _, tc := range []struct{ file, threshold string }{
		{"../../.golangci-security.yml", "low"},
		{"../../.golangci-security-hosted.yml", "medium"},
	} {
		data, err := os.ReadFile(tc.file)
		require.NoError(t, err)
		var config map[string]any
		require.NoError(t, yaml.Unmarshal(data, &config))
		settings := config["linters"].(map[string]any)["settings"].(map[string]any)["gosec"].(map[string]any)
		require.Equal(t, tc.threshold, settings["severity"])
		require.Equal(t, tc.threshold, settings["confidence"])
		delete(settings, "severity")
		delete(settings, "confidence")
		configs = append(configs, config)
	}
	require.Equal(t, configs[0], configs[1], "only historical severity/confidence thresholds differ")
}
