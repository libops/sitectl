package traefik

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	corecomponent "github.com/libops/sitectl/pkg/component"
	"github.com/libops/sitectl/pkg/config"
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

func TestNormalizeIngressModeAliases(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		mode string
		want string
		ok   bool
	}{
		{name: "blank", mode: "", want: IngressModeHTTP, ok: true},
		{name: "http", mode: IngressModeHTTP, want: IngressModeHTTP, ok: true},
		{name: "https shorthand", mode: "https", want: IngressModeHTTPSCloudflareOrigin, ok: true},
		{name: "cloudflare alias", mode: "origin-ca", want: IngressModeHTTPSCloudflareOrigin, ok: true},
		{name: "letsencrypt alias", mode: "le", want: IngressModeHTTPSLetsEncrypt, ok: true},
		{name: "custom alias", mode: "byo", want: IngressModeHTTPSCustom, ok: true},
		{name: "mkcert alias", mode: "self-signed", want: IngressModeHTTPSMkcert, ok: true},
		{name: "invalid", mode: "ftp", want: "ftp", ok: false},
	}
	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got, ok := NormalizeIngressMode(tt.mode)
			if got != tt.want || ok != tt.ok {
				t.Fatalf("NormalizeIngressMode(%q) = %q, %v; want %q, %v", tt.mode, got, ok, tt.want, tt.ok)
			}
		})
	}
}

func TestResolveIngressSettingsCanonicalizesHTTPSAlias(t *testing.T) {
	t.Parallel()

	settings, err := resolveIngressSettings(map[string]string{
		ingressModeName:   "https",
		ingressDomainName: "app.example.org",
	})
	if err != nil {
		t.Fatalf("resolveIngressSettings() error = %v", err)
	}
	if settings.Mode != IngressModeHTTPSCloudflareOrigin || !settings.HTTPS || settings.Scheme != "https" {
		t.Fatalf("settings = %#v, want Cloudflare Origin HTTPS settings", settings)
	}
}

func TestResolveIngressSettingsValidatesDomainAndTuningValues(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		values  map[string]string
		wantErr string
	}{
		{name: "hostname", values: map[string]string{ingressDomainName: "app.example.org"}},
		{name: "ipv4", values: map[string]string{ingressDomainName: "192.0.2.10"}},
		{name: "ipv6", values: map[string]string{ingressDomainName: "[2001:db8::10]"}},
		{name: "upload bytes", values: map[string]string{uploadSizeName: "1048576"}},
		{name: "upload megabytes", values: map[string]string{uploadSizeName: "128M"}},
		{name: "upload lowercase suffix", values: map[string]string{uploadSizeName: "128m"}},
		{name: "timeout milliseconds", values: map[string]string{uploadTimeoutName: "500ms"}},
		{name: "timeout hours", values: map[string]string{uploadTimeoutName: "1h"}},
		{name: "domain control character", values: map[string]string{ingressDomainName: "app.example.org\nmalicious"}, wantErr: "control character"},
		{name: "domain scheme", values: map[string]string{ingressDomainName: "https://app.example.org"}, wantErr: "DNS hostname or IP address"},
		{name: "domain port", values: map[string]string{ingressDomainName: "app.example.org:8443"}, wantErr: "DNS hostname or IP address"},
		{name: "domain empty label", values: map[string]string{ingressDomainName: "app..example.org"}, wantErr: "DNS hostname or IP address"},
		{name: "upload decimal", values: map[string]string{uploadSizeName: "1.5G"}, wantErr: "maximum upload size"},
		{name: "upload unsupported suffix", values: map[string]string{uploadSizeName: "1T"}, wantErr: "maximum upload size"},
		{name: "timeout missing unit", values: map[string]string{uploadTimeoutName: "300"}, wantErr: "upload timeout"},
		{name: "timeout compound", values: map[string]string{uploadTimeoutName: "1m30s"}, wantErr: "upload timeout"},
		{name: "ACME display name", values: map[string]string{ingressModeName: IngressModeHTTPSLetsEncrypt, ingressACMEEmailName: "Ops <ops@example.org>"}, wantErr: "bare email address"},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			_, err := resolveIngressSettings(tt.values)
			if tt.wantErr == "" {
				if err != nil {
					t.Fatalf("resolveIngressSettings() error = %v", err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
				t.Fatalf("resolveIngressSettings() error = %v, want text %q", err, tt.wantErr)
			}
		})
	}
}

func TestMkcertHostsUnwrapsIPv6Literal(t *testing.T) {
	t.Parallel()

	hosts := mkcertHosts(ingressSettings{Domain: "[2001:db8::10]"})
	if len(hosts) == 0 || hosts[0] != "2001:db8::10" {
		t.Fatalf("mkcertHosts() = %#v, want unwrapped IPv6 SAN first", hosts)
	}
	for _, host := range hosts {
		if host == "[2001:db8::10]" {
			t.Fatalf("mkcertHosts() passed URL brackets to mkcert: %#v", hosts)
		}
	}
}

func TestApplyIngressComposeTLSModes(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		mode    string
		email   string
		want    []string
		notWant []string
	}{
		{
			name: "cloudflare origin",
			mode: IngressModeHTTPSCloudflareOrigin,
			want: []string{
				`SITECTL_TLS_MODE: "https-cloudflare-origin"`,
				`--entryPoints.https.address=:443`,
				`./certs/cert.pem:/certs/cert.pem:ro,z`,
				`./certs/privkey.pem:/certs/privkey.pem:ro,z`,
			},
			notWant: []string{
				`certificatesResolvers.letsencrypt`,
				`acme-data:/acme:rw`,
			},
		},
		{
			name:  "letsencrypt",
			mode:  IngressModeHTTPSLetsEncrypt,
			email: "ops@example.org",
			want: []string{
				`SITECTL_TLS_MODE: "https-letsencrypt"`,
				`--certificatesResolvers.letsencrypt.acme.email=ops@example.org`,
				`acme-data:/acme:rw`,
			},
			notWant: []string{
				`./certs/cert.pem:/certs/cert.pem`,
				`./certs/privkey.pem:/certs/privkey.pem`,
			},
		},
		{
			name: "custom",
			mode: IngressModeHTTPSCustom,
			want: []string{
				`SITECTL_TLS_MODE: "https-custom"`,
				`--entryPoints.https.address=:443`,
				`./certs/cert.pem:/certs/cert.pem:ro,z`,
				`./certs/privkey.pem:/certs/privkey.pem:ro,z`,
			},
			notWant: []string{
				`certificatesResolvers.letsencrypt`,
			},
		},
		{
			name: "mkcert",
			mode: IngressModeHTTPSMkcert,
			want: []string{
				`SITECTL_TLS_MODE: "https-mkcert"`,
				`--entryPoints.https.address=:443`,
				`./certs/cert.pem:/certs/cert.pem:ro,z`,
				`./certs/privkey.pem:/certs/privkey.pem:ro,z`,
			},
			notWant: []string{
				`certificatesResolvers.letsencrypt`,
			},
		},
	}
	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			path := filepath.Join(t.TempDir(), "docker-compose.yml")
			input := `services:
  traefik:
    image: traefik:v3
  app:
    image: example/app
`
			if err := os.WriteFile(path, []byte(input), 0o600); err != nil {
				t.Fatalf("WriteFile() error = %v", err)
			}
			compose, err := corecomponent.LoadComposeFile(path)
			if err != nil {
				t.Fatalf("LoadComposeFile() error = %v", err)
			}
			values := map[string]string{
				ingressModeName:   tt.mode,
				ingressDomainName: "app.example.org",
			}
			if tt.email != "" {
				values[ingressACMEEmailName] = tt.email
			}
			settings, err := resolveIngressSettings(values)
			if err != nil {
				t.Fatalf("resolveIngressSettings() error = %v", err)
			}
			if err := applyIngressCompose(&config.Context{DockerHostType: config.ContextLocal}, compose, normalizeIngressOptions(IngressOptions{AppService: "app"}), settings); err != nil {
				t.Fatalf("applyIngressCompose() error = %v", err)
			}
			if err := compose.Save(); err != nil {
				t.Fatalf("Save() error = %v", err)
			}
			data, err := os.ReadFile(path)
			if err != nil {
				t.Fatalf("ReadFile() error = %v", err)
			}
			got := string(data)
			for _, want := range append(tt.want, `INGRESS_HOSTNAMES: "app.example.org,localhost,127.0.0.1,::1"`, `INGRESS_SCHEME: "`+settings.Scheme+`"`) {
				if !strings.Contains(got, want) {
					t.Fatalf("expected compose to contain %q:\n%s", want, got)
				}
			}
			for _, notWant := range tt.notWant {
				if strings.Contains(got, notWant) {
					t.Fatalf("expected compose not to contain %q:\n%s", notWant, got)
				}
			}
		})
	}
}

func TestApplyIngressComposeHTTPRemovesTLSModeMarker(t *testing.T) {
	t.Parallel()

	path := filepath.Join(t.TempDir(), "docker-compose.yml")
	input := `services:
  traefik:
    image: traefik:v3
    environment:
      SITECTL_TLS_MODE: "https-mkcert"
    command:
      - --entryPoints.https.address=:443
    ports:
      - "443:443"
    volumes:
      - ./certs:/certs:ro
`
	if err := os.WriteFile(path, []byte(input), 0o600); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}
	compose, err := corecomponent.LoadComposeFile(path)
	if err != nil {
		t.Fatalf("LoadComposeFile() error = %v", err)
	}
	settings, err := resolveIngressSettings(map[string]string{ingressModeName: IngressModeHTTP})
	if err != nil {
		t.Fatalf("resolveIngressSettings() error = %v", err)
	}
	if err := applyIngressCompose(nil, compose, normalizeIngressOptions(IngressOptions{NoAppService: true}), settings); err != nil {
		t.Fatalf("applyIngressCompose() error = %v", err)
	}
	if err := compose.Save(); err != nil {
		t.Fatalf("Save() error = %v", err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile() error = %v", err)
	}
	got := string(data)
	for _, stale := range []string{`SITECTL_TLS_MODE`, `--entryPoints.https.address=:443`, `"443:443"`, `./certs:/certs:ro`} {
		if strings.Contains(got, stale) {
			t.Fatalf("expected stale HTTPS value %q to be removed:\n%s", stale, got)
		}
	}
}

func TestApplyIngressReplacesLegacyCertificateDirectoryMount(t *testing.T) {
	t.Parallel()

	path := filepath.Join(t.TempDir(), "docker-compose.yml")
	input := `services:
  traefik:
    image: traefik:v3
    volumes:
      - ./certs:/certs:ro,z
`
	if err := os.WriteFile(path, []byte(input), 0o600); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}
	compose, err := corecomponent.LoadComposeFile(path)
	if err != nil {
		t.Fatalf("LoadComposeFile() error = %v", err)
	}
	settings, err := resolveIngressSettings(map[string]string{ingressModeName: IngressModeHTTPSCustom})
	if err != nil {
		t.Fatalf("resolveIngressSettings() error = %v", err)
	}
	if err := applyIngressPortsAndVolumes(compose, normalizeIngressOptions(IngressOptions{NoAppService: true}), settings); err != nil {
		t.Fatalf("applyIngressPortsAndVolumes() error = %v", err)
	}
	if err := compose.Save(); err != nil {
		t.Fatalf("Save() error = %v", err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile() error = %v", err)
	}
	got := string(data)
	for _, want := range []string{
		"./certs/cert.pem:/certs/cert.pem:ro,z",
		"./certs/privkey.pem:/certs/privkey.pem:ro,z",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("expected leaf certificate mount %q:\n%s", want, got)
		}
	}
	for _, forbidden := range []string{"./certs:/certs", "rootCA-key.pem"} {
		if strings.Contains(got, forbidden) {
			t.Fatalf("Traefik must not expose certificate directory value %q:\n%s", forbidden, got)
		}
	}
}

func TestApplyIngressUploadTimeoutCoversTraefikNginxAndPHP(t *testing.T) {
	t.Parallel()

	path := filepath.Join(t.TempDir(), "docker-compose.yml")
	input := `services:
  traefik:
    image: traefik:v3
  app:
    image: example/app
`
	if err := os.WriteFile(path, []byte(input), 0o600); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}
	compose, err := corecomponent.LoadComposeFile(path)
	if err != nil {
		t.Fatalf("LoadComposeFile() error = %v", err)
	}
	settings, err := resolveIngressSettings(map[string]string{
		uploadSizeName:    "2G",
		uploadTimeoutName: "10m",
	})
	if err != nil {
		t.Fatalf("resolveIngressSettings() error = %v", err)
	}
	if err := applyIngressCompose(nil, compose, normalizeIngressOptions(IngressOptions{AppService: "app"}), settings); err != nil {
		t.Fatalf("applyIngressCompose() error = %v", err)
	}
	if err := compose.Save(); err != nil {
		t.Fatalf("Save() error = %v", err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile() error = %v", err)
	}
	got := string(data)
	for _, want := range []string{
		`--entryPoints.http.transport.respondingTimeouts.readTimeout=10m`,
		`NGINX_CLIENT_BODY_TIMEOUT: "10m"`,
		`NGINX_FASTCGI_READ_TIMEOUT: "10m"`,
		`NGINX_FASTCGI_SEND_TIMEOUT: "10m"`,
		`PHP_MAX_INPUT_TIME: "600"`,
		`PHP_MAX_EXECUTION_TIME: "600"`,
		`PHP_REQUEST_TERMINATE_TIMEOUT: "600"`,
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("expected end-to-end timeout setting %q:\n%s", want, got)
		}
	}
}

func TestIngressPHPTimeoutSecondsRoundsSubsecondValuesUp(t *testing.T) {
	t.Parallel()

	got, err := ingressPHPTimeoutSeconds("500ms")
	if err != nil {
		t.Fatalf("ingressPHPTimeoutSeconds() error = %v", err)
	}
	if got != "1" {
		t.Fatalf("ingressPHPTimeoutSeconds() = %q, want 1", got)
	}
}

func TestApplyIngressCallsAppUpdateHook(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	path := filepath.Join(dir, "docker-compose.yml")
	input := `services:
  traefik:
    image: traefik:v3
  app:
    image: example/app
`
	if err := os.WriteFile(path, []byte(input), 0o600); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}

	called := false
	ctx := &config.Context{DockerHostType: config.ContextLocal, ProjectDir: dir}
	opts := IngressOptions{
		AppService: "app",
		AppUpdate: func(_ context.Context, gotCtx *config.Context, compose *corecomponent.ComposeFile, update IngressAppUpdate) error {
			called = true
			if gotCtx != ctx {
				t.Fatalf("hook context = %#v, want original context", gotCtx)
			}
			if update.Mode != IngressModeHTTP || update.Domain != "app.example.org" || update.BaseURL != "http://app.example.org" || update.HTTPS {
				t.Fatalf("unexpected update: %#v", update)
			}
			return compose.SetServiceEnv("app", "APP_BASE_URL", update.BaseURL)
		},
	}
	if err := applyIngress(context.Background(), ctx, opts, map[string]string{
		ingressModeName:   IngressModeHTTP,
		ingressDomainName: "app.example.org",
	}); err != nil {
		t.Fatalf("applyIngress() error = %v", err)
	}
	if !called {
		t.Fatal("expected app update hook to be called")
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile() error = %v", err)
	}
	if !strings.Contains(string(data), `APP_BASE_URL: "http://app.example.org"`) {
		t.Fatalf("expected hook mutation in compose:\n%s", data)
	}
}

func TestSuggestedApplicationHosts(t *testing.T) {
	t.Parallel()

	got := SuggestedApplicationHosts(&config.Context{
		DockerHostType: config.ContextRemote,
		SSHHostname:    "172.239.194.15",
	}, IngressAppUpdate{Domain: "https://qa-origin.libops.io:443/path"})
	want := []string{"qa-origin.libops.io", "localhost", "127.0.0.1", "::1", "172.239.194.15"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("SuggestedApplicationHosts() = %#v, want %#v", got, want)
	}
}

func TestPrepareIngressTLSGeneratesMkcertCertificates(t *testing.T) {
	projectDir := t.TempDir()
	ctx := &config.Context{
		Name:           "site-dev",
		DockerHostType: config.ContextLocal,
		Environment:    "dev",
		ProjectDir:     projectDir,
	}
	settings, err := resolveIngressSettings(map[string]string{
		ingressModeName:   IngressModeHTTPSMkcert,
		ingressDomainName: "dev.example.test",
	})
	if err != nil {
		t.Fatalf("resolveIngressSettings() error = %v", err)
	}
	var gotHosts []string
	original := ingressMkcertRunner
	ingressMkcertRunner = func(_ context.Context, _ *config.Context, certPath, keyPath string, hosts []string) error {
		gotHosts = append([]string{}, hosts...)
		if err := os.WriteFile(certPath, []byte("cert"), 0o600); err != nil {
			return err
		}
		return os.WriteFile(keyPath, []byte("key"), 0o600)
	}
	t.Cleanup(func() { ingressMkcertRunner = original })
	originalPrereqs := ingressEnsureMkcertPrerequisites
	ingressEnsureMkcertPrerequisites = func(context.Context, *config.Context, corecomponent.ApplyOptions) error {
		return nil
	}
	t.Cleanup(func() { ingressEnsureMkcertPrerequisites = originalPrereqs })

	if err := validateIngressSettingsForContext(ctx, settings); err != nil {
		t.Fatalf("validateIngressSettingsForContext() error = %v", err)
	}
	if err := prepareIngressTLS(context.Background(), ctx, settings, corecomponent.ApplyOptions{}); err != nil {
		t.Fatalf("prepareIngressTLS() error = %v", err)
	}
	if got := strings.Join(gotHosts, ","); got != "dev.example.test,localhost,127.0.0.1,::1" {
		t.Fatalf("mkcert hosts = %q", got)
	}
	for _, rel := range []string{filepath.Join("certs", "cert.pem"), filepath.Join("certs", "privkey.pem")} {
		if _, err := os.Stat(filepath.Join(projectDir, rel)); err != nil {
			t.Fatalf("expected %s to exist: %v", rel, err)
		}
	}
}

func TestPrepareIngressTLSRunsMkcertOnRemoteHost(t *testing.T) {
	ctx := &config.Context{
		Name:           "site-qa",
		DockerHostType: config.ContextRemote,
		Environment:    "qa",
		ProjectDir:     "/srv/site",
		SSHHostname:    "qa-origin.libops.io",
	}
	settings, err := resolveIngressSettings(map[string]string{
		ingressModeName:   IngressModeHTTPSMkcert,
		ingressDomainName: "qa-origin.libops.io",
	})
	if err != nil {
		t.Fatalf("resolveIngressSettings() error = %v", err)
	}

	originalPrereqs := ingressEnsureMkcertPrerequisites
	ingressEnsureMkcertPrerequisites = func(_ context.Context, gotCtx *config.Context, opts corecomponent.ApplyOptions) error {
		if gotCtx != ctx {
			t.Fatalf("prereq context = %#v, want original context", gotCtx)
		}
		if !opts.Yolo {
			t.Fatal("expected yolo option to reach mkcert prerequisites")
		}
		return nil
	}
	t.Cleanup(func() { ingressEnsureMkcertPrerequisites = originalPrereqs })

	var gotCommands [][]string
	originalCommandRunner := ingressRunHostCommand
	ingressRunHostCommand = func(_ context.Context, gotCtx *config.Context, args []string) (string, error) {
		if gotCtx != ctx {
			t.Fatalf("command context = %#v, want original context", gotCtx)
		}
		gotCommands = append(gotCommands, append([]string{}, args...))
		return "", nil
	}
	t.Cleanup(func() { ingressRunHostCommand = originalCommandRunner })

	var gotCertPath string
	var gotKeyPath string
	var gotHosts []string
	originalRunner := ingressMkcertRunner
	ingressMkcertRunner = func(_ context.Context, gotCtx *config.Context, certPath, keyPath string, hosts []string) error {
		if gotCtx != ctx {
			t.Fatalf("mkcert context = %#v, want original context", gotCtx)
		}
		gotCertPath = certPath
		gotKeyPath = keyPath
		gotHosts = append([]string{}, hosts...)
		return nil
	}
	t.Cleanup(func() { ingressMkcertRunner = originalRunner })

	if err := prepareIngressTLS(context.Background(), ctx, settings, corecomponent.ApplyOptions{Yolo: true}); err != nil {
		t.Fatalf("prepareIngressTLS() error = %v", err)
	}
	if gotCertPath != "/srv/site/certs/cert.pem" || gotKeyPath != "/srv/site/certs/privkey.pem" {
		t.Fatalf("mkcert paths = %q %q", gotCertPath, gotKeyPath)
	}
	if got := strings.Join(gotHosts, ","); got != "qa-origin.libops.io,localhost,127.0.0.1,::1" {
		t.Fatalf("mkcert hosts = %q", got)
	}
	if len(gotCommands) != 1 || strings.Join(gotCommands[0], " ") != "mkdir -p /srv/site/certs" {
		t.Fatalf("expected remote cert directory command, got %#v", gotCommands)
	}
}

func TestValidateIngressSettingsRejectsMkcertForProduction(t *testing.T) {
	t.Parallel()

	settings, err := resolveIngressSettings(map[string]string{
		ingressModeName:   IngressModeHTTPSMkcert,
		ingressDomainName: "prod.example.org",
	})
	if err != nil {
		t.Fatalf("resolveIngressSettings() error = %v", err)
	}
	ctx := &config.Context{
		Name:           "site-prod",
		DockerHostType: config.ContextRemote,
		Environment:    "prod",
	}
	if err := validateIngressSettingsForContext(ctx, settings); err == nil {
		t.Fatal("expected production mkcert validation error")
	}
}

func TestApplyIngressTraefikCommandsRemovesStaleHTTPEntrypointAddress(t *testing.T) {
	t.Parallel()

	path := filepath.Join(t.TempDir(), "docker-compose.yml")
	input := `services:
  traefik:
    image: traefik:v3
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

func TestNormalizeTraefikFileProviderPreservesBotMitigationMounts(t *testing.T) {
	t.Parallel()

	path := filepath.Join(t.TempDir(), "docker-compose.yml")
	input := `services:
  traefik:
    image: traefik:v3
    command:
      - --providers.file.filename=/etc/traefik/dynamic/drupal.yml
      - --providers.docker=true
      - --providers.docker.exposedByDefault=false
    volumes:
      - ./conf/traefik:/etc/traefik/dynamic:ro,z
      - /var/run/docker.sock:/var/run/docker.sock
      - "/var/run/docker.sock:/var/run/docker.sock:rw"
      - '/var/run/docker.sock:/var/run/docker.sock:z'
      - type: bind
        source: "/var/run/docker.sock"
        target: /var/run/docker.sock
        read_only: true
      - { type: bind, source: /var/run/docker.sock, target: /var/run/docker.sock }
      - ./conf/traefik/plugins/captcha-protect:/plugins-local/src/github.com/libops/captcha-protect:r
      - ./conf/traefik/challenge.tmpl.html:/challenge.tmpl.html:ro
`
	if err := os.WriteFile(path, []byte(input), 0o600); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}
	compose, err := corecomponent.LoadComposeFile(path)
	if err != nil {
		t.Fatalf("LoadComposeFile() error = %v", err)
	}

	opts := normalizeIngressOptions(IngressOptions{NoAppService: true})
	if err := normalizeTraefikFileProvider(compose, opts); err != nil {
		t.Fatalf("normalizeTraefikFileProvider() error = %v", err)
	}
	if err := compose.Save(); err != nil {
		t.Fatalf("Save() error = %v", err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile() error = %v", err)
	}
	got := string(data)
	for _, want := range []string{
		"./conf/traefik:/etc/traefik/dynamic:ro,z",
		"./conf/traefik/plugins/captcha-protect:/plugins-local/src/github.com/libops/captcha-protect:r",
		"./conf/traefik/challenge.tmpl.html:/challenge.tmpl.html:ro",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("expected Traefik volume %q to be present:\n%s", want, got)
		}
	}
	for _, stale := range []string{
		"--providers.file.filename=",
		"--providers.docker",
		"/var/run/docker.sock:/var/run/docker.sock",
		"./conf/traefik:/etc/traefik/dynamic:ro,Z",
	} {
		if strings.Contains(got, stale) {
			t.Fatalf("stale Traefik provider value %q was not removed:\n%s", stale, got)
		}
	}
	if count := strings.Count(got, "/etc/traefik/dynamic"); count != 2 {
		// One command and one mount should refer to the dynamic directory.
		t.Fatalf("dynamic provider reference count = %d, want 2:\n%s", count, got)
	}
	assertComposeHasSingleTraefikDynamicMount(t, path)
}

func assertComposeHasSingleTraefikDynamicMount(t *testing.T, composePath string) {
	t.Helper()
	if _, err := exec.LookPath("docker"); err != nil {
		t.Skip("docker CLI is unavailable; text-level mount assertions completed")
	}
	command := exec.Command("docker", "compose", "-f", composePath, "config", "--format", "json")
	output, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("docker compose config failed: %v\n%s", err, output)
	}
	var rendered struct {
		Services map[string]struct {
			Volumes []struct {
				Source string `json:"source"`
				Target string `json:"target"`
			} `json:"volumes"`
		} `json:"services"`
	}
	if err := json.Unmarshal(output, &rendered); err != nil {
		t.Fatalf("parse docker compose config JSON: %v\n%s", err, output)
	}
	count := 0
	for _, volume := range rendered.Services["traefik"].Volumes {
		if volume.Source == "/var/run/docker.sock" {
			t.Fatalf("Traefik Docker socket mount survived normalization: %s", output)
		}
		if volume.Target == "/etc/traefik/dynamic" {
			count++
		}
	}
	if count != 1 {
		t.Fatalf("Traefik dynamic mount target count = %d, want 1: %s", count, output)
	}
}

func TestRewriteIngressRouterTextHTTPRemovesHostGates(t *testing.T) {
	t.Parallel()

	input := `http:
  services:
    app:
      loadBalancer:
        servers:
          - url: http://app:80
  routers:
    app-api:
      rule: 'Host("localhost") && PathPrefix("/api")'
      entryPoints:
        - websecure
      service: app
      tls: {}
    app:
      rule: Host("localhost")
      entryPoints:
        - websecure
      service: app
      tls: {}
`
	settings := ingressSettings{
		Mode:        IngressModeHTTP,
		Domain:      DefaultIngressDomain,
		UploadSize:  DefaultMaxUploadSize,
		ReadTimeout: DefaultUploadTimeout,
		Scheme:      "http",
	}
	got := rewriteIngressRouterText(input, normalizeIngressOptions(IngressOptions{
		NoAppService:    true,
		HTTPEntrypoint:  "web",
		HTTPSEntrypoint: "websecure",
	}), settings)

	if strings.Contains(got, "Host(") {
		t.Fatalf("expected HTTP routers to be hostless, got:\n%s", got)
	}
	for _, want := range []string{
		`rule: 'PathPrefix("/api")'`,
		"rule: PathPrefix(`/`)",
		"entryPoints:\n        - web",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("expected rewritten router config to contain %q, got:\n%s", want, got)
		}
	}
	if strings.Contains(got, "- websecure") {
		t.Fatalf("expected HTTP routers to use the HTTP entrypoint, got:\n%s", got)
	}
	if strings.Contains(got, "tls:") {
		t.Fatalf("expected HTTP routers to remove TLS blocks, got:\n%s", got)
	}
}

func TestRewriteIngressRouterTextHTTPSRestoresHostGates(t *testing.T) {
	t.Parallel()

	input := `http:
  services:
    app:
      loadBalancer:
        servers:
          - url: http://app:80
  routers:
    app-api:
      rule: 'PathPrefix("/api")'
      entryPoints:
        - web
      service: app
    app:
      rule: 'PathPrefix("/")'
      entryPoints:
        - web
      service: app
`
	settings := ingressSettings{
		Mode:        IngressModeHTTPSCustom,
		Domain:      "repo.example.org",
		UploadSize:  DefaultMaxUploadSize,
		ReadTimeout: DefaultUploadTimeout,
		Scheme:      "https",
		HTTPS:       true,
	}
	opts := normalizeIngressOptions(IngressOptions{
		NoAppService:    true,
		HTTPEntrypoint:  "web",
		HTTPSEntrypoint: "websecure",
		RouterHosts: map[string]string{
			"app-api": "api.{domain}",
		},
	})
	got := rewriteIngressRouterText(input, opts, settings)

	for _, want := range []string{
		"rule: 'Host(`api.repo.example.org`) && PathPrefix(\"/api\")'",
		"rule: 'Host(`repo.example.org`)'",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("expected rewritten router config to contain %q, got:\n%s", want, got)
		}
	}
	if count := strings.Count(got, "tls: {}"); count != 2 {
		t.Fatalf("TLS block count = %d, want 2:\n%s", count, got)
	}
	if count := strings.Count(got, "- websecure"); count != 2 {
		t.Fatalf("websecure entrypoint count = %d, want 2:\n%s", count, got)
	}
	if strings.Contains(got, "- web\n") {
		t.Fatalf("expected HTTPS routers to stop using the HTTP entrypoint, got:\n%s", got)
	}
}

func TestRewriteIngressRouterTextHTTPPreservesSubdomainOnlyServiceRouters(t *testing.T) {
	t.Parallel()

	input := `http:
  services:
    drupal:
      loadBalancer:
        servers:
          - url: http://drupal:80
    fcrepo:
      loadBalancer:
        servers:
          - url: http://fcrepo:8080
    triplet:
      loadBalancer:
        servers:
          - url: http://triplet:8080
  routers:
    drupal:
      rule: Host("localhost")
      service: drupal
    fcrepo:
      rule: Host("fcrepo.localhost")
      service: fcrepo
    triplet:
      rule: Host("localhost") && PathPrefix("/iiif")
      service: triplet
`
	settings := ingressSettings{
		Mode:        IngressModeHTTP,
		Domain:      DefaultIngressDomain,
		UploadSize:  DefaultMaxUploadSize,
		ReadTimeout: DefaultUploadTimeout,
		Scheme:      "http",
	}
	opts := normalizeIngressOptions(IngressOptions{
		AppService: "drupal",
		RouterHosts: map[string]string{
			"drupal":  "{domain}",
			"fcrepo":  "fcrepo.{domain}",
			"triplet": "{domain}",
		},
	})
	got := rewriteIngressRouterText(input, opts, settings)

	for _, want := range []string{
		"rule: PathPrefix(`/`)",
		`rule: Host("fcrepo.localhost")`,
		`rule: PathPrefix("/iiif")`,
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("expected rewritten router config to contain %q, got:\n%s", want, got)
		}
	}
	if strings.Contains(got, `Host("localhost")`) {
		t.Fatalf("expected localhost app/path routers to be hostless, got:\n%s", got)
	}
}
