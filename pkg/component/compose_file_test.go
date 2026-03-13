package component

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestComposeFilePreservesAnchorsAndFoldedScalars(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	path := filepath.Join(dir, "docker-compose.yml")
	input := `---
# Common to all services
x-common: &common
  restart: unless-stopped
  tty: true # keep tty comment
services:
  alpaca:
    <<: *common
    environment:
      ALPACA_FCREPO_INDEXER_ENABLED: "true"
  fcrepo:
    <<: *common
    image: islandora/fcrepo6
  traefik:
    <<: *common
    command: >-
      --ping=true
      --log.level=INFO
      --entryPoints.http.address=:80
volumes:
  fcrepo-data: {}
`
	if err := os.WriteFile(path, []byte(input), 0o644); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}

	compose, err := LoadComposeFile(path)
	if err != nil {
		t.Fatalf("LoadComposeFile() error = %v", err)
	}
	if err := compose.DeleteService("fcrepo"); err != nil {
		t.Fatalf("DeleteService() error = %v", err)
	}
	if err := compose.DeleteVolume("fcrepo-data"); err != nil {
		t.Fatalf("DeleteVolume() error = %v", err)
	}
	if err := compose.SetServiceEnv("alpaca", "ALPACA_FCREPO_INDEXER_ENABLED", "false"); err != nil {
		t.Fatalf("SetServiceEnv() error = %v", err)
	}
	if err := compose.Save(); err != nil {
		t.Fatalf("Save() error = %v", err)
	}

	out, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile() error = %v", err)
	}
	rendered := string(out)
	if !strings.Contains(rendered, "x-common: &common") {
		t.Fatalf("expected anchor preserved, got:\n%s", rendered)
	}
	if !strings.Contains(rendered, "<<: *common") {
		t.Fatalf("expected merge key preserved, got:\n%s", rendered)
	}
	if !strings.Contains(rendered, "# keep tty comment") {
		t.Fatalf("expected comment preserved, got:\n%s", rendered)
	}
	if !strings.Contains(rendered, "command: >-") {
		t.Fatalf("expected folded scalar style preserved, got:\n%s", rendered)
	}
	if !strings.Contains(rendered, "      --ping=true\n      --log.level=INFO\n      --entryPoints.http.address=:80") {
		t.Fatalf("expected folded scalar content preserved, got:\n%s", rendered)
	}
	if strings.Contains(rendered, "\n  fcrepo:\n") {
		t.Fatalf("expected fcrepo service removed, got:\n%s", rendered)
	}
	if strings.Contains(rendered, "fcrepo-data") {
		t.Fatalf("expected fcrepo-data removed, got:\n%s", rendered)
	}
	if !strings.Contains(rendered, `ALPACA_FCREPO_INDEXER_ENABLED: "false"`) {
		t.Fatalf("expected updated env value, got:\n%s", rendered)
	}
}

func TestComposeFileDeleteServiceEnv(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	path := filepath.Join(dir, "docker-compose.yml")
	input := `services:
  drupal:
    environment:
      DRUPAL_DEFAULT_FCREPO_HOST: fcrepo
      DRUPAL_DEFAULT_TRIPLESTORE_NAMESPACE: islandora
`
	if err := os.WriteFile(path, []byte(input), 0o644); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}

	compose, err := LoadComposeFile(path)
	if err != nil {
		t.Fatalf("LoadComposeFile() error = %v", err)
	}
	if err := compose.DeleteServiceEnv("drupal", "DRUPAL_DEFAULT_FCREPO_HOST"); err != nil {
		t.Fatalf("DeleteServiceEnv() error = %v", err)
	}
	if err := compose.Save(); err != nil {
		t.Fatalf("Save() error = %v", err)
	}

	out, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile() error = %v", err)
	}
	rendered := string(out)
	if strings.Contains(rendered, "DRUPAL_DEFAULT_FCREPO_HOST") {
		t.Fatalf("expected env removed, got:\n%s", rendered)
	}
	if !strings.Contains(rendered, "DRUPAL_DEFAULT_TRIPLESTORE_NAMESPACE: islandora") {
		t.Fatalf("expected unrelated env preserved, got:\n%s", rendered)
	}
}
