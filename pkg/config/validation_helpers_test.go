package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestValidateComposeAccessUsesShellProbeSemantics(t *testing.T) {
	binDir := t.TempDir()
	logPath := filepath.Join(t.TempDir(), "shell-command.txt")
	shPath := filepath.Join(binDir, "sh")
	shScript := "#!/bin/sh\nprintf '%s\\n' \"$*\" > \"$SHELL_PROBE_LOG\"\n"
	if err := os.WriteFile(shPath, []byte(shScript), 0o755); err != nil {
		t.Fatalf("WriteFile(sh) error = %v", err)
	}
	t.Setenv("PATH", binDir)
	t.Setenv("SHELL_PROBE_LOG", logPath)

	ctx := &Context{
		DockerHostType: ContextLocal,
		ProjectDir:     t.TempDir(),
		ComposeFile:    []string{"compose.yaml", "compose local.yaml"},
		EnvFile:        []string{".env"},
	}
	if err := ctx.ValidateComposeAccess(); err != nil {
		t.Fatalf("ValidateComposeAccess() error = %v", err)
	}

	data, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatalf("ReadFile(shell log) error = %v", err)
	}
	got := strings.TrimSpace(string(data))
	if !strings.HasPrefix(got, "-lc docker compose ") {
		t.Fatalf("expected shell -lc docker compose probe, got %q", got)
	}
	if !strings.Contains(got, ">/dev/null 2>&1") {
		t.Fatalf("expected shell redirection in compose probe, got %q", got)
	}
	if !strings.Contains(got, "'compose local.yaml'") {
		t.Fatalf("expected shell-quoted compose file path, got %q", got)
	}
}
