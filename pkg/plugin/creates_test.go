package plugin

import (
	"fmt"
	"path/filepath"
	"testing"

	corecomponent "github.com/libops/sitectl/pkg/component"
	"github.com/libops/sitectl/pkg/config"
	"github.com/spf13/cobra"
)

func TestEnsureComposeCreateContextUsesDatabaseDefaults(t *testing.T) {
	tempHome := t.TempDir()
	t.Setenv("HOME", tempHome)

	sdk := NewSDK(Metadata{Name: "archivesspace"})
	ctx, err := sdk.EnsureComposeCreateContext(ComposeCreateRequest{
		TargetType: config.ContextLocal,
		Path:       filepath.Join(tempHome, "archivesspace"),
	}, ComposeCreateContextOptions{
		DefaultName:                   "archivesspace-local",
		DefaultSite:                   "archivesspace",
		DefaultPlugin:                 "archivesspace",
		DefaultProjectName:            "archivesspace",
		DefaultDatabaseService:        "mysql",
		DefaultDatabaseUser:           "as",
		DefaultDatabasePasswordSecret: "ARCHIVESSPACE_DB_PASSWORD",
		DefaultDatabaseName:           "archivesspace",
		Input: func(question ...string) (string, error) {
			t.Fatal("did not expect prompt")
			return "", nil
		},
	})
	if err != nil {
		t.Fatalf("EnsureComposeCreateContext() error = %v", err)
	}

	if ctx.DatabaseService != "mysql" {
		t.Fatalf("expected database service mysql, got %q", ctx.DatabaseService)
	}
	if ctx.DatabaseUser != "as" {
		t.Fatalf("expected database user as, got %q", ctx.DatabaseUser)
	}
	if ctx.DatabasePasswordSecret != "ARCHIVESSPACE_DB_PASSWORD" {
		t.Fatalf("expected database password secret ARCHIVESSPACE_DB_PASSWORD, got %q", ctx.DatabasePasswordSecret)
	}
	if ctx.DatabaseName != "archivesspace" {
		t.Fatalf("expected database name archivesspace, got %q", ctx.DatabaseName)
	}
}

func TestPopulateRemoteCreateRequestUsesProvidedSSHValuesWithoutPrompt(t *testing.T) {
	req := &ComposeCreateRequest{
		SSHHostname: "192.0.2.10",
		SSHUser:     "root",
		SSHPort:     2222,
		SSHKeyPath:  "/tmp/sitectl-key",
	}

	err := populateRemoteCreateRequest(req, func(question ...string) (string, error) {
		t.Fatalf("did not expect prompt: %v", question)
		return "", fmt.Errorf("unexpected prompt")
	})
	if err != nil {
		t.Fatalf("populateRemoteCreateRequest() error = %v", err)
	}

	if req.SSHHostname != "192.0.2.10" {
		t.Fatalf("expected SSH hostname to be preserved, got %q", req.SSHHostname)
	}
	if req.SSHUser != "root" {
		t.Fatalf("expected SSH user to be preserved, got %q", req.SSHUser)
	}
	if req.SSHPort != 2222 {
		t.Fatalf("expected SSH port to be preserved, got %d", req.SSHPort)
	}
	if req.SSHKeyPath != "/tmp/sitectl-key" {
		t.Fatalf("expected SSH key path to be preserved, got %q", req.SSHKeyPath)
	}
	if req.DockerSocket != "/var/run/docker.sock" {
		t.Fatalf("expected default docker socket, got %q", req.DockerSocket)
	}
}

func TestApplyRemoteIngressCreateDefaultsUsesSSHHostname(t *testing.T) {
	decisions := map[string]corecomponent.ReviewDecision{
		"ingress": {
			Options: map[string]string{
				"mode":   "http",
				"domain": "localhost",
			},
		},
	}
	applyRemoteIngressCreateDefaults(&config.Context{
		DockerHostType: config.ContextRemote,
		SSHHostname:    "192.0.2.10",
	}, decisions)
	if got := decisions["ingress"].Options["domain"]; got != "192.0.2.10" {
		t.Fatalf("ingress domain = %q, want remote SSH hostname", got)
	}
}

func TestApplyRemoteIngressCreateDefaultsPreservesExplicitDomain(t *testing.T) {
	decisions := map[string]corecomponent.ReviewDecision{
		"ingress": {
			Options: map[string]string{
				"mode":   "http",
				"domain": "app.example.org",
			},
		},
	}
	applyRemoteIngressCreateDefaults(&config.Context{
		DockerHostType: config.ContextRemote,
		SSHHostname:    "192.0.2.10",
	}, decisions)
	if got := decisions["ingress"].Options["domain"]; got != "app.example.org" {
		t.Fatalf("ingress domain = %q, want explicit domain preserved", got)
	}
}

func TestResolveComposeCreateRequestBindsExplicitContextForRemoteCreate(t *testing.T) {
	tempHome := t.TempDir()
	t.Setenv("HOME", tempHome)

	sdk := NewSDK(Metadata{Name: "wp"})
	cmd := &cobra.Command{Use: "create"}
	if err := sdk.BindComposeCreateFlags(cmd, CreateSpec{
		DockerComposeRepo:   "https://example.org/template.git",
		DockerComposeBranch: "main",
	}, nil, ""); err != nil {
		t.Fatalf("BindComposeCreateFlags() error = %v", err)
	}
	for name, value := range map[string]string{
		"context":         "wp-remote-qa",
		"type":            string(config.ContextRemote),
		"checkout-source": "template",
		"path":            "/srv/wp",
		"ssh-hostname":    "192.0.2.10",
		"ssh-user":        "root",
		"ssh-port":        "2222",
		"ssh-key":         filepath.Join(tempHome, ".ssh", "id_ed25519"),
	} {
		if err := cmd.Flags().Set(name, value); err != nil {
			t.Fatalf("Set(%s) error = %v", name, err)
		}
	}

	req, err := sdk.ResolveComposeCreateRequest(cmd, func(question ...string) (string, error) {
		t.Fatalf("did not expect prompt: %v", question)
		return "", fmt.Errorf("unexpected prompt")
	}, "wp", "", "", "", "")
	if err != nil {
		t.Fatalf("ResolveComposeCreateRequest() error = %v", err)
	}

	if req.ContextName != "wp-remote-qa" {
		t.Fatalf("expected context name wp-remote-qa, got %q", req.ContextName)
	}
	if req.TargetType != config.ContextRemote {
		t.Fatalf("expected remote target type, got %q", req.TargetType)
	}
}

func TestEnsureComposeCreateContextRemoteUsesProvidedValuesWithoutPrompt(t *testing.T) {
	tempHome := t.TempDir()
	t.Setenv("HOME", tempHome)

	projectDir := "/srv/sitectl/wp"
	keyPath := filepath.Join(tempHome, ".ssh", "id_ed25519")
	sdk := NewSDK(Metadata{Name: "wp"})
	ctx, err := sdk.EnsureComposeCreateContext(ComposeCreateRequest{
		TargetType:         config.ContextRemote,
		Path:               projectDir,
		Site:               "qa-site",
		Environment:        "qa",
		ProjectName:        "wp",
		ComposeProjectName: "wp",
		SSHHostname:        "192.0.2.10",
		SSHUser:            "root",
		SSHPort:            2222,
		SSHKeyPath:         keyPath,
	}, ComposeCreateContextOptions{
		DefaultName:        "wp-remote",
		DefaultSite:        "wp",
		DefaultPlugin:      "wp",
		DefaultProjectName: "wp",
		Input: func(question ...string) (string, error) {
			t.Fatalf("did not expect prompt: %v", question)
			return "", fmt.Errorf("unexpected prompt")
		},
	})
	if err != nil {
		t.Fatalf("EnsureComposeCreateContext() error = %v", err)
	}

	if ctx.DockerHostType != config.ContextRemote {
		t.Fatalf("expected remote context, got %q", ctx.DockerHostType)
	}
	if ctx.ProjectDir != projectDir {
		t.Fatalf("expected project dir %q, got %q", projectDir, ctx.ProjectDir)
	}
	if ctx.SSHHostname != "192.0.2.10" {
		t.Fatalf("expected SSH hostname to be preserved, got %q", ctx.SSHHostname)
	}
	if ctx.SSHUser != "root" {
		t.Fatalf("expected SSH user to be preserved, got %q", ctx.SSHUser)
	}
	if ctx.SSHPort != 2222 {
		t.Fatalf("expected SSH port to be preserved, got %d", ctx.SSHPort)
	}
	if ctx.SSHKeyPath != keyPath {
		t.Fatalf("expected SSH key path %q, got %q", keyPath, ctx.SSHKeyPath)
	}
}
