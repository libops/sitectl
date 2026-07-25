package config

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"strings"
	"testing"
)

type testRemoteExitError struct {
	status int
}

func (e *testRemoteExitError) Error() string {
	return fmt.Sprintf("Process exited with status %d", e.status)
}

func (e *testRemoteExitError) ExitStatus() int {
	return e.status
}

func TestRunCommandLocal(t *testing.T) {
	ctx := &Context{
		DockerHostType: ContextLocal,
	}
	cmd := exec.Command("echo", "hello")
	output, err := ctx.RunCommand(cmd)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(output) == 0 || !strings.Contains(output, "hello") {
		t.Fatalf("expected output to contain 'hello', got %v", output)
	}
}

func TestRunCommandLocalPreservesCommandEnv(t *testing.T) {
	ctx := &Context{
		DockerHostType: ContextLocal,
	}
	cmd := exec.Command("bash", "-lc", "printf %s \"$SITECTL_TEST_VALUE\"")
	cmd.Env = append(os.Environ(), "SITECTL_TEST_VALUE=preserved")
	output, err := ctx.RunCommand(cmd)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if output != "preserved" {
		t.Fatalf("expected preserved env, got %q", output)
	}
}

func TestRunCommandRemoteSudoUnsupported(t *testing.T) {
	ctx := &Context{
		DockerHostType: ContextRemote,
		SSHUser:        "deploy",
		SSHHostname:    "example.org",
	}

	_, err := ctx.RunCommand(exec.Command("docker", "ps"))
	if err == nil {
		t.Fatal("expected remote ssh error")
	}
	if !strings.Contains(err.Error(), "error establishing SSH connection") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestRemoteCommandWaitErrorDoesNotTreatExit130AsSuccess(t *testing.T) {
	exitErr := &testRemoteExitError{status: 130}
	err := remoteCommandWaitError(context.Background(), "docker compose exec app migrate", exitErr)
	if err == nil {
		t.Fatal("expected remote exit status 130 to remain an error")
	}
	if !errors.Is(err, exitErr) {
		t.Fatalf("remoteCommandWaitError() = %v, want wrapped exit error", err)
	}
}

func TestRemoteCommandWaitErrorReportsContextCancellation(t *testing.T) {
	runCtx, cancel := context.WithCancel(context.Background())
	cancel()
	err := remoteCommandWaitError(runCtx, "docker compose up", errors.New("EOF"))
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("remoteCommandWaitError() = %v, want context cancellation", err)
	}
}

func TestRedactCommandForLog(t *testing.T) {
	t.Parallel()

	command := `docker compose run --rm -e DB_PASSWORD="open sesame" -e API_TOKEN=abc123 app migrate --password hunter2 --private-key /keys/deploy`
	got := redactCommandForLog(command)

	for _, secret := range []string{"open sesame", "abc123", "hunter2"} {
		if strings.Contains(got, secret) {
			t.Fatalf("redacted command contains %q: %s", secret, got)
		}
	}
	for _, teachingDetail := range []string{"docker compose run --rm", "DB_PASSWORD=<redacted>", "API_TOKEN=<redacted>", "--password <redacted>", "--private-key <redacted>"} {
		if !strings.Contains(got, teachingDetail) {
			t.Fatalf("redacted command does not contain %q: %s", teachingDetail, got)
		}
	}
}

func TestRedactCommandForLogRetainsSecretReferencesAndKeyPaths(t *testing.T) {
	t.Parallel()

	command := `docker compose --env-file .env config --secret database-password-secret --ssh-key /keys/deploy`
	if got := redactCommandForLog(command); got != command {
		t.Fatalf("redactCommandForLog() = %q, want %q", got, command)
	}
}

func TestLogDockerComposeCommandLogsTeachingContextAndRedactsCredentials(t *testing.T) {
	var output bytes.Buffer
	original := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&output, &slog.HandlerOptions{Level: slog.LevelInfo})))
	t.Cleanup(func() { slog.SetDefault(original) })

	LogDockerComposeCommand(&Context{
		Name:           "museum-prod",
		DockerHostType: ContextRemote,
		ProjectDir:     "/srv/museum",
	}, `docker compose run -e API_TOKEN=secret app migrate`)

	got := output.String()
	for _, want := range []string{"Running Docker Compose command", "docker compose run", "museum-prod", "/srv/museum", "API_TOKEN=<redacted>"} {
		if !strings.Contains(got, want) {
			t.Fatalf("Compose teaching log missing %q: %s", want, got)
		}
	}
	if strings.Contains(got, "API_TOKEN=secret") {
		t.Fatalf("Compose teaching log leaked credential: %s", got)
	}
}
