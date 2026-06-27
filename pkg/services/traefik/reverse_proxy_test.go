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

func TestReverseProxyApplyAndRemove(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	path := filepath.Join(dir, "docker-compose.yml")
	input := `services:
  drupal:
    image: libops/drupal
    environment: {}
  traefik:
    image: traefik
    command: >-
      --entrypoints.web.address=:80
      --entryPoints.websecure.address=:443
      --entryPoints.web.forwardedHeaders.trustedIPs=192.0.2.1
`
	if err := os.WriteFile(path, []byte(input), 0o644); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}
	ctx := &config.Context{DockerHostType: config.ContextLocal, ProjectDir: dir}
	component, err := ReverseProxy(ReverseProxyOptions{AppService: "drupal"})
	if err != nil {
		t.Fatalf("ReverseProxy() error = %v", err)
	}

	manager := corecomponent.NewManager(ctx)
	spec := component.SpecForWithOptions(corecomponent.StateOn, map[string]string{"trusted-ip": "10.0.0.0/8,203.0.113.4"})
	if err := manager.EnableComponentWithOptions(context.Background(), spec, corecomponent.ApplyOptions{Yolo: true}); err != nil {
		t.Fatalf("EnableComponentWithOptions() error = %v", err)
	}

	out, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile() error = %v", err)
	}
	rendered := string(out)
	if strings.Contains(rendered, "192.0.2.1") {
		t.Fatalf("expected stale trusted IP command removed, got:\n%s", rendered)
	}
	for _, want := range []string{
		"--entryPoints.web.forwardedHeaders.trustedIPs=10.0.0.0/8,203.0.113.4",
		"--entryPoints.websecure.forwardedHeaders.trustedIPs=10.0.0.0/8,203.0.113.4",
		`NGINX_SET_REAL_IP_FROM: "10.0.0.0/8"`,
		`NGINX_SET_REAL_IP_FROM2: "203.0.113.4"`,
		`NGINX_REAL_IP_RECURSIVE: "on"`,
	} {
		if !strings.Contains(rendered, want) {
			t.Fatalf("expected %q in compose, got:\n%s", want, rendered)
		}
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
	for _, notWant := range []string{"forwardedHeaders.trustedIPs", "NGINX_SET_REAL_IP_FROM", "NGINX_REAL_IP_RECURSIVE"} {
		if strings.Contains(rendered, notWant) {
			t.Fatalf("expected %q removed, got:\n%s", notWant, rendered)
		}
	}
}

func TestInspectReverseProxyCompose(t *testing.T) {
	t.Parallel()

	compose := []byte(`services:
  drupal:
    environment:
      NGINX_SET_REAL_IP_FROM: 10.0.0.0/8
      NGINX_SET_REAL_IP_FROM2: 203.0.113.4
      NGINX_REAL_IP_RECURSIVE: "on"
  traefik:
    command:
      - --entrypoints.web.address=:80
      - --entryPoints.web.forwardedHeaders.trustedIPs=10.0.0.0/8,203.0.113.4
`)
	inspection, err := inspectReverseProxyCompose("docker-compose.yml", compose)
	if err != nil {
		t.Fatalf("inspectReverseProxyCompose() error = %v", err)
	}
	if got := strings.Join(inspection.Traefik["web"], ","); got != "10.0.0.0/8,203.0.113.4" {
		t.Fatalf("unexpected traefik trusted IPs %q", got)
	}
	cfg, ok := inspection.NginxServices["drupal"]
	if !ok {
		t.Fatalf("expected drupal nginx config, got %+v", inspection.NginxServices)
	}
	if got := strings.Join(cfg.TrustedIP, ","); got != "10.0.0.0/8,203.0.113.4" {
		t.Fatalf("unexpected nginx trusted IPs %q", got)
	}
	if cfg.Recursive != "on" {
		t.Fatalf("expected recursive on, got %q", cfg.Recursive)
	}
}
