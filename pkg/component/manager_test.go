package component

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/libops/sitectl/pkg/config"
)

func TestComposeFileRemoveServiceAndPrune(t *testing.T) {
	t.Parallel()

	input := []byte(`
services:
  app:
    image: app
    depends_on:
      - fedora
    volumes:
      - app-data:/data
  fedora:
    image: fedora
    volumes:
      - fedora-data:/var/lib/fedora
volumes:
  app-data: {}
  fedora-data: {}
`)

	composeFile, err := LoadComposeFile(input)
	if err != nil {
		t.Fatalf("LoadComposeFile() error = %v", err)
	}

	if !composeFile.RemoveService("fedora") {
		t.Fatalf("RemoveService() did not remove service")
	}
	composeFile.PruneUnusedResources()

	output, err := composeFile.Bytes()
	if err != nil {
		t.Fatalf("Bytes() error = %v", err)
	}

	rendered := string(output)
	if strings.Contains(rendered, "fedora-data") {
		t.Fatalf("expected fedora volume to be pruned, got:\n%s", rendered)
	}
	if strings.Contains(rendered, "depends_on") {
		t.Fatalf("expected depends_on reference to removed service to be cleared, got:\n%s", rendered)
	}
}

func TestManagerDisableAndEnableComponent(t *testing.T) {
	t.Parallel()

	projectDir := t.TempDir()
	composePath := filepath.Join(projectDir, "docker-compose.yml")
	configDir := filepath.Join(projectDir, "config", "sync")

	if err := os.MkdirAll(configDir, 0o755); err != nil {
		t.Fatalf("MkdirAll() error = %v", err)
	}

	if err := os.WriteFile(composePath, []byte(`
services:
  drupal:
    image: drupal
    depends_on:
      - fedora
  fedora:
    image: fedora
volumes:
  fedora-data: {}
`), 0o644); err != nil {
		t.Fatalf("WriteFile(compose) error = %v", err)
	}

	if err := os.WriteFile(filepath.Join(configDir, "example.settings.yml"), []byte("role_map:\n  fedoraadmin: '0'\nuri: fedora\n"), 0o644); err != nil {
		t.Fatalf("WriteFile(example.settings.yml) error = %v", err)
	}
	if err := os.WriteFile(filepath.Join(configDir, "user.role.fedoraadmin.yml"), []byte("id: fedoraadmin\n"), 0o644); err != nil {
		t.Fatalf("WriteFile(user.role.fedoraadmin.yml) error = %v", err)
	}

	ctx := &config.Context{
		DockerHostType: config.ContextLocal,
		ProjectDir:     projectDir,
	}

	manager := NewManager(ctx)
	spec := ComponentSpec{
		Name: "fedora",
		Compose: ComposeSpec{
			RemoveServices:      []string{"fedora"},
			PruneUnusedResource: true,
			Definitions: mustParseComposeDefinitions(t, []byte(`
services:
  fedora:
    image: fedora
volumes:
  fedora-data: {}
`)),
		},
		Drupal: DrupalSpec{
			Files: map[string][]byte{
				"user.role.fedoraadmin.yml": []byte("id: fedoraadmin\n"),
			},
			DeleteFiles: []string{"user.role.fedoraadmin.yml"},
			DisableTransforms: []DrupalTransform{
				DeleteMapEntriesTransform{Matches: []MapEntryMatch{{Key: "fedoraadmin", Value: "0"}}},
				ReplaceStringsTransform{Replacements: []StringReplacement{{Old: "fedora", New: "gs-production"}}},
			},
			EnableTransforms: []DrupalTransform{
				ReplaceStringsTransform{Replacements: []StringReplacement{{Old: "gs-production", New: "fedora"}}},
			},
		},
	}

	if err := manager.DisableComponentWithOptions(context.Background(), spec, ApplyOptions{Yolo: true}); err != nil {
		t.Fatalf("DisableComponent() error = %v", err)
	}

	composeAfterDisable, err := os.ReadFile(composePath)
	if err != nil {
		t.Fatalf("ReadFile(compose after disable) error = %v", err)
	}
	if strings.Contains(string(composeAfterDisable), "\nfedora:") {
		t.Fatalf("expected fedora service removed, got:\n%s", string(composeAfterDisable))
	}

	configAfterDisable, err := os.ReadFile(filepath.Join(configDir, "example.settings.yml"))
	if err != nil {
		t.Fatalf("ReadFile(example after disable) error = %v", err)
	}
	if strings.Contains(string(configAfterDisable), "fedoraadmin") {
		t.Fatalf("expected fedoraadmin map entry removed, got:\n%s", string(configAfterDisable))
	}
	if !strings.Contains(string(configAfterDisable), "gs-production") {
		t.Fatalf("expected URI replacement applied, got:\n%s", string(configAfterDisable))
	}
	if _, err := os.Stat(filepath.Join(configDir, "user.role.fedoraadmin.yml")); !os.IsNotExist(err) {
		t.Fatalf("expected component-owned file deleted, stat err = %v", err)
	}

	if err := manager.EnableComponentWithOptions(context.Background(), spec, ApplyOptions{Yolo: true}); err != nil {
		t.Fatalf("EnableComponent() error = %v", err)
	}

	composeAfterEnable, err := os.ReadFile(composePath)
	if err != nil {
		t.Fatalf("ReadFile(compose after enable) error = %v", err)
	}
	if !strings.Contains(string(composeAfterEnable), "fedora:") {
		t.Fatalf("expected fedora service restored, got:\n%s", string(composeAfterEnable))
	}

	configAfterEnable, err := os.ReadFile(filepath.Join(configDir, "example.settings.yml"))
	if err != nil {
		t.Fatalf("ReadFile(example after enable) error = %v", err)
	}
	if !strings.Contains(string(configAfterEnable), "fedora") {
		t.Fatalf("expected URI replacement reversed, got:\n%s", string(configAfterEnable))
	}
	if _, err := os.Stat(filepath.Join(configDir, "user.role.fedoraadmin.yml")); err != nil {
		t.Fatalf("expected component-owned file restored, stat err = %v", err)
	}
}

func TestManagerLocalOnlyGate(t *testing.T) {
	t.Parallel()

	ctx := &config.Context{DockerHostType: config.ContextRemote}
	manager := NewManager(ctx)

	err := manager.DisableComponent(context.Background(), ComponentSpec{
		Name:  "fedora",
		Gates: GateSpec{LocalOnly: true},
	})
	if err == nil {
		t.Fatal("expected local-only gate to reject remote context")
	}
}

func TestManagerConfirmationGate(t *testing.T) {
	t.Parallel()

	ctx := &config.Context{DockerHostType: config.ContextLocal, Name: "local"}
	manager := NewManager(ctx)

	called := false
	err := manager.DisableComponentWithOptions(context.Background(), ComponentSpec{
		Name: "fedora",
	}, ApplyOptions{
		Confirm: func(prompt string) (bool, error) {
			called = true
			if !strings.Contains(prompt, `Disable component "fedora" on context "local"?`) {
				t.Fatalf("unexpected prompt %q", prompt)
			}
			return false, nil
		},
	})
	if err == nil {
		t.Fatal("expected confirmation cancellation error")
	}
	if err != ErrActionCancelled {
		t.Fatalf("expected ErrActionCancelled, got %v", err)
	}
	if !called {
		t.Fatal("expected confirmation function to be called")
	}
}

func TestManagerYoloBypassesConfirmation(t *testing.T) {
	t.Parallel()

	ctx := &config.Context{DockerHostType: config.ContextLocal, Name: "local"}
	manager := NewManager(ctx)

	err := manager.DisableComponentWithOptions(context.Background(), ComponentSpec{
		Name: "fedora",
	}, ApplyOptions{
		Yolo: true,
		Confirm: func(prompt string) (bool, error) {
			t.Fatalf("confirm should not be called, got prompt %q", prompt)
			return false, nil
		},
	})
	if err != nil {
		t.Fatalf("expected yolo to bypass confirmation, got %v", err)
	}
}

func TestManagerConfirmationDeclineDoesNotMutateFiles(t *testing.T) {
	t.Parallel()

	projectDir := t.TempDir()
	composePath := filepath.Join(projectDir, "docker-compose.yml")
	configDir := filepath.Join(projectDir, "config", "sync")

	if err := os.MkdirAll(configDir, 0o755); err != nil {
		t.Fatalf("MkdirAll() error = %v", err)
	}

	originalCompose := []byte("services:\n  fcrepo:\n    image: fcrepo\n")
	originalConfig := []byte("uri: fedora\n")

	if err := os.WriteFile(composePath, originalCompose, 0o644); err != nil {
		t.Fatalf("WriteFile(compose) error = %v", err)
	}
	if err := os.WriteFile(filepath.Join(configDir, "example.settings.yml"), originalConfig, 0o644); err != nil {
		t.Fatalf("WriteFile(config) error = %v", err)
	}

	ctx := &config.Context{
		DockerHostType: config.ContextLocal,
		ProjectDir:     projectDir,
		Name:           "local",
	}

	manager := NewManager(ctx)
	err := manager.DisableComponentWithOptions(context.Background(), ComponentSpec{
		Name: "fcrepo",
		Compose: ComposeSpec{
			RemoveServices: []string{"fcrepo"},
		},
		Drupal: DrupalSpec{
			DisableTransforms: []DrupalTransform{
				ReplaceStringsTransform{Replacements: []StringReplacement{{Old: "fedora", New: "gs-production"}}},
			},
		},
	}, ApplyOptions{
		Confirm: func(prompt string) (bool, error) {
			return false, nil
		},
	})
	if err != ErrActionCancelled {
		t.Fatalf("expected ErrActionCancelled, got %v", err)
	}

	gotCompose, err := os.ReadFile(composePath)
	if err != nil {
		t.Fatalf("ReadFile(compose) error = %v", err)
	}
	if string(gotCompose) != string(originalCompose) {
		t.Fatalf("expected compose to remain unchanged, got:\n%s", string(gotCompose))
	}

	gotConfig, err := os.ReadFile(filepath.Join(configDir, "example.settings.yml"))
	if err != nil {
		t.Fatalf("ReadFile(config) error = %v", err)
	}
	if string(gotConfig) != string(originalConfig) {
		t.Fatalf("expected config to remain unchanged, got:\n%s", string(gotConfig))
	}
}

func TestManagerReconcileComponentOffAndOn(t *testing.T) {
	t.Parallel()

	projectDir := t.TempDir()
	composePath := filepath.Join(projectDir, "docker-compose.yml")
	configDir := filepath.Join(projectDir, "config", "sync")

	if err := os.MkdirAll(configDir, 0o755); err != nil {
		t.Fatalf("MkdirAll() error = %v", err)
	}

	if err := os.WriteFile(composePath, []byte(`
services:
  drupal:
    image: drupal
  fcrepo:
    image: fcrepo
`), 0o644); err != nil {
		t.Fatalf("WriteFile(compose) error = %v", err)
	}

	if err := os.WriteFile(filepath.Join(configDir, "example.settings.yml"), []byte("uri: fcrepo\n"), 0o644); err != nil {
		t.Fatalf("WriteFile(example.settings.yml) error = %v", err)
	}

	ctx := &config.Context{
		DockerHostType: config.ContextLocal,
		ProjectDir:     projectDir,
		Name:           "local",
	}

	manager := NewManager(ctx)
	fcrepo := NewStaticComponent(
		"fcrepo",
		StateOn,
		ComponentSpec{
			Compose: ComposeSpec{
				Definitions: mustParseComposeDefinitions(t, []byte(`
services:
  fcrepo:
    image: fcrepo
`)),
			},
			Drupal: DrupalSpec{
				EnableTransforms: []DrupalTransform{
					ReplaceStringsTransform{Replacements: []StringReplacement{{Old: "disabled", New: "fcrepo"}}},
				},
			},
		},
		ComponentSpec{
			Gates: GateSpec{LocalOnly: true},
			Compose: ComposeSpec{
				RemoveServices: []string{"fcrepo"},
			},
			Drupal: DrupalSpec{
				DisableTransforms: []DrupalTransform{
					ReplaceStringsTransform{Replacements: []StringReplacement{{Old: "fcrepo", New: "disabled"}}},
				},
			},
		},
	)

	if err := manager.ReconcileComponent(context.Background(), fcrepo, StateOff, ApplyOptions{Yolo: true}); err != nil {
		t.Fatalf("ReconcileComponent(off) error = %v", err)
	}
	composeAfterOff, err := os.ReadFile(composePath)
	if err != nil {
		t.Fatalf("ReadFile(compose after off) error = %v", err)
	}
	if strings.Contains(string(composeAfterOff), "\nfcrepo:") {
		t.Fatalf("expected fcrepo service removed, got:\n%s", string(composeAfterOff))
	}

	if err := manager.ReconcileComponent(context.Background(), fcrepo, StateOn, ApplyOptions{Yolo: true}); err != nil {
		t.Fatalf("ReconcileComponent(on) error = %v", err)
	}
	composeAfterOn, err := os.ReadFile(composePath)
	if err != nil {
		t.Fatalf("ReadFile(compose after on) error = %v", err)
	}
	if !strings.Contains(string(composeAfterOn), "fcrepo:") {
		t.Fatalf("expected fcrepo service restored, got:\n%s", string(composeAfterOn))
	}
}

func TestManagerReconcileAllUsesDefaultsAndOverrides(t *testing.T) {
	t.Parallel()

	projectDir := t.TempDir()
	composePath := filepath.Join(projectDir, "docker-compose.yml")

	if err := os.WriteFile(composePath, []byte(`
services:
  drupal:
    image: drupal
  fcrepo:
    image: fcrepo
  blazegraph:
    image: blazegraph
`), 0o644); err != nil {
		t.Fatalf("WriteFile(compose) error = %v", err)
	}

	ctx := &config.Context{
		DockerHostType: config.ContextLocal,
		ProjectDir:     projectDir,
		Name:           "local",
	}

	manager := NewManager(ctx)
	fcrepo := NewStaticComponent(
		"fcrepo",
		StateOn,
		ComponentSpec{
			Compose: ComposeSpec{
				Definitions: mustParseComposeDefinitions(t, []byte(`
services:
  fcrepo:
    image: fcrepo
`)),
			},
		},
		ComponentSpec{
			Compose: ComposeSpec{
				RemoveServices: []string{"fcrepo"},
			},
		},
	)
	blazegraph := NewStaticComponent(
		"blazegraph",
		StateOff,
		ComponentSpec{
			Compose: ComposeSpec{
				Definitions: mustParseComposeDefinitions(t, []byte(`
services:
  blazegraph:
    image: blazegraph
`)),
			},
		},
		ComponentSpec{
			Compose: ComposeSpec{
				RemoveServices: []string{"blazegraph"},
			},
		},
	)

	err := manager.ReconcileAll(context.Background(), map[string]State{
		"fcrepo": StateOff,
	}, ApplyOptions{Yolo: true}, fcrepo, blazegraph)
	if err != nil {
		t.Fatalf("ReconcileAll() error = %v", err)
	}

	composeAfter, err := os.ReadFile(composePath)
	if err != nil {
		t.Fatalf("ReadFile(compose after reconcile all) error = %v", err)
	}
	rendered := string(composeAfter)
	if strings.Contains(rendered, "\nfcrepo:") {
		t.Fatalf("expected fcrepo removed by explicit override, got:\n%s", rendered)
	}
	if strings.Contains(rendered, "\nblazegraph:") {
		t.Fatalf("expected blazegraph removed by default off state, got:\n%s", rendered)
	}
}

func TestRegistryRejectsDuplicateRegistration(t *testing.T) {
	t.Parallel()

	registry := NewRegistry()
	feature := NewStaticComponent("blazegraph", StateOn, ComponentSpec{}, ComponentSpec{})
	if err := registry.Register(feature); err != nil {
		t.Fatalf("Register() error = %v", err)
	}
	if err := registry.Register(feature); err == nil {
		t.Fatal("expected duplicate registration error")
	}
}

func TestParseState(t *testing.T) {
	t.Parallel()

	state, err := ParseState("OFF")
	if err != nil {
		t.Fatalf("ParseState() error = %v", err)
	}
	if state != StateOff {
		t.Fatalf("expected %q, got %q", StateOff, state)
	}

	if _, err := ParseState("maybe"); err == nil {
		t.Fatal("expected invalid state error")
	}
}

func TestRegistryRegisterAndLookup(t *testing.T) {
	t.Parallel()

	registry := NewRegistry()
	feature := NewStaticComponent("blazegraph", StateOn, ComponentSpec{}, ComponentSpec{})
	if err := registry.Register(feature); err != nil {
		t.Fatalf("Register() error = %v", err)
	}

	got, ok := registry.Component("blazegraph")
	if !ok {
		t.Fatal("expected component lookup to succeed")
	}
	if got.Name() != "blazegraph" {
		t.Fatalf("expected blazegraph, got %q", got.Name())
	}
}

func mustParseComposeDefinitions(t *testing.T, data []byte) *ComposeDefinitions {
	t.Helper()

	defs, err := ParseComposeDefinitions(data)
	if err != nil {
		t.Fatalf("ParseComposeDefinitions() error = %v", err)
	}
	return defs
}
