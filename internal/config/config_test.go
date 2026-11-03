package config

import (
	"github.com/stretchr/testify/require"
	"testing"
)

func TestConfigValidatePure(t *testing.T) {
	for _, enabled := range []bool{false, true} {
		for _, level := range []string{"", "info", "DEBUG", "TrAcE", "warn", "error"} {
			c := Config{Host: "ssh://fixture.invalid", LogLevel: level, EnableLabelPorts: enabled}
			before := c
			t.Setenv("RDHPF_ENABLE_LABEL_PORTS", "1")
			require.NoError(t, c.Validate())
			require.Equal(t, before, c, "Validate must not resolve environment or insert defaults")
			t.Setenv("RDHPF_ENABLE_LABEL_PORTS", "0")
			require.NoError(t, c.Validate())
			require.Equal(t, before, c)
		}
	}
}

func TestConfigRejectsInvalidLevel(t *testing.T) {
	for _, level := range []string{"private-payload", " info", "info ", "WARNING"} {
		c := Config{Host: "ssh://fixture.invalid", LogLevel: level}
		err := c.Validate()
		require.Error(t, err)
		require.EqualError(t, err, "log level must be one of: trace, debug, info, warn, error")
	}
}
