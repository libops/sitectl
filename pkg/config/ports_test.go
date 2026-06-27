package config

import (
	"net"
	"os"
	"path/filepath"
	"strconv"
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
