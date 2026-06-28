package healthcheck

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/libops/sitectl/pkg/config"
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
