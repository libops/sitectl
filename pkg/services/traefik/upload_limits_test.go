package traefik

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	corecomponent "github.com/libops/sitectl/pkg/component"
	"github.com/libops/sitectl/pkg/config"
)

func TestUploadLimitsApplyAndRemove(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	path := filepath.Join(dir, "docker-compose.yml")
	input := `services:
  drupal:
    image: libops/drupal
    environment: {}
  traefik:
    image: traefik
    command:
      - --entrypoints.web.address=:80
      - --entrypoints.web.transport.respondingTimeouts.readTimeout=300s
`
	if err := os.WriteFile(path, []byte(input), 0o644); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}
	ctx := &config.Context{DockerHostType: config.ContextLocal, ProjectDir: dir}
	component, err := UploadLimits(UploadLimitsOptions{AppService: "drupal"})
	if err != nil {
		t.Fatalf("UploadLimits() error = %v", err)
	}

	manager := corecomponent.NewManager(ctx)
	spec := component.SpecForWithOptions(corecomponent.StateOn, map[string]string{
		"max-upload-size": "2G",
		"upload-timeout":  "10m",
	})
	if err := manager.EnableComponentWithOptions(context.Background(), spec, corecomponent.ApplyOptions{Yolo: true}); err != nil {
		t.Fatalf("EnableComponentWithOptions() error = %v", err)
	}
	out, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile() error = %v", err)
	}
	rendered := string(out)
	for _, want := range []string{
		`PHP_UPLOAD_MAX_FILESIZE: "2G"`,
		`PHP_POST_MAX_SIZE: "2G"`,
		`NGINX_CLIENT_MAX_BODY_SIZE: "2G"`,
		`NGINX_FASTCGI_READ_TIMEOUT: "10m"`,
		"--entryPoints.web.transport.respondingTimeouts.readTimeout=10m",
	} {
		if !strings.Contains(rendered, want) {
			t.Fatalf("expected %q in compose, got:\n%s", want, rendered)
		}
	}
	if strings.Contains(rendered, "--entrypoints.web.transport.respondingTimeouts.readTimeout=300s") {
		t.Fatalf("expected old readTimeout removed, got:\n%s", rendered)
	}

	spec = component.SpecForWithOptions(corecomponent.StateOff, nil)
	if err := manager.DisableComponentWithOptions(context.Background(), spec, corecomponent.ApplyOptions{Yolo: true}); err != nil {
		t.Fatalf("DisableComponentWithOptions() error = %v", err)
	}
	out, err = os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile(after disable) error = %v", err)
	}
	rendered = string(out)
	for _, removed := range []string{"PHP_UPLOAD_MAX_FILESIZE", "PHP_POST_MAX_SIZE", "NGINX_CLIENT_MAX_BODY_SIZE", "NGINX_FASTCGI_READ_TIMEOUT=10m"} {
		if strings.Contains(rendered, removed) {
			t.Fatalf("expected %q removed, got:\n%s", removed, rendered)
		}
	}
	if !strings.Contains(rendered, "--entryPoints.web.transport.respondingTimeouts.readTimeout=300s") {
		t.Fatalf("expected default Traefik readTimeout restored, got:\n%s", rendered)
	}
}
