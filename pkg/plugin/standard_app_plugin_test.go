package plugin

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/libops/sitectl/pkg/config"
	coredevmode "github.com/libops/sitectl/pkg/services/devmode"
	coretraefik "github.com/libops/sitectl/pkg/services/traefik"
	sitevalidate "github.com/libops/sitectl/pkg/validate"
	"github.com/spf13/cobra"
)

type standardAppHealthcheckStub struct{}

func (standardAppHealthcheckStub) BindFlags(cmd *cobra.Command) {}

func (standardAppHealthcheckStub) Run(cmd *cobra.Command, ctx *config.Context) ([]sitevalidate.Result, error) {
	return nil, nil
}

func TestRegisterStandardComposeAppPlugin(t *testing.T) {
	sdk := NewSDK(Metadata{Name: "wp"})

	err := sdk.RegisterStandardComposeAppPlugin(StandardComposeAppPluginOptions{
		DisplayName:  "WordPress",
		AppService:   "wordpress",
		Router:       "wordpress-web",
		DefaultPath:  "./wp",
		ReadyMessage: "WordPress is ready",
		CreateSpec: CreateSpec{
			Name:    "default",
			Default: true,
		},
		IngressOptions: coretraefik.IngressOptions{
			RouterHosts: map[string]string{"wordpress-web": "INGRESS_HOSTNAMES"},
		},
		DevModeOptions: coredevmode.Options{
			Volumes: []string{"./wp-content:/var/www/html/wp-content:rw"},
		},
		Healthcheck: standardAppHealthcheckStub{},
	})
	if err != nil {
		t.Fatalf("RegisterStandardComposeAppPlugin() error = %v", err)
	}

	if len(sdk.creates) != 1 {
		t.Fatalf("registered creates = %d, want 1", len(sdk.creates))
	}
	if got := sdk.creates[0].Spec.Name; got != "default" {
		t.Fatalf("create spec name = %q, want default", got)
	}
	if got := sdk.serviceComponentDisplayName; got != "WordPress" {
		t.Fatalf("service component display name = %q, want WordPress", got)
	}
	if len(sdk.serviceComponents) != 2 {
		t.Fatalf("registered service components = %d, want 2", len(sdk.serviceComponents))
	}
	if !sdk.hasHealthcheck {
		t.Fatalf("healthcheck was not registered")
	}
	if !sdk.hasIngressRoutes {
		t.Fatalf("ingress route provider was not registered")
	}

	projectDir := t.TempDir()
	compose := []byte("services:\n  wordpress:\n    image: example/wordpress:latest\n")
	if err := os.WriteFile(filepath.Join(projectDir, "docker-compose.yml"), compose, 0o600); err != nil {
		t.Fatalf("write compose file: %v", err)
	}
	claim, err := sdk.detectOwnProject(projectDir)
	if err != nil {
		t.Fatalf("detectOwnProject() error = %v", err)
	}
	if claim == nil {
		t.Fatalf("detectOwnProject() claim = nil, want claim")
	}
	if got := claim.Plugin; got != "wp" {
		t.Fatalf("claim plugin = %q, want wp", got)
	}
	if got := claim.Reason; got != "wordpress service" {
		t.Fatalf("claim reason = %q, want wordpress service", got)
	}
}
