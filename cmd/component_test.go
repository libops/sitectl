package cmd

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/libops/sitectl/pkg/config"
	"github.com/spf13/cobra"
)

func TestComponentDescribeHelpExplainsObservedDrift(t *testing.T) {
	t.Parallel()

	if strings.Contains(componentDescribeCmd.Long, "last recorded state") {
		t.Fatalf("component describe help must not imply persisted component state: %q", componentDescribeCmd.Long)
	}
	if !strings.Contains(componentDescribeCmd.Long, "does not match a complete supported disposition") {
		t.Fatalf("component describe help must define drift against supported dispositions: %q", componentDescribeCmd.Long)
	}
}

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

func TestResolveComponentSetInvocationForwardsPluginSpecificFlags(t *testing.T) {
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

	cmd := &cobra.Command{Use: "set"}
	contextName, owner, forwarded, err := resolveComponentSetInvocation(cmd, []string{
		"iiif-topology",
		"distributed",
		"--iiif-upstream-url",
		"https://iiif.example.org",
		"--yolo",
	})
	if err != nil {
		t.Fatalf("resolveComponentSetInvocation() error = %v", err)
	}
	if contextName != "museum" || owner != "isle" {
		t.Fatalf("unexpected owner resolution: context=%q owner=%q", contextName, owner)
	}
	want := []string{
		"iiif-topology",
		"distributed",
		"--iiif-upstream-url",
		"https://iiif.example.org",
		"--yolo",
	}
	if !reflect.DeepEqual(forwarded, want) {
		t.Fatalf("forwarded args = %#v, want %#v", forwarded, want)
	}
}

func TestResolveComponentSetInvocationRejectsUnknownFlagBeforeComponent(t *testing.T) {
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

	cmd := &cobra.Command{Use: "set"}
	_, _, _, err := resolveComponentSetInvocation(cmd, []string{
		"--force",
		"fcrepo",
		"off",
	})
	if err == nil {
		t.Fatal("expected component name error")
	}
	if !strings.Contains(err.Error(), "component name is required") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestResolveComponentSetInvocationStripsNamespace(t *testing.T) {
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

	cmd := &cobra.Command{Use: "set"}
	contextName, owner, forwarded, err := resolveComponentSetInvocation(cmd, []string{
		"isle/fcrepo",
		"superseded",
		"--isle-file-system-uri",
		"private",
	})
	if err != nil {
		t.Fatalf("resolveComponentSetInvocation() error = %v", err)
	}
	if contextName != "museum" || owner != "isle" {
		t.Fatalf("unexpected owner resolution: context=%q owner=%q", contextName, owner)
	}
	want := []string{"fcrepo", "superseded", "--isle-file-system-uri", "private"}
	if !reflect.DeepEqual(forwarded, want) {
		t.Fatalf("forwarded args = %#v, want %#v", forwarded, want)
	}
}

func TestComponentCodebaseRootfsFlagValue(t *testing.T) {
	tests := []struct {
		name          string
		codebaseValue string
		drupalValue   string
		setCodebase   bool
		setDrupal     bool
		want          string
		wantErr       bool
	}{
		{
			name:          "canonical flag",
			codebaseValue: "app/rootfs",
			setCodebase:   true,
			want:          "app/rootfs",
		},
		{
			name:        "deprecated alias",
			drupalValue: "drupal/rootfs",
			setDrupal:   true,
			want:        "drupal/rootfs",
		},
		{
			name:          "matching aliases",
			codebaseValue: "app/rootfs",
			drupalValue:   "app/rootfs",
			setCodebase:   true,
			setDrupal:     true,
			want:          "app/rootfs",
		},
		{
			name:          "conflicting aliases",
			codebaseValue: "app/rootfs",
			drupalValue:   "drupal/rootfs",
			setCodebase:   true,
			setDrupal:     true,
			wantErr:       true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var codebaseRootfs, drupalRootfs string
			cmd := &cobra.Command{Use: "describe"}
			cmd.Flags().StringVar(&codebaseRootfs, "codebase-rootfs", "", "")
			cmd.Flags().StringVar(&drupalRootfs, "drupal-rootfs", "", "")
			if tt.setCodebase {
				if err := cmd.Flags().Set("codebase-rootfs", tt.codebaseValue); err != nil {
					t.Fatalf("Set(codebase-rootfs) error = %v", err)
				}
			}
			if tt.setDrupal {
				if err := cmd.Flags().Set("drupal-rootfs", tt.drupalValue); err != nil {
					t.Fatalf("Set(drupal-rootfs) error = %v", err)
				}
			}

			got, err := componentCodebaseRootfsFlagValue(cmd, codebaseRootfs, drupalRootfs)
			if tt.wantErr {
				if err == nil {
					t.Fatal("expected error")
				}
				return
			}
			if err != nil {
				t.Fatalf("componentCodebaseRootfsFlagValue() error = %v", err)
			}
			if got != tt.want {
				t.Fatalf("componentCodebaseRootfsFlagValue() = %q, want %q", got, tt.want)
			}
		})
	}
}
