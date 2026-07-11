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
		tag     string
		image   string
	}{
		{plugin: "drupal", service: "drupal", tag: "nginx-1.30.3-php84", image: "libops/drupal:nginx-1.30.3-php84"},
		{plugin: "ojs", service: "ojs", tag: "3.5.0-5-php84", image: "libops/ojs:3.5.0-5-php84"},
		{plugin: "omeka-classic", service: "omeka-classic", tag: "3.2.1-php84", image: "libops/omeka-classic:3.2.1-php84"},
		{plugin: "omeka-s", service: "omeka-s", tag: "4.2.1-php84", image: "libops/omeka-s:4.2.1-php84"},
		{plugin: "wp", service: "wp", tag: "nginx-1.30.3-php84", image: "libops/wp:nginx-1.30.3-php84"},
	}

	for _, tt := range cases {
		t.Run(tt.plugin, func(t *testing.T) {
			overrides, err := ResolveComposeImageOverrides(tt.plugin, []string{tt.service + "=" + tt.tag}, nil, nil)
			if err != nil {
				t.Fatalf("ResolveComposeImageOverrides() error = %v", err)
			}
			args := overrides.BuildArgs[tt.service]
			if args["BASE_IMAGE"] != tt.image {
				t.Fatalf("BASE_IMAGE for %s = %q, want %q", tt.service, args["BASE_IMAGE"], tt.image)
			}
			if _, ok := overrides.Images[tt.service]; ok {
				t.Fatalf("buildable service %s must use build args, got image override %#v", tt.service, overrides.Images)
			}
		})
	}
}

func TestApplyComposeImageOverridesAddsBuildArgsWithoutBaseComposeArgs(t *testing.T) {
	t.Parallel()
	projectDir := t.TempDir()
	overrides, err := ResolveComposeImageOverrides("ojs", []string{"ojs=3.5.0-5-php84"}, nil, nil)
	if err != nil {
		t.Fatalf("ResolveComposeImageOverrides() error = %v", err)
	}
	if err := ApplyComposeImageOverrides(projectDir, overrides); err != nil {
		t.Fatalf("ApplyComposeImageOverrides() error = %v", err)
	}
	data, err := os.ReadFile(filepath.Join(projectDir, ComposeImageOverrideFile))
	if err != nil {
		t.Fatalf("ReadFile() error = %v", err)
	}
	got := string(data)
	for _, want := range []string{"build:", "args:", `BASE_IMAGE: "libops/ojs:3.5.0-5-php84"`} {
		if !strings.Contains(got, want) {
			t.Fatalf("override missing %q:\n%s", want, got)
		}
	}
	if strings.Contains(got, "image:") {
		t.Fatalf("buildable app override must not write image:\n%s", got)
	}
}

func TestResolveComposeImageOverridesArchivesSpaceSolrUsesCompanionImage(t *testing.T) {
	t.Parallel()

	overrides, err := ResolveComposeImageOverrides("archivesspace", []string{"solr=4.2.0"}, nil, nil)
	if err != nil {
		t.Fatalf("ResolveComposeImageOverrides() error = %v", err)
	}
	if got := overrides.Images["solr"]; got != "libops/archivesspace-solr:4.2.0" {
		t.Fatalf("solr image = %q, want libops/archivesspace-solr:4.2.0", got)
	}
	if _, ok := overrides.BuildArgs["solr"]; ok {
		t.Fatalf("expected a direct solr image override, got build args %#v", overrides.BuildArgs["solr"])
	}
}

func TestResolveComposeImageOverridesRejectsImageForBuildableApplication(t *testing.T) {
	t.Parallel()

	_, err := ResolveComposeImageOverrides("omeka-s", nil, []string{"omeka-s=ghcr.io/example/omeka-s:test"}, nil)
	if err == nil || !strings.Contains(err.Error(), "Compose would still build") || !strings.Contains(err.Error(), "--tag omeka-s=TAG") {
		t.Fatalf("ResolveComposeImageOverrides() error = %v, want buildable-service guidance", err)
	}
}

func TestResolveComposeImageOverridesTagUsesIslandoraBaseImage(t *testing.T) {
	for _, pluginName := range []string{"isle", "islandora"} {
		t.Run(pluginName, func(t *testing.T) {
			overrides, err := ResolveComposeImageOverrides(pluginName, []string{"drupal=nginx-1.30.3-php84"}, nil, nil)
			if err != nil {
				t.Fatalf("ResolveComposeImageOverrides() error = %v", err)
			}
			args := overrides.BuildArgs["drupal"]
			if args["BASE_IMAGE"] != "libops/islandora:nginx-1.30.3-php84" {
				t.Fatalf("drupal BASE_IMAGE = %q, want libops/islandora:nginx-1.30.3-php84", args["BASE_IMAGE"])
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
	if drupalArgs["BASE_IMAGE"] != "libops/islandora:nginx-1.30.3-php84" {
		t.Fatalf("drupal args = %#v, want BASE_IMAGE libops/islandora:nginx-1.30.3-php84", drupalArgs)
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

func TestComposeImageOverridesPreserveForkedComposeTextAcrossSetUpdateAndClear(t *testing.T) {
	projectDir := t.TempDir()
	path := filepath.Join(projectDir, ComposeImageOverrideFile)
	original := `# downstream operator comment
x-common: &common
    restart: unless-stopped

services:
    app:
        image: old.example/app:v1 # keep image rationale
        build:
            context: .
            dockerfile: Dockerfile.custom
            args:
                BASE_IMAGE: old.example/base:v1 # keep base rationale
        volumes:
            - ./custom:/opt/custom:ro,z
    worker:
        <<: *common
        image: old.example/worker:v1 # untouched worker
`
	if err := os.WriteFile(path, []byte(original), 0o644); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}

	overrides := ComposeImageOverrides{}
	overrides.AddImage("app", "registry.example/app:v2")
	overrides.AddBuildArg("app", "BASE_IMAGE", "registry.example/base:v2")
	overrides.AddBuildArg("app", "EXTRA_FLAG", "enabled")
	if err := ApplyComposeImageOverrides(projectDir, overrides); err != nil {
		t.Fatalf("ApplyComposeImageOverrides(first) error = %v", err)
	}
	first := readComposeOverrideForTest(t, path)
	for _, want := range []string{
		"# downstream operator comment",
		"x-common: &common",
		"        image: \"registry.example/app:v2\" # keep image rationale",
		"                BASE_IMAGE: \"registry.example/base:v2\" # keep base rationale",
		"                EXTRA_FLAG: \"enabled\"",
		"            context: .",
		"            dockerfile: Dockerfile.custom",
		"            - ./custom:/opt/custom:ro,z",
		"        <<: *common",
		"        image: old.example/worker:v1 # untouched worker",
	} {
		if !strings.Contains(first, want) {
			t.Fatalf("set did not preserve or write %q:\n%s", want, first)
		}
	}

	overrides = ComposeImageOverrides{}
	overrides.AddImage("app", "registry.example/app:v3")
	overrides.AddBuildArg("app", "BASE_IMAGE", "registry.example/base:v3")
	if err := ApplyComposeImageOverrides(projectDir, overrides); err != nil {
		t.Fatalf("ApplyComposeImageOverrides(update) error = %v", err)
	}
	updated := readComposeOverrideForTest(t, path)
	for _, want := range []string{
		"        image: \"registry.example/app:v3\" # keep image rationale",
		"                BASE_IMAGE: \"registry.example/base:v3\" # keep base rationale",
		"                EXTRA_FLAG: \"enabled\"",
	} {
		if !strings.Contains(updated, want) {
			t.Fatalf("update did not preserve or write %q:\n%s", want, updated)
		}
	}
	if strings.Count(updated, "BASE_IMAGE:") != 1 || strings.Count(updated, "image: \"registry.example/app:v3\"") != 1 {
		t.Fatalf("update duplicated managed keys:\n%s", updated)
	}

	if err := ClearComposeImageOverrides(projectDir, []string{"app"}); err != nil {
		t.Fatalf("ClearComposeImageOverrides() error = %v", err)
	}
	cleared := readComposeOverrideForTest(t, path)
	for _, want := range []string{
		"# downstream operator comment",
		"x-common: &common",
		"            context: .",
		"            dockerfile: Dockerfile.custom",
		"            - ./custom:/opt/custom:ro,z",
		"        <<: *common",
		"        image: old.example/worker:v1 # untouched worker",
	} {
		if !strings.Contains(cleared, want) {
			t.Fatalf("clear did not preserve unrelated value %q:\n%s", want, cleared)
		}
	}
	for _, removed := range []string{"registry.example/app", "BASE_IMAGE:", "EXTRA_FLAG:", "args:"} {
		if strings.Contains(cleared, removed) {
			t.Fatalf("clear retained managed value %q:\n%s", removed, cleared)
		}
	}
}

func TestComposeImageOverridesExpandScalarBuildContextWithoutLosingIt(t *testing.T) {
	projectDir := t.TempDir()
	path := filepath.Join(projectDir, ComposeImageOverrideFile)
	if err := os.WriteFile(path, []byte("services:\n  app:\n    build: ./docker # custom context\n"), 0o644); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}
	overrides := ComposeImageOverrides{}
	overrides.AddBuildArg("app", "BASE_IMAGE", "registry.example/base:v2")
	if err := ApplyComposeImageOverrides(projectDir, overrides); err != nil {
		t.Fatalf("ApplyComposeImageOverrides() error = %v", err)
	}
	got := readComposeOverrideForTest(t, path)
	for _, want := range []string{
		"    build: # custom context",
		"      context: ./docker",
		`      args:`,
		`        BASE_IMAGE: "registry.example/base:v2"`,
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("scalar build expansion missing %q:\n%s", want, got)
		}
	}
}

func readComposeOverrideForTest(t *testing.T, path string) string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile(%s) error = %v", path, err)
	}
	return string(data)
}
