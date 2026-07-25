package cmd

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/libops/sitectl/pkg/config"
	"github.com/spf13/cobra"
)

func newSetContextReviewTestCommand(t *testing.T) *cobra.Command {
	t.Helper()
	cmd := &cobra.Command{Use: "set-context"}
	config.SetCommandFlags(cmd.Flags())
	cmd.Flags().Bool("default", false, "Use as default context.")
	cmd.Flags().Bool("yolo", false, "Skip review.")
	return cmd
}

func TestReviewSetContextStartsWithPathAndUsesFlagAsReviewedDefault(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	cmd := newSetContextReviewTestCommand(t)
	if err := cmd.Flags().Set("project-dir", "/srv/museum"); err != nil {
		t.Fatal(err)
	}
	if err := cmd.Flags().Set("site", "collections"); err != nil {
		t.Fatal(err)
	}

	var prompts []string
	ctx, _, err := reviewSetContext(cmd, "museum-local", func(question ...string) (string, error) {
		prompts = append(prompts, strings.Join(question, "\n"))
		return "", nil
	})
	if err != nil {
		t.Fatalf("reviewSetContext() error = %v", err)
	}
	if len(prompts) == 0 || !strings.Contains(strings.ToLower(prompts[0]), "working path") {
		t.Fatalf("first prompt does not establish the working path: %v", prompts)
	}
	if ctx.ProjectDir != "/srv/museum" || ctx.Site != "collections" {
		t.Fatalf("reviewed context did not preserve flag defaults: %+v", ctx)
	}
}

func TestReviewSetContextYoloUsesExistingValuesWithoutPrompt(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	if err := config.SaveContext(&config.Context{
		Name:               "museum-prod",
		Site:               "museum",
		Plugin:             "isle",
		DockerHostType:     config.ContextRemote,
		Environment:        "prod",
		DockerSocket:       "/var/run/docker.sock",
		ComposeProjectName: "museum",
		ComposeNetwork:     "museum_default",
		ProjectDir:         "/srv/museum",
		SSHHostname:        "museum.example.org",
		SSHUser:            "deploy",
		SSHPort:            22,
		SSHKeyPath:         "/keys/deploy",
	}, false); err != nil {
		t.Fatal(err)
	}
	cmd := newSetContextReviewTestCommand(t)
	if err := cmd.Flags().Set("yolo", "true"); err != nil {
		t.Fatal(err)
	}

	ctx, _, err := reviewSetContext(cmd, "museum-prod", func(question ...string) (string, error) {
		t.Fatalf("--yolo prompted for %v", question)
		return "", nil
	})
	if err != nil {
		t.Fatalf("reviewSetContext() error = %v", err)
	}
	if ctx.ProjectDir != "/srv/museum" || ctx.SSHHostname != "museum.example.org" || ctx.Environment != "prod" {
		t.Fatalf("existing context defaults were not retained: %+v", ctx)
	}
}

func TestReviewSetContextYoloMapsDeprecatedProjectNameFlag(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	cmd := newSetContextReviewTestCommand(t)
	for name, value := range map[string]string{
		"project-dir":  "/srv/museum",
		"project-name": "museum",
		"yolo":         "true",
	} {
		if err := cmd.Flags().Set(name, value); err != nil {
			t.Fatalf("Set(%s) error = %v", name, err)
		}
	}

	ctx, setDefault, err := reviewSetContext(cmd, "museum-local", func(question ...string) (string, error) {
		t.Fatalf("--yolo prompted for %v", question)
		return "", nil
	})
	if err != nil {
		t.Fatalf("reviewSetContext() error = %v", err)
	}
	if ctx.Site != "museum" || ctx.ComposeProjectName != "museum" {
		t.Fatalf("deprecated project name was not mapped to canonical fields: %+v", ctx)
	}
	if !setDefault {
		t.Fatal("--yolo did not automatically select the first saved context as default")
	}
}

func TestReviewSetContextDetectsComposeIdentityFromWorkingPath(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	projectDir := t.TempDir()
	compose := "name: detected-project\nservices: {}\nnetworks:\n  default:\n    name: shared-network\n"
	if err := os.WriteFile(filepath.Join(projectDir, "compose.yaml"), []byte(compose), 0o644); err != nil {
		t.Fatal(err)
	}
	cmd := newSetContextReviewTestCommand(t)
	if err := cmd.Flags().Set("project-dir", projectDir); err != nil {
		t.Fatal(err)
	}

	ctx, _, err := reviewSetContext(cmd, "museum-local", func(...string) (string, error) {
		return "", nil
	})
	if err != nil {
		t.Fatalf("reviewSetContext() error = %v", err)
	}
	if ctx.ComposeProjectName != "detected-project" || ctx.ComposeNetwork != "shared-network" {
		t.Fatalf("compose identity was not detected from working path: %+v", ctx)
	}
}
