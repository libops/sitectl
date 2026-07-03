package plugin

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/libops/sitectl/pkg/config"
)

func TestShouldPreferTraefikHealthcheckURLKeepsEnvDomainOverHostlessRouter(t *testing.T) {
	t.Parallel()

	if shouldPreferTraefikHealthcheckURL("http://app-test.libops.io/", "http://localhost/") {
		t.Fatal("expected app env domain to win over hostless Traefik localhost fallback")
	}
}

func TestShouldPreferTraefikHealthcheckURLUsesTraefikDomain(t *testing.T) {
	t.Parallel()

	if !shouldPreferTraefikHealthcheckURL("http://localhost/", "http://repo.example.org/") {
		t.Fatal("expected Traefik domain to win over localhost env fallback")
	}
}

func TestHealthcheckURLFromServiceEnvironmentUsesAppURL(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	compose := []byte(`services:
  drupal:
    environment:
      DRUPAL_DEFAULT_SITE_URL: "http://app-test.libops.io"
      DRUPAL_ENABLE_HTTPS: "true"
`)
	if err := os.WriteFile(filepath.Join(dir, "docker-compose.yml"), compose, 0o644); err != nil {
		t.Fatalf("write compose: %v", err)
	}
	ctx := &config.Context{
		DockerHostType: config.ContextLocal,
		ProjectDir:     dir,
	}

	got := healthcheckURLFromServiceEnvironment(ctx, "drupal", StandardComposeWebHealthcheckOptions{
		URLVariables:   []string{"DRUPAL_DEFAULT_SITE_URL"},
		HTTPSVariables: []string{"DRUPAL_ENABLE_HTTPS"},
	})
	if got != "https://app-test.libops.io/" {
		t.Fatalf("healthcheckURLFromServiceEnvironment() = %q, want https://app-test.libops.io/", got)
	}
}
