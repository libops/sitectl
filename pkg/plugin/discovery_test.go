package plugin

import (
	"os"
	"testing"
)

func TestParsePluginInfoOutput(t *testing.T) {
	output := `Name: isle
Version: 1.2.3
Description: Islandora support
Author: LibOps
Template-Repo: https://github.com/islandora-devops/isle-site-template
`

	info := ParsePluginInfoOutput(output)
	if info.Name != "isle" {
		t.Fatalf("expected name isle, got %q", info.Name)
	}
	if info.TemplateRepo != "https://github.com/islandora-devops/isle-site-template" {
		t.Fatalf("expected template repo to be parsed, got %q", info.TemplateRepo)
	}
	if info.Description != "Islandora support" {
		t.Fatalf("expected description to be parsed, got %q", info.Description)
	}
}

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

func TestDiscoverInstalledFromPathDetectsCreateCommand(t *testing.T) {
	dir := t.TempDir()
	pathEnv := dir

	script := `#!/bin/sh
if [ "$1" = "create" ] && [ "$2" = "--help" ]; then
  echo "create help"
  exit 0
fi
if [ "$1" = "plugin-info" ]; then
  echo "Name: demo"
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
		t.Fatalf("expected plugin create command to be detected")
	}
}
