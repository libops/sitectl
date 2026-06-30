package healthcheck

import (
	"context"
	"fmt"
	"net"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"

	dockercontainer "github.com/docker/docker/api/types/container"
	"github.com/libops/sitectl/pkg/config"
	sitectldocker "github.com/libops/sitectl/pkg/docker"
)

func TestSolrCoreStatusDetailLoaded(t *testing.T) {
	detail, err := solrCoreStatusDetail(`{
		"responseHeader": {"status": 0},
		"initFailures": {},
		"status": {
			"default": {"name": "default", "instanceDir": "/opt/solr/server/solr/default"}
		}
	}`, "default")
	if err != nil {
		t.Fatalf("solrCoreStatusDetail() error = %v", err)
	}
	if detail != "Solr core default is loaded" {
		t.Fatalf("detail = %q", detail)
	}
}

func TestSolrCoreStatusDetailInitFailure(t *testing.T) {
	_, err := solrCoreStatusDetail(`{
		"initFailures": {
			"default": {
				"msg": "Error loading class 'solr.ICUCollationField'"
			}
		},
		"status": {}
	}`, "default")
	if err == nil {
		t.Fatal("expected init failure error")
	}
	if !strings.Contains(err.Error(), "solr core default has init failure") || !strings.Contains(err.Error(), "ICUCollationField") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestSolrCoreStatusDetailMissingCore(t *testing.T) {
	_, err := solrCoreStatusDetail(`{"initFailures": {}, "status": {}}`, "default")
	if err == nil {
		t.Fatal("expected missing core error")
	}
	if !strings.Contains(err.Error(), "solr core default not found") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestTrimOutputCollapsesWhitespace(t *testing.T) {
	got := trimOutput("{\n  \"status\":\"OK\"\n}")
	if got != `{ "status":"OK" }` {
		t.Fatalf("trimOutput() = %q", got)
	}
}

func TestPublicURLFromEnvPrefersSiteURL(t *testing.T) {
	projectDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(projectDir, ".env"), []byte("DOMAIN=example.test\nSITE_URL=http://example.test:8080/\n"), 0o600); err != nil {
		t.Fatalf("WriteFile(.env) error = %v", err)
	}

	got := PublicURLFromEnv(&config.Context{ProjectDir: projectDir, DockerHostType: config.ContextLocal}, "http", "localhost")
	if got != "http://example.test:8080/" {
		t.Fatalf("PublicURLFromEnv() = %q", got)
	}
}

func TestServiceEnvReadsConfiguredContainerEnv(t *testing.T) {
	checker := &DockerChecker{
		Context: &config.Context{Name: "test", ProjectName: "project"},
		Client: &sitectldocker.DockerClient{CLI: fakeDockerAPI{
			containers: []dockercontainer.Summary{{
				ID:     "abc123",
				Names:  []string{"/project-drupal-1"},
				Labels: map[string]string{"com.docker.compose.service": "drupal"},
			}},
			inspect: map[string]dockercontainer.InspectResponse{
				"/project-drupal-1": {Config: &dockercontainer.Config{Env: []string{
					"DRUPAL_DEFAULT_SITE_URL=http://localhost:8081/",
					"EMPTY=",
				}}},
			},
		}},
	}

	got, ok, err := checker.ServiceEnv(context.Background(), "drupal", "DRUPAL_DEFAULT_SITE_URL")
	if err != nil {
		t.Fatalf("ServiceEnv() error = %v", err)
	}
	if !ok {
		t.Fatal("expected DRUPAL_DEFAULT_SITE_URL to be present")
	}
	if got != "http://localhost:8081/" {
		t.Fatalf("ServiceEnv() = %q", got)
	}
}

func TestDockerHostFallbackURLPreservesHostHeader(t *testing.T) {
	gotURL, gotHost, ok := dockerHostFallbackURL("http://localhost:8080/")
	if !ok {
		t.Fatal("expected localhost URL to have Docker host fallback")
	}
	if gotURL != "http://host.docker.internal:8080/" {
		t.Fatalf("fallback URL = %q", gotURL)
	}
	if gotHost != "localhost:8080" {
		t.Fatalf("host header = %q", gotHost)
	}
}

func TestDockerHostFallbackURLUsesLoopbackResolvedDomain(t *testing.T) {
	originalLookupIP := lookupIP
	t.Cleanup(func() { lookupIP = originalLookupIP })
	lookupIP = func(host string) ([]net.IP, error) {
		if host != "example.test" {
			return nil, fmt.Errorf("unexpected host %q", host)
		}
		return []net.IP{net.ParseIP("127.0.0.1")}, nil
	}

	gotURL, gotHost, ok := dockerHostFallbackURL("http://example.test/")
	if !ok {
		t.Fatal("expected loopback-resolved URL to have Docker host fallback")
	}
	if gotURL != "http://host.docker.internal/" {
		t.Fatalf("fallback URL = %q", gotURL)
	}
	if gotHost != "example.test" {
		t.Fatalf("host header = %q", gotHost)
	}
}

func TestComposeDependencyConditionRequiresHealthy(t *testing.T) {
	condition, ok := composeDependencyCondition([]byte(`{"mariadb":{"condition":"service_healthy","required":true}}`), "mariadb")
	if !ok {
		t.Fatal("expected mariadb dependency")
	}
	if condition != "service_healthy" {
		t.Fatalf("condition = %q", condition)
	}

	condition, ok = composeDependencyCondition([]byte(`["mariadb"]`), "mariadb")
	if !ok {
		t.Fatal("expected short-form mariadb dependency")
	}
	if condition != "service_started" {
		t.Fatalf("short-form condition = %q", condition)
	}
}

func TestContainerHTTPRouteURLPreservesPathAndQuery(t *testing.T) {
	parsed, err := url.Parse("https://example.test/admin?check=1")
	if err != nil {
		t.Fatalf("Parse() error = %v", err)
	}
	got := containerHTTPRouteURL(parsed)
	if got != "http://127.0.0.1/admin?check=1" {
		t.Fatalf("containerHTTPRouteURL() = %q", got)
	}
}

func TestTraefikHTTPRouteURLPreservesPathAndQuery(t *testing.T) {
	parsed, err := url.Parse("https://example.test/admin?check=1")
	if err != nil {
		t.Fatalf("Parse() error = %v", err)
	}
	got := traefikHTTPRouteURL(parsed)
	if got != "http://traefik/admin?check=1" {
		t.Fatalf("traefikHTTPRouteURL() = %q", got)
	}
}

type fakeDockerAPI struct {
	containers []dockercontainer.Summary
	inspect    map[string]dockercontainer.InspectResponse
}

func (f fakeDockerAPI) ContainerList(ctx context.Context, options dockercontainer.ListOptions) ([]dockercontainer.Summary, error) {
	return f.containers, nil
}

func (f fakeDockerAPI) ContainerInspect(ctx context.Context, container string) (dockercontainer.InspectResponse, error) {
	if inspect, ok := f.inspect[container]; ok {
		return inspect, nil
	}
	return dockercontainer.InspectResponse{}, fmt.Errorf("container %s not found", container)
}
