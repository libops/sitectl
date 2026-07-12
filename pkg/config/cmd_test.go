package config

import (
	"context"
	"errors"
	"fmt"
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
