package plugin

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestResolveComposeImageOverridesTagUsesApplicationBaseImages(t *testing.T) {
	cases := []struct {
		plugin  string
		service string
		image   string
	}{
		{plugin: "drupal", service: "drupal", image: "libops/drupal:nginx-1.30.2-php84"},
		{plugin: "ojs", service: "ojs", image: "libops/ojs:nginx-1.30.2-php84"},
		{plugin: "omeka-classic", service: "omeka-classic", image: "libops/omeka-classic:nginx-1.30.2-php84"},
		{plugin: "omeka-s", service: "omeka-s", image: "libops/omeka-s:nginx-1.30.2-php84"},
		{plugin: "wp", service: "wp", image: "libops/wp:nginx-1.30.2-php84"},
	}

	for _, tt := range cases {
		t.Run(tt.plugin, func(t *testing.T) {
			overrides, err := ResolveComposeImageOverrides(tt.plugin, []string{tt.service + "=nginx-1.30.2-php84"}, nil, nil)
			if err != nil {
				t.Fatalf("ResolveComposeImageOverrides() error = %v", err)
			}
			args := overrides.BuildArgs[tt.service]
			if args["BASE_IMAGE"] != tt.image {
				t.Fatalf("BASE_IMAGE for %s = %q, want %q", tt.service, args["BASE_IMAGE"], tt.image)
			}
		})
	}
}

func TestResolveComposeImageOverridesTagUsesIslandoraRepositoryTagArgs(t *testing.T) {
	for _, pluginName := range []string{"isle", "islandora"} {
		t.Run(pluginName, func(t *testing.T) {
			overrides, err := ResolveComposeImageOverrides(pluginName, []string{"drupal=nginx-1.30.3-php84"}, nil, nil)
			if err != nil {
				t.Fatalf("ResolveComposeImageOverrides() error = %v", err)
			}
			args := overrides.BuildArgs["drupal"]
			if args["REPOSITORY"] != "libops" {
				t.Fatalf("drupal REPOSITORY = %q, want libops", args["REPOSITORY"])
			}
			if args["TAG"] != "nginx-1.30.3-php84" {
				t.Fatalf("drupal TAG = %q, want nginx-1.30.3-php84", args["TAG"])
			}
			if _, ok := args["BASE_IMAGE"]; ok {
				t.Fatalf("did not expect ISLE drupal BASE_IMAGE override, got %#v", args)
			}
		})
	}
}

func TestResolveComposeImageOverridesTagSetsKnownServiceImages(t *testing.T) {
	overrides, err := ResolveComposeImageOverrides(
		"isle",
		[]string{
			"drupal=nginx-1.30.3-php84",
			"solr=solr-9.10.0",
			"mariadb=mariadb-11.8.5",
			"init=base-3.24.1",
			"activemq=activemq-6.1.7",
		},
		nil,
		nil,
	)
	if err != nil {
		t.Fatalf("ResolveComposeImageOverrides() error = %v", err)
	}

	drupalArgs := overrides.BuildArgs["drupal"]
	if drupalArgs["REPOSITORY"] != "libops" || drupalArgs["TAG"] != "nginx-1.30.3-php84" {
		t.Fatalf("drupal args = %#v, want REPOSITORY libops and TAG nginx-1.30.3-php84", drupalArgs)
	}
	expectedImages := map[string]string{
		"activemq": "libops/activemq:activemq-6.1.7",
		"init":     "libops/base:base-3.24.1",
		"mariadb":  "libops/mariadb:mariadb-11.8.5",
		"solr":     "libops/solr:solr-9.10.0",
	}
	for service, image := range expectedImages {
		if overrides.Images[service] != image {
			t.Fatalf("image override for %s = %q, want %q", service, overrides.Images[service], image)
		}
	}
}

func TestResolveComposeImageOverridesImageAcceptsFullRefs(t *testing.T) {
	overrides, err := ResolveComposeImageOverrides(
		"isle",
		nil,
		[]string{
			"milliner=islandora/milliner:6@sha256:abc",
			"solr=ghcr.io/example/solr:9@sha256:def",
		},
		nil,
	)
	if err != nil {
		t.Fatalf("ResolveComposeImageOverrides() error = %v", err)
	}

	if overrides.Images["milliner"] != "islandora/milliner:6@sha256:abc" {
		t.Fatalf("milliner image = %q", overrides.Images["milliner"])
	}
	if overrides.Images["solr"] != "ghcr.io/example/solr:9@sha256:def" {
		t.Fatalf("solr image = %q", overrides.Images["solr"])
	}
}

func TestResolveComposeImageOverridesTagRequiresKnownService(t *testing.T) {
	_, err := ResolveComposeImageOverrides("isle", []string{"milliner=main"}, nil, nil)
	if err == nil {
		t.Fatal("expected unsupported service error")
	}
	want := "--image SERVICE=IMAGE"
	if !strings.Contains(err.Error(), want) {
		t.Fatalf("error = %q, want guidance containing %q", err.Error(), want)
	}
}

func TestClearComposeImageOverridesPreservesUnrelatedOverrides(t *testing.T) {
	projectDir := t.TempDir()
	path := filepath.Join(projectDir, ComposeImageOverrideFile)
	if err := os.WriteFile(path, []byte(`services:
  wp:
    image: ghcr.io/example/wp:test
    build:
      args:
        BASE_IMAGE: libops/wp:test
    volumes:
      - ./web:/var/www/html
  traefik:
    ports:
      - "8080:80"
`), 0o644); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}

	if err := ClearComposeImageOverrides(projectDir, []string{"wp"}); err != nil {
		t.Fatalf("ClearComposeImageOverrides() error = %v", err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile() error = %v", err)
	}
	got := string(data)
	for _, unexpected := range []string{"image:", "BASE_IMAGE", "args:"} {
		if strings.Contains(got, unexpected) {
			t.Fatalf("expected %q removed, got:\n%s", unexpected, got)
		}
	}
	for _, expected := range []string{"./web:/var/www/html", "8080:80"} {
		if !strings.Contains(got, expected) {
			t.Fatalf("expected %q preserved, got:\n%s", expected, got)
		}
	}
}
