package component

import (
	"strings"
	"testing"
)

func TestYAMLDocumentPreservesComposeAnchorsAndComments(t *testing.T) {
	t.Parallel()

	input := `---
# Common to all services
x-common: &common
  restart: unless-stopped
  tty: true # keep tty comment
services:
  alpaca:
    <<: *common
    environment:
      ALPACA_FCREPO_INDEXER_ENABLED: "true"
  fcrepo:
    <<: *common
    image: islandora/fcrepo6
volumes:
  fcrepo-data: {}
`

	doc, err := LoadYAMLDocument([]byte(input))
	if err != nil {
		t.Fatalf("LoadYAMLDocument() error = %v", err)
	}
	if err := doc.DeletePath(".services.fcrepo"); err != nil {
		t.Fatalf("DeletePath() error = %v", err)
	}
	if err := doc.SetString(".services.alpaca.environment.ALPACA_FCREPO_INDEXER_ENABLED", "false"); err != nil {
		t.Fatalf("SetString() error = %v", err)
	}
	if err := doc.DeletePath(".volumes.fcrepo-data"); err != nil {
		t.Fatalf("DeletePath(volume) error = %v", err)
	}

	out, err := doc.Bytes()
	if err != nil {
		t.Fatalf("Bytes() error = %v", err)
	}
	rendered := string(out)

	if !strings.Contains(rendered, "x-common: &common") {
		t.Fatalf("expected anchor preserved, got:\n%s", rendered)
	}
	if !strings.Contains(rendered, "<<: *common") {
		t.Fatalf("expected merge key preserved, got:\n%s", rendered)
	}
	if !strings.Contains(rendered, "# keep tty comment") {
		t.Fatalf("expected comment preserved, got:\n%s", rendered)
	}
	if strings.Contains(rendered, "services:\n  fcrepo:") || strings.Contains(rendered, "\n  fcrepo:\n") {
		t.Fatalf("expected fcrepo removed, got:\n%s", rendered)
	}
	if strings.Contains(rendered, "fcrepo-data") {
		t.Fatalf("expected fcrepo-data removed, got:\n%s", rendered)
	}
	if !strings.Contains(rendered, `ALPACA_FCREPO_INDEXER_ENABLED: "false"`) {
		t.Fatalf("expected updated env string, got:\n%s", rendered)
	}
}

func TestYAMLDocumentSetStringPreservesExistingOrder(t *testing.T) {
	t.Parallel()

	input := "settings:\n  target_type: file\n  uri_scheme: fedora\n"
	doc, err := LoadYAMLDocument([]byte(input))
	if err != nil {
		t.Fatalf("LoadYAMLDocument() error = %v", err)
	}
	if err := doc.SetString(".settings.uri_scheme", "private"); err != nil {
		t.Fatalf("SetString() error = %v", err)
	}

	out, err := doc.Bytes()
	if err != nil {
		t.Fatalf("Bytes() error = %v", err)
	}
	rendered := string(out)
	if !strings.Contains(rendered, "target_type: file\n  uri_scheme: \"private\"") {
		t.Fatalf("expected key order preserved, got:\n%s", rendered)
	}
}

func TestYAMLDocumentDoesNotAddExplicitMergeTagWhenOriginalDidNotUseIt(t *testing.T) {
	t.Parallel()

	input := "x-common: &common\n  restart: unless-stopped\nservices:\n  alpaca:\n    <<: *common\n"
	doc, err := LoadYAMLDocument([]byte(input))
	if err != nil {
		t.Fatalf("LoadYAMLDocument() error = %v", err)
	}

	out, err := doc.Bytes()
	if err != nil {
		t.Fatalf("Bytes() error = %v", err)
	}
	rendered := string(out)
	if strings.Contains(rendered, "!!merge <<:") {
		t.Fatalf("expected implicit merge key to stay untagged, got:\n%s", rendered)
	}
	if !strings.Contains(rendered, "<<: *common") {
		t.Fatalf("expected merge key preserved, got:\n%s", rendered)
	}
}

func TestYAMLDocumentPreservesExplicitMergeTagWhenOriginalUsedIt(t *testing.T) {
	t.Parallel()

	input := "x-common: &common\n  restart: unless-stopped\nservices:\n  alpaca:\n    !!merge <<: *common\n"
	doc, err := LoadYAMLDocument([]byte(input))
	if err != nil {
		t.Fatalf("LoadYAMLDocument() error = %v", err)
	}

	out, err := doc.Bytes()
	if err != nil {
		t.Fatalf("Bytes() error = %v", err)
	}
	rendered := string(out)
	if !strings.Contains(rendered, "!!merge <<: *common") {
		t.Fatalf("expected explicit merge tag preserved, got:\n%s", rendered)
	}
}
