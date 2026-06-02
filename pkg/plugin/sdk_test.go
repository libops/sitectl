package plugin

import (
	"bytes"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/spf13/cobra"

	"github.com/libops/sitectl/pkg/config"
	"github.com/libops/sitectl/pkg/validate"
)

func TestGetContextAllowsIncludedPlugin(t *testing.T) {
	tempHome := t.TempDir()
	t.Setenv("HOME", tempHome)

	ctx := config.Context{
		Name:           "museum",
		Site:           "museum",
		Plugin:         "isle",
		DockerHostType: config.ContextLocal,
		Environment:    "local",
		DockerSocket:   "/var/run/docker.sock",
		ProjectName:    "museum",
		ProjectDir:     tempHome,
	}
	if err := config.SaveContext(&ctx, true); err != nil {
		t.Fatalf("SaveContext() error = %v", err)
	}

	sdk := NewSDK(Metadata{Name: "drupal"})
	sdk.Config.Context = "museum"

	got, err := sdk.GetContext()
	if err != nil {
		t.Fatalf("GetContext() error = %v", err)
	}
	if got.Plugin != "isle" {
		t.Fatalf("expected context plugin isle, got %q", got.Plugin)
	}
}

func TestGetContextSupportsDotContext(t *testing.T) {
	tempHome := t.TempDir()
	t.Setenv("HOME", tempHome)

	projectDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(projectDir, "docker-compose.yml"), []byte("services:\n  drupal:\n    image: drupal:latest\n"), 0o600); err != nil {
		t.Fatalf("WriteFile(docker-compose.yml) error = %v", err)
	}

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

	previousDetector := config.SetProjectClaimDetector(nil)
	sdk := NewSDK(Metadata{Name: "isle"})
	t.Cleanup(func() {
		config.SetProjectClaimDetector(previousDetector)
	})
	sdk.SetProjectDiscovery(func(projectDir string) (*config.ProjectClaim, error) {
		return &config.ProjectClaim{Plugin: "isle", ProjectDir: projectDir, Reason: "test claim"}, nil
	})
	sdk.Config.Context = "."

	got, err := sdk.GetContext()
	if err != nil {
		t.Fatalf("GetContext() error = %v", err)
	}
	if got.Name != "." || got.Plugin != "isle" || got.ProjectDir != projectDir || !got.Ephemeral {
		t.Fatalf("unexpected transient context: %+v", got)
	}
}

func TestGetContextRejectsUnsupportedPlugin(t *testing.T) {
	tempHome := t.TempDir()
	t.Setenv("HOME", tempHome)

	ctx := config.Context{
		Name:           "museum",
		Site:           "museum",
		Plugin:         "drupal",
		DockerHostType: config.ContextLocal,
		Environment:    "local",
		DockerSocket:   "/var/run/docker.sock",
		ProjectName:    "museum",
		ProjectDir:     tempHome,
	}
	if err := config.SaveContext(&ctx, true); err != nil {
		t.Fatalf("SaveContext() error = %v", err)
	}

	sdk := NewSDK(Metadata{Name: "isle"})
	sdk.Config.Context = "museum"

	if _, err := sdk.GetContext(); err == nil {
		t.Fatal("expected plugin compatibility error")
	}
}

func TestContextPluginSupportsBuiltinHierarchy(t *testing.T) {
	if !ContextPluginSupports("isle", "drupal") {
		t.Fatal("expected isle contexts to support drupal")
	}
	if ContextPluginSupports("drupal", "isle") {
		t.Fatal("did not expect drupal contexts to support isle")
	}
}

func TestPluginIncludesMergesBuiltinAndInstalledWithoutDuplicates(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))

	script := `#!/bin/sh
if [ "$1" = "__plugin-metadata" ]; then
  cat <<'YAML'
name: isle
includes:
  - drupal
  - libops
YAML
  exit 0
fi
if [ "$1" = "create" ] && [ "$2" = "--help" ]; then
  exit 0
fi
exit 1
`
	writePluginScript(t, dir, "sitectl-isle", script)

	got := pluginIncludes("isle")
	want := []string{"drupal", "libops"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("pluginIncludes() = %v, want %v", got, want)
	}
}

func TestInvokePluginCommandCapturePassesContextAndLogLevel(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("COLUMNS", "123")

	script := `#!/bin/sh
if [ "$1" = "__plugin-metadata" ]; then
  cat <<'YAML'
name: child
YAML
  exit 0
fi
if [ "$1" = "create" ] && [ "$2" = "--help" ]; then
  exit 1
fi
printf 'ARGS=%s\n' "$*"
printf 'COLUMNS=%s\n' "$COLUMNS"
`
	writePluginScript(t, dir, "sitectl-child", script)

	sdk := NewSDK(Metadata{Name: "isle"})
	sdk.Config.Context = "demo"
	sdk.Config.LogLevel = "DEBUG"

	out, err := sdk.InvokePluginCommand("child", []string{"__debug", "--verbose"}, CommandExecOptions{Capture: true})
	if err != nil {
		t.Fatalf("InvokePluginCommand() error = %v", err)
	}
	if !strings.Contains(out, "ARGS=--context demo --log-level DEBUG __debug --verbose") {
		t.Fatalf("expected context/log-level args in output, got %q", out)
	}
	if !strings.Contains(out, "COLUMNS=123") {
		t.Fatalf("expected COLUMNS env in output, got %q", out)
	}
}

func TestInvokePluginCommandCaptureReturnsStderrDetail(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))

	script := `#!/bin/sh
if [ "$1" = "__plugin-metadata" ]; then
  cat <<'YAML'
name: broken
YAML
  exit 0
fi
echo "something went wrong" >&2
exit 2
`
	writePluginScript(t, dir, "sitectl-broken", script)

	sdk := NewSDK(Metadata{Name: "isle"})
	_, err := sdk.InvokePluginCommand("broken", []string{"__debug"}, CommandExecOptions{Capture: true})
	if err == nil {
		t.Fatal("expected InvokePluginCommand() error")
	}
	if !strings.Contains(err.Error(), "something went wrong") {
		t.Fatalf("expected stderr detail in error, got %v", err)
	}
}

func TestInvokePluginCommandCaptureCanMirrorLiveStderr(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))

	script := `#!/bin/sh
if [ "$1" = "__plugin-metadata" ]; then
  echo "Name: noisy"
  exit 0
fi
echo "visible stderr" >&2
echo "stdout payload"
`
	writePluginScript(t, dir, "sitectl-noisy", script)

	sdk := NewSDK(Metadata{Name: "isle"})
	var stderr bytes.Buffer
	out, err := sdk.InvokePluginCommand("noisy", []string{"__debug"}, CommandExecOptions{
		Capture:    true,
		LiveStderr: true,
		Stderr:     &stderr,
	})
	if err != nil {
		t.Fatalf("InvokePluginCommand() error = %v", err)
	}
	if !strings.Contains(stderr.String(), "visible stderr") {
		t.Fatalf("expected mirrored stderr, got %q", stderr.String())
	}
	if !strings.Contains(out, "stdout payload") {
		t.Fatalf("expected stdout payload, got %q", out)
	}
}

func TestInvokeIncludedPluginCommandRejectsUnincludedPlugin(t *testing.T) {
	sdk := NewSDK(Metadata{Name: "isle", Includes: []string{"drupal"}})

	_, err := sdk.InvokeIncludedPluginCommand("libops", []string{"__debug"}, CommandExecOptions{Capture: true})
	if err == nil {
		t.Fatal("expected included plugin validation error")
	}
	if !strings.Contains(err.Error(), `is not included by "isle"`) {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestInvokeIncludedPluginsCollectsTrimmedOutputs(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))

	writePluginScript(t, dir, "sitectl-drupal", `#!/bin/sh
if [ "$1" = "__plugin-metadata" ]; then
  cat <<'YAML'
name: drupal
YAML
  exit 0
fi
echo "  drupal output  "
`)
	writePluginScript(t, dir, "sitectl-libops", `#!/bin/sh
if [ "$1" = "__plugin-metadata" ]; then
  cat <<'YAML'
name: libops
YAML
  exit 0
fi
echo ""
`)

	sdk := NewSDK(Metadata{Name: "isle", Includes: []string{"drupal", "libops"}})
	outputs, err := sdk.InvokeIncludedPlugins([]string{"__debug"})
	if err != nil {
		t.Fatalf("InvokeIncludedPlugins() error = %v", err)
	}
	want := []string{"drupal output"}
	if !reflect.DeepEqual(outputs, want) {
		t.Fatalf("InvokeIncludedPlugins() = %v, want %v", outputs, want)
	}
}

func TestContextValidatorsReturnsCopy(t *testing.T) {
	sdk := NewSDK(Metadata{Name: "isle"})
	first := validate.Validator(func(*config.Context) ([]validate.Result, error) { return nil, nil })
	second := validate.Validator(func(*config.Context) ([]validate.Result, error) { return nil, nil })

	sdk.RegisterContextValidator(first)
	sdk.RegisterContextValidator(nil)
	sdk.RegisterContextValidator(second)

	got := sdk.ContextValidators()
	if len(got) != 2 {
		t.Fatalf("expected 2 validators, got %d", len(got))
	}

	got[0] = nil
	again := sdk.ContextValidators()
	if again[0] == nil {
		t.Fatal("expected ContextValidators() to return a copy")
	}
}

func writePluginScript(t *testing.T, dir, name, script string) {
	t.Helper()
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, []byte(script), 0o755); err != nil {
		t.Fatalf("WriteFile(%s) error = %v", name, err)
	}
}

func TestRegisterCreateRunnerExposesDefinitions(t *testing.T) {
	sdk := NewSDK(Metadata{Name: "isle"})
	sdk.RegisterCreateRunner(CreateSpec{
		Name:              "default",
		Description:       "Create an ISLE stack",
		Default:           true,
		DockerComposeRepo: "https://github.com/example/isle",
	}, createRunnerStub{})

	defs := sdk.CreateDefinitions()
	if len(defs) != 1 {
		t.Fatalf("expected 1 create definition, got %d", len(defs))
	}
	if defs[0].Name != "default" {
		t.Fatalf("expected default create definition, got %+v", defs[0])
	}
	if sdk.createRootCmd == nil {
		t.Fatal("expected hidden create root command to be registered")
	}
}

type createRunnerStub struct{}

func (createRunnerStub) BindFlags(cmd *cobra.Command) {}

func (createRunnerStub) Run(cmd *cobra.Command) error { return nil }

func TestRegisterStandardComposeTemplateAddsLifecycleCommands(t *testing.T) {
	sdk := NewSDK(Metadata{Name: "demo"})
	sdk.RegisterStandardComposeTemplate(CreateSpec{
		Name:                 "default",
		DockerComposeRepo:    "https://github.com/example/demo",
		DockerComposeBuild:   []string{"make build"},
		DockerComposeInit:    []string{"make init"},
		DockerComposeUp:      []string{"make up"},
		DockerComposeDown:    []string{"make down"},
		DockerComposeRollout: []string{"make rollout"},
	}, StandardComposeTemplateOptions{
		DefaultPath:   "./demo",
		DefaultPlugin: "demo",
		DisplayName:   "Demo",
	})

	defs := sdk.CreateDefinitions()
	if len(defs) != 1 {
		t.Fatalf("expected 1 create definition, got %d", len(defs))
	}
	if defs[0].DockerComposeRollout[0] != "make rollout" {
		t.Fatalf("expected rollout command in create definition, got %+v", defs[0].DockerComposeRollout)
	}
	for _, name := range []string{"build", "init", "up", "down", "status", "logs", "rollout"} {
		if _, _, err := sdk.RootCmd.Find([]string{name}); err != nil {
			t.Fatalf("expected %q command to be registered: %v", name, err)
		}
	}
}

func TestDockerComposeExecCommandQuotesArgs(t *testing.T) {
	got := DockerComposeExecCommand("wp", "wp", "--path=/var/www/wp", "post", "get", "hello's world")
	want := "'docker' 'compose' 'exec' '-T' 'wp' 'wp' '--path=/var/www/wp' 'post' 'get' 'hello'\\''s world'"
	if got != want {
		t.Fatalf("DockerComposeExecCommand() = %q, want %q", got, want)
	}
}
