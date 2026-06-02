package plugin

import (
	"os"
	"path/filepath"
	"testing"
)

func TestComposeProjectDiscoveryClaimsMatchingService(t *testing.T) {
	projectDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(projectDir, "docker-compose.yml"), []byte("services:\n  ojs:\n    image: ojs:latest\n"), 0o600); err != nil {
		t.Fatalf("WriteFile(docker-compose.yml) error = %v", err)
	}

	claim, err := claimComposeProject("ojs", projectDir, ComposeProjectDiscovery{
		RequiredServices: []string{"ojs"},
	})
	if err != nil {
		t.Fatalf("claimComposeProject() error = %v", err)
	}
	if claim == nil {
		t.Fatal("expected project claim")
	}
	if claim.Plugin != "ojs" {
		t.Fatalf("expected ojs claim, got %q", claim.Plugin)
	}
}

func TestComposeProjectDiscoveryRejectsForbiddenComposerPackage(t *testing.T) {
	projectDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(projectDir, "docker-compose.yml"), []byte("services:\n  drupal:\n    image: drupal:latest\n"), 0o600); err != nil {
		t.Fatalf("WriteFile(docker-compose.yml) error = %v", err)
	}
	if err := os.WriteFile(filepath.Join(projectDir, "composer.json"), []byte(`{"require":{"drupal/islandora":"^2"}}`), 0o600); err != nil {
		t.Fatalf("WriteFile(composer.json) error = %v", err)
	}

	claim, err := claimComposeProject("drupal", projectDir, ComposeProjectDiscovery{
		RequiredServices:          []string{"drupal"},
		ForbiddenComposerPackages: []string{"drupal/islandora"},
	})
	if err != nil {
		t.Fatalf("claimComposeProject() error = %v", err)
	}
	if claim != nil {
		t.Fatalf("expected no Drupal claim for Islandora composer package, got %+v", claim)
	}
}
