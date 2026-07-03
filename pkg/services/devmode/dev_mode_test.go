package devmode

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	corecomponent "github.com/libops/sitectl/pkg/component"
	"github.com/libops/sitectl/pkg/config"
)

func TestComponentWritesAndRemovesOverride(t *testing.T) {
	t.Parallel()

	projectDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(projectDir, "docker-compose.yml"), []byte("services:\n  drupal:\n    image: drupal\n"), 0o644); err != nil {
		t.Fatalf("WriteFile(compose) error = %v", err)
	}
	ctx := &config.Context{DockerHostType: config.ContextLocal, ProjectDir: projectDir}
	component, err := Component(Options{
		AppService: "drupal",
		Volumes: []string{
			"./config:/var/www/drupal/config:z,rw",
			"./web/modules/custom:/var/www/drupal/web/modules/custom:z,rw",
		},
	})
	if err != nil {
		t.Fatalf("Component() error = %v", err)
	}

	manager := corecomponent.NewManager(ctx)
	if err := manager.EnableComponentWithOptions(context.Background(), component.SpecFor(corecomponent.StateOn), corecomponent.ApplyOptions{Yolo: true}); err != nil {
		t.Fatalf("EnableComponentWithOptions() error = %v", err)
	}
	data, err := os.ReadFile(filepath.Join(projectDir, "docker-compose.override.yml"))
	if err != nil {
		t.Fatalf("ReadFile(override) error = %v", err)
	}
	rendered := string(data)
	for _, want := range []string{
		"services:",
		"drupal:",
		`UID: ${UID:-1000}`,
		"./config:/var/www/drupal/config:z,rw",
		"./web/modules/custom:/var/www/drupal/web/modules/custom:z,rw",
	} {
		if !strings.Contains(rendered, want) {
			t.Fatalf("expected override to contain %q, got:\n%s", want, rendered)
		}
	}

	if err := manager.DisableComponentWithOptions(context.Background(), component.SpecFor(corecomponent.StateOff), corecomponent.ApplyOptions{Yolo: true}); err != nil {
		t.Fatalf("DisableComponentWithOptions() error = %v", err)
	}
	if _, err := os.Stat(filepath.Join(projectDir, "docker-compose.override.yml")); !os.IsNotExist(err) {
		t.Fatalf("expected override removed, stat error = %v", err)
	}
}

func TestComponentAssistantWritesCliSandboxService(t *testing.T) {
	t.Parallel()

	projectDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(projectDir, "docker-compose.yml"), []byte("services:\n  wp:\n    image: wordpress\n"), 0o644); err != nil {
		t.Fatalf("WriteFile(compose) error = %v", err)
	}
	ctx := &config.Context{DockerHostType: config.ContextLocal, ProjectDir: projectDir}
	component, err := Component(Options{
		AppService: "wp",
		Volumes: []string{
			"./web/app/plugins:/var/www/bedrock/web/app/plugins:z,rw",
			"./web/app/themes:/var/www/bedrock/web/app/themes:z,rw",
		},
	})
	if err != nil {
		t.Fatalf("Component() error = %v", err)
	}

	manager := corecomponent.NewManager(ctx)
	spec := component.SpecForWithOptions(corecomponent.StateOn, map[string]string{
		"assistant":            "true",
		"harness":              "codex",
		"model":                "gpt-5-codex",
		"compose-access":       "true",
		"skip-egress-firewall": "false",
	})
	if err := manager.EnableComponentWithOptions(context.Background(), spec, corecomponent.ApplyOptions{Yolo: true}); err != nil {
		t.Fatalf("EnableComponentWithOptions() error = %v", err)
	}
	rendered := readOverrideForTest(t, projectDir)
	for _, want := range []string{
		"cli-sandbox:",
		"image: ${SITECTL_ASSISTANT_IMAGE:-ghcr.io/libops/cli-sandbox:codex}",
		"pull_policy: always",
		"- assistant",
		"SKIP_EGRESS_FIREWALL: \"false\"",
		"DOCKER_HOST: unix:///var/run/docker.sock",
		"- NET_ADMIN",
		"- NET_RAW",
		"- codex",
		"- --sandbox",
		"- danger-full-access",
		"- --ask-for-approval",
		"- never",
		"- --model",
		"- gpt-5-codex",
		"${HOME}/.codex:/home/node/.codex:rw",
		"./:/workspace:rw",
		"./web/app/plugins:/var/www/bedrock/web/app/plugins:z,rw",
		"/var/run/docker.sock:/var/run/docker.sock:ro",
		"- default",
		"- cli-sandbox",
		"networks:",
	} {
		if !strings.Contains(rendered, want) {
			t.Fatalf("expected override to contain %q, got:\n%s", want, rendered)
		}
	}
}

func TestComponentAssistantCanSkipEgressFirewallAndUseEndpointModel(t *testing.T) {
	t.Parallel()

	projectDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(projectDir, "docker-compose.yml"), []byte("services:\n  ojs:\n    image: ojs\n"), 0o644); err != nil {
		t.Fatalf("WriteFile(compose) error = %v", err)
	}
	ctx := &config.Context{DockerHostType: config.ContextLocal, ProjectDir: projectDir}
	component, err := Component(Options{AppService: "ojs"})
	if err != nil {
		t.Fatalf("Component() error = %v", err)
	}

	manager := corecomponent.NewManager(ctx)
	spec := component.SpecForWithOptions(corecomponent.StateOn, map[string]string{
		"assistant":            "true",
		"harness":              "gemini",
		"model":                "https://models.example/v1",
		"skip-egress-firewall": "true",
	})
	if err := manager.EnableComponentWithOptions(context.Background(), spec, corecomponent.ApplyOptions{Yolo: true}); err != nil {
		t.Fatalf("EnableComponentWithOptions() error = %v", err)
	}
	rendered := readOverrideForTest(t, projectDir)
	for _, want := range []string{
		"image: ${SITECTL_ASSISTANT_IMAGE:-ghcr.io/libops/cli-sandbox:gemini}",
		"SKIP_EGRESS_FIREWALL: \"true\"",
		"OPENAI_BASE_URL: https://models.example/v1",
		"OPENAI_API_BASE: https://models.example/v1",
		"TASK_AGENT_MODEL_BASE_URL: https://models.example/v1",
		"- gemini",
		"- --approval-mode",
		"- yolo",
		"- cli-sandbox",
	} {
		if !strings.Contains(rendered, want) {
			t.Fatalf("expected override to contain %q, got:\n%s", want, rendered)
		}
	}
	for _, unwanted := range []string{"NET_ADMIN", "NET_RAW", "--model", "/var/run/docker.sock"} {
		if strings.Contains(rendered, unwanted) {
			t.Fatalf("expected override not to contain %q, got:\n%s", unwanted, rendered)
		}
	}
}

func readOverrideForTest(t *testing.T, projectDir string) string {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(projectDir, "docker-compose.override.yml"))
	if err != nil {
		t.Fatalf("ReadFile(override) error = %v", err)
	}
	return string(data)
}
