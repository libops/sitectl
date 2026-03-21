package cmd

import (
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

	owner, name, err := resolveComponentOwner(cmd, "drupal/modules")
	if err != nil {
		t.Fatalf("resolveComponentOwner() error = %v", err)
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

	owner, name, err := resolveComponentOwner(cmd, "fcrepo")
	if err != nil {
		t.Fatalf("resolveComponentOwner() error = %v", err)
	}
	if owner != "isle" || name != "fcrepo" {
		t.Fatalf("unexpected owner/name: %q %q", owner, name)
	}
}
