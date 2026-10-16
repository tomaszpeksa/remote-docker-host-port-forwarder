//go:build rdhpf_integration

package integration

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"github.com/tomaszpeksa/remote-docker-host-port-forwarder/internal/docker"
	"github.com/tomaszpeksa/remote-docker-host-port-forwarder/internal/ssh"
	"github.com/tomaszpeksa/remote-docker-host-port-forwarder/internal/testutil/sshfixture"
)

func TestSSHTrust(t *testing.T) {
	for _, name := range []string{"UserPinMatching", "UserPinMismatch", "DefaultUnknown", "ConfiguredAcceptNew", "GlobalOnlyMatching", "GlobalOnlyMismatch", "AlternateAuthority", "ExplicitWeakUnknown", "EncryptedIdentityNoPrompt", "PasswordNoPrompt"} {
		t.Run(name, func(t *testing.T) {
			f := sshfixture.New(t)
			policy, userKey, globalKey, identity := "yes", f.PublicKey, "", f.Key
			success := false
			switch name {
			case "UserPinMatching":
				success = true
			case "UserPinMismatch":
				userKey = f.WrongKey
			case "DefaultUnknown":
				policy = ""
				userKey = ""
			case "ConfiguredAcceptNew":
				policy = "accept-new"
				userKey = ""
				success = true
			case "GlobalOnlyMatching":
				userKey = ""
				globalKey = f.PublicKey
				success = true
			case "GlobalOnlyMismatch":
				userKey = ""
				globalKey = f.WrongKey
			case "AlternateAuthority":
				userKey = f.WrongKey
				globalKey = f.PublicKey
				success = true
			case "ExplicitWeakUnknown":
				policy = "no"
				userKey = ""
				success = true
			case "EncryptedIdentityNoPrompt":
				identity = f.EncryptedKey
			case "PasswordNoPrompt":
				identity = filepath.Join(f.Dir, "no-identity")
				f.PasswordChallenge()
			}
			f.Configure(policy, userKey, globalKey, identity, "no")
			// Application authentication must use configured IdentityFile, never
			// this fixture-only variable. A usable alternate key must not bypass
			// the encrypted-key/password negative cases either.
			conflictingIdentity := f.Key
			if identity == f.Key {
				conflictingIdentity = f.EncryptedKey
			}
			t.Setenv("SSH_TEST_KEY_PATH", conflictingIdentity)
			if name == "DefaultUnknown" || name == "EncryptedIdentityNoPrompt" || name == "PasswordNoPrompt" {
				f.PromptControl()
			}
			beforeUser, beforeGlobal := f.Contents(f.UserHosts), f.Contents(f.GlobalHosts)
			port := 0
			body := ""
			if success {
				listener, err := net.Listen("tcp4", "127.0.0.1:0")
				require.NoError(t, err)
				port = listener.Addr().(*net.TCPAddr).Port
				require.NoError(t, listener.Close())
				id := f.Backend(sshfixture.Nginx, port)
				body = f.Alias
				f.Docker("exec", id, "sh", "-c", "printf %s "+body+" > /usr/share/nginx/html/index.html")
				ready, cancel := context.WithTimeout(context.Background(), 5*time.Second)
				require.NoError(t, waitForNginxBackend(ready, "ssh://testuser@localhost:2222", id))
				cancel()
			}
			connect := func(wantSuccess bool) {
				ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
				defer cancel()
				m, err := ssh.NewMaster(f.Host, slog.Default())
				require.NoError(t, err)
				defer func() { require.NoError(t, m.Close()) }()
				err = m.Open(ctx)
				require.NoError(t, ctx.Err(), "outer deadline is not nonprompt proof")
				if !wantSuccess {
					require.Error(t, err)
					f.NoPrompt()
					return
				}
				require.NoError(t, err)
				require.NoError(t, ssh.AddForward(ctx, m.ControlPath(), f.Host, port, port, slog.Default()))
				client := &http.Client{Timeout: 2 * time.Second, Transport: &http.Transport{Proxy: nil}}
				response, err := client.Get(fmt.Sprintf("http://127.0.0.1:%d/", port))
				require.NoError(t, err)
				data, err := io.ReadAll(response.Body)
				require.NoError(t, err)
				require.NoError(t, response.Body.Close())
				client.CloseIdleConnections()
				require.Equal(t, http.StatusOK, response.StatusCode)
				require.Equal(t, body, string(data))
				f.NoPrompt()
				t.Logf("authenticated unique HTTP body=%s port=%d", body, port)
			}
			connect(success)
			if name == "ConfiguredAcceptNew" || name == "ExplicitWeakUnknown" {
				enrolled := f.Contents(f.UserHosts)
				require.Contains(t, enrolled, f.PublicKey)
				connect(true)
				require.Equal(t, enrolled, f.Contents(f.UserHosts))
				if name == "ConfiguredAcceptNew" {
					require.NoError(t, os.WriteFile(f.UserHosts, []byte("[127.0.0.1]:2222 "+f.WrongKey+"\n"), 0600))
					connect(false)
				}
			} else {
				require.Equal(t, beforeUser, f.Contents(f.UserHosts))
			}
			require.Equal(t, beforeGlobal, f.Contents(f.GlobalHosts))
		})
	}
}

func TestDockerMissingMuxTrust(t *testing.T) {
	for _, operation := range []string{"ports", "labels", "events"} {
		for _, scenario := range []string{"matching", "mismatch", "unknown", "accept-new", "encrypted", "password"} {
			t.Run(operation+"/"+scenario, func(t *testing.T) {
				f := sshfixture.New(t)
				policy, key, identity := "yes", f.PublicKey, f.Key
				success := scenario == "matching" || scenario == "accept-new"
				switch scenario {
				case "mismatch":
					key = f.WrongKey
				case "unknown":
					policy = ""
					key = ""
				case "accept-new":
					policy = "accept-new"
					key = ""
				case "encrypted":
					identity = f.EncryptedKey
				case "password":
					identity = filepath.Join(f.Dir, "no-identity")
					f.PasswordChallenge()
				}
				f.Configure(policy, key, "", identity, "no")
				if scenario == "unknown" || scenario == "encrypted" || scenario == "password" {
					f.PromptControl()
				}
				before := f.Contents(f.UserHosts)
				id := f.Backend(sshfixture.Alpine, 0, "rdhpf.forward.18081=80")
				ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
				defer cancel()
				missing := filepath.Join(f.Dir, "absent-mux")
				switch operation {
				case "ports":
					// Real nonempty published-port data, not an empty parser success.
					published := f.Backend(sshfixture.Nginx, 18082)
					ports, err := docker.InspectPorts(ctx, f.Host, missing, published)
					if success {
						require.NoError(t, err)
						require.Equal(t, []int{18082}, ports)
					} else {
						require.Error(t, err)
					}
				case "labels":
					t.Setenv("RDHPF_ENABLE_LABEL_PORTS", "1")
					ports, err := docker.InspectPorts(ctx, f.Host, missing, id)
					if success {
						require.NoError(t, err)
						require.Equal(t, []int{18081}, ports)
					} else {
						require.Error(t, err)
					}
				case "events":
					events, errs := docker.NewEventReader(f.Host, missing, slog.Default()).Stream(ctx)
					if success {
						// Start a second owned container only after the real stream is listening.
						// docker events --since is not added to the production command.
						time.Sleep(300 * time.Millisecond)
						// This owned sleep container is only an event trigger. PID1
						// can ignore TERM; do not spend Docker's default 10s grace
						// period inside the 8s event-observation deadline.
						f.Docker("stop", "--time", "0", id)
						f.Docker("start", id)
						matched := false
						for !matched {
							select {
							case e, ok := <-events:
								require.True(t, ok)
								matched = e.ContainerID == id
							case err := <-errs:
								require.NoError(t, err)
							case <-ctx.Done():
								t.Fatal("owned event not observed")
							}
						}
					} else {
						select {
						case err := <-errs:
							require.Error(t, err)
						case <-ctx.Done():
							t.Fatal("rejection did not terminate")
						}
					}
					require.NoError(t, ctx.Err())
					cancel()
					for range events {
					}
					for range errs {
					}
				}
				if operation != "events" {
					require.NoError(t, ctx.Err())
				}
				f.NoPrompt()
				if scenario == "accept-new" {
					require.Contains(t, f.Contents(f.UserHosts), f.PublicKey)
				} else {
					require.Equal(t, before, f.Contents(f.UserHosts))
				}
			})
		}
	}
}
