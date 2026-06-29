package validate

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/libops/sitectl/pkg/config"
)

func TestRequiredFieldsValidator(t *testing.T) {
	results, err := requiredFieldsValidator(&config.Context{})
	if err != nil {
		t.Fatalf("requiredFieldsValidator() error = %v", err)
	}
	if len(results) == 0 {
		t.Fatal("expected validation results")
	}
	failed := 0
	for _, result := range results {
		if result.Status == StatusFailed {
			failed++
		}
	}
	if failed == 0 {
		t.Fatal("expected required field failures")
	}
}

func TestComposeProjectValidator(t *testing.T) {
	projectDir := t.TempDir()
	ctx := &config.Context{
		DockerHostType: config.ContextLocal,
		ProjectDir:     projectDir,
	}

	results, err := composeProjectValidator(ctx)
	if err != nil {
		t.Fatalf("composeProjectValidator() error = %v", err)
	}
	if len(results) != 1 || results[0].Status != StatusFailed {
		t.Fatalf("expected compose-project failure, got %+v", results)
	}

	if err := os.WriteFile(filepath.Join(projectDir, "docker-compose.yml"), []byte("services: {}\n"), 0o644); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}

	results, err = composeProjectValidator(ctx)
	if err != nil {
		t.Fatalf("composeProjectValidator() error = %v", err)
	}
	if len(results) != 1 || results[0].Status != StatusOK {
		t.Fatalf("expected compose-project success, got %+v", results)
	}
}

func TestOverrideSymlinkValidator(t *testing.T) {
	projectDir := t.TempDir()
	ctx := &config.Context{
		DockerHostType: config.ContextLocal,
		ProjectDir:     projectDir,
		Environment:    "local",
	}

	tracked := filepath.Join(projectDir, "docker-compose.local.yml")
	if err := os.WriteFile(tracked, []byte("services: {}\n"), 0o644); err != nil {
		t.Fatalf("WriteFile(tracked) error = %v", err)
	}

	results, err := overrideSymlinkValidator(ctx)
	if err != nil {
		t.Fatalf("overrideSymlinkValidator() error = %v", err)
	}
	if len(results) != 1 || results[0].Status != StatusFailed {
		t.Fatalf("expected symlink validation failure, got %+v", results)
	}

	if err := os.Symlink(filepath.Base(tracked), filepath.Join(projectDir, config.RuntimeComposeOverrideName)); err != nil {
		t.Fatalf("Symlink() error = %v", err)
	}

	results, err = overrideSymlinkValidator(ctx)
	if err != nil {
		t.Fatalf("overrideSymlinkValidator() error = %v", err)
	}
	if len(results) != 1 || results[0].Status != StatusOK {
		t.Fatalf("expected symlink validation success, got %+v", results)
	}
}

func TestIngressValidator(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name       string
		compose    string
		wantStatus string
	}{
		{
			name: "matching traefik and nginx settings pass",
			compose: `services:
  drupal:
    environment:
      NGINX_SET_REAL_IP_FROM: 10.0.0.0/8
      NGINX_SET_REAL_IP_FROM2: 203.0.113.4
      NGINX_REAL_IP_RECURSIVE: "on"
  traefik:
    command:
      - --entrypoints.web.address=:80
      - --entryPoints.web.forwardedHeaders.trustedIPs=10.0.0.0/8,203.0.113.4
`,
			wantStatus: StatusOK,
		},
		{
			name: "nginx without traefik fails",
			compose: `services:
  drupal:
    environment:
      NGINX_SET_REAL_IP_FROM: 10.0.0.0/8
      NGINX_REAL_IP_RECURSIVE: "on"
  traefik:
    command:
      - --entrypoints.web.address=:80
`,
			wantStatus: StatusFailed,
		},
		{
			name: "traefik without nginx warns",
			compose: `services:
  archivesspace:
    image: archivesspace
  traefik:
    command:
      - --entrypoints.web.address=:80
      - --entryPoints.web.forwardedHeaders.trustedIPs=10.0.0.0/8
`,
			wantStatus: StatusWarning,
		},
		{
			name: "mismatched values fail",
			compose: `services:
  drupal:
    environment:
      NGINX_SET_REAL_IP_FROM: 10.0.0.0/8
      NGINX_REAL_IP_RECURSIVE: "on"
  traefik:
    command:
      - --entrypoints.web.address=:80
      - --entryPoints.web.forwardedHeaders.trustedIPs=203.0.113.4
`,
			wantStatus: StatusFailed,
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			projectDir := t.TempDir()
			if err := os.WriteFile(filepath.Join(projectDir, "docker-compose.yml"), []byte(tt.compose), 0o644); err != nil {
				t.Fatalf("WriteFile() error = %v", err)
			}
			results, err := ingressValidator(&config.Context{
				DockerHostType: config.ContextLocal,
				ProjectDir:     projectDir,
			})
			if err != nil {
				t.Fatalf("ingressValidator() error = %v", err)
			}
			if len(results) != 1 {
				t.Fatalf("expected one result, got %+v", results)
			}
			if results[0].Status != tt.wantStatus {
				t.Fatalf("status = %s, want %s; result=%+v", results[0].Status, tt.wantStatus, results[0])
			}
		})
	}
}
