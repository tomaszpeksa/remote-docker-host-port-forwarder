package integration

import (
	"context"
	"os"
	"os/exec"
	"testing"
	"time"
)

// captureS01Diagnostic runs only during application cleanup, after the original
// assertions, so no active probe delays or rescues their first traffic attempt.
func captureS01Diagnostic(t *testing.T, containerID string) {
	t.Helper()
	fixture := os.Getenv("S01_DIAGNOSTIC_FIXTURE")
	t.Logf("S01DIAG capture-start utc=%s failed=%t container=%s fixture=%s", time.Now().UTC().Format(time.RFC3339Nano), t.Failed(), containerID, fixture)
	commands := [][]string{
		{"inspect", "--format", "state={{json .State}} ports={{json .NetworkSettings.Ports}} image={{.Image}}", containerID},
		{"logs", "--timestamps", "--tail", "200", containerID},
		{"logs", "--timestamps", "--tail", "300", fixture},
		{"exec", fixture, "sh", "-c", "date -u; cat /etc/hosts; ip route"},
		{"exec", fixture, "wget", "-S", "-O", "-", "-T", "2", "http://localhost:18080/"},
		{"exec", fixture, "wget", "-S", "-O", "-", "-T", "2", "http://172.17.0.1:18080/"},
		{"exec", containerID, "wget", "-S", "-O", "-", "-T", "2", "http://127.0.0.1:80/"},
		{"logs", "--timestamps", "--tail", "200", containerID},
	}
	for _, args := range commands {
		ctx, cancel := context.WithTimeout(context.Background(), 4*time.Second)
		started := time.Now().UTC()
		output, err := exec.CommandContext(ctx, "docker", args...).CombinedOutput()
		cancel()
		t.Logf("S01DIAG utc=%s args=%q elapsed=%s error=%v output=\n%s", started.Format(time.RFC3339Nano), args, time.Since(started), err, output)
	}
	t.Logf("S01DIAG capture-end utc=%s", time.Now().UTC().Format(time.RFC3339Nano))
}
