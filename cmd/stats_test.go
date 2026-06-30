package cmd

import (
	"bytes"
	"encoding/json"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/libops/sitectl/pkg/config"
	"github.com/libops/sitectl/pkg/docker"
	"github.com/libops/sitectl/pkg/plugin"
	"github.com/spf13/cobra"
)

func TestWriteStatsReportPrettyPrintsJSON(t *testing.T) {
	t.Parallel()

	var out bytes.Buffer
	cmd := &cobra.Command{}
	cmd.SetOut(&out)
	report := statsReport{
		Context: statsContextReport{Plugin: "drupal"},
		Ingress: statsIngressReport{
			Status:    "ok",
			PublicURL: "http://localhost",
			Routes: []statsIngressRoute{{
				Name:    "app",
				URL:     "http://localhost",
				Status:  "resolved",
				Primary: true,
			}},
			Ports: []statsIngressPort{{
				Name:          "http",
				ContainerPort: 80,
				HostPort:      8080,
				State:         "would_publish",
				WouldPublish:  true,
			}},
		},
		Containers: statsContainersReport{Status: "not running"},
	}

	if err := writeStatsReport(cmd, report); err != nil {
		t.Fatalf("writeStatsReport() error = %v", err)
	}
	if !strings.Contains(out.String(), "\n  \"ingress\": {") {
		t.Fatalf("expected pretty JSON, got:\n%s", out.String())
	}
	var decoded statsReport
	if err := json.Unmarshal(out.Bytes(), &decoded); err != nil {
		t.Fatalf("stats output is not JSON: %v\n%s", err, out.String())
	}
	if decoded.Ingress.PublicURL != "http://localhost" {
		t.Fatalf("public URL = %q", decoded.Ingress.PublicURL)
	}
	if len(decoded.Ingress.Ports) != 1 || decoded.Ingress.Ports[0].State != "would_publish" || decoded.Ingress.Ports[0].HostPort != 8080 {
		t.Fatalf("unexpected ports: %+v", decoded.Ingress.Ports)
	}
}

func TestBuildStatsReportIncludesContainerRows(t *testing.T) {
	t.Parallel()

	report := buildStatsReport(t.Context(), statsResolvedContext{}, docker.ProjectSummary{
		Status:      "running",
		Running:     1,
		Total:       1,
		Healthy:     1,
		CPUPercent:  2.5,
		MemoryBytes: 1500,
		Services: []docker.ServiceSummary{{
			Service:     "drupal",
			State:       "running",
			Status:      "healthy",
			CPUPercent:  2.5,
			MemoryBytes: 1500,
		}},
	})

	if report.Containers.Status != "running" || report.Containers.Running != 1 || report.Containers.Total != 1 {
		t.Fatalf("unexpected container summary: %+v", report.Containers)
	}
	if len(report.Containers.Services) != 1 || report.Containers.Services[0].Service != "drupal" {
		t.Fatalf("unexpected service rows: %+v", report.Containers.Services)
	}
}

func TestResolveStatsIngressRouteUsesTraefikConfig(t *testing.T) {
	t.Parallel()

	projectDir := t.TempDir()
	copyStatsFixture(t, projectDir, "local-port")
	ctx := statsFixtureContext(t, projectDir, "drupal")

	route := resolveStatsIngressRoute(t.Context(), ctx, plugin.IngressRoute{
		Name:          "app",
		Service:       "drupal",
		Router:        "drupal",
		DefaultScheme: "http",
		DefaultDomain: "localhost",
		Primary:       true,
	})

	if route.URL != "http://localhost:8080" {
		t.Fatalf("route URL = %q", route.URL)
	}
	if route.Domain != "localhost" || route.Scheme != "http" || route.Status != "resolved" {
		t.Fatalf("unexpected route: %+v", route)
	}
}

func TestStatsPublicURLKeepsNonRootPath(t *testing.T) {
	t.Parallel()

	if got := statsPublicURL("http://localhost/api"); got != "http://localhost/api" {
		t.Fatalf("statsPublicURL() = %q", got)
	}
}

func copyStatsFixture(t *testing.T, projectDir, name string) {
	t.Helper()

	root := filepath.Join("..", "pkg", "healthcheck", "testdata", "traefik", name)
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

func statsFixtureContext(t *testing.T, projectDir, pluginName string) *config.Context {
	t.Helper()

	ctx, err := config.NewLocalProjectContext(projectDir, pluginName)
	if err != nil {
		t.Fatalf("NewLocalProjectContext() error = %v", err)
	}
	ctx.ComposeFile = []string{"docker-compose.yml", "docker-compose.override.yml"}
	return ctx
}
