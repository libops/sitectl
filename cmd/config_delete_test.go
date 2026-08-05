package cmd

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/libops/sitectl/pkg/config"
	"github.com/spf13/cobra"
)

func TestDeleteContextWithProjectRequiresTwoTypedConfirmations(t *testing.T) {
	restoreDeleteContextTestHooks(t)
	home := t.TempDir()
	t.Setenv("HOME", home)
	projectDir := filepath.Join(t.TempDir(), "museum")
	if err := os.MkdirAll(projectDir, 0o755); err != nil {
		t.Fatal(err)
	}
	saveDeleteContextTestConfig(t, &config.Config{
		CurrentContext: "museum",
		Contexts: []config.Context{
			{Name: "museum", DockerHostType: config.ContextLocal, ProjectDir: projectDir},
			{Name: "archive", DockerHostType: config.ContextLocal, ProjectDir: filepath.Join(t.TempDir(), "archive")},
		},
	})

	answers := []string{"delete", "wipe museum"}
	var prompts []string
	deleteContextInput = func(lines ...string) (string, error) {
		prompts = append(prompts, strings.Join(lines, "\n"))
		answer := answers[0]
		answers = answers[1:]
		return answer, nil
	}
	composeCalls := 0
	deleteContextRunComposeDown = func(_ *cobra.Command, ctx *config.Context) error {
		composeCalls++
		if ctx.ProjectDir != projectDir {
			t.Fatalf("compose project dir = %q, want %q", ctx.ProjectDir, projectDir)
		}
		return nil
	}
	deleteContextRemoveProject = os.RemoveAll

	var output bytes.Buffer
	cmd := &cobra.Command{}
	cmd.SetOut(&output)
	if err := runDeleteContextCommand(cmd, "museum", true); err != nil {
		t.Fatalf("runDeleteContextCommand() error = %v", err)
	}
	if len(prompts) != 2 {
		t.Fatalf("confirmation prompts = %d, want 2", len(prompts))
	}
	if !strings.Contains(prompts[0], `Type "delete"`) {
		t.Fatalf("record prompt did not require exact delete token:\n%s", prompts[0])
	}
	if !strings.Contains(prompts[1], "docker compose down -v") || !strings.Contains(prompts[1], `Type "wipe museum"`) {
		t.Fatalf("project prompt did not describe teardown and exact wipe token:\n%s", prompts[1])
	}
	if composeCalls != 1 {
		t.Fatalf("compose teardown calls = %d, want 1", composeCalls)
	}
	if _, err := os.Stat(projectDir); !os.IsNotExist(err) {
		t.Fatalf("project directory still exists or stat failed unexpectedly: %v", err)
	}
	cfg, err := config.Load()
	if err != nil {
		t.Fatal(err)
	}
	if len(cfg.Contexts) != 1 || cfg.Contexts[0].Name != "archive" {
		t.Fatalf("remaining contexts = %#v, want archive", cfg.Contexts)
	}
	if cfg.CurrentContext != "archive" {
		t.Fatalf("current context = %q, want archive", cfg.CurrentContext)
	}
	if !strings.Contains(output.String(), "Deleted local project directory") {
		t.Fatalf("output did not report project deletion:\n%s", output.String())
	}
}

func TestDeleteContextProjectCancellationLeavesEverythingUntouched(t *testing.T) {
	restoreDeleteContextTestHooks(t)
	t.Setenv("HOME", t.TempDir())
	projectDir := filepath.Join(t.TempDir(), "museum")
	if err := os.MkdirAll(projectDir, 0o755); err != nil {
		t.Fatal(err)
	}
	saveDeleteContextTestConfig(t, &config.Config{
		CurrentContext: "archive",
		Contexts: []config.Context{
			{Name: "museum", DockerHostType: config.ContextLocal, ProjectDir: projectDir},
			{Name: "archive", DockerHostType: config.ContextLocal},
		},
	})

	answers := []string{"delete", "no"}
	deleteContextInput = func(_ ...string) (string, error) {
		answer := answers[0]
		answers = answers[1:]
		return answer, nil
	}
	deleteContextRunComposeDown = func(_ *cobra.Command, _ *config.Context) error {
		t.Fatal("compose teardown ran after project deletion was cancelled")
		return nil
	}
	deleteContextRemoveProject = func(string) error {
		t.Fatal("project deletion ran after confirmation was cancelled")
		return nil
	}

	err := runDeleteContextCommand(&cobra.Command{}, "museum", true)
	if err == nil || !strings.Contains(err.Error(), "cancelled") {
		t.Fatalf("error = %v, want cancellation", err)
	}
	assertDeleteTestContextExists(t, "museum")
	if _, err := os.Stat(projectDir); err != nil {
		t.Fatalf("project directory changed after cancellation: %v", err)
	}
}

func TestDeleteRemoteContextNeverTouchesItsProjectPath(t *testing.T) {
	restoreDeleteContextTestHooks(t)
	t.Setenv("HOME", t.TempDir())
	localCollision := filepath.Join(t.TempDir(), "remote-path")
	if err := os.MkdirAll(localCollision, 0o755); err != nil {
		t.Fatal(err)
	}
	saveDeleteContextTestConfig(t, &config.Config{
		CurrentContext: "prod",
		Contexts: []config.Context{{
			Name:           "prod",
			DockerHostType: config.ContextRemote,
			ProjectDir:     localCollision,
		}},
	})

	promptCount := 0
	deleteContextInput = func(_ ...string) (string, error) {
		promptCount++
		return "delete", nil
	}
	deleteContextRunComposeDown = func(_ *cobra.Command, _ *config.Context) error {
		t.Fatal("remote context ran local Compose teardown")
		return nil
	}
	deleteContextRemoveProject = func(string) error {
		t.Fatal("remote context deleted a local path")
		return nil
	}

	if err := runDeleteContextCommand(&cobra.Command{}, "prod", true); err != nil {
		t.Fatalf("runDeleteContextCommand() error = %v", err)
	}
	if promptCount != 1 {
		t.Fatalf("remote confirmation prompts = %d, want only record confirmation", promptCount)
	}
	if _, err := os.Stat(localCollision); err != nil {
		t.Fatalf("local path matching remote project changed: %v", err)
	}
	cfg, err := config.Load()
	if err != nil {
		t.Fatal(err)
	}
	if len(cfg.Contexts) != 0 || cfg.CurrentContext != "" {
		t.Fatalf("config after remote deletion = %#v", cfg)
	}
}

func TestDeleteContextRefusesUnsafeOrSharedProjectDirectory(t *testing.T) {
	tests := []struct {
		name      string
		configure func(t *testing.T, home string) (*config.Config, string)
		wantError string
	}{
		{
			name: "home directory",
			configure: func(t *testing.T, home string) (*config.Config, string) {
				return &config.Config{Contexts: []config.Context{{Name: "site", DockerHostType: config.ContextLocal, ProjectDir: home}}}, home
			},
			wantError: "home directory",
		},
		{
			name: "shared directory",
			configure: func(t *testing.T, _ string) (*config.Config, string) {
				dir := filepath.Join(t.TempDir(), "shared")
				if err := os.MkdirAll(dir, 0o755); err != nil {
					t.Fatal(err)
				}
				return &config.Config{Contexts: []config.Context{
					{Name: "site", DockerHostType: config.ContextLocal, ProjectDir: dir},
					{Name: "other", DockerHostType: config.ContextLocal, ProjectDir: dir},
				}}, dir
			},
			wantError: "also uses it",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			restoreDeleteContextTestHooks(t)
			home := t.TempDir()
			t.Setenv("HOME", home)
			cfg, projectDir := tt.configure(t, home)
			saveDeleteContextTestConfig(t, cfg)
			deleteContextInput = func(_ ...string) (string, error) { return "delete", nil }
			deleteContextRunComposeDown = func(_ *cobra.Command, _ *config.Context) error {
				t.Fatal("unsafe project reached Compose teardown")
				return nil
			}
			deleteContextRemoveProject = func(string) error {
				t.Fatal("unsafe project reached directory deletion")
				return nil
			}

			err := runDeleteContextCommand(&cobra.Command{}, "site", true)
			if err == nil || !strings.Contains(err.Error(), tt.wantError) {
				t.Fatalf("error = %v, want substring %q", err, tt.wantError)
			}
			assertDeleteTestContextExists(t, "site")
			if _, statErr := os.Stat(projectDir); statErr != nil {
				t.Fatalf("guarded project directory changed: %v", statErr)
			}
		})
	}
}

func TestDeleteContextRestoresRecordWhenComposeTeardownFails(t *testing.T) {
	restoreDeleteContextTestHooks(t)
	t.Setenv("HOME", t.TempDir())
	projectDir := filepath.Join(t.TempDir(), "museum")
	if err := os.MkdirAll(projectDir, 0o755); err != nil {
		t.Fatal(err)
	}
	saveDeleteContextTestConfig(t, &config.Config{
		CurrentContext: "museum",
		Contexts: []config.Context{{
			Name:           "museum",
			DockerHostType: config.ContextLocal,
			ProjectDir:     projectDir,
		}},
	})
	answers := []string{"delete", "wipe museum"}
	deleteContextInput = func(_ ...string) (string, error) {
		answer := answers[0]
		answers = answers[1:]
		return answer, nil
	}
	deleteContextRunComposeDown = func(_ *cobra.Command, _ *config.Context) error {
		return errors.New("docker unavailable")
	}
	deleteContextRemoveProject = func(string) error {
		t.Fatal("project directory deleted after Compose teardown failed")
		return nil
	}

	err := runDeleteContextCommand(&cobra.Command{}, "museum", true)
	if err == nil || !strings.Contains(err.Error(), "docker unavailable") {
		t.Fatalf("error = %v, want Compose failure", err)
	}
	assertDeleteTestContextExists(t, "museum")
	if _, statErr := os.Stat(projectDir); statErr != nil {
		t.Fatalf("project directory changed after Compose failure: %v", statErr)
	}
}

func TestDeleteContextRefusesPathSwapAfterComposeTeardown(t *testing.T) {
	restoreDeleteContextTestHooks(t)
	t.Setenv("HOME", t.TempDir())
	projectDir := filepath.Join(t.TempDir(), "museum")
	outsideDir := filepath.Join(t.TempDir(), "outside")
	if err := os.MkdirAll(projectDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(outsideDir, 0o755); err != nil {
		t.Fatal(err)
	}
	sentinel := filepath.Join(outsideDir, "keep.txt")
	if err := os.WriteFile(sentinel, []byte("keep"), 0o600); err != nil {
		t.Fatal(err)
	}
	saveDeleteContextTestConfig(t, &config.Config{Contexts: []config.Context{{
		Name:           "museum",
		DockerHostType: config.ContextLocal,
		ProjectDir:     projectDir,
	}}})
	answers := []string{"delete", "wipe museum"}
	deleteContextInput = func(_ ...string) (string, error) {
		answer := answers[0]
		answers = answers[1:]
		return answer, nil
	}
	deleteContextRunComposeDown = func(_ *cobra.Command, _ *config.Context) error {
		if err := os.RemoveAll(projectDir); err != nil {
			return err
		}
		return os.Symlink(outsideDir, projectDir)
	}
	deleteContextRemoveProject = func(string) error {
		t.Fatal("directory removal ran after the confirmed path was replaced")
		return nil
	}

	err := runDeleteContextCommand(&cobra.Command{}, "museum", true)
	if err == nil || !strings.Contains(err.Error(), "revalidate project directory") {
		t.Fatalf("error = %v, want path revalidation failure", err)
	}
	assertDeleteTestContextExists(t, "museum")
	if data, readErr := os.ReadFile(sentinel); readErr != nil || string(data) != "keep" {
		t.Fatalf("outside directory was changed: data=%q err=%v", data, readErr)
	}
}

func restoreDeleteContextTestHooks(t *testing.T) {
	t.Helper()
	oldInput := deleteContextInput
	oldRunComposeDown := deleteContextRunComposeDown
	oldRemoveProject := deleteContextRemoveProject
	t.Cleanup(func() {
		deleteContextInput = oldInput
		deleteContextRunComposeDown = oldRunComposeDown
		deleteContextRemoveProject = oldRemoveProject
	})
}

func saveDeleteContextTestConfig(t *testing.T, cfg *config.Config) {
	t.Helper()
	if err := config.Save(cfg); err != nil {
		t.Fatalf("save test config: %v", err)
	}
}

func assertDeleteTestContextExists(t *testing.T, name string) {
	t.Helper()
	if _, err := config.GetContext(name); err != nil {
		t.Fatalf("context %q was not preserved: %v", name, err)
	}
}
