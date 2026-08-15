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
	for _, command := range []string{"--ping=true", "--log.level=INFO", "--entryPoints.http.address=:80"} {
		if !strings.Contains(rendered, command) {
			t.Fatalf("expected folded scalar value %q preserved, got:\n%s", command, rendered)
		}
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
	if !strings.Contains(rendered, "command: >-") || !strings.Contains(rendered, value) {
		t.Fatalf("expected folded command style preserved with appended value, got:\n%s", rendered)
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
	for _, command := range []string{"command: >-", "--ping=true", "--log.level=INFO", "--entryPoints.http.address=:80"} {
		if !strings.Contains(rendered, command) {
			t.Fatalf("expected original folded command value %q preserved after remove, got:\n%s", command, rendered)
		}
	}
}

func TestComposeFileRemoveServiceStringsByPrefix(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	path := filepath.Join(dir, "docker-compose.yml")
	input := `services:
  traefik:
    command: >-
      --entryPoints.web.forwardedHeaders.trustedIPs=10.0.0.0/8
      --entrypoints.web.address=:80
      --log.level=INFO
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
	if !strings.Contains(rendered, "--entrypoints.web.address=:80") {
		t.Fatalf("expected unrelated web address flag preserved, got:\n%s", rendered)
	}
	if !strings.Contains(rendered, "--log.level=INFO") {
		t.Fatalf("expected unrelated log level flag preserved, got:\n%s", rendered)
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

func TestComposeFileAddVolumeBlockPreservesSectionOrder(t *testing.T) {
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
	if !strings.Contains(rendered, "  solr-data: {}\n  triplet-cache: {}\nservices:") {
		t.Fatalf("expected new volume before the services section, got:\n%s", rendered)
	}
}

func TestComposeFileSetServiceScalar(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	path := filepath.Join(dir, "docker-compose.yml")
	input := `services:
  blazegraph:
    image:
      name: libops/blazegraph
    volumes:
      - blazegraph-data:/data:rw
  drupal:
    environment: {}
`
	if err := os.WriteFile(path, []byte(input), 0o644); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}

	compose, err := LoadComposeFile(path)
	if err != nil {
		t.Fatalf("LoadComposeFile() error = %v", err)
	}
	if err := compose.SetServiceScalar("blazegraph", "image", "islandora/blazegraph"); err != nil {
		t.Fatalf("SetServiceScalar(image) error = %v", err)
	}
	if err := compose.SetServiceScalar("drupal", "restart", "unless-stopped"); err != nil {
		t.Fatalf("SetServiceScalar(restart) error = %v", err)
	}
	if err := compose.Save(); err != nil {
		t.Fatalf("Save() error = %v", err)
	}

	out, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile() error = %v", err)
	}
	rendered := string(out)
	if !strings.Contains(rendered, "  blazegraph:\n    image: islandora/blazegraph\n    volumes:\n      - blazegraph-data:/data:rw\n") {
		t.Fatalf("expected image scalar replaced without losing volumes, got:\n%s", rendered)
	}
	if !strings.Contains(rendered, "  drupal:\n    environment: {}\n    restart: unless-stopped\n") {
		t.Fatalf("expected new scalar added to drupal, got:\n%s", rendered)
	}
}

func TestComposeFileAddServiceBlockResolvesDocumentAnchor(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	path := filepath.Join(dir, "docker-compose.yml")
	input := `---
x-common: &common
  restart: unless-stopped
services:
  app:
    <<: *common
    image: app
`
	if err := os.WriteFile(path, []byte(input), 0o644); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}

	compose, err := LoadComposeFile(path)
	if err != nil {
		t.Fatalf("LoadComposeFile() error = %v", err)
	}
	if err := compose.AddServiceBlock("worker", `  worker:
    <<: *common
    image: worker`); err != nil {
		t.Fatalf("AddServiceBlock() error = %v", err)
	}
	if err := compose.Save(); err != nil {
		t.Fatalf("Save() error = %v", err)
	}

	reloaded, err := LoadComposeFile(path)
	if err != nil {
		t.Fatalf("LoadComposeFile(after save) error = %v", err)
	}
	block, ok := reloaded.ServiceBlock("worker")
	if !ok || !strings.Contains(block, "<<: *common") || !strings.Contains(block, "image: worker") {
		t.Fatalf("expected added service to retain merge alias, got:\n%s", block)
	}
}

func TestComposeFileInsertedMergeUsesLiveAnchorAndLocalOverrides(t *testing.T) {
	t.Parallel()

	compose, err := newComposeFile(nil, "compose.yml", []byte(`x-common: &common
  restart: unless-stopped
services:
  sibling:
    <<: *common
`))
	if err != nil {
		t.Fatalf("newComposeFile() error = %v", err)
	}
	if err := compose.AddServiceBlock("worker", `  worker:
    <<: *common
`); err != nil {
		t.Fatalf("AddServiceBlock() error = %v", err)
	}
	if err := compose.SetServiceScalar("worker", "restart", "always"); err != nil {
		t.Fatalf("SetServiceScalar() error = %v", err)
	}
	if err := compose.DeleteServiceKey("sibling", "restart"); err != nil {
		t.Fatalf("DeleteServiceKey(inherited key) error = %v", err)
	}

	out, err := compose.doc.Bytes()
	if err != nil {
		t.Fatalf("Bytes() error = %v", err)
	}
	rendered := string(out)
	if strings.Count(rendered, "restart: unless-stopped") != 1 {
		t.Fatalf("expected shared anchor to remain unchanged, got:\n%s", rendered)
	}
	if !strings.Contains(rendered, "worker:\n    <<: *common\n    restart: always") {
		t.Fatalf("expected worker-local override, got:\n%s", rendered)
	}
	if !strings.Contains(rendered, "sibling:\n    <<: *common") {
		t.Fatalf("expected sibling merge preserved, got:\n%s", rendered)
	}
}

func TestComposeFileDropsUnresolvedMergeAliasInStandaloneOverride(t *testing.T) {
	t.Parallel()

	compose, err := newComposeFile(nil, "override.yml", nil)
	if err != nil {
		t.Fatalf("newComposeFile() error = %v", err)
	}
	if err := compose.AddServiceBlock("worker", `  worker:
    <<: *common
    image: worker`); err != nil {
		t.Fatalf("AddServiceBlock() error = %v", err)
	}
	if err := compose.SetServiceScalar("worker", "restart", "always"); err != nil {
		t.Fatalf("SetServiceScalar() error = %v", err)
	}

	out, err := compose.doc.Bytes()
	if err != nil {
		t.Fatalf("Bytes() error = %v", err)
	}
	rendered := string(out)
	if strings.Contains(rendered, "*common") {
		t.Fatalf("expected unresolved merge alias omitted from standalone override, got:\n%s", rendered)
	}
	if !strings.Contains(rendered, "worker:\n    image: worker\n    restart: always") {
		t.Fatalf("expected standalone service and local override preserved, got:\n%s", rendered)
	}
}
