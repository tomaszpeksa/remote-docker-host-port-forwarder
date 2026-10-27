//go:build rdhpf_integration

// Package sshfixture provisions only the disposable hosted SSH trust scenarios.
// It imports no application package: callers exercise their actual command paths.
package sshfixture

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"os"
	"os/exec"
	"os/user"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

const Alpine = "alpine:3.24.2@sha256:294b683cb724975bec92580e1e685676bd4b50bda910ddb8c51d4cabeaec77e6"
const Nginx = "nginx:1.30.5-alpine3.24@sha256:0985e772fb9f729e6fa0980da05fca5d9c468e870eed43071545afa9d2e27d94"

// Fixture owns a serial Host block and files. It must not be used with Parallel.
type Fixture struct {
	T                                                                                                              *testing.T
	Dir, Host, Alias, Key, EncryptedKey, PublicKey, WrongKey, UserHosts, GlobalHosts, Marker, SSH, Path, Container string
	config                                                                                                         string
	previous                                                                                                       []byte
	mode                                                                                                           os.FileMode
	backendSequence                                                                                                uint64
}

func New(t *testing.T) *Fixture {
	t.Helper()
	if os.Getenv("RDHPF_HOSTED_TRUST") != "1" {
		t.Skip("owned trust proof requires disposable hosted selection")
	}
	require.Equal(t, "true", os.Getenv("GITHUB_ACTIONS"))
	require.Equal(t, "ssh://testuser@localhost:2222", os.Getenv("SSH_TEST_HOST"))
	account, err := user.Current()
	require.NoError(t, err)
	home, err := os.UserHomeDir()
	require.NoError(t, err)
	require.Equal(t, account.HomeDir, home, "only disposable account configuration may be changed")
	root, err := filepath.Abs(".")
	require.NoError(t, err)
	for {
		if _, err := os.Stat(filepath.Join(root, "go.mod")); err == nil {
			break
		}
		next := filepath.Dir(root)
		require.NotEqual(t, root, next)
		root = next
	}
	owned := filepath.Join(root, ".itests")
	// #nosec G304 -- Fixed owned harness state path; ID is validated before Docker use, not an application path input.
	id, err := os.ReadFile(filepath.Join(owned, "container-id"))
	require.NoError(t, err)
	// #nosec G304 -- Fixed private harness owner file; producer syntax and retained container label are checked below.
	owner, err := os.ReadFile(filepath.Join(owned, "owner"))
	require.NoError(t, err)
	require.Regexp(t, `^[1-9][0-9]*-[1-9][0-9]*\n$`, string(owner), "invalid fixture owner; expected timestamp-PID from itest-up")
	f := &Fixture{T: t, Dir: t.TempDir(), Container: strings.TrimSpace(string(id)), Key: filepath.Join(owned, "home/.ssh/id_ed25519"), Path: os.Getenv("PATH")}
	require.Regexp(t, `^[a-f0-9]{64}$`, f.Container)
	require.Equal(t, strings.TrimSpace(string(owner)), f.Docker("inspect", "--format", `{{index .Config.Labels "rdhpf.itest"}}`, f.Container))
	var token [8]byte
	_, err = rand.Read(token[:])
	require.NoError(t, err)
	f.Alias = "rdhpf-trust-" + hex.EncodeToString(token[:])
	f.Host = "ssh://" + f.Alias
	f.SSH, err = exec.LookPath("ssh")
	require.NoError(t, err)
	f.config = filepath.Join(home, ".ssh/config")
	info, err := os.Lstat(f.config)
	require.NoError(t, err)
	require.True(t, info.Mode().IsRegular())
	f.mode = info.Mode().Perm()
	f.previous, err = os.ReadFile(f.config)
	require.NoError(t, err)
	t.Cleanup(func() {
		require.NoError(t, os.WriteFile(f.config, f.previous, f.mode))
		t.Log("restored exact disposable-account SSH config bytes and mode")
	})
	// #nosec G304 -- Public host key from retained owned container provisioning, never keyscan or an arbitrary caller path.
	key, err := os.ReadFile(filepath.Join(owned, "host-key.pub"))
	require.NoError(t, err)
	fields := strings.Fields(string(key))
	require.GreaterOrEqual(t, len(fields), 2)
	require.Equal(t, "ssh-ed25519", fields[0])
	f.PublicKey = strings.Join(fields[:2], " ")
	t.Logf("owned server id=%s public-host-key=%s", f.Container, f.PublicKey)
	f.UserHosts = filepath.Join(f.Dir, "known_hosts")
	f.GlobalHosts = filepath.Join(f.Dir, "global_hosts")
	f.Marker = filepath.Join(f.Dir, "askpass-called")
	f.EncryptedKey = filepath.Join(f.Dir, "encrypted")
	// A disposable test-only passphrase, never printed or uploaded.
	keyCtx, keyCancel := context.WithTimeout(context.Background(), 5*time.Second)
	// #nosec G204 -- Trusted hosted PATH, fixed keygen options, separate -f operand in t.TempDir; no shell or external input.
	keyErr := exec.CommandContext(keyCtx, "ssh-keygen", "-q", "-t", "ed25519", "-N", "owned-test-passphrase", "-f", f.EncryptedKey).Run()
	require.NoError(t, keyErr)
	require.NoError(t, keyCtx.Err())
	keyCancel()
	pub, err := os.ReadFile(f.EncryptedKey + ".pub")
	require.NoError(t, err)
	fields = strings.Fields(string(pub))
	f.WrongKey = strings.Join(fields[:2], " ")
	// Public authorization only; private key never enters the server/container logs.
	f.authorizeTemporaryKey(string(pub))
	askpass := filepath.Join(f.Dir, "askpass")
	// #nosec G306 -- Owner-only execution is required for real native askpass; fixed marker-only script in private t.TempDir.
	require.NoError(t, os.WriteFile(askpass, []byte("#!/bin/sh\nprintf called >> \"$RDHPF_ASKPASS_MARKER\"\nexit 1\n"), 0700))
	t.Setenv("SSH_ASKPASS", askpass)
	t.Setenv("SSH_ASKPASS_REQUIRE", "force")
	t.Setenv("DISPLAY", ":rdhpf-owned")
	t.Setenv("RDHPF_ASKPASS_MARKER", f.Marker)
	t.Setenv("SSH_AUTH_SOCK", "")
	t.Setenv("SSH_TEST_KEY_PATH", "")
	return f
}

func (f *Fixture) Docker(args ...string) string {
	f.T.Helper()
	return strings.TrimSpace(f.dockerOutput(args...))
}

func (f *Fixture) dockerOutput(args ...string) string {
	f.T.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	// #nosec G204 -- Only reviewed trusted test callers: owned inspect/cp/exec/run/start/stop/rm operands; not an arbitrary Docker API.
	cmd := exec.CommandContext(ctx, "docker", args...)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	require.NoError(f.T, ctx.Err())
	require.NoError(f.T, err, "owned Docker fixture: %s", stderr.String())
	return string(out)
}

func (f *Fixture) authorizeTemporaryKey(public string) {
	f.T.Helper()
	original := f.dockerOutput("exec", f.Container, "cat", "/home/testuser/.ssh/authorized_keys")
	f.T.Cleanup(func() {
		f.setAuthorized(original)
		require.Equal(f.T, original, f.dockerOutput("exec", f.Container, "cat", "/home/testuser/.ssh/authorized_keys"))
		f.T.Log("restored authorized_keys exact bytes on owned server")
	})
	augmented := original
	if augmented != "" && !strings.HasSuffix(augmented, "\n") {
		augmented += "\n"
	}
	f.setAuthorized(augmented + public)
}

func (f *Fixture) setAuthorized(public string) {
	f.T.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	// #nosec G204 -- Validated owned container ID, constant command/path; public authorization bytes enter stdin, not shell text.
	cmd := exec.CommandContext(ctx, "docker", "exec", "-i", f.Container, "sh", "-c", "cat > /home/testuser/.ssh/authorized_keys")
	cmd.Stdin = strings.NewReader(public)
	require.NoError(f.T, cmd.Run())
	require.NoError(f.T, ctx.Err())
}

// Configure changes only this fixture's exact Host block. Empty policy means
// native default ask; no BatchMode is added by a launcher or a fixture command.
func (f *Fixture) Configure(policy, userKey, globalKey, identity, control string) {
	f.T.Helper()
	pin := func(path, key string) {
		text := ""
		if key != "" {
			text = "[127.0.0.1]:2222 " + key + "\n"
		}
		require.NoError(f.T, os.WriteFile(path, []byte(text), 0600))
	}
	pin(f.UserHosts, userKey)
	pin(f.GlobalHosts, globalKey)
	strict := ""
	if policy != "" {
		strict = "  StrictHostKeyChecking " + policy + "\n"
	}
	block := fmt.Sprintf("Host %s\n  HostName 127.0.0.1\n  User testuser\n  Port 2222\n  IdentityFile %s\n  IdentitiesOnly yes\n  IdentityAgent none\n  BatchMode no\n  ControlMaster %s\n  ControlPersist no\n  UserKnownHostsFile %s\n  GlobalKnownHostsFile %s\n  HostKeyAlgorithms ssh-ed25519\n  KnownHostsCommand none\n  VerifyHostKeyDNS no\n  UpdateHostKeys no\n  CanonicalizeHostname no\n  ProxyCommand none\n  ProxyJump none\n  LogLevel ERROR\n%s\n", f.Alias, identity, control, f.UserHosts, f.GlobalHosts, strict)
	require.NoError(f.T, os.WriteFile(f.config, append([]byte(block), f.previous...), f.mode))
	f.T.Logf("owned Host configuration (application argv can override):\n%s", block)
	f.ClearPrompt()
}
func (f *Fixture) ClearPrompt() {
	f.T.Helper()
	err := os.Remove(f.Marker)
	if !os.IsNotExist(err) {
		require.NoError(f.T, err)
	}
}
func (f *Fixture) NoPrompt() {
	f.T.Helper()
	_, err := os.Stat(f.Marker)
	require.True(f.T, os.IsNotExist(err), "native askpass invoked")
}
func (f *Fixture) Contents(path string) string {
	f.T.Helper()
	// #nosec G304 -- Reviewed callers read only private test pin files or the retained container's saved public sshd config.
	b, err := os.ReadFile(path)
	require.NoError(f.T, err)
	return string(b)
}

// PromptControl must reach a native prompt, not merely fail authentication.
func (f *Fixture) PromptControl() {
	f.T.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	// #nosec G204 -- Captured trusted hosted OpenSSH, fixed control options/command, random safe alias after --; genuine prompt control.
	out, err := exec.CommandContext(ctx, f.SSH, "-o", "BatchMode=no", "-o", "ControlMaster=no", "-S", "none", "--", f.Alias, "true").CombinedOutput()
	require.NoError(f.T, ctx.Err(), "prompt control deadline is not proof")
	require.Error(f.T, err, "marker-only askpass never supplies credentials")
	require.FileExists(f.T, f.Marker, "challenge did not prompt: %s", out)
	f.T.Log("BatchMode=no positive control invoked native askpass")
	f.ClearPrompt()
}

// PasswordChallenge enables only the disposable server's test user challenge,
// restores exact bytes, and verifies effective policy before returning.
func (f *Fixture) PasswordChallenge() {
	f.T.Helper()
	path := "/etc/ssh/sshd_config"
	saved := filepath.Join(f.Dir, "sshd_config")
	f.Docker("cp", f.Container+":"+path, saved)
	previous := f.Contents(saved)
	set := func(text string) {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		// #nosec G204 -- Validated owned container, constant config/reload command; exact config bytes via stdin, never shell interpolation.
		cmd := exec.CommandContext(ctx, "docker", "exec", "-i", f.Container, "sh", "-ec", "cat > /etc/ssh/sshd_config; /usr/sbin/sshd -t; kill -HUP 1")
		cmd.Stdin = strings.NewReader(text)
		require.NoError(f.T, cmd.Run())
		require.NoError(f.T, ctx.Err())
	}
	f.T.Cleanup(func() {
		set(previous)
		require.Equal(f.T, previous, f.dockerOutput("exec", f.Container, "cat", path))
		f.T.Log("restored sshd_config exact bytes and reloaded owned server")
	})
	set("PasswordAuthentication yes\n" + previous)
	require.Contains(f.T, f.Docker("exec", f.Container, "/usr/sbin/sshd", "-T"), "passwordauthentication yes")
}

// Backend registers label-checked cleanup before run; lost run replies retain a name.
func (f *Fixture) Backend(image string, port int, labels ...string) string {
	f.T.Helper()
	f.backendSequence++
	name := fmt.Sprintf("%s-%d-%d", f.Alias, port, f.backendSequence)
	f.T.Cleanup(func() {
		owner := f.Docker("inspect", "--format", `{{index .Config.Labels "rdhpf.trust"}}`, name)
		require.Equal(f.T, f.Alias, owner)
		f.Docker("rm", "-f", name)
		f.T.Logf("removed owned backend name=%s verified-label=%s", name, owner)
	})
	args := []string{"run", "-d", "--name", name, "--label", "rdhpf.trust=" + f.Alias}
	for _, label := range labels {
		args = append(args, "--label", label)
	}
	if port != 0 {
		args = append(args, "-p", fmt.Sprintf("172.17.0.1:%d:80", port))
	}
	args = append(args, image)
	if port == 0 {
		args = append(args, "sleep", "300")
	}
	id := f.Docker(args...)
	require.Regexp(f.T, `^[a-f0-9]{64}$`, id)
	return id
}
