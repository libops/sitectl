package plugin

import (
	"os"
	"reflect"
	"strings"
	"testing"
	"time"
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
	if strings.TrimSpace(plugins[0].MetadataError) == "" {
		t.Fatalf("expected failing plugin inspection to record metadata error")
	}
}

func TestDiscoverInstalledFromPathDetectsCreateDefinitions(t *testing.T) {
	dir := t.TempDir()
	pathEnv := dir

	writeRPCFixturePlugin(t, dir, "sitectl-demo", InstalledPlugin{
		ProtocolVersion: RPCProtocolVersion,
		Name:            "demo",
		Description:     "Demo plugin",
		CanCreate:       true,
		CreateDefinitions: []CreateSpec{{
			Name:              "default",
			Description:       "Demo stack",
			Default:           true,
			DockerComposeRepo: "https://github.com/example/demo",
		}},
	}, "")

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

func TestDiscoverInstalledFromPathRejectsMetadataProtocolMismatch(t *testing.T) {
	dir := t.TempDir()
	pathEnv := dir

	writeRPCFixturePlugin(t, dir, "sitectl-demo", PluginMetadata{
		ProtocolVersion: 2,
		Name:            "demo",
		Description:     "Demo plugin",
	}, "")

	plugins := DiscoverInstalledFromPath(pathEnv)
	if len(plugins) != 1 {
		t.Fatalf("expected one plugin, got %d", len(plugins))
	}
	if !strings.Contains(plugins[0].MetadataError, "unsupported rpc protocol version 2") {
		t.Fatalf("MetadataError = %q, want unsupported protocol version", plugins[0].MetadataError)
	}
	if !strings.Contains(plugins[0].MetadataError, "rebuild or reinstall the plugin to match sitectl") {
		t.Fatalf("MetadataError = %q, want reinstall guidance", plugins[0].MetadataError)
	}
}

func TestDiscoverInstalledFromPathTimesOutMetadataRPC(t *testing.T) {
	oldTimeout := installedPluginMetadataTimeout
	installedPluginMetadataTimeout = 50 * time.Millisecond
	t.Cleanup(func() {
		installedPluginMetadataTimeout = oldTimeout
		InvalidateInstalledDiscoveryCache()
	})

	dir := t.TempDir()
	writePluginScript(t, dir, "sitectl-slow", "#!/bin/sh\nsleep 5\n")

	started := time.Now()
	plugins := DiscoverInstalledFromPath(dir)
	elapsed := time.Since(started)
	if elapsed > time.Second {
		t.Fatalf("DiscoverInstalledFromPath() took %s, want timeout-bound return", elapsed)
	}
	if len(plugins) != 1 {
		t.Fatalf("expected one plugin, got %d", len(plugins))
	}
	if !strings.Contains(plugins[0].MetadataError, "context deadline exceeded") {
		t.Fatalf("MetadataError = %q, want deadline exceeded", plugins[0].MetadataError)
	}
}

func TestInvalidateInstalledDiscoveryCacheClearsMetadata(t *testing.T) {
	t.Cleanup(InvalidateInstalledDiscoveryCache)

	dir := t.TempDir()
	pathEnv := dir
	metadata := fullPluginMetadataForTest()
	metadata.Name = "demo"
	metadata.BinaryName = "sitectl-demo"
	metadata.Description = "first description"
	writeRPCFixturePlugin(t, dir, "sitectl-demo", metadata, "")

	first := DiscoverInstalledFromPath(pathEnv)
	if len(first) != 1 || first[0].Description != "first description" {
		t.Fatalf("first discovery = %+v, want first description", first)
	}

	metadata.Description = "second description"
	writeRPCFixturePlugin(t, dir, "sitectl-demo", metadata, "")
	cached := DiscoverInstalledFromPath(pathEnv)
	if len(cached) != 1 || cached[0].Description != "first description" {
		t.Fatalf("cached discovery = %+v, want first description", cached)
	}

	InvalidateInstalledDiscoveryCache()
	refreshed := DiscoverInstalledFromPath(pathEnv)
	if len(refreshed) != 1 || refreshed[0].Description != "second description" {
		t.Fatalf("refreshed discovery = %+v, want second description", refreshed)
	}
}

func TestInstalledPluginMetadataFieldsStayInLockstep(t *testing.T) {
	t.Parallel()

	metadata := fullPluginMetadataForTest()
	got := installedPluginFromMetadata(metadata, InstalledPlugin{
		Name:         "fallback",
		BinaryName:   "sitectl-fallback",
		Path:         "/usr/local/bin/sitectl-fallback",
		Description:  "Fallback description",
		TemplateRepo: "https://github.com/example/fallback",
	})

	assertMetadataFieldsCopiedToInstalledPlugin(t, metadata, got)
}

func TestDiscoverInstalledFromPathPropagatesAdvertisedCapabilities(t *testing.T) {
	dir := t.TempDir()
	pathEnv := dir
	metadata := fullPluginMetadataForTest()
	metadata.Name = "demo"
	metadata.BinaryName = "sitectl-demo"

	writeRPCFixturePlugin(t, dir, "sitectl-demo", metadata, "")

	plugins := DiscoverInstalledFromPath(pathEnv)
	if len(plugins) != 1 {
		t.Fatalf("expected one plugin, got %d", len(plugins))
	}
	got := plugins[0]
	if !got.CanCreate || !got.CanDeploy || !got.CanDebug || !got.CanConverge || !got.CanSet || !got.CanValidate || !got.CanHealthcheck {
		t.Fatalf("advertised capabilities were not propagated: %+v", got)
	}
	if !reflect.DeepEqual(got.Includes, metadata.Includes) {
		t.Fatalf("includes = %#v, want %#v", got.Includes, metadata.Includes)
	}
	if !reflect.DeepEqual(got.CreateDefinitions, metadata.CreateDefinitions) {
		t.Fatalf("create definitions = %#v, want %#v", got.CreateDefinitions, metadata.CreateDefinitions)
	}
	if !reflect.DeepEqual(got.DeployDefinitions, metadata.DeployDefinitions) {
		t.Fatalf("deploy definitions = %#v, want %#v", got.DeployDefinitions, metadata.DeployDefinitions)
	}
}

func fullPluginMetadataForTest() PluginMetadata {
	return PluginMetadata{
		ProtocolVersion: RPCProtocolVersion,
		Name:            "metadata-name",
		BinaryName:      "sitectl-metadata",
		Version:         "1.2.3",
		Description:     "Metadata description",
		Author:          "LibOps",
		TemplateRepo:    "https://github.com/example/template",
		CanCreate:       true,
		CanDeploy:       true,
		CanDebug:        true,
		CanConverge:     true,
		CanSet:          true,
		CanValidate:     true,
		CanHealthcheck:  true,
		Includes:        []string{"drupal", "libops"},
		CreateDefinitions: []CreateSpec{{
			Name:                "default",
			Description:         "Default stack",
			Default:             true,
			MinCPUCores:         2,
			MinMemory:           "4GB",
			MinDiskSpace:        "20GB",
			DockerComposeRepo:   "https://github.com/example/template",
			DockerComposeBranch: "main",
			DockerComposeBuild:  []string{"build"},
			DockerComposeInit:   []string{"init"},
			DockerComposeUp:     []string{"up"},
			DockerComposeDown:   []string{"down"},
			DockerComposeRollout: []string{
				"pull",
				"up",
			},
		}},
		DeployDefinitions: []DeploySpec{{
			Name:        "default",
			Plugin:      "metadata-name",
			Description: "Default deploy",
			Default:     true,
		}},
	}
}

func assertMetadataFieldsCopiedToInstalledPlugin(t *testing.T, metadata PluginMetadata, installed InstalledPlugin) {
	t.Helper()

	metadataType := reflect.TypeOf(metadata)
	metadataValue := reflect.ValueOf(metadata)
	installedType := reflect.TypeOf(installed)
	installedValue := reflect.ValueOf(installed)

	for i := 0; i < metadataType.NumField(); i++ {
		field := metadataType.Field(i)
		if !field.IsExported() {
			continue
		}
		source := metadataValue.Field(i)
		if source.IsZero() {
			t.Fatalf("metadata propagation test fixture leaves %s zero", field.Name)
		}

		targetField, ok := installedType.FieldByName(field.Name)
		if !ok {
			t.Fatalf("InstalledPlugin is missing PluginMetadata field %s", field.Name)
		}
		if targetField.Type != field.Type {
			t.Fatalf("InstalledPlugin.%s type = %s, want %s", field.Name, targetField.Type, field.Type)
		}

		target := installedValue.FieldByName(field.Name)
		if !reflect.DeepEqual(target.Interface(), source.Interface()) {
			t.Fatalf("InstalledPlugin.%s = %#v, want %#v", field.Name, target.Interface(), source.Interface())
		}
	}
}
