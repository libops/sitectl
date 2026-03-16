package config

import (
	"os"
	"path/filepath"
	"testing"
)

func TestEnsureTrackedComposeOverrideSymlinkCreatesRuntimeSymlink(t *testing.T) {
	projectDir := t.TempDir()
	tracked := filepath.Join(projectDir, "docker-compose.local.yml")
	if err := os.WriteFile(tracked, []byte("services: {}\n"), 0o644); err != nil {
		t.Fatalf("WriteFile(tracked) error = %v", err)
	}

	ctx := Context{
		ProjectDir:  projectDir,
		Environment: "local",
	}
	if err := ctx.EnsureTrackedComposeOverrideSymlink(); err != nil {
		t.Fatalf("EnsureTrackedComposeOverrideSymlink() error = %v", err)
	}

	runtimePath := filepath.Join(projectDir, RuntimeComposeOverrideName)
	target, err := os.Readlink(runtimePath)
	if err != nil {
		t.Fatalf("Readlink(runtime) error = %v", err)
	}
	if target != "docker-compose.local.yml" {
		t.Fatalf("expected symlink target docker-compose.local.yml, got %q", target)
	}
}

func TestEnsureTrackedComposeOverrideSymlinkRemovesMissingTrackedLink(t *testing.T) {
	projectDir := t.TempDir()
	runtimePath := filepath.Join(projectDir, RuntimeComposeOverrideName)
	if err := os.Symlink("docker-compose.local.yml", runtimePath); err != nil {
		t.Fatalf("Symlink(runtime) error = %v", err)
	}

	ctx := Context{
		ProjectDir:  projectDir,
		Environment: "local",
	}
	if err := ctx.EnsureTrackedComposeOverrideSymlink(); err != nil {
		t.Fatalf("EnsureTrackedComposeOverrideSymlink() error = %v", err)
	}

	if _, err := os.Lstat(runtimePath); !os.IsNotExist(err) {
		t.Fatalf("expected runtime symlink removed, got err=%v", err)
	}
}
