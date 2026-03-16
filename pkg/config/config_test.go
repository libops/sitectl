package config

import (
	"os"
	"path/filepath"
	"testing"
)

func TestLoadAndSave(t *testing.T) {
	tmpDir := t.TempDir()
	oldHome := os.Getenv("HOME")
	defer os.Setenv("HOME", oldHome)
	os.Setenv("HOME", tmpDir)

	cfg := &Config{
		CurrentContext: "test-context",
		Contexts: []Context{
			{
				Name:           "test-context",
				DockerHostType: ContextLocal,
				DockerSocket:   "/var/run/docker.sock",
			},
		},
	}

	if err := Save(cfg); err != nil {
		t.Fatalf("failed to save config: %v", err)
	}

	loaded, err := Load()
	if err != nil {
		t.Fatalf("failed to load config: %v", err)
	}

	if loaded.CurrentContext != cfg.CurrentContext {
		t.Errorf("expected current context %s, got %s", cfg.CurrentContext, loaded.CurrentContext)
	}
	if len(loaded.Contexts) != len(cfg.Contexts) {
		t.Errorf("expected %d contexts, got %d", len(cfg.Contexts), len(loaded.Contexts))
	}
	if loaded.Contexts[0].Name != cfg.Contexts[0].Name {
		t.Errorf("expected context name %s, got %s", cfg.Contexts[0].Name, loaded.Contexts[0].Name)
	}
}

func TestLoadEmptyConfig(t *testing.T) {
	tmpDir := t.TempDir()
	oldHome := os.Getenv("HOME")
	defer os.Setenv("HOME", oldHome)
	os.Setenv("HOME", tmpDir)

	cfg, err := Load()
	if err != nil {
		t.Fatalf("failed to load config: %v", err)
	}
	if cfg.CurrentContext != "" {
		t.Errorf("expected empty current context, got %s", cfg.CurrentContext)
	}
	if len(cfg.Contexts) != 0 {
		t.Errorf("expected 0 contexts, got %d", len(cfg.Contexts))
	}
}

func TestConfigFilePath(t *testing.T) {
	home := os.Getenv("HOME")
	expected := filepath.Join(home, ".sitectl", "config.yaml")
	if path := ConfigFilePath(); path != expected {
		t.Errorf("expected config file path %s, got %s", expected, path)
	}
}

func TestCurrentPrefersAutodiscoveredLocalContext(t *testing.T) {
	tmpDir := t.TempDir()
	oldHome := os.Getenv("HOME")
	defer os.Setenv("HOME", oldHome)
	os.Setenv("HOME", tmpDir)

	projectDir := filepath.Join(tmpDir, "site")
	if err := os.MkdirAll(projectDir, 0o755); err != nil {
		t.Fatalf("MkdirAll(projectDir) error = %v", err)
	}

	oldWd, err := os.Getwd()
	if err != nil {
		t.Fatalf("Getwd() error = %v", err)
	}
	if err := os.Chdir(projectDir); err != nil {
		t.Fatalf("Chdir(projectDir) error = %v", err)
	}
	t.Cleanup(func() {
		_ = os.Chdir(oldWd)
	})

	cfg := &Config{
		CurrentContext: "other",
		Contexts: []Context{
			{Name: "other", DockerHostType: ContextLocal, ProjectDir: filepath.Join(tmpDir, "other")},
			{Name: "site-local", DockerHostType: ContextLocal, ProjectDir: projectDir},
		},
	}
	if err := Save(cfg); err != nil {
		t.Fatalf("Save(cfg) error = %v", err)
	}

	current, err := Current()
	if err != nil {
		t.Fatalf("Current() error = %v", err)
	}
	if current != "site-local" {
		t.Fatalf("expected autodiscovered current context site-local, got %q", current)
	}
}

func TestCurrentFallsBackToConfiguredDefaultWhenCWDDoesNotMatch(t *testing.T) {
	tmpDir := t.TempDir()
	oldHome := os.Getenv("HOME")
	defer os.Setenv("HOME", oldHome)
	os.Setenv("HOME", tmpDir)

	projectDir := filepath.Join(tmpDir, "site")
	otherDir := filepath.Join(tmpDir, "other")
	if err := os.MkdirAll(projectDir, 0o755); err != nil {
		t.Fatalf("MkdirAll(projectDir) error = %v", err)
	}
	if err := os.MkdirAll(otherDir, 0o755); err != nil {
		t.Fatalf("MkdirAll(otherDir) error = %v", err)
	}

	oldWd, err := os.Getwd()
	if err != nil {
		t.Fatalf("Getwd() error = %v", err)
	}
	if err := os.Chdir(otherDir); err != nil {
		t.Fatalf("Chdir(otherDir) error = %v", err)
	}
	t.Cleanup(func() {
		_ = os.Chdir(oldWd)
	})

	cfg := &Config{
		CurrentContext: "site-local",
		Contexts: []Context{
			{Name: "site-local", DockerHostType: ContextLocal, ProjectDir: projectDir},
		},
	}
	if err := Save(cfg); err != nil {
		t.Fatalf("Save(cfg) error = %v", err)
	}

	current, err := Current()
	if err != nil {
		t.Fatalf("Current() error = %v", err)
	}
	if current != "site-local" {
		t.Fatalf("expected configured default current context site-local, got %q", current)
	}
}
