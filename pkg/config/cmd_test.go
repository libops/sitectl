package config

import (
	"os/exec"
	"strings"
	"testing"
)

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
