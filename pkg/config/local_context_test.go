package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestPromptAndSaveLocalContextUsesProvidedValues(t *testing.T) {
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
	if ctx.Site != "docker-compose" {
		t.Fatalf("expected default site docker-compose, got %q", ctx.Site)
	}
	if ctx.Plugin != "core" {
		t.Fatalf("expected default plugin core, got %q", ctx.Plugin)
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
	if ctx.Environment != "local" {
		t.Fatalf("expected default environment local, got %q", ctx.Environment)
	}
}

func TestPromptAndSaveLocalContextPromptsForMissingValues(t *testing.T) {
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

	prompts := []string{filepath.Join(tempHome, "site-a")}
	ctx, err := PromptAndSaveLocalContext(LocalContextCreateOptions{
		DefaultName: "site-a",
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
		t.Fatalf("expected defaulted name site-a, got %q", ctx.Name)
	}
	if ctx.Site != "docker-compose" {
		t.Fatalf("expected default site docker-compose, got %q", ctx.Site)
	}
	if ctx.Plugin != "core" {
		t.Fatalf("expected default plugin core, got %q", ctx.Plugin)
	}
	if ctx.ProjectDir != filepath.Join(tempHome, "site-a") {
		t.Fatalf("expected prompted project dir, got %q", ctx.ProjectDir)
	}
}

func TestPromptAndSaveLocalContextExpandsTildeInProvidedProjectDir(t *testing.T) {
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

	prompts := []string{"~/sites/site-home"}
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
		DefaultName:       "isle-local",
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

func TestPromptAndSaveLocalContextPromptsForNameWhenDefaultTaken(t *testing.T) {
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

	prompts := []string{"isle-local-custom", projectDir}
	ctx, err := PromptAndSaveLocalContext(LocalContextCreateOptions{
		DefaultName: "isle-local",
		Input: func(question ...string) (string, error) {
			value := prompts[0]
			prompts = prompts[1:]
			return value, nil
		},
	})
	if err != nil {
		t.Fatalf("PromptAndSaveLocalContext() error = %v", err)
	}

	if ctx.Name != "isle-local-custom" {
		t.Fatalf("expected prompted name isle-local-custom, got %q", ctx.Name)
	}
}

func TestPromptAndSaveLocalContextRepromptsForInvalidProjectDir(t *testing.T) {
	tempHome := t.TempDir()
	t.Setenv("HOME", tempHome)

	invalidDir := filepath.Join(tempHome, "occupied")
	if err := os.MkdirAll(invalidDir, 0o755); err != nil {
		t.Fatalf("MkdirAll(invalidDir) error = %v", err)
	}
	if err := os.WriteFile(filepath.Join(invalidDir, "docker-compose.yml"), []byte("services: {}\n"), 0o644); err != nil {
		t.Fatalf("WriteFile(docker-compose.yml) error = %v", err)
	}

	validDir := filepath.Join(tempHome, "empty")
	var prompts [][]string
	inputs := []string{invalidDir, validDir}
	ctx, err := PromptAndSaveLocalContext(LocalContextCreateOptions{
		DefaultName: "site-a",
		Input: func(question ...string) (string, error) {
			prompts = append(prompts, append([]string{}, question...))
			value := inputs[0]
			inputs = inputs[1:]
			return value, nil
		},
	})
	if err != nil {
		t.Fatalf("PromptAndSaveLocalContext() error = %v", err)
	}

	if ctx.ProjectDir != validDir {
		t.Fatalf("expected valid project dir %q, got %q", validDir, ctx.ProjectDir)
	}
	if len(prompts) != 2 {
		t.Fatalf("expected 2 prompts, got %d", len(prompts))
	}
	if len(prompts[1]) < 1 || !strings.HasPrefix(prompts[1][0], "Directory validation failed:") {
		t.Fatalf("expected validation message before retry, got %#v", prompts[1])
	}
}

func TestValidateExistingComposeProjectDirAcceptsComposeProject(t *testing.T) {
	projectDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(projectDir, "docker-compose.yml"), []byte("services: {}\n"), 0o644); err != nil {
		t.Fatalf("WriteFile(docker-compose.yml) error = %v", err)
	}

	if err := ValidateExistingComposeProjectDir(projectDir); err != nil {
		t.Fatalf("ValidateExistingComposeProjectDir() error = %v", err)
	}
}
