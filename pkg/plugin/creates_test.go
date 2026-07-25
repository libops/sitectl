package plugin

import (
	"fmt"
	"path/filepath"
	"strings"
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

func TestResolveComposeCreateRequestReviewsPathFirstAndUsesFlagDefaults(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	sdk := NewSDK(Metadata{Name: "wp"})
	cmd := &cobra.Command{Use: "create"}
	if err := sdk.BindComposeCreateFlags(cmd, CreateSpec{DockerComposeRepo: "https://example.org/wp.git", DockerComposeBranch: "main"}, nil, ""); err != nil {
		t.Fatal(err)
	}
	for name, value := range map[string]string{
		"path":            "/srv/wp",
		"type":            "local",
		"checkout-source": "template",
		"site":            "museum",
	} {
		if err := cmd.Flags().Set(name, value); err != nil {
			t.Fatal(err)
		}
	}
	var prompts []string
	req, err := sdk.ResolveComposeCreateRequest(cmd, func(question ...string) (string, error) {
		prompts = append(prompts, strings.Join(question, "\n"))
		return "", nil
	}, "wp", "", "", "", "")
	if err != nil {
		t.Fatalf("ResolveComposeCreateRequest() error = %v", err)
	}
	if len(prompts) == 0 || !strings.Contains(strings.ToLower(prompts[0]), "working path") {
		t.Fatalf("first prompt does not establish the working path: %v", prompts)
	}
	if req.Path != "/srv/wp" || req.Site != "museum" || req.TargetType != config.ContextLocal {
		t.Fatalf("request did not preserve reviewed flag defaults: %+v", req)
	}
	if !strings.Contains(strings.ToLower(prompts[len(prompts)-1]), "review create") {
		t.Fatalf("last prompt does not review the combined create decisions: %v", prompts)
	}
}

func TestResolveComposeCreateRequestYoloSkipsDecisionReview(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	sdk := NewSDK(Metadata{Name: "wp"})
	cmd := &cobra.Command{Use: "create"}
	if err := sdk.BindComposeCreateFlags(cmd, CreateSpec{DockerComposeRepo: "https://example.org/wp.git", DockerComposeBranch: "main"}, nil, ""); err != nil {
		t.Fatal(err)
	}
	for name, value := range map[string]string{"path": "/srv/wp", "type": "local", "checkout-source": "template", "yolo": "true"} {
		if err := cmd.Flags().Set(name, value); err != nil {
			t.Fatal(err)
		}
	}
	req, err := sdk.ResolveComposeCreateRequest(cmd, func(question ...string) (string, error) {
		t.Fatalf("--yolo prompted for %v", question)
		return "", nil
	}, "wp", "", "", "", "")
	if err != nil {
		t.Fatalf("ResolveComposeCreateRequest() error = %v", err)
	}
	if !req.SetDefaultContext {
		t.Fatal("--yolo did not automatically select the first created context as default")
	}
}

func TestResolveComposeCreateRequestMapsDeprecatedProjectNameFlag(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	sdk := NewSDK(Metadata{Name: "wp"})
	cmd := &cobra.Command{Use: "create"}
	if err := sdk.BindComposeCreateFlags(cmd, CreateSpec{DockerComposeRepo: "https://example.org/wp.git", DockerComposeBranch: "main"}, nil, ""); err != nil {
		t.Fatal(err)
	}
	for name, value := range map[string]string{
		"path":            "/srv/wp",
		"type":            "local",
		"checkout-source": "existing",
		"project-name":    "legacy-runtime",
		"yolo":            "true",
	} {
		if err := cmd.Flags().Set(name, value); err != nil {
			t.Fatalf("Set(%s) error = %v", name, err)
		}
	}

	req, err := sdk.ResolveComposeCreateRequest(cmd, func(question ...string) (string, error) {
		t.Fatalf("--yolo prompted for %v", question)
		return "", nil
	}, "wp", "", "", "", "")
	if err != nil {
		t.Fatalf("ResolveComposeCreateRequest() error = %v", err)
	}
	if req.ComposeProjectName != "legacy-runtime" || req.ProjectName != "legacy-runtime" {
		t.Fatalf("deprecated project name was not mapped to the compose identity: %+v", req)
	}
}

func TestPrepareCreateDecisionReviewUsesFollowUpFlagAsPromptDefault(t *testing.T) {
	cmd := &cobra.Command{Use: "create"}
	options := []corecomponent.CreateOption{{
		Name:           "ingress",
		PromptOnCreate: true,
		FollowUps: []corecomponent.FollowUpSpec{{
			Name:           "domain",
			PromptOnCreate: true,
		}},
	}}
	corecomponent.AddCreateFlags(cmd, options...)
	if err := cmd.Flags().Set("ingress-domain", "app.example.org"); err != nil {
		t.Fatal(err)
	}

	prepared, restore, err := prepareCreateDecisionReview(cmd, options, false)
	if err != nil {
		t.Fatal(err)
	}
	if got := prepared[0].FollowUps[0].DefaultValue; got != "app.example.org" {
		t.Fatalf("follow-up prompt default = %q, want app.example.org", got)
	}
	if cmd.Flags().Changed("ingress-domain") {
		t.Fatal("explicit follow-up flag should be reviewed instead of skipping its prompt")
	}
	restore()
	if !cmd.Flags().Changed("ingress-domain") {
		t.Fatal("follow-up flag changed state was not restored")
	}
}

func TestPrepareCreateDecisionReviewMapsLegacyStateToAllowedDisposition(t *testing.T) {
	cmd := &cobra.Command{Use: "create"}
	options := []corecomponent.CreateOption{{
		Name:                "fcrepo",
		PromptOnCreate:      true,
		DefaultDisposition:  corecomponent.DispositionEnabled,
		AllowedDispositions: []corecomponent.Disposition{corecomponent.DispositionEnabled, corecomponent.DispositionSuperseded},
	}}
	corecomponent.AddCreateFlags(cmd, options...)
	if err := cmd.Flags().Set("fcrepo", "off"); err != nil {
		t.Fatal(err)
	}

	prepared, restore, err := prepareCreateDecisionReview(cmd, options, false)
	if err != nil {
		t.Fatal(err)
	}
	defer restore()
	if got := prepared[0].DefaultDisposition; got != corecomponent.DispositionSuperseded {
		t.Fatalf("review default = %q, want %q", got, corecomponent.DispositionSuperseded)
	}
}

func TestPopulateRemoteCreateRequestUsesProvidedSSHValuesWithoutPrompt(t *testing.T) {
	req := &ComposeCreateRequest{
		SSHHostname: "192.0.2.10",
		SSHUser:     "root",
		SSHPort:     2222,
		SSHKeyPath:  "/tmp/sitectl-key",
	}

	err := populateRemoteCreateRequest(req, config.Context{}, func(question ...string) (string, error) {
		t.Fatalf("did not expect prompt: %v", question)
		return "", fmt.Errorf("unexpected prompt")
	}, true)
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
		"yolo":            "true",
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
