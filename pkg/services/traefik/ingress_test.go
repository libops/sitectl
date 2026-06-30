package traefik

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	corecomponent "github.com/libops/sitectl/pkg/component"
	"github.com/spf13/cobra"
)

func TestIngressCreateDefaultsDoNotPrompt(t *testing.T) {
	t.Parallel()

	ingress, err := Ingress(IngressOptions{NoAppService: true})
	if err != nil {
		t.Fatalf("Ingress() error = %v", err)
	}
	option := ingress.Definition().CreateOption()
	cmd := &cobra.Command{Use: "create"}
	corecomponent.AddCreateFlags(cmd, option)

	decisions, err := corecomponent.ResolveCreateDecisions(cmd, func(question ...string) (string, error) {
		t.Fatalf("did not expect create prompt for ingress defaults: %v", question)
		return "", nil
	}, option)
	if err != nil {
		t.Fatalf("ResolveCreateDecisions() error = %v", err)
	}

	decision := decisions[IngressName]
	if decision.Disposition != corecomponent.DispositionEnabled {
		t.Fatalf("ingress disposition = %q, want %q", decision.Disposition, corecomponent.DispositionEnabled)
	}
	if decision.State != corecomponent.StateOn {
		t.Fatalf("ingress state = %q, want %q", decision.State, corecomponent.StateOn)
	}

	wantOptions := map[string]string{
		ingressModeName:   IngressModeHTTP,
		ingressDomainName: DefaultIngressDomain,
		uploadSizeName:    DefaultMaxUploadSize,
		uploadTimeoutName: DefaultUploadTimeout,
	}
	for name, want := range wantOptions {
		if got := decision.Options[name]; got != want {
			t.Fatalf("ingress option %q = %q, want %q", name, got, want)
		}
	}
	if _, ok := decision.Options[ingressACMEEmailName]; ok {
		t.Fatalf("ingress option %q should not be set by default", ingressACMEEmailName)
	}
	if _, ok := decision.Options[ingressTrustedIPName]; ok {
		t.Fatalf("ingress option %q should not be set by default", ingressTrustedIPName)
	}
}

func TestApplyIngressTraefikCommandsRemovesStaleHTTPEntrypointAddress(t *testing.T) {
	t.Parallel()

	path := filepath.Join(t.TempDir(), "docker-compose.yml")
	input := `services:
  traefik:
    command:
      - --providers.file.directory=/etc/traefik/dynamic
      - --entrypoints.web.address=:80
      - --entrypoints.web.transport.respondingTimeouts.readTimeout=300s
      - --entryPoints.web.forwardedHeaders.trustedIPs=10.0.0.0/8
      - --entryPoints.https.address=:443
`
	if err := os.WriteFile(path, []byte(input), 0o600); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}
	compose, err := corecomponent.LoadComposeFile(path)
	if err != nil {
		t.Fatalf("LoadComposeFile() error = %v", err)
	}

	opts := normalizeIngressOptions(IngressOptions{NoAppService: true})
	settings := ingressSettings{
		Mode:        IngressModeHTTP,
		Domain:      DefaultIngressDomain,
		UploadSize:  DefaultMaxUploadSize,
		ReadTimeout: DefaultUploadTimeout,
		Scheme:      "http",
	}
	if err := applyIngressTraefikCommands(compose, opts, settings); err != nil {
		t.Fatalf("applyIngressTraefikCommands() error = %v", err)
	}
	if err := compose.Save(); err != nil {
		t.Fatalf("Save() error = %v", err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile() error = %v", err)
	}
	got := string(data)
	for _, stale := range []string{
		"--entrypoints.web.address=:80",
		"--entrypoints.web.transport.respondingTimeouts.readTimeout=300s",
		"--entryPoints.web.forwardedHeaders.trustedIPs=10.0.0.0/8",
		"--entryPoints.https.address=:443",
	} {
		if strings.Contains(got, stale) {
			t.Fatalf("stale Traefik command %q was not removed:\n%s", stale, got)
		}
	}
	want := "--entryPoints.http.address=:80"
	if count := strings.Count(got, want); count != 1 {
		t.Fatalf("Traefik command %q count = %d, want 1:\n%s", want, count, got)
	}
	if !strings.Contains(got, "--entryPoints.http.transport.respondingTimeouts.readTimeout="+DefaultUploadTimeout) {
		t.Fatalf("expected active HTTP read timeout command:\n%s", got)
	}
}
