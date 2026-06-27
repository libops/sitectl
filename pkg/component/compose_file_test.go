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

func TestComposeFileAppendRemoveServiceStringPreservesFoldedScalar(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	path := filepath.Join(dir, "docker-compose.yml")
	input := `services:
  traefik:
    command: >-
      --ping=true
      --log.level=INFO
      --entryPoints.http.address=:80
`
	if err := os.WriteFile(path, []byte(input), 0o644); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}

	compose, err := LoadComposeFile(path)
	if err != nil {
		t.Fatalf("LoadComposeFile() error = %v", err)
	}
	value := "--experimental.localPlugins.captcha-protect.modulename=github.com/libops/captcha-protect"
	if err := compose.AppendUniqueServiceString("traefik", "command", value); err != nil {
		t.Fatalf("AppendUniqueServiceString() error = %v", err)
	}
	if err := compose.AppendUniqueServiceString("traefik", "command", value); err != nil {
		t.Fatalf("AppendUniqueServiceString(duplicate) error = %v", err)
	}
	if err := compose.Save(); err != nil {
		t.Fatalf("Save() error = %v", err)
	}

	out, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile() error = %v", err)
	}
	rendered := string(out)
	if !strings.Contains(rendered, "command: >-\n      --ping=true\n      --log.level=INFO\n      --entryPoints.http.address=:80\n      "+value) {
		t.Fatalf("expected folded command lines preserved with appended value, got:\n%s", rendered)
	}
	if strings.Count(rendered, value) != 1 {
		t.Fatalf("expected appended value once, got:\n%s", rendered)
	}

	compose, err = LoadComposeFile(path)
	if err != nil {
		t.Fatalf("LoadComposeFile(after append) error = %v", err)
	}
	if err := compose.RemoveServiceString("traefik", "command", value); err != nil {
		t.Fatalf("RemoveServiceString() error = %v", err)
	}
	if err := compose.Save(); err != nil {
		t.Fatalf("Save(after remove) error = %v", err)
	}
	out, err = os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile(after remove) error = %v", err)
	}
	rendered = string(out)
	if strings.Contains(rendered, value) {
		t.Fatalf("expected appended value removed, got:\n%s", rendered)
	}
	if !strings.Contains(rendered, "command: >-\n      --ping=true\n      --log.level=INFO\n      --entryPoints.http.address=:80") {
		t.Fatalf("expected original folded command lines preserved after remove, got:\n%s", rendered)
	}
}

func TestComposeFileRemoveServiceStringsByPrefix(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	path := filepath.Join(dir, "docker-compose.yml")
	input := `services:
  traefik:
    command: >-
      --entrypoints.web.address=:80
      --entryPoints.web.forwardedHeaders.trustedIPs=10.0.0.0/8
      --entryPoints.websecure.forwardedHeaders.trustedIPs=10.0.0.0/8
`
	if err := os.WriteFile(path, []byte(input), 0o644); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}

	compose, err := LoadComposeFile(path)
	if err != nil {
		t.Fatalf("LoadComposeFile() error = %v", err)
	}
	if err := compose.RemoveServiceStringsByPrefix("traefik", "command", "--entryPoints.web.forwardedHeaders.trustedIPs="); err != nil {
		t.Fatalf("RemoveServiceStringsByPrefix() error = %v", err)
	}
	if err := compose.Save(); err != nil {
		t.Fatalf("Save() error = %v", err)
	}

	out, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile() error = %v", err)
	}
	rendered := string(out)
	if strings.Contains(rendered, "--entryPoints.web.forwardedHeaders.trustedIPs=") {
		t.Fatalf("expected web trusted IP flag removed, got:\n%s", rendered)
	}
	if !strings.Contains(rendered, "--entryPoints.websecure.forwardedHeaders.trustedIPs=10.0.0.0/8") {
		t.Fatalf("expected websecure trusted IP flag preserved, got:\n%s", rendered)
	}
}

func TestComposeFileAppendRemoveServiceStringPreservesSequence(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	path := filepath.Join(dir, "docker-compose.yml")
	input := `services:
  traefik:
    volumes:
      - ./certs:/certs:ro
`
	if err := os.WriteFile(path, []byte(input), 0o644); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}

	compose, err := LoadComposeFile(path)
	if err != nil {
		t.Fatalf("LoadComposeFile() error = %v", err)
	}
	value := "./conf/traefik/challenge.tmpl.html:/challenge.tmpl.html:ro"
	if err := compose.AppendUniqueServiceString("traefik", "volumes", value); err != nil {
		t.Fatalf("AppendUniqueServiceString() error = %v", err)
	}
	if err := compose.AppendUniqueServiceString("traefik", "volumes", value); err != nil {
		t.Fatalf("AppendUniqueServiceString(duplicate) error = %v", err)
	}
	if err := compose.Save(); err != nil {
		t.Fatalf("Save() error = %v", err)
	}

	out, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile() error = %v", err)
	}
	rendered := string(out)
	if strings.Count(rendered, value) != 1 {
		t.Fatalf("expected appended value once, got:\n%s", rendered)
	}
	if !strings.Contains(rendered, "      - ./certs:/certs:ro\n      - "+value) {
		t.Fatalf("expected volume sequence preserved with appended value, got:\n%s", rendered)
	}

	compose, err = LoadComposeFile(path)
	if err != nil {
		t.Fatalf("LoadComposeFile(after append) error = %v", err)
	}
	if err := compose.RemoveServiceString("traefik", "volumes", value); err != nil {
		t.Fatalf("RemoveServiceString() error = %v", err)
	}
	if err := compose.Save(); err != nil {
		t.Fatalf("Save(after remove) error = %v", err)
	}
	out, err = os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile(after remove) error = %v", err)
	}
	rendered = string(out)
	if strings.Contains(rendered, value) {
		t.Fatalf("expected appended value removed, got:\n%s", rendered)
	}
	if !strings.Contains(rendered, "      - ./certs:/certs:ro") {
		t.Fatalf("expected original volume to remain, got:\n%s", rendered)
	}
}

func TestComposeFileAddVolumeBlockInsertsBeforeSectionSeparatorBlank(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	path := filepath.Join(dir, "docker-compose.yml")
	input := `volumes:
  solr-data: {}

services:
  drupal:
    image: drupal
`
	if err := os.WriteFile(path, []byte(input), 0o644); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}

	compose, err := LoadComposeFile(path)
	if err != nil {
		t.Fatalf("LoadComposeFile() error = %v", err)
	}
	if err := compose.AddVolumeBlock("triplet-cache", "  triplet-cache: {}"); err != nil {
		t.Fatalf("AddVolumeBlock() error = %v", err)
	}
	if err := compose.Save(); err != nil {
		t.Fatalf("Save() error = %v", err)
	}

	out, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile() error = %v", err)
	}
	rendered := string(out)
	if !strings.Contains(rendered, "  solr-data: {}\n  triplet-cache: {}\n\nservices:") {
		t.Fatalf("expected new volume before separator blank, got:\n%s", rendered)
	}
	if strings.Contains(rendered, "  solr-data: {}\n\n  triplet-cache: {}") {
		t.Fatalf("expected no blank line between volume entries, got:\n%s", rendered)
	}
}
