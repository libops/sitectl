package plugin

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"testing"

	"github.com/libops/sitectl/pkg/config"
	"github.com/spf13/cobra"
)

func TestResolveIngressRouteSelectsNamedAndPrimaryRoutesDeterministically(t *testing.T) {
	t.Parallel()

	routes := IngressRoutes{
		Scheme: "https",
		Domain: "repo.example.org",
		Routes: []IngressRoute{
			{Name: "fcrepo", Service: "fcrepo"},
			{Name: "app", Service: "drupal", Primary: true},
			{Name: "fcrepo", Service: "other", DefaultDomain: "other.example.org"},
		},
	}

	primary, err := ResolveIngressRoute(nil, routes, "")
	if err != nil {
		t.Fatalf("ResolveIngressRoute(primary) error = %v", err)
	}
	if primary.Route.Name != "app" || primary.URL != "https://repo.example.org" {
		t.Fatalf("ResolveIngressRoute(primary) = %+v", primary)
	}

	named, err := ResolveIngressRoute(nil, routes, "fcrepo")
	if err != nil {
		t.Fatalf("ResolveIngressRoute(fcrepo) error = %v", err)
	}
	if named.Route.Service != "fcrepo" || named.URL != "https://repo.example.org" {
		t.Fatalf("ResolveIngressRoute(fcrepo) = %+v", named)
	}
}

func TestResolveIngressRouteDistinguishesMissingRouteAndUnresolvedURL(t *testing.T) {
	t.Parallel()

	_, err := ResolveIngressRoute(nil, IngressRoutes{Routes: []IngressRoute{{Name: "app", Service: "drupal"}}}, "fcrepo")
	if !errors.Is(err, ErrIngressRouteNotFound) {
		t.Fatalf("missing route error = %v, want ErrIngressRouteNotFound", err)
	}
	if errors.Is(err, ErrIngressRouteURLUnresolved) {
		t.Fatalf("missing route error unexpectedly matches ErrIngressRouteURLUnresolved: %v", err)
	}

	_, err = ResolveIngressRoute(nil, IngressRoutes{Routes: []IngressRoute{{Name: "app", Service: "drupal"}}}, "app")
	if !errors.Is(err, ErrIngressRouteURLUnresolved) {
		t.Fatalf("unresolved URL error = %v, want ErrIngressRouteURLUnresolved", err)
	}
	if errors.Is(err, ErrIngressRouteNotFound) {
		t.Fatalf("unresolved URL error unexpectedly matches ErrIngressRouteNotFound: %v", err)
	}
}

func TestResolveIngressRouteUsesTraefikForLocalPublishedPortAndComposesPathOnce(t *testing.T) {
	t.Parallel()

	projectDir := t.TempDir()
	copyIngressResolverFixture(t, projectDir, "local-port")
	ctx := &config.Context{
		DockerHostType: config.ContextLocal,
		ProjectDir:     projectDir,
		ComposeFile:    []string{"docker-compose.yml", "docker-compose.override.yml"},
	}
	routes := IngressRoutes{Routes: []IngressRoute{{
		Name:          "app",
		Service:       "drupal",
		Router:        "drupal",
		DefaultScheme: "http",
		DefaultDomain: "localhost",
		Path:          "/iiif",
		Primary:       true,
	}}}

	got, err := ResolveIngressRoute(ctx, routes, "")
	if err != nil {
		t.Fatalf("ResolveIngressRoute() error = %v", err)
	}
	if got.URL != "http://localhost:8080/iiif" {
		t.Fatalf("ResolveIngressRoute().URL = %q, want http://localhost:8080/iiif", got.URL)
	}
	if got.Resolution != IngressRouteResolutionTraefik {
		t.Fatalf("ResolveIngressRoute().Resolution = %q", got.Resolution)
	}

	composed, err := composeIngressRoutePath(got.URL, "/iiif")
	if err != nil {
		t.Fatalf("composeIngressRoutePath() error = %v", err)
	}
	if composed != got.URL {
		t.Fatalf("composeIngressRoutePath() = %q, want path exactly once in %q", composed, got.URL)
	}
}

func TestResolveIngressRouteRemoteCatalogFallbackNeverAssumesLocalhost(t *testing.T) {
	t.Parallel()

	ctx := &config.Context{DockerHostType: config.ContextRemote}
	route := IngressRoute{
		Name:          "app",
		DefaultScheme: "https",
		DefaultDomain: "repo.example.org",
		Path:          "api",
	}

	got, err := ResolveIngressRoute(ctx, IngressRoutes{Routes: []IngressRoute{route}}, "app")
	if err != nil {
		t.Fatalf("ResolveIngressRoute() error = %v", err)
	}
	if got.URL != "https://repo.example.org/api" || got.Resolution != IngressRouteResolutionCatalog {
		t.Fatalf("ResolveIngressRoute() = %+v", got)
	}

	route.DefaultDomain = ""
	_, err = ResolveIngressRoute(ctx, IngressRoutes{Routes: []IngressRoute{route}}, "app")
	if !errors.Is(err, ErrIngressRouteURLUnresolved) {
		t.Fatalf("ResolveIngressRoute() error = %v, want ErrIngressRouteURLUnresolved", err)
	}
}

func TestStandardComposeWebProviderDoesNotDeclareRemoteLocalhost(t *testing.T) {
	t.Parallel()

	remote := &config.Context{DockerHostType: config.ContextRemote}
	if got := standardComposeWebDefaultDomain(remote); got != "" {
		t.Fatalf("standardComposeWebDefaultDomain(remote) = %q, want empty", got)
	}
	local := &config.Context{DockerHostType: config.ContextLocal}
	if got := standardComposeWebDefaultDomain(local); got != "localhost" {
		t.Fatalf("standardComposeWebDefaultDomain(local) = %q, want localhost", got)
	}

	routes := IngressRoutes{Routes: []IngressRoute{{Name: "app"}}}
	_, err := ResolveIngressRoute(remote, routes, "app")
	if !errors.Is(err, ErrIngressRouteURLUnresolved) {
		t.Fatalf("ResolveIngressRoute() error = %v, want ErrIngressRouteURLUnresolved", err)
	}
}

func TestResolveIngressRouteLocalCatalogFallbackDefaultsToLocalhost(t *testing.T) {
	t.Parallel()

	ctx := &config.Context{DockerHostType: config.ContextLocal}
	routes := IngressRoutes{Routes: []IngressRoute{{Name: "app", Path: "/api"}}}
	got, err := ResolveIngressRoute(ctx, routes, "app")
	if err != nil {
		t.Fatalf("ResolveIngressRoute() error = %v", err)
	}
	if got.URL != "http://localhost/api" || got.Resolution != IngressRouteResolutionCatalog {
		t.Fatalf("ResolveIngressRoute() = %+v", got)
	}
}

func TestResolveIngressRouteFromProvider(t *testing.T) {
	t.Parallel()

	provider := StaticIngressRoutes(IngressRoute{
		Name:          "app",
		Service:       "drupal",
		DefaultScheme: "https",
		DefaultDomain: "repo.example.org",
		Primary:       true,
	})
	got, err := ResolveIngressRouteFromProvider(&cobra.Command{}, nil, provider, "")
	if err != nil {
		t.Fatalf("ResolveIngressRouteFromProvider() error = %v", err)
	}
	if got.Route.Name != "app" || got.URL != "https://repo.example.org" {
		t.Fatalf("ResolveIngressRouteFromProvider() = %+v", got)
	}
}

func TestComposeIngressRoutePath(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		baseURL   string
		routePath string
		want      string
	}{
		{name: "adds to root", baseURL: "https://example.org/", routePath: "/iiif", want: "https://example.org/iiif"},
		{name: "does not duplicate equal path", baseURL: "https://example.org/iiif", routePath: "/iiif", want: "https://example.org/iiif"},
		{name: "keeps more specific discovered path", baseURL: "https://example.org/iiif/2", routePath: "/iiif", want: "https://example.org/iiif/2"},
		{name: "uses more specific catalog path", baseURL: "https://example.org/iiif", routePath: "/iiif/2", want: "https://example.org/iiif/2"},
		{name: "does not duplicate path after proxy prefix", baseURL: "https://example.org/proxy/iiif", routePath: "/iiif", want: "https://example.org/proxy/iiif"},
		{name: "joins independent prefixes", baseURL: "https://example.org/proxy", routePath: "/iiif", want: "https://example.org/proxy/iiif"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got, err := composeIngressRoutePath(tt.baseURL, tt.routePath)
			if err != nil {
				t.Fatalf("composeIngressRoutePath() error = %v", err)
			}
			if got != tt.want {
				t.Fatalf("composeIngressRoutePath() = %q, want %q", got, tt.want)
			}
		})
	}
}

func copyIngressResolverFixture(t *testing.T, projectDir, name string) {
	t.Helper()

	root := filepath.Join("..", "healthcheck", "testdata", "traefik", name)
	if err := filepath.WalkDir(root, func(source string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.IsDir() {
			return nil
		}
		rel, err := filepath.Rel(root, source)
		if err != nil {
			return err
		}
		data, err := os.ReadFile(source)
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
