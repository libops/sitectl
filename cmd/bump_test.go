package cmd

import (
	"encoding/json"
	"os"
	"slices"
	"strings"
	"testing"
)

func TestBumpOptionsRequireExactlyOneUpdateType(t *testing.T) {
	if _, err := (bumpOptions{}).updateType(); err == nil {
		t.Fatal("expected missing update type to fail")
	}
	if _, err := (bumpOptions{patch: true, minor: true}).updateType(); err == nil {
		t.Fatal("expected multiple update types to fail")
	}
	got, err := (bumpOptions{major: true}).updateType()
	if err != nil {
		t.Fatalf("updateType() error = %v", err)
	}
	if got != "major" {
		t.Fatalf("expected major, got %q", got)
	}
}

func TestOnOffFlagAcceptsOffValue(t *testing.T) {
	flag := onOffFlag(true)
	if err := flag.Set("off"); err != nil {
		t.Fatalf("Set(off) error = %v", err)
	}
	if bool(flag) {
		t.Fatal("expected flag to be false")
	}
	if flag.String() != "off" {
		t.Fatalf("expected off string, got %q", flag.String())
	}
}

func TestParseGitHubRepoSlug(t *testing.T) {
	for remote, want := range map[string]string{
		"https://github.com/libops/sitectl.git":    "libops/sitectl",
		"git@github.com:libops/sitectl.git":        "libops/sitectl",
		"ssh://git@github.com/libops/sitectl":      "libops/sitectl",
		"https://github.com/libops/sitectl":        "libops/sitectl",
		"https://token@github.com/libops/repo.git": "libops/repo",
	} {
		got, err := parseGitHubRepoSlug(remote)
		if err != nil {
			t.Fatalf("parseGitHubRepoSlug(%q) error = %v", remote, err)
		}
		if got != want {
			t.Fatalf("parseGitHubRepoSlug(%q) = %q, want %q", remote, got, want)
		}
	}
	if _, err := parseGitHubRepoSlug("https://gitlab.com/libops/sitectl.git"); err == nil {
		t.Fatal("expected non-GitHub remote to fail")
	}
}

func TestBuildRenovateConfigScopesAndUpdateType(t *testing.T) {
	cfg := buildRenovateConfig(
		bumpTarget{RepoSlug: "libops/sitectl"},
		"minor",
		bumpOptions{
			minor:        true,
			compose:      onOffFlag(true),
			actions:      onOffFlag(false),
			dependencies: onOffFlag(true),
		},
	)
	if cfg.Platform != "github" {
		t.Fatalf("expected github platform, got %q", cfg.Platform)
	}
	if len(cfg.Repositories) != 1 || cfg.Repositories[0] != "libops/sitectl" {
		t.Fatalf("unexpected repositories: %#v", cfg.Repositories)
	}
	if !slices.Contains(cfg.PackageRules[0].MatchUpdateTypes, "major") ||
		slices.Contains(cfg.PackageRules[0].MatchUpdateTypes, "minor") ||
		slices.Contains(cfg.PackageRules[0].MatchUpdateTypes, "patch") {
		t.Fatalf("update rule should disable everything outside minor scope: %#v", cfg.PackageRules[0].MatchUpdateTypes)
	}
	foundActionsRule := false
	for _, rule := range cfg.PackageRules {
		if slices.Contains(rule.MatchManagers, "github-actions") && rule.Enabled != nil && !*rule.Enabled {
			foundActionsRule = true
		}
	}
	if !foundActionsRule {
		t.Fatalf("expected disabled github-actions package rule: %#v", cfg.PackageRules)
	}
}

func TestAllowedUpdateTypesAreCumulative(t *testing.T) {
	tests := []struct {
		updateType string
		want       []string
	}{
		{updateType: "patch", want: []string{"patch"}},
		{updateType: "minor", want: []string{"minor", "patch"}},
		{updateType: "major", want: []string{"major", "minor", "patch"}},
	}
	for _, tt := range tests {
		t.Run(tt.updateType, func(t *testing.T) {
			got := allowedUpdateTypes(tt.updateType)
			if !slices.Equal(got, tt.want) {
				t.Fatalf("allowedUpdateTypes(%q) = %#v, want %#v", tt.updateType, got, tt.want)
			}
			disabled := disabledUpdateTypes(tt.updateType)
			for _, updateType := range tt.want {
				if slices.Contains(disabled, updateType) {
					t.Fatalf("disabledUpdateTypes(%q) disabled allowed update %q: %#v", tt.updateType, updateType, disabled)
				}
			}
		})
	}
}

func TestBuildRenovateConfigDependenciesOffLimitsManagers(t *testing.T) {
	cfg := buildRenovateConfig(
		bumpTarget{RepoSlug: "libops/sitectl"},
		"patch",
		bumpOptions{
			patch:        true,
			compose:      onOffFlag(true),
			actions:      onOffFlag(false),
			dependencies: onOffFlag(false),
		},
	)
	if len(cfg.EnabledManagers) != 1 || cfg.EnabledManagers[0] != "docker-compose" {
		t.Fatalf("expected only docker-compose enabled, got %#v", cfg.EnabledManagers)
	}
}

func TestBuildRenovateInvocationDryRunUsesLocalPlatform(t *testing.T) {
	t.Setenv("RENOVATE_TOKEN", "")
	t.Setenv("GITHUB_TOKEN", "")
	target := bumpTarget{ProjectDir: "/tmp/repo", RepoSlug: "libops/sitectl", Source: "cwd"}
	invocation, err := buildRenovateInvocation(target, "major", bumpOptions{
		major:        true,
		compose:      onOffFlag(true),
		actions:      onOffFlag(true),
		dependencies: onOffFlag(true),
		dryRun:       true,
	})
	if err != nil {
		t.Fatalf("buildRenovateInvocation() error = %v", err)
	}
	if !slices.Contains(invocation.Args, "-v") || !slices.Contains(invocation.Args, "/tmp/repo:/workspace") {
		t.Fatalf("expected dry-run invocation to mount project dir, got %#v", invocation.Args)
	}
	if !slices.Contains(invocation.Args, defaultRenovateImage) ||
		!strings.Contains(defaultRenovateImage, "@sha256:") ||
		strings.Contains(defaultRenovateImage, ":latest") {
		t.Fatalf("expected immutable Renovate image, got %q", defaultRenovateImage)
	}
	cfg := renovateConfigFromEnv(t, invocation.Env)
	if cfg.Platform != "local" || cfg.DryRun != "lookup" || len(cfg.Repositories) != 0 {
		t.Fatalf("unexpected dry-run config: %#v", cfg)
	}
}

func TestBuildRenovateInvocationRequiresTokenForGitHubRun(t *testing.T) {
	t.Setenv("RENOVATE_TOKEN", "")
	t.Setenv("GITHUB_TOKEN", "")
	_, err := buildRenovateInvocation(
		bumpTarget{ProjectDir: "/tmp/repo", RepoSlug: "libops/sitectl"},
		"major",
		bumpOptions{major: true, compose: onOffFlag(true), actions: onOffFlag(true), dependencies: onOffFlag(true)},
	)
	if err == nil || !strings.Contains(err.Error(), "RENOVATE_TOKEN") {
		t.Fatalf("expected token error, got %v", err)
	}
}

func TestBuildRenovateInvocationMapsGitHubToken(t *testing.T) {
	t.Setenv("RENOVATE_TOKEN", "")
	t.Setenv("GITHUB_TOKEN", "secret")
	invocation, err := buildRenovateInvocation(
		bumpTarget{ProjectDir: "/tmp/repo", RepoSlug: "libops/sitectl"},
		"major",
		bumpOptions{major: true, compose: onOffFlag(true), actions: onOffFlag(true), dependencies: onOffFlag(true)},
	)
	if err != nil {
		t.Fatalf("buildRenovateInvocation() error = %v", err)
	}
	if !slices.Contains(invocation.Env, "RENOVATE_TOKEN=secret") {
		t.Fatalf("expected RENOVATE_TOKEN to be mapped from GITHUB_TOKEN, got %#v", scrubSecrets(invocation.Env))
	}
}

func renovateConfigFromEnv(t *testing.T, env []string) renovateConfig {
	t.Helper()
	for _, item := range env {
		if value, ok := strings.CutPrefix(item, "RENOVATE_CONFIG="); ok {
			var cfg renovateConfig
			if err := json.Unmarshal([]byte(value), &cfg); err != nil {
				t.Fatalf("unmarshal RENOVATE_CONFIG: %v", err)
			}
			return cfg
		}
	}
	t.Fatalf("RENOVATE_CONFIG not found in env %#v", env)
	return renovateConfig{}
}

func scrubSecrets(values []string) []string {
	out := append([]string{}, values...)
	for i, value := range out {
		if strings.HasPrefix(value, "RENOVATE_TOKEN=") || strings.HasPrefix(value, "GITHUB_COM_TOKEN=") {
			out[i] = strings.SplitN(value, "=", 2)[0] + "=***"
		}
	}
	return out
}

func TestMain(m *testing.M) {
	os.Exit(m.Run())
}
