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
