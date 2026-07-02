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

func TestPublicURLFromTraefikUsesDefaultDomainForHostlessRouter(t *testing.T) {
	t.Parallel()

	projectDir := t.TempDir()
	writeTraefikFile(t, projectDir, "docker-compose.yml", `services:
  traefik:
    command:
      - --providers.file.directory=/etc/traefik/dynamic
      - --entrypoints.web.address=:80
    ports:
      - "80:80"
    volumes:
      - ./conf/traefik:/etc/traefik/dynamic:ro
  drupal: {}
`)
	writeTraefikFile(t, projectDir, "docker-compose.override.yml", `services:
  traefik:
    ports: !override
      - "8080:80"
`)
	writeTraefikFile(t, projectDir, "conf/traefik/drupal.yml", `http:
  services:
    drupal:
      loadBalancer:
        servers:
          - url: http://drupal:80
  routers:
    drupal:
      rule: PathPrefix("/")
      entryPoints:
        - web
      service: drupal
`)

	got, ok, err := PublicURLFromTraefik(&config.Context{ProjectDir: projectDir, DockerHostType: config.ContextLocal}, TraefikRouteOptions{
		AppService:    "drupal",
		Router:        "drupal",
		DefaultScheme: "http",
		DefaultDomain: "172.234.17.94",
	})
	if err != nil {
		t.Fatalf("PublicURLFromTraefik() error = %v", err)
	}
	if !ok {
		t.Fatal("expected Traefik route to resolve")
	}
	if got != "http://172.234.17.94:8080/" {
		t.Fatalf("PublicURLFromTraefik() = %q, want http://172.234.17.94:8080/", got)
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

func writeTraefikFile(t *testing.T, projectDir, rel, content string) {
	t.Helper()

	target := filepath.Join(projectDir, filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
		t.Fatalf("MkdirAll(%q) error = %v", filepath.Dir(target), err)
	}
	if err := os.WriteFile(target, []byte(content), 0o600); err != nil {
		t.Fatalf("WriteFile(%q) error = %v", target, err)
	}
}
