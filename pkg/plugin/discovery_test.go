package plugin

import (
	"os"
	"strings"
	"testing"
)

func TestDiscoverInstalledFromPathFallsBackToBuiltinTemplateRepo(t *testing.T) {
	dir := t.TempDir()
	pathEnv := dir

	if err := os.WriteFile(dir+"/sitectl-isle", []byte("#!/bin/sh\nexit 1\n"), 0o755); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}

	plugins := DiscoverInstalledFromPath(pathEnv)
	if len(plugins) != 1 {
		t.Fatalf("expected one plugin, got %d", len(plugins))
	}
	if plugins[0].TemplateRepo != "https://github.com/islandora-devops/isle-site-template" {
		t.Fatalf("expected builtin template repo, got %q", plugins[0].TemplateRepo)
	}
	if plugins[0].CanCreate {
		t.Fatalf("expected failing plugin inspection to report CanCreate=false")
	}
}

func TestDiscoverInstalledFromPathDetectsCreateDefinitions(t *testing.T) {
	dir := t.TempDir()
	pathEnv := dir

	script := `#!/bin/sh
if [ "$1" = "__plugin-metadata" ]; then
  cat <<'YAML'
name: demo
description: Demo plugin
cancreate: true
createdefinitions:
  - name: default
    description: Demo stack
    default: true
    docker_compose_repo: https://github.com/example/demo
YAML
  exit 0
fi
exit 1
`
	if err := os.WriteFile(dir+"/sitectl-demo", []byte(script), 0o755); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}

	plugins := DiscoverInstalledFromPath(pathEnv)
	if len(plugins) != 1 {
		t.Fatalf("expected one plugin, got %d", len(plugins))
	}
	if !plugins[0].CanCreate {
		t.Fatalf("expected plugin create definitions to be detected")
	}
	if len(plugins[0].CreateDefinitions) != 1 {
		t.Fatalf("expected one create definition, got %d", len(plugins[0].CreateDefinitions))
	}
	if plugins[0].CreateDefinitions[0].Name != "default" {
		t.Fatalf("expected default create definition, got %+v", plugins[0].CreateDefinitions[0])
	}
	if !strings.Contains(plugins[0].TemplateRepo, "github.com/example/demo") {
		t.Fatalf("expected template repo from create definition, got %q", plugins[0].TemplateRepo)
	}
}
