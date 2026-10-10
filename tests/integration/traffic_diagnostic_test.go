package integration

import (
	"context"
	"os"
	"os/exec"
	"testing"
	"time"

	"github.com/tomaszpeksa/remote-docker-host-port-forwarder/internal/ssh"
)

// Capture only after the original traffic assertions, before removal destroys
// application evidence. Later probe success cannot establish earlier readiness.
func captureTrafficDiagnostic(t *testing.T, containerID, host string) {
	t.Helper()
	fixture := os.Getenv("TRAFFIC_DIAGNOSTIC_FIXTURE")
	if fixture == "" {
		return
	}
	control, err := ssh.DeriveControlPath(host)
	t.Logf("TRAFFIC-DIAG capture-start utc=%s failed=%t app=%s fixture=%s control=%s control_error=%v",
		time.Now().UTC().Format(time.RFC3339Nano), t.Failed(), containerID, fixture, control, err)
	commands := []struct {
		name string
		argv []string
	}{
		{"original-app-log", []string{"docker", "logs", "--timestamps", "--tail", "250", containerID}},
		{"original-sshd-log", []string{"docker", "logs", "--timestamps", "--tail", "500", fixture}},
		{"app-state", []string{"docker", "inspect", "--format", "id={{.Id}} state={{json .State}} bindings={{json .HostConfig.PortBindings}} ports={{json .NetworkSettings.Ports}} networks={{json .NetworkSettings.Networks}} image={{.Image}}", containerID}},
		{"fixture-state", []string{"docker", "inspect", "--format", "id={{.Id}} state={{json .State}} ports={{json .NetworkSettings.Ports}} networks={{json .NetworkSettings.Networks}} image={{.Image}}", fixture}},
		{"images", []string{"docker", "image", "inspect", "--format", "id={{.Id}} digests={{json .RepoDigests}}", "nginx:alpine", "rdhpf-sshd-stub"}},
		{"runner-listener", []string{"ss", "-ltnp", "sport = :18080"}},
		{"runner-control", []string{"ssh", "-S", control, "-O", "check", "-p", "2222", "testuser@localhost"}},
		{"runner-route", []string{"ip", "route"}},
		{"fixture-route-hosts", []string{"docker", "exec", fixture, "sh", "-c", "date -u; cat /etc/hosts; ip route"}},
		{"probe-runner-publication", []string{"curl", "--noproxy", "*", "--max-time", "2", "-sv", "-A", "rdhpf-diagnostic-runner", "http://172.17.0.1:18080/"}},
		{"probe-fixture-localhost", []string{"docker", "exec", fixture, "wget", "-S", "-O", "-", "-T", "2", "-U", "rdhpf-diagnostic-fixture-localhost", "http://localhost:18080/"}},
		{"probe-fixture-gateway", []string{"docker", "exec", fixture, "sh", "-c", `gateway=$(ip route show default | awk '/default/ {print $3; exit}'); echo "diagnostic gateway=$gateway"; test -n "$gateway" && exec wget -S -O - -T 2 -U rdhpf-diagnostic-fixture-gateway "http://$gateway:18080/"`}},
		{"probe-app-local", []string{"docker", "exec", containerID, "wget", "-S", "-O", "-", "-T", "2", "-U", "rdhpf-diagnostic-app-local", "http://127.0.0.1:80/"}},
		{"post-probe-app-log", []string{"docker", "logs", "--timestamps", "--tail", "250", containerID}},
	}
	for _, command := range commands {
		// Three seconds for the process, at most one more for inherited pipes.
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		started := time.Now().UTC()
		cmd := exec.CommandContext(ctx, command.argv[0], command.argv[1:]...)
		cmd.WaitDelay = time.Second
		output, err := cmd.CombinedOutput()
		cancel()
		t.Logf("TRAFFIC-DIAG name=%s utc=%s argv=%q elapsed=%s error=%v output=\n%s",
			command.name, started.Format(time.RFC3339Nano), command.argv, time.Since(started), err, output)
	}
	t.Logf("TRAFFIC-DIAG capture-end utc=%s", time.Now().UTC().Format(time.RFC3339Nano))
}
