package config

import (
	"errors"
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

func TestComposePublicSchemePrefersTraefikEntrypoint(t *testing.T) {
	t.Parallel()

	projectDir := t.TempDir()
	ctx := Context{
		DockerHostType: ContextLocal,
		ProjectDir:     projectDir,
	}
	writePortCompose(t, projectDir, `services:
  app:
    ports:
      - "443:443"
  traefik:
    command:
      - --entryPoints.http.address=:80
`)

	if got := ctx.ComposePublicScheme("https"); got != "http" {
		t.Fatalf("ComposePublicScheme() = %q, want http", got)
	}
}

func TestComposeTLSProviderInfersLetsEncryptFromTraefikCommand(t *testing.T) {
	t.Parallel()

	projectDir := t.TempDir()
	ctx := Context{
		DockerHostType: ContextLocal,
		ProjectDir:     projectDir,
	}
	writePortCompose(t, projectDir, `services:
  traefik:
    command:
      - --entryPoints.http.address=:80
      - --entryPoints.https.address=:443
      - --certificatesresolvers.letsencrypt.acme.httpchallenge=true
`)

	if got := ctx.ComposeTLSProvider("self-managed"); got != "letsencrypt" {
		t.Fatalf("ComposeTLSProvider() = %q, want letsencrypt", got)
	}
}

func TestComposeTLSProviderPrefersSitectlTLSModeMarker(t *testing.T) {
	t.Parallel()

	projectDir := t.TempDir()
	ctx := Context{
		DockerHostType: ContextLocal,
		ProjectDir:     projectDir,
	}
	writePortCompose(t, projectDir, `services:
  traefik:
    environment:
      SITECTL_TLS_MODE: "https-cloudflare-origin"
    command:
      - --entryPoints.http.address=:80
      - --entryPoints.https.address=:443
      - --certificatesresolvers.letsencrypt.acme.httpchallenge=true
`)

	if got := ctx.ComposeTLSProvider("self-managed"); got != "cloudflare-origin" {
		t.Fatalf("ComposeTLSProvider() = %q, want cloudflare-origin", got)
	}
}

func TestComposeTLSProviderReadsSitectlTLSModeMarkerFromEnvList(t *testing.T) {
	t.Parallel()

	projectDir := t.TempDir()
	ctx := Context{
		DockerHostType: ContextLocal,
		ProjectDir:     projectDir,
	}
	writePortCompose(t, projectDir, `services:
  traefik:
    environment:
      - SITECTL_TLS_MODE=https-mkcert
    command:
      - --entryPoints.http.address=:80
      - --entryPoints.https.address=:443
`)

	if got := ctx.ComposeTLSProvider("self-managed"); got != "mkcert" {
		t.Fatalf("ComposeTLSProvider() = %q, want mkcert", got)
	}
}

func TestComposeTLSProviderIgnoresNonTraefikACMECommands(t *testing.T) {
	t.Parallel()

	projectDir := t.TempDir()
	ctx := Context{
		DockerHostType: ContextLocal,
		ProjectDir:     projectDir,
	}
	writePortCompose(t, projectDir, `services:
  app:
    command:
      - --certificatesresolvers.letsencrypt.acme.httpchallenge=true
  traefik:
    command:
      - --entryPoints.http.address=:80
`)

	if got := ctx.ComposeTLSProvider("self-managed"); got != "self-managed" {
		t.Fatalf("ComposeTLSProvider() = %q, want self-managed", got)
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

func TestResolveLocalDevPortTreatsDockerPublishedPortAsInUse(t *testing.T) {
	restore := stubLocalDevPortChecks(
		t,
		func(port int) bool {
			return false
		},
		func(project string, port int) (dockerPublishedPortStatus, error) {
			if port == defaultHTTPPort {
				return dockerPublishedPortStatus{Occupied: true}, nil
			}
			return dockerPublishedPortStatus{}, nil
		},
	)
	defer restore()

	ctx := Context{}
	port, messages, err := ctx.resolveLocalDevPort("drupal", defaultHTTPPort, defaultHTTPFallback)
	if err != nil {
		t.Fatalf("resolveLocalDevPort() error = %v", err)
	}
	if port != defaultHTTPFallback {
		t.Fatalf("port = %d, want %d", port, defaultHTTPFallback)
	}
	if len(messages) != 1 || messages[0] != "Port 80 is already in use; trying 8080" {
		t.Fatalf("messages = %#v", messages)
	}
}

func TestResolveLocalDevPortAllowsDockerPublishedPortOwnedByProject(t *testing.T) {
	restore := stubLocalDevPortChecks(
		t,
		func(port int) bool {
			return false
		},
		func(project string, port int) (dockerPublishedPortStatus, error) {
			if project != "drupal" {
				t.Fatalf("project = %q, want drupal", project)
			}
			return dockerPublishedPortStatus{Occupied: true, Owned: true}, nil
		},
	)
	defer restore()

	ctx := Context{}
	port, messages, err := ctx.resolveLocalDevPort("drupal", defaultHTTPPort, defaultHTTPFallback)
	if err != nil {
		t.Fatalf("resolveLocalDevPort() error = %v", err)
	}
	if port != defaultHTTPPort {
		t.Fatalf("port = %d, want %d", port, defaultHTTPPort)
	}
	if len(messages) != 0 {
		t.Fatalf("messages = %#v", messages)
	}
}

func TestTCPPortListenPermissionDeniedIsNotOccupied(t *testing.T) {
	t.Parallel()

	if !tcpPortListenPermissionDenied(os.ErrPermission) {
		t.Fatal("expected permission denied to be detected")
	}
	if tcpPortListenPermissionDenied(errors.New("address already in use")) {
		t.Fatal("did not expect address-in-use error to be permission denied")
	}
}

func TestDockerPublishedPortStatusFromInspectChecksHostPort(t *testing.T) {
	t.Parallel()

	inspect := []byte(`[
  {
    "Config": {
      "Labels": {
        "com.docker.compose.project": "wp"
      }
    },
    "NetworkSettings": {
      "Ports": {
        "80/tcp": [
          {
            "HostIp": "0.0.0.0",
            "HostPort": "8080"
          }
        ]
      }
    }
  }
]`)

	status, err := dockerPublishedPortStatusFromInspect("wp", defaultHTTPPort, inspect)
	if err != nil {
		t.Fatalf("dockerPublishedPortStatusFromInspect(80) error = %v", err)
	}
	if status.Occupied {
		t.Fatalf("expected host port 80 to be available for 8080:80 binding, got %#v", status)
	}

	status, err = dockerPublishedPortStatusFromInspect("wp", defaultHTTPFallback, inspect)
	if err != nil {
		t.Fatalf("dockerPublishedPortStatusFromInspect(8080) error = %v", err)
	}
	if !status.Occupied || !status.Owned {
		t.Fatalf("expected host port 8080 to be occupied by project, got %#v", status)
	}
}

func TestDockerPublishedPortStatusFromInspectRejectsOtherProject(t *testing.T) {
	t.Parallel()

	inspect := []byte(`[
  {
    "Config": {
      "Labels": {
        "com.docker.compose.project": "ojs"
      }
    },
    "NetworkSettings": {
      "Ports": {
        "80/tcp": [
          {
            "HostPort": "80"
          }
        ]
      }
    }
  }
]`)

	status, err := dockerPublishedPortStatusFromInspect("wp", defaultHTTPPort, inspect)
	if err != nil {
		t.Fatalf("dockerPublishedPortStatusFromInspect() error = %v", err)
	}
	if !status.Occupied || status.Owned {
		t.Fatalf("expected host port 80 to be occupied by another project, got %#v", status)
	}
}

func TestDockerPublishedPortStatusFromInspectAllowsSameProject(t *testing.T) {
	t.Parallel()

	inspect := []byte(`[
  {
    "Config": {
      "Labels": {
        "com.docker.compose.project": "wp"
      }
    },
    "NetworkSettings": {
      "Ports": {
        "80/tcp": [
          {
            "HostPort": "80"
          }
        ]
      }
    }
  }
]`)

	status, err := dockerPublishedPortStatusFromInspect("wp", defaultHTTPPort, inspect)
	if err != nil {
		t.Fatalf("dockerPublishedPortStatusFromInspect() error = %v", err)
	}
	if !status.Occupied || !status.Owned {
		t.Fatalf("expected host port 80 to be owned by project, got %#v", status)
	}
}

func writePortCompose(t *testing.T, projectDir, contents string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(projectDir, "docker-compose.yml"), []byte(contents), 0o644); err != nil {
		t.Fatalf("WriteFile(compose) error = %v", err)
	}
}

func stubLocalDevPortChecks(t *testing.T, tcp func(int) bool, docker func(string, int) (dockerPublishedPortStatus, error)) func() {
	t.Helper()
	previousTCP := localDevTCPPortInUse
	previousDocker := localDevDockerPublishedPortUse
	localDevTCPPortInUse = tcp
	localDevDockerPublishedPortUse = docker
	return func() {
		localDevTCPPortInUse = previousTCP
		localDevDockerPublishedPortUse = previousDocker
	}
}
