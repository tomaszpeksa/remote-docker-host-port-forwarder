package main

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"os/signal"
	"regexp"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/spf13/cobra"
	"github.com/tomaszpeksa/remote-docker-host-port-forwarder/internal/config"
	"github.com/tomaszpeksa/remote-docker-host-port-forwarder/internal/docker"
	"github.com/tomaszpeksa/remote-docker-host-port-forwarder/internal/logging"
	"github.com/tomaszpeksa/remote-docker-host-port-forwarder/internal/manager"
	"github.com/tomaszpeksa/remote-docker-host-port-forwarder/internal/reconcile"
	"github.com/tomaszpeksa/remote-docker-host-port-forwarder/internal/ssh"
	"github.com/tomaszpeksa/remote-docker-host-port-forwarder/internal/state"
	"github.com/tomaszpeksa/remote-docker-host-port-forwarder/internal/status"
)

var (
	// Version information (set by build)
	version = "dev"
	commit  = "unknown"
	date    = "unknown"
)

func main() {
	if err := rootCmd.Execute(); err != nil {
		os.Exit(1)
	}
}

var rootCmd = &cobra.Command{
	Use:   "rdhpf",
	Short: "Remote Docker Host Port Forwarder",
	Long: `rdhpf automatically forwards published container ports from a remote Docker host 
to your local machine via SSH.

When a container starts with published ports, rdhpf detects it and establishes 
SSH port forwards so you can access the services on localhost.`,
	SilenceUsage: true,
}

var runCmd = &cobra.Command{
	Use:   "run",
	Short: "Run the port forwarder",
	Long: `Start the port forwarding manager. This will:
  1. Establish an SSH ControlMaster connection to the remote host
  2. Subscribe to Docker events on the remote host
  3. Automatically forward published container ports to localhost
  4. Continue running until interrupted (Ctrl+C)`,
	RunE: runMain,
}

var (
	flagHost     string
	flagLogLevel string
	flagTrace    bool
	flagFormat   string
)
var statusCmd = &cobra.Command{
	Use:   "status",
	Short: "Show status of active port forwards",
	Long: `Display the current status of all active port forwards.
	
This command queries the SSH ControlMaster to determine which forwards
are currently active and displays them in the requested format.`,
	RunE: runStatus,
}

func init() {
	// Add commands to root
	rootCmd.AddCommand(runCmd)
	rootCmd.AddCommand(statusCmd)

	// Add version command
	rootCmd.AddCommand(&cobra.Command{
		Use:   "version",
		Short: "Print version information",
		Run: func(cmd *cobra.Command, args []string) {
			fmt.Printf("rdhpf %s (commit: %s, built: %s)\n", version, commit, date)
		},
	})

	// Run command flags
	runCmd.Flags().StringVar(&flagHost, "host", "", "SSH host in format ssh://user@host (required)")
	runCmd.Flags().StringVar(&flagLogLevel, "log-level", "info", "Log level (trace, debug, info, warn, error)")
	runCmd.Flags().BoolVar(&flagTrace, "trace", false, "Enable trace mode (maximum verbosity)")

	// Mark required flags
	if err := runCmd.MarkFlagRequired("host"); err != nil {
		panic(fmt.Sprintf("failed to mark host flag as required: %v", err))
	}

	// Status command flags
	statusCmd.Flags().StringVar(&flagHost, "host", "", "SSH host in format ssh://user@host (required)")
	statusCmd.Flags().StringVar(&flagFormat, "format", "table", "Output format: table, json, yaml")

	// Mark required flags
	if err := statusCmd.MarkFlagRequired("host"); err != nil {
		panic(fmt.Sprintf("failed to mark host flag as required: %v", err))
	}
}
func runMain(cmd *cobra.Command, args []string) error {
	// Validate host format
	if flagHost == "" {
		return fmt.Errorf("--host is required")
	}

	// Determine log level (check environment variable, then flag, then trace flag)
	logLevel := flagLogLevel
	if envLevel := os.Getenv("RDHPF_LOG_LEVEL"); envLevel != "" {
		logLevel = envLevel
	}
	if flagTrace {
		logLevel = "trace"
	}

	// Create base config
	cfg := &config.Config{
		Host:     flagHost,
		LogLevel: logLevel,
	}

	// Validate config
	if err := cfg.Validate(); err != nil {
		return fmt.Errorf("invalid configuration: %w", err)
	}

	// Create logger
	logger := logging.NewLogger(cfg.LogLevel)
	logger.Info("rdhpf starting",
		"version", version,
		"host", cfg.Host)

	// Create context with signal handling
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// Setup signal handling
	sigChan := make(chan os.Signal, 1)
	signal.Notify(sigChan, os.Interrupt, syscall.SIGTERM)
	go func() {
		sig := <-sigChan
		logger.Info("received signal, shutting down", "signal", sig.String())
		cancel()
	}()

	// Initialize components
	if err := run(ctx, cfg, logger); err != nil {
		// context.Canceled is expected during graceful shutdown
		if err == context.Canceled {
			logger.Info("rdhpf stopped")
			return nil
		}
		logger.Error("fatal error", "error", err.Error())
		return err
	}

	logger.Info("rdhpf stopped")
	return nil
}

func run(ctx context.Context, cfg *config.Config, logger *slog.Logger) error {
	// 1. Create SSH Master
	logger.Info("establishing SSH ControlMaster connection")
	sshMaster, err := ssh.NewMaster(cfg.Host, logger)
	if err != nil {
		return fmt.Errorf("failed to create SSH master: %w", err)
	}

	if err := sshMaster.Open(ctx); err != nil {
		return fmt.Errorf("failed to open SSH master: %w", err)
	}
	defer func() {
		logger.Info("closing SSH ControlMaster connection")
		if err := sshMaster.Close(); err != nil {
			logger.Warn("failed to close SSH master", "error", err.Error())
		}
	}()

	logger.Info("SSH ControlMaster established")

	// 2. Derive control path for other operations
	controlPath, err := ssh.DeriveControlPath(cfg.Host)
	if err != nil {
		return fmt.Errorf("failed to derive control path: %w", err)
	}

	// 3. Create Docker event reader
	eventReader := docker.NewEventReader(cfg.Host, controlPath, logger)

	// 4. Create shared state
	stateManager := state.NewState()

	// 5. Create reconciler
	reconciler := reconcile.NewReconciler(stateManager, logger)

	// 6. Create manager
	mgr := manager.NewManager(
		cfg,
		eventReader,
		reconciler,
		sshMaster,
		stateManager,
		logger,
	)

	// 7. Run manager (blocks until context canceled)
	logger.Info("starting manager")
	if err := mgr.Run(ctx); err != nil {
		// context.Canceled is expected during graceful shutdown
		if err != context.Canceled {
			return fmt.Errorf("manager error: %w", err)
		}
	}

	// 8. Perform cleanup after manager stops
	logger.Info("shutdown initiated, cleaning up forwards")
	if err := cleanup(stateManager, reconciler, sshMaster, cfg.Host, logger); err != nil {
		logger.Warn("cleanup encountered errors", "error", err.Error())
		// Continue with shutdown even if cleanup had errors
	}

	logger.Info("shutdown complete, all forwards removed")
	return nil
}

func runStatus(cmd *cobra.Command, args []string) error {
	// Validate host format
	if flagHost == "" {
		return fmt.Errorf("--host is required")
	}

	// Validate format
	validFormats := map[string]bool{"table": true, "json": true, "yaml": true}
	if !validFormats[flagFormat] {
		return fmt.Errorf("invalid format: %s (valid: table, json, yaml)", flagFormat)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	// Get active forwards from SSH ControlMaster
	forwards, err := getActiveForwards(ctx, flagHost)
	if err != nil {
		return fmt.Errorf("failed to get active forwards: %w", err)
	}

	// Format and display output
	var output string
	switch flagFormat {
	case "json":
		output = status.FormatJSON(forwards)
	case "yaml":
		output = status.FormatYAML(forwards)
	default: // table
		output = status.FormatTable(forwards)
	}

	fmt.Print(output)
	return nil
}

// getActiveForwards queries the SSH ControlMaster to get currently active port forwards
func getActiveForwards(ctx context.Context, host string) ([]status.Forward, error) {
	// Derive control path
	controlPath, err := ssh.DeriveControlPath(host)
	if err != nil {
		return nil, fmt.Errorf("failed to derive control path: %w", err)
	}

	// Remove ssh:// prefix for SSH command
	sshHost := strings.TrimPrefix(host, "ssh://")

	// Check SSH connection and get forwarded ports
	// Use ssh -O check to see if ControlMaster is active
	// #nosec G204 - SSH command with validated host format (checked in config.Validate)
	checkCmd := exec.CommandContext(ctx, "ssh",
		"-S", controlPath,
		"-O", "check",
		sshHost)

	checkOutput, checkErr := checkCmd.CombinedOutput()

	// If control master is not running, return empty list
	if checkErr != nil {
		// ControlMaster not running means no forwards
		if strings.Contains(string(checkOutput), "No such file") ||
			strings.Contains(string(checkOutput), "Control socket connect") {
			return []status.Forward{}, nil
		}
		return nil, fmt.Errorf("failed to check SSH connection: %w", checkErr)
	}

	// Use netstat or ss to find active SSH tunnels
	// This is a simplified approach - parse SSH control master state
	// For now, we'll return empty list as we don't have a state file yet
	// In a real implementation, we would parse the SSH control master state
	// or maintain a state file

	// Try to get forwarded ports from SSH process
	forwards, err := parseSSHForwards(ctx, controlPath, sshHost)
	if err != nil {
		// If we can't parse, return empty list rather than error
		// (ControlMaster is running but no forwards detected)
		return []status.Forward{}, nil
	}

	return forwards, nil
}

// parseSSHForwards attempts to parse active SSH port forwards
// This is a best-effort implementation using SSH -O forward -L output
func parseSSHForwards(ctx context.Context, controlPath, sshHost string) ([]status.Forward, error) {
	// List active port forwards using netstat/lsof
	// Look for processes listening on 127.0.0.1 spawned by our SSH connection

	// Use lsof to find ports forwarded by SSH process using our control socket
	lsofCmd := exec.CommandContext(ctx, "lsof",
		"-iTCP",
		"-sTCP:LISTEN",
		"-n",
		"-P",
		"-F", "pcn")

	output, err := lsofCmd.Output()
	if err != nil {
		// lsof might not be available or no ports open
		return []status.Forward{}, nil
	}

	// Parse lsof output to find SSH-forwarded ports
	forwards := parseLsofOutput(string(output), controlPath)

	return forwards, nil
}

// parseLsofOutput parses lsof -F output to find SSH port forwards
func parseLsofOutput(output, controlPath string) []status.Forward {
	var forwards []status.Forward

	lines := strings.Split(output, "\n")
	var currentCmd string

	for _, line := range lines {
		if len(line) < 2 {
			continue
		}

		switch line[0] {
		case 'c': // Command name
			currentCmd = line[1:]
		case 'n': // Name/address
			// Look for 127.0.0.1:PORT or localhost:PORT
			if currentCmd == "ssh" && strings.Contains(line, "127.0.0.1:") {
				// Parse port from format like "127.0.0.1:8080"
				re := regexp.MustCompile(`127\.0\.0\.1:(\d+)`)
				matches := re.FindStringSubmatch(line)
				if len(matches) > 1 {
					port, err := strconv.Atoi(matches[1])
					if err == nil {
						// Add as unknown state since we don't track duration yet
						forwards = append(forwards, status.Forward{
							ContainerID: "unknown",
							LocalPort:   port,
							RemotePort:  port,
							State:       "active",
							Duration:    0,
						})
					}
				}
			}
		}
	}

	return forwards
}

// cleanup removes all active forwards before shutdown.
// It has a 10-second timeout to prevent hanging on cleanup.
func cleanup(stateManager *state.State, reconciler *reconcile.Reconciler, sshMaster *ssh.Master, host string, logger *slog.Logger) error {
	// Create a timeout context for cleanup (10 seconds max)
	cleanupCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	logger.Info("removing all active forwards")

	// Clear all desired state (this will make reconciler want to remove everything)
	allContainers := stateManager.GetAllContainers()
	for _, containerID := range allContainers {
		stateManager.SetDesired(containerID, []int{})
	}

	// Perform reconciliation to remove forwards
	_, toRemove := reconciler.Diff()

	if len(toRemove) == 0 {
		logger.Info("no forwards to remove")
		return nil
	}

	logger.Info("removing forwards during shutdown",
		"count", len(toRemove))

	// Apply removal actions
	if err := reconciler.Apply(cleanupCtx, sshMaster, host, toRemove); err != nil {
		return fmt.Errorf("failed to remove forwards: %w", err)
	}

	logger.Info("all forwards removed successfully",
		"count", len(toRemove))

	return nil
}
