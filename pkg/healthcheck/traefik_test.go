package healthcheck

import (
	"io/fs"
	"os"
	"path/filepath"
	"testing"

	"github.com/libops/sitectl/pkg/config"
)

func TestPublicURLFromTraefikUsesRouterHostAndLocalDevPort(t *testing.T) {
	t.Parallel()

	projectDir := t.TempDir()
	copyTraefikFixture(t, projectDir, "local-port")

	got, ok, err := PublicURLFromTraefik(&config.Context{ProjectDir: projectDir, DockerHostType: config.ContextLocal}, TraefikRouteOptions{
		AppService:    "drupal",
		Router:        "drupal",
		DefaultScheme: "http",
		DefaultDomain: "drupal.traefik.me",
	})
	if err != nil {
		t.Fatalf("PublicURLFromTraefik() error = %v", err)
	}
	if !ok {
		t.Fatal("expected Traefik route to resolve")
	}
	if got != "http://localhost:8080/" {
		t.Fatalf("PublicURLFromTraefik() = %q, want http://localhost:8080/", got)
	}
}

func TestPublicURLFromTraefikPrefersPathlessPrefixedRouter(t *testing.T) {
	t.Parallel()

	projectDir := t.TempDir()
	copyTraefikFixture(t, projectDir, "pathless")

	got, ok, err := PublicURLFromTraefik(&config.Context{ProjectDir: projectDir, DockerHostType: config.ContextLocal}, TraefikRouteOptions{
		AppService:    "archivesspace",
		DefaultScheme: "http",
		DefaultDomain: "localhost",
	})
	if err != nil {
		t.Fatalf("PublicURLFromTraefik() error = %v", err)
	}
	if !ok {
		t.Fatal("expected Traefik route to resolve")
	}
	if got != "http://localhost/" {
		t.Fatalf("PublicURLFromTraefik() = %q, want http://localhost/", got)
	}
}

func TestPublicURLFromTraefikUsesTLSRouter(t *testing.T) {
	t.Parallel()

	projectDir := t.TempDir()
	copyTraefikFixture(t, projectDir, "tls")

	got, ok, err := PublicURLFromTraefik(&config.Context{ProjectDir: projectDir, DockerHostType: config.ContextLocal}, TraefikRouteOptions{
		AppService:    "drupal",
		Router:        "drupal",
		DefaultScheme: "http",
		DefaultDomain: "localhost",
	})
	if err != nil {
		t.Fatalf("PublicURLFromTraefik() error = %v", err)
	}
	if !ok {
		t.Fatal("expected Traefik route to resolve")
	}
	if got != "https://repo.example.org/" {
		t.Fatalf("PublicURLFromTraefik() = %q, want https://repo.example.org/", got)
	}
}

func copyTraefikFixture(t *testing.T, projectDir, name string) {
	t.Helper()

	root := filepath.Join("testdata", "traefik", name)
	if err := filepath.WalkDir(root, func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.IsDir() {
			return nil
		}
		rel, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		target := filepath.Join(projectDir, rel)
		if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
			return err
		}
		return os.WriteFile(target, data, 0o600)
	}); err != nil {
		t.Fatalf("copy fixture %q: %v", name, err)
	}
}
