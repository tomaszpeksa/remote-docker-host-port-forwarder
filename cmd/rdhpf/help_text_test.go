package main

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestCommandHelpTextPreservesWhitespace(t *testing.T) {
	// These are the existing runtime strings, including spaces and the tab-only
	// line. Formatting hooks must not silently rewrite literal contents.
	require.Equal(t, "rdhpf automatically forwards published container ports from a remote Docker host \n"+
		"to your local machine via SSH.\n\n"+
		"When a container starts with published ports, rdhpf detects it and establishes \n"+
		"SSH port forwards so you can access the services on localhost.", rootCmd.Long)
	require.Equal(t, "Display the current status of all active port forwards.\n\t\n"+
		"This command queries the SSH ControlMaster to determine which forwards\n"+
		"are currently active and displays them in the requested format.", statusCmd.Long)
}
