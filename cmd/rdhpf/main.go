package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"sort"
	"syscall"
	"time"

	"github.com/spf13/cobra"
	"github.com/tomaszpeksa/remote-docker-host-port-forwarder/internal/config"
	"github.com/tomaszpeksa/remote-docker-host-port-forwarder/internal/docker"
	"github.com/tomaszpeksa/remote-docker-host-port-forwarder/internal/instancepath"
	"github.com/tomaszpeksa/remote-docker-host-port-forwarder/internal/logging"
	"github.com/tomaszpeksa/remote-docker-host-port-forwarder/internal/manager"
	"github.com/tomaszpeksa/remote-docker-host-port-forwarder/internal/reconcile"
	"github.com/tomaszpeksa/remote-docker-host-port-forwarder/internal/socket"
	"github.com/tomaszpeksa/remote-docker-host-port-forwarder/internal/ssh"
	"github.com/tomaszpeksa/remote-docker-host-port-forwarder/internal/state"
	"github.com/tomaszpeksa/remote-docker-host-port-forwarder/internal/statefile"
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
	Long: "rdhpf automatically forwards published container ports from a remote Docker host \n" +
		"to your local machine via SSH.\n\n" +
		"When a container starts with published ports, rdhpf detects it and establishes \n" +
		"SSH port forwards so you can access the services on localhost.",
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
	Long: "Display the current status of all active port forwards.\n\t\n" +
		"This command queries the SSH ControlMaster to determine which forwards\n" +
		"are currently active and displays them in the requested format.",
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

// Keep the owning run frame (and its admission defer) alive until the retained
// child's completion is proved. This is not a background supervisor or deadline.
func closeSSHMaster(closeMaster func() error, logger *slog.Logger) {
	warned := false
	for {
		err := closeMaster()
		if !errors.Is(err, ssh.ErrRetirementUncertain) {
			if err != nil {
				logger.Warn("failed to close SSH master", "error", err)
			}
			return
		}
		if !warned {
			logger.Warn("waiting for owned SSH retirement; retaining publisher ownership", "error", err)
			warned = true
		}
		// Close already uses bounded retirement waits. Avoid spinning if Wait
		// returned without proof; cancellation must not abandon this obligation.
		time.Sleep(100 * time.Millisecond)
	}
}

func run(ctx context.Context, cfg *config.Config, logger *slog.Logger) error {
	owner, err := instancepath.Acquire(cfg.Host)
	if err != nil {
		return err
	}
	defer func() {
		if err := owner.Close(); err != nil {
			logger.Warn("publisher ownership close failed", "error", err)
		}
	}()
	// 1. Create SSH Master
	logger.Info("establishing SSH ControlMaster connection")
	sshMaster, err := ssh.NewMaster(cfg.Host, logger)
	if err != nil {
		return fmt.Errorf("failed to create SSH master: %w", err)
	}

	// Own cleanup before startup can create a child, including failed Open.
	defer func() {
		logger.Info("closing SSH ControlMaster connection")
		closeSSHMaster(sshMaster.Close, logger)
	}()
	if err := sshMaster.Open(ctx); err != nil {
		return fmt.Errorf("failed to open SSH master: %w", err)
	}

	logger.Info("SSH ControlMaster established")

	// 2. Every operation uses this Master's stable private endpoint.
	controlPath := sshMaster.ControlPath()

	// 3. Create Docker event reader
	eventReader := docker.NewEventReader(cfg.Host, controlPath, logger)

	// 4. Create shared state and history
	stateManager := state.NewState()
	history := state.NewHistory()

	// 5. Create reconciler
	reconciler := reconcile.NewReconciler(stateManager, history, logger)

	// 6. Create manager
	mgr := manager.NewManager(
		cfg,
		eventReader,
		reconciler,
		sshMaster,
		stateManager,
		history,
		logger,
	)

	// 7. Run manager (blocks until context canceled)
	logger.Info("starting manager")
	if err := mgr.Run(ctx, owner); err != nil {
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

	logger.Info("forward cleanup attempt complete; closing owned SSH connection")
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

	// Sort by most recent activity (current forwards first, then by most recent end time for history)
	sort.Slice(forwards, func(i, j int) bool {
		// Current forwards always come before history
		if !forwards[i].IsHistory && forwards[j].IsHistory {
			return true
		}
		if forwards[i].IsHistory && !forwards[j].IsHistory {
			return false
		}

		// Within same type, sort by most recent activity
		// For current forwards, use duration (shorter = more recent)
		// For history, use EndedAt (more recent = later)
		if forwards[i].IsHistory {
			if forwards[i].EndedAt != nil && forwards[j].EndedAt != nil {
				return forwards[i].EndedAt.After(*forwards[j].EndedAt)
			}
			return false
		}
		// For current forwards, shorter duration = more recent
		return forwards[i].Duration < forwards[j].Duration
	})

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

// getActiveForwards queries status via socket or state file
func getActiveForwards(ctx context.Context, host string) ([]status.Forward, error) {
	// Try socket first (real-time)
	client, err := socket.NewClient(host)
	if err == nil {
		snapshot, err := client.GetStatus()
		if err == nil {
			return convertSnapshotToForwards(snapshot), nil
		}
		// Socket failed, fall back to file
	}

	// Fallback to state file
	reader, err := statefile.NewReader(host)
	if err != nil {
		return nil, fmt.Errorf("no running rdhpf instance found: %w", err)
	}

	snapshot, err := reader.Read()
	if err != nil {
		if os.IsNotExist(err) {
			return []status.Forward{}, nil
		}
		return nil, fmt.Errorf("failed to read state file: %w", err)
	}

	// Check staleness
	if snapshot.IsStale() {
		age := time.Since(snapshot.UpdatedAt)
		fmt.Fprintf(os.Stderr, "Warning: State is %v old (rdhpf may not be running)\n",
			age.Round(time.Second))
	}

	return convertSnapshotToForwards(snapshot), nil
}

// convertSnapshotToForwards converts state file snapshot to status forwards
func convertSnapshotToForwards(snapshot *statefile.StateFile) []status.Forward {
	// Combine current forwards and history
	allForwards := make([]status.Forward, 0, len(snapshot.Forwards)+len(snapshot.History))

	// Add current forwards
	for _, f := range snapshot.Forwards {
		allForwards = append(allForwards, status.Forward{
			ContainerID: f.ContainerID,
			LocalPort:   f.Port,
			RemotePort:  f.Port,
			State:       f.Status,
			Duration:    time.Since(f.CreatedAt),
			Reason:      f.Reason,
			IsHistory:   false,
		})
	}

	// Add history entries
	for _, h := range snapshot.History {
		// Determine display status based on final status
		displayStatus := "stopped"
		if h.FinalStatus == "conflict" {
			displayStatus = "conflict"
		}

		allForwards = append(allForwards, status.Forward{
			ContainerID: h.ContainerID,
			LocalPort:   h.Port,
			RemotePort:  h.Port,
			State:       displayStatus,
			Duration:    h.EndedAt.Sub(h.StartedAt),
			Reason:      h.EndReason,
			IsHistory:   true,
			EndedAt:     &h.EndedAt,
		})
	}

	return allForwards
}

// cleanup removes all active forwards before shutdown.
// It has a 10-second timeout to prevent hanging on cleanup.
func cleanup(stateManager *state.State, reconciler *reconcile.Reconciler, sshMaster *ssh.Master, host string, logger *slog.Logger) error {
	// Create a timeout context for cleanup (10 seconds max)
	cleanupCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	return sshMaster.WithForwarding(cleanupCtx, func(connection *ssh.Forwarding) error {
		reconciler.RetireBefore(connection.Generation())
		if !connection.Ready() {
			return fmt.Errorf("SSH connection unavailable during cleanup")
		}
		return cleanupForwardsLocked(cleanupCtx, stateManager, reconciler, connection, logger)
	})
}

func cleanupForwardsLocked(cleanupCtx context.Context, stateManager *state.State, reconciler *reconcile.Reconciler, connection *ssh.Forwarding, logger *slog.Logger) error {
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

	// Apply removal actions; an ineligible/uncertain obligation is not removal.
	for i := range toRemove {
		toRemove[i].EndReason = "rdhpf shutdown"
	}
	if err := reconciler.Apply(cleanupCtx, connection, toRemove); err != nil {
		return fmt.Errorf("failed to remove forwards: %w", err)
	}
	if len(stateManager.GetResources()) != 0 {
		return fmt.Errorf("forward cleanup obligations remain")
	}

	logger.Info("all forwards removed successfully",
		"count", len(toRemove))

	return nil
}
