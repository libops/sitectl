package component

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/libops/sitectl/pkg/config"
)

func TestDetectComponentStatusOnOffAndDrifted(t *testing.T) {
	t.Parallel()

	def := Definition{
		Name: "fcrepo",
		On: DomainSpec{
			Compose: YAMLStateSpec{
				Rules: []YAMLRule{
					{Files: []string{"docker-compose.yml"}, Op: OpRestore, Path: ".services.fcrepo"},
				},
			},
			Drupal: YAMLStateSpec{
				Rules: []YAMLRule{
					{Files: []string{"user.role.fedoraadmin.yml"}, Op: OpRestore, Path: "."},
				},
			},
		},
		Off: DomainSpec{
			Compose: YAMLStateSpec{
				Rules: []YAMLRule{
					{Files: []string{"docker-compose.yml"}, Op: OpDelete, Path: ".services.fcrepo"},
				},
			},
			Drupal: YAMLStateSpec{
				Rules: []YAMLRule{
					{Files: []string{"user.role.fedoraadmin.yml"}, Op: OpDelete, Path: "."},
				},
			},
		},
	}

	t.Run("on", func(t *testing.T) {
		t.Parallel()
		projectDir := t.TempDir()
		writeStatusFixture(t, projectDir, true, true)
		status := detectStatus(t, projectDir, def)
		if status.State != DetectedState(StateOn) {
			t.Fatalf("expected state on, got %q", status.State)
		}
	})

	t.Run("off", func(t *testing.T) {
		t.Parallel()
		projectDir := t.TempDir()
		writeStatusFixture(t, projectDir, false, false)
		status := detectStatus(t, projectDir, def)
		if status.State != DetectedState(StateOff) {
			t.Fatalf("expected state off, got %q", status.State)
		}
	})

	t.Run("drifted", func(t *testing.T) {
		t.Parallel()
		projectDir := t.TempDir()
		writeStatusFixture(t, projectDir, false, true)
		status := detectStatus(t, projectDir, def)
		if status.State != StateDrifted {
			t.Fatalf("expected state drifted, got %q", status.State)
		}
		if status.On.Failed == 0 || status.Off.Failed == 0 {
			t.Fatalf("expected failures on both on/off checks, got on=%d off=%d", status.On.Failed, status.Off.Failed)
		}
	})
}

func TestDetectComponentStatusWildcardAndReplaceRules(t *testing.T) {
	t.Parallel()

	projectDir := t.TempDir()
	configDir := filepath.Join(projectDir, "config", "sync")
	if err := os.MkdirAll(configDir, 0o755); err != nil {
		t.Fatalf("MkdirAll() error = %v", err)
	}
	if err := os.WriteFile(filepath.Join(configDir, "example.yml"), []byte("roles:\n  other: '1'\nuri: gs-production://asset\n"), 0o644); err != nil {
		t.Fatalf("WriteFile(example.yml) error = %v", err)
	}
	if err := os.WriteFile(filepath.Join(projectDir, "docker-compose.yml"), []byte("services: {}\n"), 0o644); err != nil {
		t.Fatalf("WriteFile(compose) error = %v", err)
	}

	ctx := &config.Context{DockerHostType: config.ContextLocal, ProjectDir: projectDir}
	status, err := DetectComponentStatus(ctx, projectDir, Definition{
		Name: "fcrepo",
		Off: DomainSpec{
			Drupal: YAMLStateSpec{
				Rules: []YAMLRule{
					{Files: []string{"*.yml"}, Op: OpDelete, Path: ".**.fedoraadmin"},
					{Files: []string{"*.yml"}, Op: OpReplace, Path: ".**", Old: "fedora", Value: "gs-production"},
				},
			},
		},
		On: DomainSpec{
			Drupal: YAMLStateSpec{
				Rules: []YAMLRule{
					{Files: []string{"*.yml"}, Op: OpReplace, Path: ".**", Old: "gs-production", Value: "fedora"},
				},
			},
		},
	}, DetectOptions{})
	if err != nil {
		t.Fatalf("DetectComponentStatus() error = %v", err)
	}
	if status.State != DetectedState(StateOff) {
		t.Fatalf("expected state off, got %q", status.State)
	}
}

func TestDetectComponentStatusesSortsByName(t *testing.T) {
	t.Parallel()

	projectDir := t.TempDir()
	writeStatusFixture(t, projectDir, false, false)

	ctx := &config.Context{DockerHostType: config.ContextLocal, ProjectDir: projectDir}
	statuses, err := DetectComponentStatuses(ctx, projectDir, DetectOptions{},
		Definition{Name: "zeta", Off: DomainSpec{}},
		Definition{Name: "alpha", Off: DomainSpec{}},
	)
	if err != nil {
		t.Fatalf("DetectComponentStatuses() error = %v", err)
	}
	if len(statuses) != 2 {
		t.Fatalf("expected 2 statuses, got %d", len(statuses))
	}
	if statuses[0].Name != "alpha" || statuses[1].Name != "zeta" {
		t.Fatalf("expected statuses sorted by name, got %+v", statuses)
	}
}

func detectStatus(t *testing.T, projectDir string, def Definition) ComponentStatus {
	t.Helper()
	ctx := &config.Context{DockerHostType: config.ContextLocal, ProjectDir: projectDir}
	status, err := DetectComponentStatus(ctx, projectDir, def, DetectOptions{})
	if err != nil {
		t.Fatalf("DetectComponentStatus() error = %v", err)
	}
	return status
}

func writeStatusFixture(t *testing.T, projectDir string, withService, withFile bool) {
	t.Helper()
	configDir := filepath.Join(projectDir, "config", "sync")
	if err := os.MkdirAll(configDir, 0o755); err != nil {
		t.Fatalf("MkdirAll() error = %v", err)
	}

	compose := "services:\n  drupal:\n    image: drupal\n"
	if withService {
		compose += "  fcrepo:\n    image: fcrepo\n"
	}
	if err := os.WriteFile(filepath.Join(projectDir, "docker-compose.yml"), []byte(compose), 0o644); err != nil {
		t.Fatalf("WriteFile(compose) error = %v", err)
	}

	target := filepath.Join(configDir, "user.role.fedoraadmin.yml")
	if withFile {
		if err := os.WriteFile(target, []byte("id: fedoraadmin\n"), 0o644); err != nil {
			t.Fatalf("WriteFile(role) error = %v", err)
		}
		return
	}
	if err := os.Remove(target); err != nil && !os.IsNotExist(err) {
		t.Fatalf("Remove(role) error = %v", err)
	}
}
