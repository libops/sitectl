package config

import (
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

func TestComposeUpPortEnvOnlySetsSecurePortWhenComposePublishesHTTPS(t *testing.T) {
	projectDir := t.TempDir()
	ctx := Context{
		DockerHostType: ContextLocal,
		Environment:    "dev",
		ProjectDir:     projectDir,
		ProjectName:    "ports-test",
	}
	httpPort := freeTCPPort(t)
	httpsPort := freeTCPPort(t)
	t.Setenv(hostInsecurePortEnv, strconv.Itoa(httpPort))
	t.Setenv(hostSecurePortEnv, strconv.Itoa(httpsPort))

	writePortCompose(t, projectDir, `services:
  traefik:
    ports:
      - "${HOST_INSECURE_PORT:-80}:80"
`)
	values, _, err := ctx.ComposeUpPortEnv()
	if err != nil {
		t.Fatalf("ComposeUpPortEnv(http) error = %v", err)
	}
	if values[hostInsecurePortEnv] != strconv.Itoa(httpPort) {
		t.Fatalf("expected HTTP port %d, got %+v", httpPort, values)
	}
	if _, ok := values[hostSecurePortEnv]; ok {
		t.Fatalf("did not expect secure port for HTTP-only compose, got %+v", values)
	}

	writePortCompose(t, projectDir, `services:
  traefik:
    ports:
      - "${HOST_INSECURE_PORT:-80}:80"
      - "${HOST_SECURE_PORT:-443}:443"
`)
	values, _, err = ctx.ComposeUpPortEnv()
	if err != nil {
		t.Fatalf("ComposeUpPortEnv(https) error = %v", err)
	}
	if values[hostSecurePortEnv] != strconv.Itoa(httpsPort) {
		t.Fatalf("expected HTTPS port %d, got %+v", httpsPort, values)
	}
}

func TestComposeUpPortEnvReadsProjectEnvAndSetsSiteURL(t *testing.T) {
	projectDir := t.TempDir()
	ctx := Context{
		DockerHostType: ContextLocal,
		Environment:    "dev",
		ProjectDir:     projectDir,
		ProjectName:    "ports-test",
	}
	httpPort := freeTCPPort(t)
	writePortCompose(t, projectDir, `services:
  traefik:
    ports:
      - "${HOST_INSECURE_PORT:-80}:80"
`)
	if err := os.WriteFile(filepath.Join(projectDir, ".env"), []byte("DOMAIN=example.test\nHOST_INSECURE_PORT="+strconv.Itoa(httpPort)+"\n"), 0o600); err != nil {
		t.Fatalf("WriteFile(.env) error = %v", err)
	}

	values, _, err := ctx.ComposeUpPortEnv()
	if err != nil {
		t.Fatalf("ComposeUpPortEnv() error = %v", err)
	}
	if values[hostInsecurePortEnv] != strconv.Itoa(httpPort) {
		t.Fatalf("HOST_INSECURE_PORT = %q, want %d", values[hostInsecurePortEnv], httpPort)
	}
	wantURL := "http://example.test:" + strconv.Itoa(httpPort) + "/"
	if values[siteURLEnv] != wantURL {
		t.Fatalf("SITE_URL = %q, want %q", values[siteURLEnv], wantURL)
	}
}

func TestPersistComposeUpPortEnvWritesNonDefaultPorts(t *testing.T) {
	projectDir := t.TempDir()
	ctx := Context{
		DockerHostType: ContextLocal,
		Environment:    "dev",
		ProjectDir:     projectDir,
		ProjectName:    "ports-test",
	}
	httpPort := freeTCPPort(t)
	writePortCompose(t, projectDir, `services:
  traefik:
    ports:
      - "${HOST_INSECURE_PORT:-80}:80"
`)
	values := map[string]string{
		hostInsecurePortEnv: strconv.Itoa(httpPort),
		siteURLEnv:          "http://localhost:" + strconv.Itoa(httpPort) + "/",
	}

	messages, err := ctx.PersistComposeUpPortEnv(values)
	if err != nil {
		t.Fatalf("PersistComposeUpPortEnv() error = %v", err)
	}
	if len(messages) != 1 {
		t.Fatalf("expected one persistence message, got %#v", messages)
	}
	data, err := os.ReadFile(filepath.Join(projectDir, ".env"))
	if err != nil {
		t.Fatalf("ReadFile(.env) error = %v", err)
	}
	text := string(data)
	for _, want := range []string{
		`HOST_INSECURE_PORT="` + strconv.Itoa(httpPort) + `"`,
		`SITE_URL="http://localhost:` + strconv.Itoa(httpPort) + `/"`,
	} {
		if !strings.Contains(text, want) {
			t.Fatalf("expected .env to contain %q, got:\n%s", want, text)
		}
	}
}

func TestPersistComposeUpPortEnvSkipsDefaultPorts(t *testing.T) {
	projectDir := t.TempDir()
	ctx := Context{
		DockerHostType: ContextLocal,
		Environment:    "dev",
		ProjectDir:     projectDir,
		ProjectName:    "ports-test",
	}

	messages, err := ctx.PersistComposeUpPortEnv(map[string]string{
		hostInsecurePortEnv: strconv.Itoa(defaultHTTPPort),
		siteURLEnv:          "http://localhost/",
	})
	if err != nil {
		t.Fatalf("PersistComposeUpPortEnv() error = %v", err)
	}
	if len(messages) != 0 {
		t.Fatalf("expected no persistence message for default port, got %#v", messages)
	}
	if _, err := os.Stat(filepath.Join(projectDir, ".env")); err == nil {
		t.Fatal("default port persistence unexpectedly created .env")
	} else if !os.IsNotExist(err) {
		t.Fatalf("Stat(.env) error = %v", err)
	}
}

func TestComposeDataPublishesSecurePort(t *testing.T) {
	t.Parallel()

	for _, compose := range []string{
		"services:\n  traefik:\n    ports:\n      - \"443:443\"\n",
		"services:\n  traefik:\n    ports:\n      - target: 443\n        published: 443\n",
		"services:\n  traefik:\n    ports:\n      - \"${HOST_SECURE_PORT:-443}:443\"\n",
	} {
		if !composeDataPublishesSecurePort([]byte(compose)) {
			t.Fatalf("expected compose to publish secure port:\n%s", compose)
		}
	}
	if composeDataPublishesSecurePort([]byte("services:\n  traefik:\n    ports:\n      - \"80:80\"\n")) {
		t.Fatal("did not expect HTTP-only compose to publish secure port")
	}
}

func writePortCompose(t *testing.T, projectDir, contents string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(projectDir, "docker-compose.yml"), []byte(contents), 0o644); err != nil {
		t.Fatalf("WriteFile(compose) error = %v", err)
	}
}

func freeTCPPort(t *testing.T) int {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("Listen() error = %v", err)
	}
	defer listener.Close()
	return listener.Addr().(*net.TCPAddr).Port
}
