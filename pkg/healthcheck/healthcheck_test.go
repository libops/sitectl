package healthcheck

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	dockercontainer "github.com/docker/docker/api/types/container"
	"github.com/docker/go-connections/nat"
	"github.com/libops/sitectl/pkg/config"
	sitectldocker "github.com/libops/sitectl/pkg/docker"
	sitevalidate "github.com/libops/sitectl/pkg/validate"
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

func TestCheckHTTPRouteUsesRunningTraefikHostPort(t *testing.T) {
	originalCheckHTTP := checkHTTP
	t.Cleanup(func() { checkHTTP = originalCheckHTTP })

	var checkedURL string
	checkHTTP = func(ctx context.Context, name, targetURL string) sitevalidate.Result {
		checkedURL = targetURL
		return sitevalidate.Result{Name: name, Status: sitevalidate.StatusOK}
	}

	checker := &DockerChecker{
		Context: &config.Context{Name: "test", ProjectName: "archives"},
		Client: &sitectldocker.DockerClient{CLI: fakeDockerAPI{
			containers: []dockercontainer.Summary{{
				ID:     "traefik123",
				Names:  []string{"/archives-traefik-1"},
				Labels: map[string]string{"com.docker.compose.service": "traefik"},
			}},
			inspect: map[string]dockercontainer.InspectResponse{
				"/archives-traefik-1": {
					NetworkSettings: networkSettingsWithPorts(t, nat.PortMap{
						"80/tcp": []nat.PortBinding{{HostPort: "80"}},
					}),
				},
			},
		}},
	}

	result := checker.CheckHTTPRoute(context.Background(), "http:archivesspace", "archivesspace", "http://localhost:8080/")
	if result.Status != sitevalidate.StatusOK {
		t.Fatalf("CheckHTTPRoute() status = %s", result.Status)
	}
	if checkedURL != "http://localhost/" {
		t.Fatalf("checked URL = %q, want http://localhost/", checkedURL)
	}
}

func networkSettingsWithPorts(t *testing.T, ports nat.PortMap) *dockercontainer.NetworkSettings {
	t.Helper()
	payload, err := json.Marshal(struct {
		Ports nat.PortMap `json:"Ports"`
	}{Ports: ports})
	if err != nil {
		t.Fatalf("Marshal network settings error = %v", err)
	}
	var settings dockercontainer.NetworkSettings
	if err := json.Unmarshal(payload, &settings); err != nil {
		t.Fatalf("Unmarshal network settings error = %v", err)
	}
	return &settings
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

func TestCheckHTTPRetriesTransientFailures(t *testing.T) {
	var attempts atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if attempts.Add(1) < 3 {
			http.NotFound(w, r)
			return
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	result := CheckHTTP(context.Background(), "http:test", server.URL)
	if result.Status != sitevalidate.StatusOK {
		t.Fatalf("CheckHTTP() status = %s, detail = %q", result.Status, result.Detail)
	}
	if got := attempts.Load(); got != 3 {
		t.Fatalf("attempts = %d, want 3", got)
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
