package plugin

import (
	"context"
	"io"
	"strings"
	"testing"

	"github.com/libops/sitectl/pkg/config"
)

func TestRunComposeProjectCommandContextHonorsRemoteComposeFiles(t *testing.T) {
	original := runComposeProjectRemoteShellCommandContext
	t.Cleanup(func() { runComposeProjectRemoteShellCommandContext = original })

	var gotCommand string
	runComposeProjectRemoteShellCommandContext = func(_ context.Context, _ *config.Context, _, _ io.Writer, command string) (string, error) {
		gotCommand = command
		return "", nil
	}
	ctx := &config.Context{
		DockerHostType: config.ContextRemote,
		ProjectDir:     "/srv/app",
		ComposeFile:    []string{"compose.yaml", "compose production.yaml"},
		EnvFile:        []string{".env"},
	}
	sdk := &SDK{}
	if err := sdk.RunComposeProjectCommandContext(context.Background(), ctx, ctx.ProjectDir, io.Discard, io.Discard, "docker compose ps"); err != nil {
		t.Fatalf("RunComposeProjectCommandContext() error = %v", err)
	}
	for _, expected := range []string{
		"cd '/srv/app' && docker compose",
		"-f /srv/app/compose.yaml",
		"-f '/srv/app/compose production.yaml'",
		"--env-file /srv/app/.env",
		"ps",
	} {
		if !strings.Contains(gotCommand, expected) {
			t.Fatalf("remote command = %q, want %q", gotCommand, expected)
		}
	}
}
