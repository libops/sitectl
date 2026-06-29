package traefik

import (
	"strings"
	"testing"
)

func TestInspectIngressCompose(t *testing.T) {
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
	inspection, err := inspectIngressCompose("docker-compose.yml", compose)
	if err != nil {
		t.Fatalf("inspectIngressCompose() error = %v", err)
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
