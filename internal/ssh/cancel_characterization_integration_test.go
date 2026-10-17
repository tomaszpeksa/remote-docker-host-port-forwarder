//go:build rdhpf_integration

package ssh

import (
	"bytes"
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"github.com/tomaszpeksa/remote-docker-host-port-forwarder/internal/testutil/sshfixture"
)

// This is native CLI characterization, not a cancellation classifier. In
// OpenSSH V_9_6_P1, muxclient's CANCEL_FWD branch exits zero even when
// mux_client_forwards reports failure. Never use these Wait results alone as
// authority to forget an owned listener.
func TestControlRequestTrustCancelCharacterization(t *testing.T) {
	if os.Getenv("RDHPF_HOSTED_TRUST") != "1" {
		t.Skip("owned cancellation characterization requires disposable hosted selection")
	}
	require.False(t, testing.Short(), "selected characterization must execute")
	cancelCharacterizationSource(t, "before")
	t.Cleanup(func() { cancelCharacterizationSource(t, "after-cleanup") })
	f := sshfixture.New(t)
	f.Configure("yes", f.PublicKey, "", f.Key, "no")
	accountHome, err := os.UserHomeDir()
	require.NoError(t, err)
	configPath := filepath.Join(accountHome, ".ssh", "config")
	baselineConfig, err := os.ReadFile(configPath)
	require.NoError(t, err)
	configInfo, err := os.Stat(configPath)
	require.NoError(t, err)
	t.Logf("S12_CONFIG_SAVED mode=%04o sha256=%x", configInfo.Mode().Perm(), sha256.Sum256(baselineConfig))
	resolved, err := filepath.EvalSymlinks(f.SSH)
	require.NoError(t, err)
	executable, err := os.ReadFile(resolved)
	require.NoError(t, err)
	t.Logf("S12_EXECUTABLE path=%q resolved=%q sha256=%x", f.SSH, resolved, sha256.Sum256(executable))
	versionCtx, versionCancel := context.WithTimeout(context.Background(), 5*time.Second)
	version := exec.CommandContext(versionCtx, f.SSH, "-V")
	var versionOut, versionErr bytes.Buffer
	version.Stdout, version.Stderr = &versionOut, &versionErr
	versionResult := version.Run()
	t.Logf("S12_VERSION argv=%q stdout=%q stderr=%q wait=%v state=%v", version.Args, versionOut.String(), versionErr.String(), versionResult, version.ProcessState)
	require.NoError(t, versionCtx.Err())
	versionCancel()
	require.NoError(t, versionResult)
	t.Logf("S12_DOCKER_HOST\n%s", f.Docker("version"))
	t.Logf("S12_DOCKER_APPLICATION\n%s", f.Docker("exec", f.Container, "docker", "version"))
	t.Logf("S12_SERVER_PACKAGES\n%s", f.Docker("exec", f.Container, "apk", "info", "-v"))

	// Provision every backend before the Master exists. Backend registers its
	// cleanup internally and can fail before returning; no late registration
	// may let backend/config cleanup overtake an owned Master join.
	portA, portB := cancelCharacterizationPort(t), cancelCharacterizationPort(t)
	require.NotEqual(t, portA, portB)
	idA := f.Backend(sshfixture.Nginx, portA)
	idB := f.Backend(sshfixture.Nginx, portB)
	bodyA, bodyB := f.Alias+"-cancel-A", f.Alias+"-control-B"
	for _, backend := range []struct{ id, body string }{{idA, bodyA}, {idB, bodyB}} {
		require.Equal(t, f.Alias, f.Docker("inspect", "--format", `{{index .Config.Labels "rdhpf.trust"}}`, backend.id))
		f.Docker("exec", backend.id, "sh", "-c", "printf %s "+backend.body+" > /usr/share/nginx/html/index.html")
		t.Logf("S12_BACKEND id=%s owner=%s body=%s", backend.id, f.Alias, backend.body)
	}
	home, err := os.MkdirTemp("/tmp", "s12-cancel-")
	require.NoError(t, err)
	t.Setenv("HOME", home)
	master, err := NewMaster(f.Host, slog.Default())
	if err != nil {
		require.NoError(t, os.Remove(home))
		t.Fatal(err)
	}
	var retained *masterConnection
	t.Cleanup(func() {
		if retained == nil {
			retained = master.connection
		}
		err := master.Close()
		if errors.Is(err, ErrRetirementUncertain) && retained != nil {
			t.Logf("S12_CLEANUP_PENDING pid=%d namespace=%q home=%q; retaining fixture until Wait", retained.cmd.Process.Pid, master.ControlPath(), home)
			<-retained.done
			err = master.Close()
		}
		if retained != nil {
			<-retained.done
			t.Logf("S12_MASTER_WAIT pid=%d wait=%v state=%v generation=%d close=%v", retained.cmd.Process.Pid, retained.waitErr, retained.cmd.ProcessState, master.generation, err)
			require.NotNil(t, retained.cmd.ProcessState)
		}
		require.NoError(t, err, "uncertain namespace/runtime HOME must be retained")
		require.NoError(t, os.WriteFile(configPath, baselineConfig, configInfo.Mode().Perm()))
		restored, restoreErr := os.ReadFile(configPath)
		require.NoError(t, restoreErr)
		require.Equal(t, baselineConfig, restored)
		t.Logf("S12_CONFIG_RESTORED sha256=%x", sha256.Sum256(restored))
		require.NoError(t, os.Remove(home))
		f.NoPrompt()
		t.Log("S12_CLEANUP Master reaped before runtime HOME/backend/account-config retirement")
	})
	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	defer cancel()
	cancelCharacterizationConfig(t, f, "baseline", "", false)
	require.NoError(t, master.Open(ctx))
	retained = master.connection
	require.NotNil(t, retained)
	t.Logf("S12_MASTER pid=%d generation=%d controlPath=%q argv=%q", retained.cmd.Process.Pid, master.generation, master.ControlPath(), retained.cmd.Args)
	require.NoError(t, master.WithConnection(ctx, func(generation uint64, ready bool) error {
		require.True(t, ready)
		t.Logf("S12_ASSOCIATED generation=%d pid=%d tupleA=127.0.0.1:%d:localhost:%d tupleB=127.0.0.1:%d:localhost:%d", generation, retained.cmd.Process.Pid, portA, portA, portB, portB)
		if err := AddForward(ctx, master.ControlPath(), f.Host, portA, portA, slog.Default()); err != nil {
			return err
		}
		return AddForward(ctx, master.ControlPath(), f.Host, portB, portB, slog.Default())
	}))
	cancelCharacterizationHTTP(t, portA, bodyA, false)
	cancelCharacterizationHTTP(t, portB, bodyB, false)

	// Keep the serial experiment inside the owned gate. Acquire it before
	// changing logging: inherited QUIET can also affect an -O check diagnostic.
	// Each native observation precedes its hypothesis assertion.
	require.NoError(t, master.WithConnection(ctx, func(_ uint64, ready bool) error {
		require.True(t, ready)
		if !t.Run("Baseline", func(t *testing.T) {
			for _, mismatch := range []struct{ name, bind, target string }{
				{"wrong-target", "127.0.0.1", "127.0.0.1"},
				{"wrong-bind", "localhost", "localhost"},
			} {
				stderr, _ := observeOwnedCancel(t, f, master, mismatch.name, master.ControlPath(), fmt.Sprintf("%s:%d:%s:%d", mismatch.bind, portA, mismatch.target, portA), false)
				cancelCharacterizationHTTP(t, portA, bodyA, false)
				cancelCharacterizationHTTP(t, portB, bodyB, false)
				require.Contains(t, stderr, "port not forwarded", "native result contradicts exact-registry hypothesis")
			}
			stderr, waitErr := observeOwnedCancel(t, f, master, "exact", master.ControlPath(), fmt.Sprintf("127.0.0.1:%d:localhost:%d", portA, portA), false)
			cancelCharacterizationHTTP(t, portA, bodyA, true)
			cancelCharacterizationHTTP(t, portB, bodyB, false)
			require.NoError(t, waitErr)
			require.Empty(t, stderr, "record unexpected diagnostics, do not invent a classifier")
			stderr, _ = observeOwnedCancel(t, f, master, "repeat", master.ControlPath(), fmt.Sprintf("127.0.0.1:%d:localhost:%d", portA, portA), false)
			require.Contains(t, stderr, "port not forwarded")
			cancelCharacterizationHTTP(t, portB, bodyB, false)
			missing := filepath.Join(home, "never-created-mux")
			_, statErr := os.Lstat(missing)
			require.True(t, os.IsNotExist(statErr))
			stderr, waitErr = observeOwnedCancel(t, f, master, "missing-mux", missing, fmt.Sprintf("127.0.0.1:%d:localhost:%d", portA, portA), false)
			cancelCharacterizationHTTP(t, portB, bodyB, false)
			require.Error(t, waitErr)
			require.Contains(t, stderr, "Control socket connect")
			require.NotContains(t, stderr, "port not forwarded")
		}) {
			return nil
		}
		if !t.Run("Logging", func(t *testing.T) {
			ownedBlock := strings.SplitN(string(baselineConfig), "\nHost ", 2)[0]
			require.Equal(t, 1, strings.Count(ownedBlock, "  LogLevel ERROR\n"))
			quiet := bytes.Replace(baselineConfig, []byte("  LogLevel ERROR\n"), []byte("  LogLevel QUIET\n"), 1)
			require.NoError(t, os.WriteFile(configPath, quiet, configInfo.Mode().Perm()))
			cancelCharacterizationConfig(t, f, "quiet", "", false)
			spec := fmt.Sprintf("127.0.0.1:%d:localhost:%d", portA, portA)
			quietStderr, quietWait := observeOwnedCancel(t, f, master, "absent-quiet", master.ControlPath(), spec, false)
			cancelCharacterizationConfig(t, f, "forced-error", "", true)
			errorStderr, errorWait := observeOwnedCancel(t, f, master, "absent-error", master.ControlPath(), spec, true)
			t.Logf("S12_LOGGING_CONTRAST quiet_stderr=%q quiet_wait=%v error_stderr=%q error_wait=%v; not a selected classifier", quietStderr, quietWait, errorStderr, errorWait)
			require.Empty(t, quietStderr)
			require.Contains(t, errorStderr, "port not forwarded")
			cancelCharacterizationHTTP(t, portB, bodyB, false)
			require.NoError(t, os.WriteFile(configPath, baselineConfig, configInfo.Mode().Perm()))
		}) {
			return nil
		}
		t.Run("InheritedForwardings", func(t *testing.T) {
			header := []byte("Host " + f.Alias + "\n")
			require.True(t, bytes.HasPrefix(baselineConfig, header))
			additional := fmt.Sprintf("  LocalForward 127.0.0.1:%d localhost:%d\n  RemoteForward 127.0.0.1:%d localhost:%d\n", portB, portB, portA, portA)
			configured := bytes.Replace(baselineConfig, header, append(append([]byte{}, header...), []byte(additional)...), 1)
			require.NoError(t, os.WriteFile(configPath, configured, configInfo.Mode().Perm()))
			t.Logf("S12_CONFIG_OWNED_ADDITION host=%s entries=%q; remote tuple deliberately never established", f.Alias, additional)
			cancelCharacterizationConfig(t, f, "inherited-forwardings", additional, false)
			cancelCharacterizationHTTP(t, portB, bodyB, false)
			stderr, waitErr := observeOwnedCancel(t, f, master, "inherited-forwardings", master.ControlPath(), fmt.Sprintf("127.0.0.1:%d:localhost:%d", portA, portA), false)
			cancelCharacterizationHTTP(t, portB, bodyB, true)
			t.Logf("S12_COLLATERAL_COUNTEREXAMPLE only-owned-B canceled; wait=%v negative_replies=%d stderr=%q", waitErr, strings.Count(stderr, "port not forwarded"), stderr)
			require.Equal(t, 2, strings.Count(stderr, "port not forwarded"), "expected absent explicit local and configured remote requests")
			require.NoError(t, os.WriteFile(configPath, baselineConfig, configInfo.Mode().Perm()))
		})
		return nil
	}))
}

func cancelCharacterizationPort(t *testing.T) int {
	t.Helper()
	listener, err := net.Listen("tcp4", "127.0.0.1:0")
	require.NoError(t, err)
	port := listener.Addr().(*net.TCPAddr).Port
	require.NoError(t, listener.Close())
	return port
}

func cancelCharacterizationHTTP(t *testing.T, port int, body string, absent bool) {
	t.Helper()
	transport := &http.Transport{DisableKeepAlives: true}
	defer transport.CloseIdleConnections()
	client := &http.Client{Transport: transport, Timeout: time.Second}
	response, err := client.Get(fmt.Sprintf("http://127.0.0.1:%d/", port))
	if response != nil {
		actual, readErr := io.ReadAll(io.LimitReader(response.Body, 4096))
		closeErr := response.Body.Close()
		t.Logf("S12_FRESH_HTTP port=%d status=%d body=%q read=%v close=%v", port, response.StatusCode, actual, readErr, closeErr)
		require.NoError(t, readErr)
		require.NoError(t, closeErr)
		require.False(t, absent, "listener unexpectedly survived cancellation")
		require.Equal(t, http.StatusOK, response.StatusCode)
		require.Equal(t, body, string(actual))
	}
	if absent {
		t.Logf("S12_FRESH_HTTP_REFUSAL port=%d error=%v", port, err)
		require.ErrorIs(t, err, syscall.ECONNREFUSED)
	} else {
		require.NoError(t, err)
		require.NotNil(t, response)
	}
}

func observeOwnedCancel(t *testing.T, f *sshfixture.Fixture, master *Master, name, controlPath, spec string, forceError bool) (string, error) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	var stderr bytes.Buffer
	var waitErr error
	// The caller holds WithConnection throughout this serial experiment.
	require.NoError(t, master.associate(ctx))
	generation := master.generation
	host, port, err := ParseHost(f.Host)
	require.NoError(t, err)
	args := []string{"-o", "BatchMode=yes", "-S", controlPath}
	if port != "" {
		args = append(args, "-p", port)
	}
	if forceError {
		args = append(args, "-o", "LogLevel=ERROR")
	}
	args = append(args, "-O", "cancel", "-L", spec, "--", host)
	cmd := exec.CommandContext(ctx, f.SSH, args...)
	var stdout bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	started := time.Now()
	require.NoError(t, cmd.Start())
	// No assertion can abandon a started control child before its one Wait.
	waitErr = cmd.Wait()
	t.Logf("S12_CANCEL name=%s start=%s joined=%s argv=%q child=%d generation=%d master=%d stdout=%q stderr=%q wait=%v state=%v", name, started.UTC().Format(time.RFC3339Nano), time.Now().UTC().Format(time.RFC3339Nano), cmd.Args, cmd.Process.Pid, generation, master.connection.cmd.Process.Pid, stdout.String(), stderr.String(), waitErr, cmd.ProcessState)
	require.NoError(t, ctx.Err())
	require.NoError(t, master.associate(ctx))
	require.NoFileExists(t, f.Marker, "native askpass invoked")
	return stderr.String(), waitErr
}

func cancelCharacterizationConfig(t *testing.T, f *sshfixture.Fixture, name, additional string, forceError bool) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	args := []string{"-G", "-o", "BatchMode=yes"}
	if forceError {
		args = append(args, "-o", "LogLevel=ERROR")
	}
	args = append(args, "--", f.Alias)
	cmd := exec.CommandContext(ctx, f.SSH, args...)
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	err := cmd.Run()
	t.Logf("S12_EFFECTIVE_CONFIG name=%s argv=%q stderr=%q wait=%v state=%v", name, cmd.Args, stderr.String(), err, cmd.ProcessState)
	require.NoError(t, ctx.Err())
	require.NoError(t, err)
	local, remote := 0, 0
	for _, line := range strings.Split(stdout.String(), "\n") {
		fields := strings.Fields(line)
		if len(fields) == 0 {
			continue
		}
		switch fields[0] {
		case "localforward":
			local++
		case "remoteforward":
			remote++
		}
		switch fields[0] {
		case "hostname", "user", "port", "batchmode", "controlmaster", "controlpersist", "loglevel", "localforward", "remoteforward", "identityfile", "userknownhostsfile", "globalknownhostsfile", "stricthostkeychecking", "clearallforwardings", "exitonforwardfailure":
			t.Logf("S12_CONFIG %s %s", name, line)
		}
	}
	if additional == "" {
		require.Zero(t, local, "baseline has unexpected configured forwards")
		require.Zero(t, remote, "baseline has unexpected configured forwards")
	} else {
		require.Equal(t, 1, local)
		require.Equal(t, 1, remote)
	}
}

func cancelCharacterizationSource(t *testing.T, phase string) {
	t.Helper()
	// Only fixed read-only Git commands. ls-files includes ignored untracked
	// filenames, never their contents (in particular, never fixture key bytes).
	for _, args := range [][]string{{"rev-parse", "HEAD", "HEAD^{tree}"}, {"diff", "--exit-code"}, {"diff", "--cached", "--exit-code"}, {"ls-files", "--others"}} {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		cmd := exec.CommandContext(ctx, "git", args...)
		cmd.Dir = filepath.Join("..", "..") // Go runs this package in internal/ssh.
		output, err := cmd.CombinedOutput()
		t.Logf("S12_SOURCE phase=%s argv=%q output=%q wait=%v", phase, cmd.Args, output, err)
		require.NoError(t, ctx.Err())
		cancel()
		require.NoError(t, err, "source inventory failed or tracked source changed")
	}
}
