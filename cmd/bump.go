package cmd

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/libops/sitectl/pkg/config"
	"github.com/spf13/cobra"
	"github.com/spf13/pflag"
)

const defaultRenovateImage = "renovate/renovate:latest"

var bumpExecCommandContext = exec.CommandContext

type bumpOptions struct {
	patch        bool
	minor        bool
	major        bool
	compose      onOffFlag
	actions      onOffFlag
	dependencies onOffFlag
	dryRun       bool
}

type bumpTarget struct {
	ProjectDir string
	RepoSlug   string
	Source     string
}

type renovateInvocation struct {
	Args []string
	Env  []string
}

type renovateConfig struct {
	Extends             []string              `json:"extends,omitempty"`
	Platform            string                `json:"platform,omitempty"`
	Repositories        []string              `json:"repositories,omitempty"`
	RequireConfig       string                `json:"requireConfig,omitempty"`
	Onboarding          bool                  `json:"onboarding"`
	DependencyDashboard bool                  `json:"dependencyDashboard"`
	BranchPrefix        string                `json:"branchPrefix,omitempty"`
	SeparateMajorMinor  bool                  `json:"separateMajorMinor"`
	SeparateMinorPatch  bool                  `json:"separateMinorPatch"`
	EnabledManagers     []string              `json:"enabledManagers,omitempty"`
	PackageRules        []renovatePackageRule `json:"packageRules,omitempty"`
	DryRun              string                `json:"dryRun,omitempty"`
}

type renovatePackageRule struct {
	Description      string   `json:"description,omitempty"`
	MatchUpdateTypes []string `json:"matchUpdateTypes,omitempty"`
	MatchManagers    []string `json:"matchManagers,omitempty"`
	Enabled          *bool    `json:"enabled,omitempty"`
}

type onOffFlag bool

var _ pflag.Value = (*onOffFlag)(nil)

var bumpUpdateTypes = []string{
	"major",
	"minor",
	"patch",
	"pin",
	"pinDigest",
	"digest",
	"lockFileMaintenance",
	"rollback",
	"bump",
	"replacement",
}

func newBumpCommand() *cobra.Command {
	opts := bumpOptions{
		compose:      onOffFlag(true),
		actions:      onOffFlag(true),
		dependencies: onOffFlag(true),
	}
	cmd := &cobra.Command{
		Use:   "bump (--patch|--minor|--major)",
		Short: "Run Renovate for targeted dependency bumps",
		Long: `Run Renovate in Docker for the selected repository.

With --context, sitectl uses that local context's project directory. Without
--context, sitectl uses the current working directory. Dry runs analyze the
local checkout and print Renovate's candidate updates. Non-dry runs infer the
GitHub repository from origin and create or update Renovate branches and PRs.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			return runBump(cmd, opts)
		},
	}
	cmd.Flags().BoolVar(&opts.patch, "patch", false, "Allow patch updates only.")
	cmd.Flags().BoolVar(&opts.minor, "minor", false, "Allow minor updates only.")
	cmd.Flags().BoolVar(&opts.major, "major", false, "Allow major updates only.")
	cmd.Flags().Var(&opts.compose, "compose", "Update Docker Compose dependencies: on or off.")
	cmd.Flags().Var(&opts.actions, "actions", "Update GitHub Actions dependencies: on or off.")
	cmd.Flags().Var(&opts.dependencies, "dependencies", "Update application dependencies: on or off.")
	cmd.Flags().BoolVar(&opts.dryRun, "dry-run", false, "Print Renovate's candidate updates without creating branches or PRs.")
	cmd.GroupID = "workflow"
	return cmd
}

func init() {
	RootCmd.AddCommand(newBumpCommand())
}

func runBump(cmd *cobra.Command, opts bumpOptions) error {
	updateType, err := opts.updateType()
	if err != nil {
		return err
	}
	if !bool(opts.compose) && !bool(opts.actions) && !bool(opts.dependencies) {
		return fmt.Errorf("at least one of --compose, --actions, or --dependencies must be on")
	}

	target, err := resolveBumpTarget(cmd)
	if err != nil {
		return err
	}
	if !opts.dryRun && strings.TrimSpace(target.RepoSlug) == "" {
		return fmt.Errorf("unable to infer GitHub repository from %s; set an origin remote like https://github.com/OWNER/REPO.git", target.ProjectDir)
	}

	invocation, err := buildRenovateInvocation(target, updateType, opts)
	if err != nil {
		return err
	}

	out := cmd.OutOrStdout()
	if opts.dryRun {
		fmt.Fprintf(out, "Dry-run Renovate %s bump for %s (%s)\n", updateType, target.ProjectDir, target.Source)
	} else {
		fmt.Fprintf(out, "Running Renovate %s bump for %s (%s)\n", updateType, target.RepoSlug, target.Source)
	}
	fmt.Fprintf(out, "Scopes: compose=%s actions=%s dependencies=%s\n", opts.compose.String(), opts.actions.String(), opts.dependencies.String())

	return runRenovateDocker(cmd.Context(), invocation, cmd.OutOrStdout(), cmd.ErrOrStderr())
}

func (o bumpOptions) updateType() (string, error) {
	selected := []string{}
	if o.patch {
		selected = append(selected, "patch")
	}
	if o.minor {
		selected = append(selected, "minor")
	}
	if o.major {
		selected = append(selected, "major")
	}
	if len(selected) != 1 {
		return "", fmt.Errorf("choose exactly one of --patch, --minor, or --major")
	}
	return selected[0], nil
}

func resolveBumpTarget(cmd *cobra.Command) (bumpTarget, error) {
	flags := commandContextFlags(cmd)
	if flags != nil && flags.Lookup("context") != nil && flags.Changed("context") {
		contextName, err := resolveContextName(cmd)
		if err != nil {
			return bumpTarget{}, err
		}
		ctx, err := config.GetContext(contextName)
		if err != nil {
			return bumpTarget{}, err
		}
		if ctx.DockerHostType != config.ContextLocal {
			return bumpTarget{}, fmt.Errorf("bump currently requires a local context; context %q is %q", ctx.Name, ctx.DockerHostType)
		}
		projectDir := strings.TrimSpace(ctx.ProjectDir)
		if projectDir == "" {
			return bumpTarget{}, fmt.Errorf("context %q does not define a project directory", ctx.Name)
		}
		slug, _ := inferGitHubRepoSlug(cmd.Context(), projectDir)
		return bumpTarget{ProjectDir: projectDir, RepoSlug: slug, Source: "context " + ctx.Name}, nil
	}

	wd, err := os.Getwd()
	if err != nil {
		return bumpTarget{}, err
	}
	wd, err = filepath.Abs(wd)
	if err != nil {
		return bumpTarget{}, err
	}
	slug, _ := inferGitHubRepoSlug(cmd.Context(), wd)
	return bumpTarget{ProjectDir: wd, RepoSlug: slug, Source: "cwd"}, nil
}

func buildRenovateInvocation(target bumpTarget, updateType string, opts bumpOptions) (renovateInvocation, error) {
	cfg := buildRenovateConfig(target, updateType, opts)
	data, err := json.Marshal(cfg)
	if err != nil {
		return renovateInvocation{}, err
	}

	args := []string{"run", "--rm"}
	if opts.dryRun {
		args = append(args,
			"-v", target.ProjectDir+":/workspace",
			"-w", "/workspace",
		)
	}
	args = append(args,
		"-e", "RENOVATE_CONFIG",
		"-e", "RENOVATE_TOKEN",
		"-e", "GITHUB_COM_TOKEN",
		"-e", "LOG_LEVEL",
		defaultRenovateImage,
	)

	env := []string{"RENOVATE_CONFIG=" + string(data)}
	if !opts.dryRun && os.Getenv("RENOVATE_TOKEN") == "" && os.Getenv("GITHUB_TOKEN") == "" {
		return renovateInvocation{}, fmt.Errorf("RENOVATE_TOKEN or GITHUB_TOKEN must be set to run Renovate against GitHub")
	}
	if os.Getenv("RENOVATE_TOKEN") == "" {
		if token := os.Getenv("GITHUB_TOKEN"); token != "" {
			env = append(env, "RENOVATE_TOKEN="+token)
		}
	}
	if os.Getenv("GITHUB_COM_TOKEN") == "" {
		if token := firstNonEmpty(os.Getenv("RENOVATE_TOKEN"), os.Getenv("GITHUB_TOKEN")); token != "" {
			env = append(env, "GITHUB_COM_TOKEN="+token)
		}
	}
	return renovateInvocation{Args: args, Env: env}, nil
}

func buildRenovateConfig(target bumpTarget, updateType string, opts bumpOptions) renovateConfig {
	cfg := renovateConfig{
		Extends:             []string{"config:recommended"},
		Platform:            "github",
		Repositories:        []string{target.RepoSlug},
		RequireConfig:       "optional",
		Onboarding:          false,
		DependencyDashboard: false,
		BranchPrefix:        "sitectl-bump/" + updateType + "/",
		SeparateMajorMinor:  true,
		SeparateMinorPatch:  true,
	}
	if opts.dryRun {
		cfg.Platform = "local"
		cfg.Repositories = nil
		cfg.DryRun = "lookup"
	}

	disabled := false
	nonTargetTypes := otherUpdateTypes(updateType)
	if len(nonTargetTypes) > 0 {
		cfg.PackageRules = append(cfg.PackageRules, renovatePackageRule{
			Description:      "sitectl bump: disable non-" + updateType + " updates",
			MatchUpdateTypes: nonTargetTypes,
			Enabled:          &disabled,
		})
	}

	if !bool(opts.dependencies) {
		enabledManagers := []string{}
		if bool(opts.compose) {
			enabledManagers = append(enabledManagers, "docker-compose")
		}
		if bool(opts.actions) {
			enabledManagers = append(enabledManagers, "github-actions")
		}
		cfg.EnabledManagers = enabledManagers
		return cfg
	}
	if !bool(opts.compose) {
		cfg.PackageRules = append(cfg.PackageRules, renovatePackageRule{
			Description:   "sitectl bump: disable Docker Compose updates",
			MatchManagers: []string{"docker-compose"},
			Enabled:       &disabled,
		})
	}
	if !bool(opts.actions) {
		cfg.PackageRules = append(cfg.PackageRules, renovatePackageRule{
			Description:   "sitectl bump: disable GitHub Actions updates",
			MatchManagers: []string{"github-actions"},
			Enabled:       &disabled,
		})
	}
	return cfg
}

func runRenovateDocker(ctx context.Context, invocation renovateInvocation, stdout, stderr io.Writer) error {
	command := bumpExecCommandContext(ctx, "docker", invocation.Args...) // #nosec G204 -- docker args are built by sitectl from fixed Renovate options and a caller-selected project path.
	command.Env = append(os.Environ(), invocation.Env...)
	command.Stdout = stdout
	command.Stderr = stderr
	return command.Run()
}

func inferGitHubRepoSlug(ctx context.Context, projectDir string) (string, error) {
	out, err := bumpExecCommandContext(ctx, "git", "-C", projectDir, "remote", "get-url", "origin").Output() // #nosec G204 -- git args are fixed and projectDir is passed as a single argument.
	if err != nil {
		return "", err
	}
	return parseGitHubRepoSlug(strings.TrimSpace(string(out)))
}

func parseGitHubRepoSlug(remote string) (string, error) {
	remote = strings.TrimSpace(remote)
	if remote == "" {
		return "", fmt.Errorf("empty git remote")
	}
	if strings.HasPrefix(remote, "git@github.com:") {
		return cleanGitHubSlug(strings.TrimPrefix(remote, "git@github.com:"))
	}
	parsed, err := url.Parse(remote)
	if err == nil && strings.EqualFold(parsed.Hostname(), "github.com") {
		return cleanGitHubSlug(strings.TrimPrefix(parsed.Path, "/"))
	}
	return "", fmt.Errorf("remote %q is not a GitHub repository", remote)
}

func cleanGitHubSlug(path string) (string, error) {
	path = strings.Trim(strings.TrimSpace(path), "/")
	path = strings.TrimSuffix(path, ".git")
	parts := strings.Split(path, "/")
	if len(parts) != 2 || parts[0] == "" || parts[1] == "" {
		return "", fmt.Errorf("invalid GitHub repository path %q", path)
	}
	return parts[0] + "/" + parts[1], nil
}

func otherUpdateTypes(updateType string) []string {
	types := make([]string, 0, len(bumpUpdateTypes)-1)
	for _, candidate := range bumpUpdateTypes {
		if candidate != updateType {
			types = append(types, candidate)
		}
	}
	return types
}

func (f *onOffFlag) Set(value string) error {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "on", "true", "1", "yes", "y":
		*f = true
		return nil
	case "off", "false", "0", "no", "n":
		*f = false
		return nil
	default:
		return fmt.Errorf("expected on or off, got %q", value)
	}
}

func (f *onOffFlag) Type() string {
	return "on|off"
}

func (f *onOffFlag) String() string {
	if f == nil || !bool(*f) {
		return "off"
	}
	return "on"
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if value != "" {
			return value
		}
	}
	return ""
}
