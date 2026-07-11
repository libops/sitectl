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

func TestCheckHTTPRouteUsesRemoteSSHHostForLocalhostRoute(t *testing.T) {
	originalCheckHTTP := checkHTTP
	originalCheckHTTPViaOrigin := checkHTTPViaOrigin
	t.Cleanup(func() {
		checkHTTP = originalCheckHTTP
		checkHTTPViaOrigin = originalCheckHTTPViaOrigin
	})
	var checkedURL string
	checkHTTP = func(ctx context.Context, name, targetURL string) sitevalidate.Result {
		t.Fatalf("expected remote localhost route to use origin dial, got direct URL %q", targetURL)
		return sitevalidate.Result{Name: name, Status: sitevalidate.StatusFailed}
	}
	var checkedOrigin string
	checkHTTPViaOrigin = func(ctx context.Context, name, targetURL, originHost string) sitevalidate.Result {
		checkedURL = targetURL
		checkedOrigin = originHost
		return sitevalidate.Result{Name: name, Status: sitevalidate.StatusOK}
	}

	checker := &DockerChecker{
		Context: &config.Context{
			Name:           "test",
			ProjectName:    "wp",
			DockerHostType: config.ContextRemote,
			SSHHostname:    "203.0.113.10",
		},
		Client: &sitectldocker.DockerClient{CLI: fakeDockerAPI{
			containers: []dockercontainer.Summary{{
				ID:     "traefik123",
				Names:  []string{"/wp-traefik-1"},
				Labels: map[string]string{"com.docker.compose.service": "traefik"},
			}},
			inspect: map[string]dockercontainer.InspectResponse{
				"/wp-traefik-1": {
					NetworkSettings: networkSettingsWithPorts(t, nat.PortMap{
						"80/tcp": []nat.PortBinding{{HostPort: "80"}},
					}),
				},
			},
		}},
	}

	result := checker.CheckHTTPRoute(context.Background(), "http:wp", "wp", "http://localhost/")
	if result.Status != sitevalidate.StatusOK {
		t.Fatalf("CheckHTTPRoute() status = %s", result.Status)
	}
	if checkedURL != "http://localhost/" {
		t.Fatalf("checked URL = %q, want http://localhost/", checkedURL)
	}
	if checkedOrigin != "203.0.113.10" {
		t.Fatalf("checked origin = %q, want 203.0.113.10", checkedOrigin)
	}
}

func TestCheckHTTPRouteUsesRemoteOriginForDomainRoute(t *testing.T) {
	originalCheckHTTP := checkHTTP
	originalCheckHTTPViaOrigin := checkHTTPViaOrigin
	t.Cleanup(func() {
		checkHTTP = originalCheckHTTP
		checkHTTPViaOrigin = originalCheckHTTPViaOrigin
	})
	checkHTTP = func(ctx context.Context, name, targetURL string) sitevalidate.Result {
		t.Fatalf("expected remote domain route to use origin dial, got direct URL %q", targetURL)
		return sitevalidate.Result{Name: name, Status: sitevalidate.StatusFailed}
	}
	var checkedURL string
	var checkedOrigin string
	checkHTTPViaOrigin = func(ctx context.Context, name, targetURL, originHost string) sitevalidate.Result {
		checkedURL = targetURL
		checkedOrigin = originHost
		return sitevalidate.Result{Name: name, Status: sitevalidate.StatusOK}
	}

	checker := &DockerChecker{
		Context: &config.Context{
			Name:           "test",
			ProjectName:    "wp",
			DockerHostType: config.ContextRemote,
			SSHHostname:    "203.0.113.10",
		},
		Client: &sitectldocker.DockerClient{CLI: fakeDockerAPI{
			containers: []dockercontainer.Summary{{
				ID:     "traefik123",
				Names:  []string{"/wp-traefik-1"},
				Labels: map[string]string{"com.docker.compose.service": "traefik"},
			}},
			inspect: map[string]dockercontainer.InspectResponse{
				"/wp-traefik-1": {
					NetworkSettings: networkSettingsWithPorts(t, nat.PortMap{
						"443/tcp": []nat.PortBinding{{HostPort: "443"}},
					}),
				},
			},
		}},
	}

	result := checker.CheckHTTPRoute(context.Background(), "http:wp", "wp", "https://app-test.libops.io/")
	if result.Status != sitevalidate.StatusOK {
		t.Fatalf("CheckHTTPRoute() status = %s", result.Status)
	}
	if checkedURL != "https://app-test.libops.io/" {
		t.Fatalf("checked URL = %q, want https://app-test.libops.io/", checkedURL)
	}
	if checkedOrigin != "203.0.113.10" {
		t.Fatalf("checked origin = %q, want 203.0.113.10", checkedOrigin)
	}
}

func TestCheckHTTPViaOriginInsecureTLSAllowsPrivateOriginCertificate(t *testing.T) {
	t.Parallel()

	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Host == "" || !strings.HasPrefix(r.Host, "example.test:") {
			t.Fatalf("Host header = %q, want example.test with port", r.Host)
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	_, port, err := net.SplitHostPort(server.Listener.Addr().String())
	if err != nil {
		t.Fatalf("SplitHostPort() error = %v", err)
	}
	result := CheckHTTPViaOriginInsecureTLS(context.Background(), "http:test", "https://example.test:"+port+"/", "127.0.0.1")
	if result.Status != sitevalidate.StatusOK {
		t.Fatalf("CheckHTTPViaOriginInsecureTLS() status = %s, detail = %q", result.Status, result.Detail)
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

func TestRequiredSuccessfulCompletionServices(t *testing.T) {
	data, err := os.ReadFile(filepath.Join("testdata", "compose", "completed-successfully.json"))
	if err != nil {
		t.Fatalf("ReadFile(compose fixture) error = %v", err)
	}
	var document composeDependencyConfigDocument
	if err := json.Unmarshal(data, &document); err != nil {
		t.Fatalf("Unmarshal(compose fixture) error = %v", err)
	}

	services := requiredSuccessfulCompletionServices(document)
	for _, service := range []string{"database-init", "default-required-init"} {
		if _, ok := services[service]; !ok {
			t.Errorf("expected %s to require successful completion", service)
		}
	}
	for _, service := range []string{"app", "mariadb", "optional-init", "orphan-job"} {
		if _, ok := services[service]; ok {
			t.Errorf("did not expect %s to require successful completion", service)
		}
	}
}

func TestCheckComposeServicesAllowsOnlyRequiredSuccessfulCompletion(t *testing.T) {
	binDir := t.TempDir()
	callPath := filepath.Join(t.TempDir(), "docker-call")
	fixturePath, err := filepath.Abs(filepath.Join("testdata", "compose", "completed-successfully.json"))
	if err != nil {
		t.Fatalf("Abs(compose fixture) error = %v", err)
	}
	dockerPath := filepath.Join(binDir, "docker")
	dockerScript := `#!/bin/sh
printf '%s\n' "$*" > "$COMPOSE_CONFIG_CALL"
cat "$COMPOSE_CONFIG_FIXTURE"
`
	if err := os.WriteFile(dockerPath, []byte(dockerScript), 0o755); err != nil {
		t.Fatalf("WriteFile(fake docker) error = %v", err)
	}
	t.Setenv("PATH", binDir+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("COMPOSE_CONFIG_CALL", callPath)
	t.Setenv("COMPOSE_CONFIG_FIXTURE", fixturePath)

	tests := []struct {
		name     string
		service  string
		state    *dockercontainer.State
		wantOK   bool
		wantText string
	}{
		{
			name:     "required one-shot exited zero",
			service:  "database-init",
			state:    &dockercontainer.State{Status: dockercontainer.StateExited, ExitCode: 0},
			wantOK:   true,
			wantText: "completed successfully (exit=0)",
		},
		{
			name:     "default required one-shot exited zero",
			service:  "default-required-init",
			state:    &dockercontainer.State{Status: dockercontainer.StateExited, ExitCode: 0},
			wantOK:   true,
			wantText: "completed successfully (exit=0)",
		},
		{
			name:     "optional one-shot exited zero",
			service:  "optional-init",
			state:    &dockercontainer.State{Status: dockercontainer.StateExited, ExitCode: 0},
			wantText: "exited",
		},
		{
			name:     "healthy dependency exited zero",
			service:  "mariadb",
			state:    &dockercontainer.State{Status: dockercontainer.StateExited, ExitCode: 0},
			wantText: "exited",
		},
		{
			name:     "arbitrary job exited zero",
			service:  "orphan-job",
			state:    &dockercontainer.State{Status: dockercontainer.StateExited, ExitCode: 0},
			wantText: "exited",
		},
		{
			name:     "required one-shot exited nonzero",
			service:  "database-init",
			state:    &dockercontainer.State{Status: dockercontainer.StateExited, ExitCode: 23},
			wantText: "exit=23",
		},
		{
			name:     "required one-shot dead with zero",
			service:  "database-init",
			state:    &dockercontainer.State{Status: dockercontainer.StateDead, ExitCode: 0},
			wantText: "dead",
		},
		{
			name:     "required one-shot missing",
			service:  "database-init",
			wantText: "no compose container found",
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			containers := []dockercontainer.Summary{}
			inspect := map[string]dockercontainer.InspectResponse{}
			if test.state != nil {
				containerID := test.service + "-id"
				containers = append(containers, dockercontainer.Summary{
					ID:     containerID,
					Names:  []string{"/project-" + test.service + "-1"},
					Labels: map[string]string{"com.docker.compose.service": test.service},
				})
				inspect[containerID] = dockercontainer.InspectResponse{
					ContainerJSONBase: &dockercontainer.ContainerJSONBase{State: test.state},
				}
			}
			checker := &DockerChecker{
				Context: &config.Context{
					DockerHostType: config.ContextLocal,
					ProjectDir:     t.TempDir(),
					ProjectName:    "project",
				},
				Client: &sitectldocker.DockerClient{CLI: fakeDockerAPI{containers: containers, inspect: inspect}},
			}

			results, err := checker.CheckComposeServices(context.Background(), test.service)
			if err != nil {
				t.Fatalf("CheckComposeServices() error = %v", err)
			}
			if len(results) != 1 {
				t.Fatalf("CheckComposeServices() returned %d results, want 1", len(results))
			}
			gotOK := results[0].Status == sitevalidate.StatusOK
			if gotOK != test.wantOK {
				t.Errorf("status = %s, wantOK = %v; detail = %q", results[0].Status, test.wantOK, results[0].Detail)
			}
			if !strings.Contains(results[0].Detail, test.wantText) {
				t.Errorf("detail = %q, want text %q", results[0].Detail, test.wantText)
			}
		})
	}

	call, err := os.ReadFile(callPath)
	if err != nil {
		t.Fatalf("ReadFile(docker call) error = %v", err)
	}
	if got := strings.TrimSpace(string(call)); got != "compose config --format json" {
		t.Fatalf("effective Compose config command = %q", got)
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
