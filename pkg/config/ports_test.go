package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestWriteComposePortOverrideUsesComposeOverrideTag(t *testing.T) {
	projectDir := t.TempDir()
	ctx := Context{
		DockerHostType: ContextLocal,
		Environment:    "dev",
		ProjectDir:     projectDir,
		ProjectName:    "ports-test",
	}
	if err := os.WriteFile(filepath.Join(projectDir, LocalDevComposeOverrideName), []byte("services:\n  app:\n    image: ghcr.io/example/app:test\n"), 0o644); err != nil {
		t.Fatalf("WriteFile(override) error = %v", err)
	}
	plan := composePortPlan{Services: map[string]map[int]struct{}{
		"traefik": {
			defaultHTTPPort:  struct{}{},
			defaultHTTPSPort: struct{}{},
		},
	}}

	messages, err := ctx.writeComposePortOverride(plan, map[int]int{
		defaultHTTPPort:  defaultHTTPPort,
		defaultHTTPSPort: defaultHTTPSFallback,
	})
	if err != nil {
		t.Fatalf("writeComposePortOverride() error = %v", err)
	}
	if len(messages) != 1 {
		t.Fatalf("expected one write message, got %#v", messages)
	}
	data, err := os.ReadFile(filepath.Join(projectDir, LocalDevComposeOverrideName))
	if err != nil {
		t.Fatalf("ReadFile(override) error = %v", err)
	}
	text := string(data)
	for _, want := range []string{
		`image: ghcr.io/example/app:test`,
		`ports: !override`,
		`- "80:80"`,
		`- "8443:443"`,
		`sitectl-dev-ports`,
	} {
		if !strings.Contains(text, want) {
			t.Fatalf("expected override to contain %q, got:\n%s", want, text)
		}
	}
}

func TestWriteComposePortOverrideRemovesManagedPortsWhenDefaultsAreAvailable(t *testing.T) {
	projectDir := t.TempDir()
	ctx := Context{
		DockerHostType: ContextLocal,
		Environment:    "dev",
		ProjectDir:     projectDir,
		ProjectName:    "ports-test",
	}
	initial := `services:
  traefik:
    ports: !override
      - "8080:80"
x-sitectl-dev-ports:
  traefik:
    - "8080:80"
`
	if err := os.WriteFile(filepath.Join(projectDir, LocalDevComposeOverrideName), []byte(initial), 0o644); err != nil {
		t.Fatalf("WriteFile(override) error = %v", err)
	}
	plan := composePortPlan{Services: map[string]map[int]struct{}{
		"traefik": {defaultHTTPPort: struct{}{}},
	}}

	if _, err := ctx.writeComposePortOverride(plan, map[int]int{defaultHTTPPort: defaultHTTPPort}); err != nil {
		t.Fatalf("writeComposePortOverride() error = %v", err)
	}
	if _, err := os.Stat(filepath.Join(projectDir, LocalDevComposeOverrideName)); !os.IsNotExist(err) {
		t.Fatalf("expected managed-only override removed, got err=%v", err)
	}
}

func TestComposeDataPublishesSecurePort(t *testing.T) {
	t.Parallel()

	for _, compose := range []string{
		"services:\n  traefik:\n    ports:\n      - \"443:443\"\n",
		"services:\n  traefik:\n    ports:\n      - target: 443\n        published: 443\n",
		"services:\n  traefik:\n    command:\n      - --entryPoints.http.address=:80\n      - --entryPoints.https.address=:443\n",
		"services:\n  traefik:\n    command: --entryPoints.http.address=:80 --entryPoints.https.address=:443\n",
	} {
		if !composeDataPublishesSecurePort([]byte(compose)) {
			t.Fatalf("expected compose to publish secure port:\n%s", compose)
		}
	}
	if composeDataPublishesSecurePort([]byte("services:\n  traefik:\n    ports:\n      - \"80:80\"\n")) {
		t.Fatal("did not expect HTTP-only compose to publish secure port")
	}
}

func TestComposePublishedHostPortReadsLocalDevOverride(t *testing.T) {
	projectDir := t.TempDir()
	ctx := Context{
		DockerHostType: ContextLocal,
		Environment:    "dev",
		ProjectDir:     projectDir,
		ProjectName:    "ports-test",
	}
	writePortCompose(t, projectDir, `services:
  traefik:
    ports:
      - "80:80"
`)
	if err := os.WriteFile(filepath.Join(projectDir, LocalDevComposeOverrideName), []byte("services:\n  traefik:\n    ports: !override\n      - \"8080:80\"\n"), 0o644); err != nil {
		t.Fatalf("WriteFile(override) error = %v", err)
	}

	hostPort, ok := ctx.ComposePublishedHostPort(defaultHTTPPort)
	if !ok {
		t.Fatal("expected HTTP host port")
	}
	if hostPort != defaultHTTPFallback {
		t.Fatalf("host port = %d, want %d", hostPort, defaultHTTPFallback)
	}
}

func writePortCompose(t *testing.T, projectDir, contents string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(projectDir, "docker-compose.yml"), []byte(contents), 0o644); err != nil {
		t.Fatalf("WriteFile(compose) error = %v", err)
	}
}
