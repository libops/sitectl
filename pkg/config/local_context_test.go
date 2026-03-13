package config

import (
	"os"
	"path/filepath"
	"testing"
)

func TestPromptAndSaveLocalContextUsesProvidedValues(t *testing.T) {
	t.Parallel()

	tempHome := t.TempDir()
	t.Setenv("HOME", tempHome)

	oldWd, err := os.Getwd()
	if err != nil {
		t.Fatalf("Getwd() error = %v", err)
	}
	projectDir := filepath.Join(tempHome, "project")
	if err := os.MkdirAll(projectDir, 0o755); err != nil {
		t.Fatalf("MkdirAll(projectDir) error = %v", err)
	}
	if err := os.Chdir(projectDir); err != nil {
		t.Fatalf("Chdir(projectDir) error = %v", err)
	}
	t.Cleanup(func() {
		_ = os.Chdir(oldWd)
	})

	ctx, err := PromptAndSaveLocalContext(LocalContextCreateOptions{
		Name:       "isle-local",
		ProjectDir: projectDir,
		SetDefault: true,
		Input: func(question ...string) (string, error) {
			t.Fatal("did not expect prompt")
			return "", nil
		},
	})
	if err != nil {
		t.Fatalf("PromptAndSaveLocalContext() error = %v", err)
	}

	if ctx.Name != "isle-local" {
		t.Fatalf("expected name isle-local, got %q", ctx.Name)
	}
	if ctx.DockerHostType != ContextLocal {
		t.Fatalf("expected local context, got %q", ctx.DockerHostType)
	}
	if ctx.ProjectDir != projectDir {
		t.Fatalf("expected project dir %q, got %q", projectDir, ctx.ProjectDir)
	}
	if ctx.ProjectName != "docker-compose" {
		t.Fatalf("expected default project name docker-compose, got %q", ctx.ProjectName)
	}
}

func TestPromptAndSaveLocalContextPromptsForMissingValues(t *testing.T) {
	t.Parallel()

	tempHome := t.TempDir()
	t.Setenv("HOME", tempHome)

	oldWd, err := os.Getwd()
	if err != nil {
		t.Fatalf("Getwd() error = %v", err)
	}
	if err := os.Chdir(tempHome); err != nil {
		t.Fatalf("Chdir(tempHome) error = %v", err)
	}
	t.Cleanup(func() {
		_ = os.Chdir(oldWd)
	})

	prompts := []string{"site-a", filepath.Join(tempHome, "site-a")}
	ctx, err := PromptAndSaveLocalContext(LocalContextCreateOptions{
		DefaultName: "default-site",
		Input: func(question ...string) (string, error) {
			value := prompts[0]
			prompts = prompts[1:]
			return value, nil
		},
	})
	if err != nil {
		t.Fatalf("PromptAndSaveLocalContext() error = %v", err)
	}

	if ctx.Name != "site-a" {
		t.Fatalf("expected prompted name site-a, got %q", ctx.Name)
	}
	if ctx.ProjectDir != filepath.Join(tempHome, "site-a") {
		t.Fatalf("expected prompted project dir, got %q", ctx.ProjectDir)
	}
}

func TestPromptAndSaveLocalContextExpandsTildeInProvidedProjectDir(t *testing.T) {
	t.Parallel()

	tempHome := t.TempDir()
	t.Setenv("HOME", tempHome)

	ctx, err := PromptAndSaveLocalContext(LocalContextCreateOptions{
		Name:       "isle-local-home",
		ProjectDir: "~/sites/isle",
		Input: func(question ...string) (string, error) {
			t.Fatal("did not expect prompt")
			return "", nil
		},
	})
	if err != nil {
		t.Fatalf("PromptAndSaveLocalContext() error = %v", err)
	}

	expected := filepath.Join(tempHome, "sites", "isle")
	if ctx.ProjectDir != expected {
		t.Fatalf("expected project dir %q, got %q", expected, ctx.ProjectDir)
	}
}

func TestPromptAndSaveLocalContextExpandsTildeInPromptedProjectDir(t *testing.T) {
	t.Parallel()

	tempHome := t.TempDir()
	t.Setenv("HOME", tempHome)

	oldWd, err := os.Getwd()
	if err != nil {
		t.Fatalf("Getwd() error = %v", err)
	}
	if err := os.Chdir(tempHome); err != nil {
		t.Fatalf("Chdir(tempHome) error = %v", err)
	}
	t.Cleanup(func() {
		_ = os.Chdir(oldWd)
	})

	prompts := []string{"site-home", "~/sites/site-home"}
	ctx, err := PromptAndSaveLocalContext(LocalContextCreateOptions{
		DefaultName: "default-site",
		Input: func(question ...string) (string, error) {
			value := prompts[0]
			prompts = prompts[1:]
			return value, nil
		},
	})
	if err != nil {
		t.Fatalf("PromptAndSaveLocalContext() error = %v", err)
	}

	expected := filepath.Join(tempHome, "sites", "site-home")
	if ctx.ProjectDir != expected {
		t.Fatalf("expected project dir %q, got %q", expected, ctx.ProjectDir)
	}
}

func TestPromptAndSaveLocalContextDeclinesOverwrite(t *testing.T) {
	t.Parallel()

	tempHome := t.TempDir()
	t.Setenv("HOME", tempHome)

	projectDir := filepath.Join(tempHome, "project")
	if err := os.MkdirAll(projectDir, 0o755); err != nil {
		t.Fatalf("MkdirAll(projectDir) error = %v", err)
	}

	if err := SaveContext(&Context{
		Name:           "existing",
		DockerHostType: ContextLocal,
		DockerSocket:   "/var/run/docker.sock",
		ProjectDir:     projectDir,
	}, false); err != nil {
		t.Fatalf("SaveContext(existing) error = %v", err)
	}

	_, err := PromptAndSaveLocalContext(LocalContextCreateOptions{
		Name:             "existing",
		ProjectDir:       projectDir,
		ConfirmOverwrite: true,
		Input: func(question ...string) (string, error) {
			return "n", nil
		},
	})
	if err == nil {
		t.Fatal("expected overwrite cancellation error")
	}
}

func TestPromptAndSaveLocalContextUsesNextAvailableDefaultName(t *testing.T) {
	t.Parallel()

	tempHome := t.TempDir()
	t.Setenv("HOME", tempHome)

	projectDir := filepath.Join(tempHome, "project")
	if err := os.MkdirAll(projectDir, 0o755); err != nil {
		t.Fatalf("MkdirAll(projectDir) error = %v", err)
	}

	if err := SaveContext(&Context{
		Name:           "isle-local",
		DockerHostType: ContextLocal,
		DockerSocket:   "/var/run/docker.sock",
		ProjectDir:     projectDir,
	}, false); err != nil {
		t.Fatalf("SaveContext(existing) error = %v", err)
	}

	ctx, err := PromptAndSaveLocalContext(LocalContextCreateOptions{
		DefaultName:      "isle-local",
		DefaultProjectDir: projectDir,
		Input: func(question ...string) (string, error) {
			return "", nil
		},
	})
	if err != nil {
		t.Fatalf("PromptAndSaveLocalContext() error = %v", err)
	}
	if ctx.Name != "isle-local-2" {
		t.Fatalf("expected auto-suffixed context name, got %q", ctx.Name)
	}
}
