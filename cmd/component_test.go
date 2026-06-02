package cmd

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/libops/sitectl/pkg/config"
	"github.com/spf13/cobra"
)

func TestSplitNamespacedComponent(t *testing.T) {
	pluginName, componentName, ok := splitNamespacedComponent("isle/fcrepo")
	if !ok {
		t.Fatal("expected namespaced component to parse")
	}
	if pluginName != "isle" || componentName != "fcrepo" {
		t.Fatalf("unexpected parse result: %q %q", pluginName, componentName)
	}
}

func TestResolveComponentOwnerUsesNamespace(t *testing.T) {
	tempHome := t.TempDir()
	t.Setenv("HOME", tempHome)

	if err := config.SaveContext(&config.Context{
		Name:           "museum",
		Site:           "museum",
		Plugin:         "isle",
		DockerHostType: config.ContextLocal,
		DockerSocket:   "/var/run/docker.sock",
		ProjectDir:     tempHome,
	}, true); err != nil {
		t.Fatalf("SaveContext() error = %v", err)
	}

	cmd := &cobra.Command{Use: "describe"}
	cmd.Flags().String("context", "", "")
	if err := cmd.Flags().Set("context", "museum"); err != nil {
		t.Fatalf("Set(context) error = %v", err)
	}

	contextName, owner, name, err := resolveComponentOwner(cmd, "drupal/modules")
	if err != nil {
		t.Fatalf("resolveComponentOwner() error = %v", err)
	}
	if contextName != "museum" {
		t.Fatalf("unexpected context name: %q", contextName)
	}
	if owner != "drupal" || name != "modules" {
		t.Fatalf("unexpected owner/name: %q %q", owner, name)
	}
}

func TestResolveComponentOwnerFallsBackToContextPlugin(t *testing.T) {
	tempHome := t.TempDir()
	t.Setenv("HOME", tempHome)

	if err := config.SaveContext(&config.Context{
		Name:           "museum",
		Site:           "museum",
		Plugin:         "isle",
		DockerHostType: config.ContextLocal,
		DockerSocket:   "/var/run/docker.sock",
		ProjectDir:     tempHome,
	}, true); err != nil {
		t.Fatalf("SaveContext() error = %v", err)
	}

	cmd := &cobra.Command{Use: "describe"}
	cmd.Flags().String("context", "", "")
	if err := cmd.Flags().Set("context", "museum"); err != nil {
		t.Fatalf("Set(context) error = %v", err)
	}

	contextName, owner, name, err := resolveComponentOwner(cmd, "fcrepo")
	if err != nil {
		t.Fatalf("resolveComponentOwner() error = %v", err)
	}
	if contextName != "museum" {
		t.Fatalf("unexpected context name: %q", contextName)
	}
	if owner != "isle" || name != "fcrepo" {
		t.Fatalf("unexpected owner/name: %q %q", owner, name)
	}
}

func TestResolveComponentOwnerUsesCWDClaim(t *testing.T) {
	tempHome := t.TempDir()
	t.Setenv("HOME", tempHome)

	projectDir := filepath.Join(tempHome, "site")
	nestedDir := filepath.Join(projectDir, "web", "modules")
	if err := os.MkdirAll(nestedDir, 0o755); err != nil {
		t.Fatalf("MkdirAll(nestedDir) error = %v", err)
	}
	if err := os.WriteFile(filepath.Join(projectDir, "docker-compose.yml"), []byte("services:\n  drupal:\n    image: drupal:latest\n"), 0o600); err != nil {
		t.Fatalf("WriteFile(docker-compose.yml) error = %v", err)
	}

	previousDetector := config.SetProjectClaimDetector(func(projectDir, requestedPlugin string) (*config.ProjectClaim, error) {
		if requestedPlugin != "" && requestedPlugin != "isle" {
			return nil, nil
		}
		return &config.ProjectClaim{Plugin: "isle", ProjectDir: projectDir, Reason: "test claim"}, nil
	})
	t.Cleanup(func() {
		config.SetProjectClaimDetector(previousDetector)
	})

	oldWD, err := os.Getwd()
	if err != nil {
		t.Fatalf("Getwd() error = %v", err)
	}
	if err := os.Chdir(nestedDir); err != nil {
		t.Fatalf("Chdir(nestedDir) error = %v", err)
	}
	t.Cleanup(func() {
		_ = os.Chdir(oldWD)
	})

	if err := config.SaveContext(&config.Context{
		Name:           "museum",
		Site:           "museum",
		Plugin:         "isle",
		DockerHostType: config.ContextLocal,
		DockerSocket:   "/var/run/docker.sock",
		ProjectDir:     projectDir,
	}, false); err != nil {
		t.Fatalf("SaveContext() error = %v", err)
	}
	if err := config.SaveContext(&config.Context{
		Name:           "default-ojs",
		Site:           "journal",
		Plugin:         "ojs",
		DockerHostType: config.ContextLocal,
		DockerSocket:   "/var/run/docker.sock",
		ProjectDir:     filepath.Join(tempHome, "ojs"),
	}, true); err != nil {
		t.Fatalf("SaveContext(default) error = %v", err)
	}

	cmd := &cobra.Command{Use: "set"}
	contextName, owner, name, err := resolveComponentOwner(cmd, "iiif")
	if err != nil {
		t.Fatalf("resolveComponentOwner() error = %v", err)
	}
	if contextName != "museum" {
		t.Fatalf("expected cwd context museum, got %q", contextName)
	}
	if owner != "isle" || name != "iiif" {
		t.Fatalf("unexpected owner/name: %q %q", owner, name)
	}
}

func TestResolveComponentOwnerUsesTransientCWDClaim(t *testing.T) {
	tempHome := t.TempDir()
	t.Setenv("HOME", tempHome)

	projectDir := filepath.Join(tempHome, "site")
	if err := os.MkdirAll(projectDir, 0o755); err != nil {
		t.Fatalf("MkdirAll(projectDir) error = %v", err)
	}
	if err := os.WriteFile(filepath.Join(projectDir, "docker-compose.yml"), []byte("services:\n  drupal:\n    image: drupal:latest\n"), 0o600); err != nil {
		t.Fatalf("WriteFile(docker-compose.yml) error = %v", err)
	}

	previousDetector := config.SetProjectClaimDetector(func(projectDir, requestedPlugin string) (*config.ProjectClaim, error) {
		return &config.ProjectClaim{Plugin: "isle", ProjectDir: projectDir, Reason: "test claim"}, nil
	})
	t.Cleanup(func() {
		config.SetProjectClaimDetector(previousDetector)
	})

	oldWD, err := os.Getwd()
	if err != nil {
		t.Fatalf("Getwd() error = %v", err)
	}
	if err := os.Chdir(projectDir); err != nil {
		t.Fatalf("Chdir(projectDir) error = %v", err)
	}
	t.Cleanup(func() {
		_ = os.Chdir(oldWD)
	})

	cmd := &cobra.Command{Use: "set"}
	contextName, owner, name, err := resolveComponentOwner(cmd, "iiif")
	if err != nil {
		t.Fatalf("resolveComponentOwner() error = %v", err)
	}
	if contextName != "." {
		t.Fatalf("expected transient cwd context '.', got %q", contextName)
	}
	if owner != "isle" || name != "iiif" {
		t.Fatalf("unexpected owner/name: %q %q", owner, name)
	}
}
