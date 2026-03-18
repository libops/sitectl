package config

import (
	"os"
	"path/filepath"
	"testing"
)

func TestDetectComposeProjectNameFromEnv(t *testing.T) {
	projectDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(projectDir, ".env"), []byte("COMPOSE_PROJECT_NAME=lehigh-d10\n"), 0o600); err != nil {
		t.Fatalf("WriteFile(.env) error = %v", err)
	}

	if got := DetectComposeProjectName(projectDir); got != "lehigh-d10" {
		t.Fatalf("expected lehigh-d10, got %q", got)
	}
}

func TestDetectComposeProjectNameFromComposeName(t *testing.T) {
	projectDir := t.TempDir()
	content := "name: isle-preserve\nservices:\n  web:\n    image: nginx:latest\n"
	if err := os.WriteFile(filepath.Join(projectDir, "compose.yaml"), []byte(content), 0o600); err != nil {
		t.Fatalf("WriteFile(compose.yaml) error = %v", err)
	}

	if got := DetectComposeProjectName(projectDir); got != "isle-preserve" {
		t.Fatalf("expected isle-preserve, got %q", got)
	}
}

func TestDetectComposeNetworkNameUsesDefaultNetwork(t *testing.T) {
	projectDir := t.TempDir()
	content := "name: isle-preserve\nservices:\n  web:\n    image: nginx:latest\n"
	if err := os.WriteFile(filepath.Join(projectDir, "compose.yaml"), []byte(content), 0o600); err != nil {
		t.Fatalf("WriteFile(compose.yaml) error = %v", err)
	}

	if got := DetectComposeNetworkName(projectDir, "isle-preserve"); got != "isle-preserve_default" {
		t.Fatalf("expected isle-preserve_default, got %q", got)
	}
}

func TestDetectComposeNetworkNameUsesExplicitNetworkName(t *testing.T) {
	projectDir := t.TempDir()
	content := "services:\n  web:\n    image: nginx:latest\n    networks:\n      - frontend\nnetworks:\n  frontend:\n    name: shared-frontdoor\n"
	if err := os.WriteFile(filepath.Join(projectDir, "compose.yaml"), []byte(content), 0o600); err != nil {
		t.Fatalf("WriteFile(compose.yaml) error = %v", err)
	}

	if got := DetectComposeNetworkName(projectDir, "isle-preserve"); got != "shared-frontdoor" {
		t.Fatalf("expected shared-frontdoor, got %q", got)
	}
}
