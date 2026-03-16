package cmd

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	corecomponent "github.com/libops/sitectl/pkg/component"
	"github.com/libops/sitectl/pkg/config"
	"github.com/spf13/cobra"
)

func TestRunCreateConfigAutodetectsCurrentComposeProject(t *testing.T) {
	tempHome := t.TempDir()
	t.Setenv("HOME", tempHome)

	projectDir := filepath.Join(tempHome, "museum")
	if err := os.MkdirAll(projectDir, 0o755); err != nil {
		t.Fatalf("MkdirAll(projectDir) error = %v", err)
	}
	if err := os.WriteFile(filepath.Join(projectDir, "docker-compose.yml"), []byte("services: {}\n"), 0o644); err != nil {
		t.Fatalf("WriteFile(docker-compose.yml) error = %v", err)
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

	oldInput := createConfigInput
	oldPromptChoice := createConfigPromptChoice
	oldVerifyRemote := createConfigVerifyRemote
	oldProjectDirExists := createConfigProjectDirExists
	oldRunComposePS := createConfigRunComposePS
	t.Cleanup(func() {
		createConfigInput = oldInput
		createConfigPromptChoice = oldPromptChoice
		createConfigVerifyRemote = oldVerifyRemote
		createConfigProjectDirExists = oldProjectDirExists
		createConfigRunComposePS = oldRunComposePS
	})
	createConfigVerifyRemote = func(ctx *config.Context) error { return nil }
	createConfigProjectDirExists = func(ctx *config.Context) (bool, error) { return true, nil }
	createConfigRunComposePS = func(ctx *config.Context) error { return nil }
	createConfigPromptChoice = func(name string, choices []corecomponent.Choice, defaultValue string, input corecomponent.InputFunc, sections ...string) (string, error) {
		if name == "add-environment" {
			return "no", nil
		}
		t.Fatalf("did not expect choice prompt for default autodetected local context: %s", name)
		return "", nil
	}
	createConfigInput = func(question ...string) (string, error) {
		t.Fatalf("did not expect prompt for default autodetected local context: %v", question)
		return "", nil
	}

	cmd := &cobra.Command{Use: "create"}
	cmd.Flags().Bool("default", true, "")
	var out bytes.Buffer
	cmd.SetOut(&out)

	if err := runCreateConfig(cmd, nil); err != nil {
		t.Fatalf("runCreateConfig() error = %v", err)
	}

	ctx, err := config.GetContext("museum")
	if err != nil {
		t.Fatalf("GetContext(museum) error = %v", err)
	}
	if ctx.Name != "museum" {
		t.Fatalf("expected context museum, got %q", ctx.Name)
	}
	wantProjectDir := projectDir
	if resolved, err := filepath.EvalSymlinks(projectDir); err == nil {
		wantProjectDir = resolved
	}
	if ctx.ProjectDir != wantProjectDir {
		t.Fatalf("expected project dir %q, got %q", wantProjectDir, ctx.ProjectDir)
	}
	if ctx.Environment != "local" {
		t.Fatalf("expected environment local, got %q", ctx.Environment)
	}
	if ctx.Site != "museum" {
		t.Fatalf("expected site museum, got %q", ctx.Site)
	}
	if ctx.Plugin != "core" {
		t.Fatalf("expected plugin core, got %q", ctx.Plugin)
	}
	if !strings.Contains(out.String(), "CONTEXT CREATED SUCCESSFULLY") {
		t.Fatalf("expected success output, got:\n%s", out.String())
	}
}

func TestRunCreateConfigAutodetectsCurrentComposeProjectAndAddsRemoteEnvironment(t *testing.T) {
	tempHome := t.TempDir()
	t.Setenv("HOME", tempHome)

	projectDir := filepath.Join(tempHome, "museum")
	if err := os.MkdirAll(projectDir, 0o755); err != nil {
		t.Fatalf("MkdirAll(projectDir) error = %v", err)
	}
	if err := os.WriteFile(filepath.Join(projectDir, "docker-compose.yml"), []byte("services: {}\n"), 0o644); err != nil {
		t.Fatalf("WriteFile(docker-compose.yml) error = %v", err)
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

	oldInput := createConfigInput
	oldPromptChoice := createConfigPromptChoice
	oldVerifyRemote := createConfigVerifyRemote
	oldProjectDirExists := createConfigProjectDirExists
	oldRunComposePS := createConfigRunComposePS
	t.Cleanup(func() {
		createConfigInput = oldInput
		createConfigPromptChoice = oldPromptChoice
		createConfigVerifyRemote = oldVerifyRemote
		createConfigProjectDirExists = oldProjectDirExists
		createConfigRunComposePS = oldRunComposePS
	})

	createConfigVerifyRemote = func(ctx *config.Context) error { return nil }
	createConfigProjectDirExists = func(ctx *config.Context) (bool, error) { return true, nil }
	createConfigRunComposePS = func(ctx *config.Context) error { return nil }
	choiceCalls := 0
	createConfigPromptChoice = func(name string, choices []corecomponent.Choice, defaultValue string, input corecomponent.InputFunc, sections ...string) (string, error) {
		if name != "add-environment" {
			t.Fatalf("unexpected choice prompt: %s", name)
		}
		choiceCalls++
		if choiceCalls == 1 {
			return "yes", nil
		}
		return "no", nil
	}

	prompts := []string{
		"staging",
		"",
		"/opt/museum",
		"stage.example.com",
		"deploy",
		"",
		"",
	}
	createConfigInput = func(question ...string) (string, error) {
		if len(prompts) == 0 {
			t.Fatalf("unexpected prompt: %v", question)
		}
		value := prompts[0]
		prompts = prompts[1:]
		return value, nil
	}

	cmd := &cobra.Command{Use: "create"}
	cmd.Flags().Bool("default", true, "")
	var out bytes.Buffer
	cmd.SetOut(&out)

	if err := runCreateConfig(cmd, nil); err != nil {
		t.Fatalf("runCreateConfig() error = %v", err)
	}

	localCtx, err := config.GetContext("museum")
	if err != nil {
		t.Fatalf("GetContext(museum) error = %v", err)
	}
	remoteCtx, err := config.GetContext("museum-staging")
	if err != nil {
		t.Fatalf("GetContext(museum-staging) error = %v", err)
	}
	if remoteCtx.DockerHostType != config.ContextRemote {
		t.Fatalf("expected remote context, got %q", remoteCtx.DockerHostType)
	}
	if remoteCtx.Environment != "staging" {
		t.Fatalf("expected staging environment, got %q", remoteCtx.Environment)
	}
	if remoteCtx.Site != localCtx.Site {
		t.Fatalf("expected remote site %q, got %q", localCtx.Site, remoteCtx.Site)
	}
	if remoteCtx.Plugin != localCtx.Plugin {
		t.Fatalf("expected remote plugin %q, got %q", localCtx.Plugin, remoteCtx.Plugin)
	}
	if remoteCtx.ProjectDir != "/opt/museum" {
		t.Fatalf("expected remote project dir /opt/museum, got %q", remoteCtx.ProjectDir)
	}
	if remoteCtx.ProjectName != localCtx.ProjectName {
		t.Fatalf("expected remote project name %q, got %q", localCtx.ProjectName, remoteCtx.ProjectName)
	}
	if !strings.Contains(out.String(), "ENVIRONMENT CONTEXT CREATED SUCCESSFULLY") {
		t.Fatalf("expected remote environment output, got:\n%s", out.String())
	}
}

func TestRunCreateConfigRepromptsRemoteConnectionDetailsAfterVerificationFailure(t *testing.T) {
	tempHome := t.TempDir()
	t.Setenv("HOME", tempHome)

	projectDir := filepath.Join(tempHome, "museum")
	if err := os.MkdirAll(projectDir, 0o755); err != nil {
		t.Fatalf("MkdirAll(projectDir) error = %v", err)
	}
	if err := os.WriteFile(filepath.Join(projectDir, "docker-compose.yml"), []byte("services: {}\n"), 0o644); err != nil {
		t.Fatalf("WriteFile(docker-compose.yml) error = %v", err)
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

	oldInput := createConfigInput
	oldPromptChoice := createConfigPromptChoice
	oldVerifyRemote := createConfigVerifyRemote
	oldProjectDirExists := createConfigProjectDirExists
	oldRunComposePS := createConfigRunComposePS
	t.Cleanup(func() {
		createConfigInput = oldInput
		createConfigPromptChoice = oldPromptChoice
		createConfigVerifyRemote = oldVerifyRemote
		createConfigProjectDirExists = oldProjectDirExists
		createConfigRunComposePS = oldRunComposePS
	})

	verifyCalls := 0
	createConfigVerifyRemote = func(ctx *config.Context) error {
		verifyCalls++
		if verifyCalls == 1 {
			return fmt.Errorf("ssh: handshake failed: knownhosts: key is unknown")
		}
		return nil
	}
	createConfigProjectDirExists = func(ctx *config.Context) (bool, error) { return true, nil }
	createConfigRunComposePS = func(ctx *config.Context) error { return nil }

	choiceCalls := map[string]int{}
	createConfigPromptChoice = func(name string, choices []corecomponent.Choice, defaultValue string, input corecomponent.InputFunc, sections ...string) (string, error) {
		choiceCalls[name]++
		switch name {
		case "add-environment":
			if choiceCalls[name] == 1 {
				return "yes", nil
			}
			return "no", nil
		case "retry-environment-connection":
			return "retry", nil
		default:
			t.Fatalf("unexpected choice prompt: %s", name)
			return "", nil
		}
	}

	prompts := []string{
		"staging",
		"",
		"/opt/museum",
		"bad.example.com",
		"deploy",
		"",
		"",
		"good.example.com",
		"",
		"",
		"",
	}
	createConfigInput = func(question ...string) (string, error) {
		if len(prompts) == 0 {
			t.Fatalf("unexpected prompt: %v", question)
		}
		value := prompts[0]
		prompts = prompts[1:]
		return value, nil
	}

	cmd := &cobra.Command{Use: "create"}
	cmd.Flags().Bool("default", true, "")
	var out bytes.Buffer
	cmd.SetOut(&out)

	if err := runCreateConfig(cmd, nil); err != nil {
		t.Fatalf("runCreateConfig() error = %v", err)
	}

	remoteCtx, err := config.GetContext("museum-staging")
	if err != nil {
		t.Fatalf("GetContext(museum-staging) error = %v", err)
	}
	if remoteCtx.SSHHostname != "good.example.com" {
		t.Fatalf("expected retried hostname good.example.com, got %q", remoteCtx.SSHHostname)
	}
	if remoteCtx.Site != "museum" {
		t.Fatalf("expected site museum, got %q", remoteCtx.Site)
	}
	if remoteCtx.Plugin != "core" {
		t.Fatalf("expected plugin core, got %q", remoteCtx.Plugin)
	}
	if choiceCalls["retry-environment-connection"] != 1 {
		t.Fatalf("expected one retry prompt, got %d", choiceCalls["retry-environment-connection"])
	}
}

func TestRunCreateConfigRepromptsDockerSettingsAfterComposePSFailure(t *testing.T) {
	tempHome := t.TempDir()
	t.Setenv("HOME", tempHome)

	projectDir := filepath.Join(tempHome, "museum")
	if err := os.MkdirAll(projectDir, 0o755); err != nil {
		t.Fatalf("MkdirAll(projectDir) error = %v", err)
	}
	if err := os.WriteFile(filepath.Join(projectDir, "docker-compose.yml"), []byte("services: {}\n"), 0o644); err != nil {
		t.Fatalf("WriteFile(docker-compose.yml) error = %v", err)
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

	oldInput := createConfigInput
	oldPromptChoice := createConfigPromptChoice
	oldVerifyRemote := createConfigVerifyRemote
	oldProjectDirExists := createConfigProjectDirExists
	oldRunComposePS := createConfigRunComposePS
	t.Cleanup(func() {
		createConfigInput = oldInput
		createConfigPromptChoice = oldPromptChoice
		createConfigVerifyRemote = oldVerifyRemote
		createConfigProjectDirExists = oldProjectDirExists
		createConfigRunComposePS = oldRunComposePS
	})

	createConfigVerifyRemote = func(ctx *config.Context) error { return nil }
	createConfigProjectDirExists = func(ctx *config.Context) (bool, error) { return true, nil }
	composeCalls := 0
	createConfigRunComposePS = func(ctx *config.Context) error {
		composeCalls++
		if composeCalls == 1 {
			if ctx.DockerSocket != "/var/run/docker.sock" {
				t.Fatalf("expected default remote docker socket /var/run/docker.sock, got %q", ctx.DockerSocket)
			}
			return fmt.Errorf("cannot connect to docker daemon")
		}
		if ctx.DockerSocket != "/run/user/1000/docker.sock" {
			t.Fatalf("expected updated docker socket, got %q", ctx.DockerSocket)
		}
		if !ctx.RunSudo {
			t.Fatal("expected updated run sudo true")
		}
		if ctx.ProjectName != "museum-prod" {
			t.Fatalf("expected updated project name museum-prod, got %q", ctx.ProjectName)
		}
		return nil
	}

	choiceCalls := map[string]int{}
	createConfigPromptChoice = func(name string, choices []corecomponent.Choice, defaultValue string, input corecomponent.InputFunc, sections ...string) (string, error) {
		choiceCalls[name]++
		switch name {
		case "add-environment":
			if choiceCalls[name] == 1 {
				return "yes", nil
			}
			return "no", nil
		case "update-environment-context":
			return "update", nil
		case "run-docker-commands-with-sudo":
			return "yes", nil
		default:
			t.Fatalf("unexpected choice prompt: %s", name)
			return "", nil
		}
	}

	prompts := []string{
		"prod",
		"",
		"/opt/museum",
		"prod.example.com",
		"deploy",
		"",
		"",
		"",
		"museum-prod",
		"/run/user/1000/docker.sock",
	}
	createConfigInput = func(question ...string) (string, error) {
		if len(prompts) == 0 {
			t.Fatalf("unexpected prompt: %v", question)
		}
		value := prompts[0]
		prompts = prompts[1:]
		return value, nil
	}

	cmd := &cobra.Command{Use: "create"}
	cmd.Flags().Bool("default", true, "")

	if err := runCreateConfig(cmd, nil); err != nil {
		t.Fatalf("runCreateConfig() error = %v", err)
	}

	remoteCtx, err := config.GetContext("museum-prod")
	if err != nil {
		t.Fatalf("GetContext(museum-prod) error = %v", err)
	}
	if remoteCtx.DockerSocket != "/run/user/1000/docker.sock" {
		t.Fatalf("expected saved docker socket /run/user/1000/docker.sock, got %q", remoteCtx.DockerSocket)
	}
	if remoteCtx.Site != "museum" {
		t.Fatalf("expected saved site museum, got %q", remoteCtx.Site)
	}
	if remoteCtx.Plugin != "core" {
		t.Fatalf("expected saved plugin core, got %q", remoteCtx.Plugin)
	}
	if !remoteCtx.RunSudo {
		t.Fatal("expected saved run sudo true")
	}
}

func TestInheritNewContextDefaultsFromActive(t *testing.T) {
	cmd := &cobra.Command{Use: "create"}
	flags := cmd.Flags()
	config.SetCommandFlags(flags)

	target, err := config.LoadFromFlags(flags, config.Context{})
	if err != nil {
		t.Fatalf("LoadFromFlags() error = %v", err)
	}
	active := &config.Context{
		Name:                   "museum-local",
		Site:                   "museum",
		Plugin:                 "isle",
		ProjectName:            "museum",
		ComposeFile:            []string{"docker-compose.yml", "docker-compose.local.yml"},
		EnvFile:                []string{".env", ".env.local"},
		DatabaseService:        "postgres",
		DatabaseUser:           "museum",
		DatabasePasswordSecret: "DB_PASSWORD",
		DatabaseName:           "museum",
	}

	inheritNewContextDefaultsFromActive(target, active, flags)

	if target.Site != "museum" {
		t.Fatalf("expected inherited site museum, got %q", target.Site)
	}
	if target.Plugin != "isle" {
		t.Fatalf("expected inherited plugin isle, got %q", target.Plugin)
	}
	if target.ProjectName != "museum" {
		t.Fatalf("expected inherited project name museum, got %q", target.ProjectName)
	}
	if strings.Join(target.ComposeFile, ",") != "docker-compose.yml,docker-compose.local.yml" {
		t.Fatalf("expected inherited compose files, got %#v", target.ComposeFile)
	}
	if strings.Join(target.EnvFile, ",") != ".env,.env.local" {
		t.Fatalf("expected inherited env files, got %#v", target.EnvFile)
	}
	if target.DatabaseService != "postgres" || target.DatabaseUser != "museum" || target.DatabasePasswordSecret != "DB_PASSWORD" || target.DatabaseName != "museum" {
		t.Fatalf("expected inherited database settings, got %#v", target)
	}
}
