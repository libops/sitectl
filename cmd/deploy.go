package cmd

import (
	"fmt"
	"log/slog"
	"os/exec"
	"slices"
	"strings"

	"github.com/libops/sitectl/pkg/config"
	"github.com/libops/sitectl/pkg/plugin"
	"github.com/spf13/cobra"
)

var (
	deployBranch  string
	deployNoPull  bool
	deploySkipGit bool
)

var deployCmd = &cobra.Command{
	Use:   "deploy",
	Short: "Deploy the active context: pull updates and restart services",
	Long: `Deploy the active context by orchestrating a full update cycle.

The deploy sequence runs:
  1. Plugin pre-down hooks (if the context plugin registers a deploy runner)
  2. docker compose down
  3. git fetch and git checkout <branch> (unless --skip-git is set)
  4. docker compose pull (unless --no-pull is set)
  5. docker compose up -d --remove-orphans
  6. Plugin post-up hooks (if the context plugin registers a deploy runner)

The --branch flag overrides which branch is checked out during the git step.
If omitted, the repository's current branch is updated via fetch without switching.

Examples:
  sitectl deploy                         # Deploy current branch on active context
  sitectl deploy --branch main           # Switch to main and deploy
  sitectl deploy --skip-git              # Restart services without pulling git changes
  sitectl deploy --context prod          # Deploy on a specific context`,
	RunE: func(cmd *cobra.Command, args []string) error {
		contextName, err := config.ResolveCurrentContextName(cmd.Flags())
		if err != nil {
			return err
		}
		ctx, err := config.GetContext(contextName)
		if err != nil {
			return err
		}

		pluginName := strings.TrimSpace(ctx.Plugin)
		hasDeployHooks := pluginHasDeployHooks(cmd, contextName, pluginName)

		// 1. Pre-down hooks
		if hasDeployHooks {
			slog.Debug("running pre-down hooks", "context", contextName, "plugin", pluginName)
			if err := invokeDeployHook(cmd, contextName, pluginName, "pre-down"); err != nil {
				return fmt.Errorf("pre-down hook failed: %w", err)
			}
		}

		// 2. Compose down
		slog.Debug("running compose down", "context", contextName)
		if err := runContextCompose(cmd, ctx, []string{"down"}); err != nil {
			return fmt.Errorf("compose down failed: %w", err)
		}

		// 3. Git update
		if !deploySkipGit {
			slog.Debug("running git update", "context", contextName, "branch", deployBranch)
			if err := runGitUpdate(cmd, ctx, deployBranch); err != nil {
				return fmt.Errorf("git update failed: %w", err)
			}
		}

		// 4. Compose pull
		if !deployNoPull {
			slog.Debug("running compose pull", "context", contextName)
			if err := runContextCompose(cmd, ctx, []string{"pull"}); err != nil {
				return fmt.Errorf("compose pull failed: %w", err)
			}
		}

		// 5. Compose up
		slog.Debug("running compose up", "context", contextName)
		if err := runContextCompose(cmd, ctx, []string{"up", "-d", "--remove-orphans"}); err != nil {
			return fmt.Errorf("compose up failed: %w", err)
		}

		// 6. Post-up hooks
		if hasDeployHooks {
			slog.Debug("running post-up hooks", "context", contextName, "plugin", pluginName)
			if err := invokeDeployHook(cmd, contextName, pluginName, "post-up"); err != nil {
				return fmt.Errorf("post-up hook failed: %w", err)
			}
		}

		return nil
	},
}

func init() {
	deployCmd.Flags().StringVar(&deployBranch, "branch", "", "Git branch to check out during the deploy (default: update current branch)")
	deployCmd.Flags().BoolVar(&deployNoPull, "no-pull", false, "Skip docker compose pull before bringing services up")
	deployCmd.Flags().BoolVar(&deploySkipGit, "skip-git", false, "Skip the git fetch/checkout step")
	deployCmd.GroupID = "workflow"
	RootCmd.AddCommand(deployCmd)
}

// pluginHasDeployHooks checks whether the context plugin has registered deploy hooks
// using the lightweight plugin discovery metadata.
func pluginHasDeployHooks(_ *cobra.Command, _ string, pluginName string) bool {
	if pluginName == "" || pluginName == "core" {
		return false
	}
	installed, ok := plugin.FindInstalled(pluginName)
	if !ok {
		return false
	}
	return installed.CanDeploy
}

// invokeDeployHook calls __deploy <hook> on the context plugin.
func invokeDeployHook(cmd *cobra.Command, contextName, pluginName, hook string) error {
	invocation := []string{"--context", contextName, "__deploy", hook}
	_, err := pluginSDK.InvokePluginCommand(pluginName, invocation, plugin.CommandExecOptions{
		Context: cmd.Context(),
		Stdout:  cmd.OutOrStdout(),
		Stderr:  cmd.ErrOrStderr(),
	})
	return err
}

// runContextCompose runs a docker compose subcommand via the context's RunCommandContext,
// mirroring the compose.go injection of -f and --env-file flags.
func runContextCompose(cmd *cobra.Command, ctx config.Context, args []string) error {
	if ctx.DockerHostType == config.ContextLocal {
		hasProject, err := ctx.HasComposeProject()
		if err != nil {
			return fmt.Errorf("inspect compose project in %s: %w", ctx.ProjectDir, err)
		}
		if !hasProject {
			return fmt.Errorf("no compose project file found in %s", ctx.ProjectDir)
		}
		if err := ctx.EnsureTrackedComposeOverrideSymlink(); err != nil {
			return err
		}
	}

	cmdArgs := []string{"compose"}
	for _, f := range ctx.ComposeFile {
		cmdArgs = append(cmdArgs, "-f", f)
	}
	for _, e := range ctx.EnvFile {
		cmdArgs = append(cmdArgs, "--env-file", e)
	}
	// Auto-add -d --remove-orphans for up if not already present.
	if len(args) > 0 && args[0] == "up" {
		if !slices.Contains(args, "-d") && !slices.Contains(args, "--detach") {
			args = append(args, "-d", "--remove-orphans")
		}
	}
	cmdArgs = append(cmdArgs, args...)

	c := exec.Command("docker", cmdArgs...)
	c.Dir = ctx.ProjectDir
	_, err := ctx.RunCommandContext(cmd.Context(), c)
	return err
}

// runGitUpdate runs git fetch and optionally git checkout <branch> in the project dir.
func runGitUpdate(cmd *cobra.Command, ctx config.Context, branch string) error {
	fetchCmd := exec.Command("git", "fetch")
	fetchCmd.Dir = ctx.ProjectDir
	if _, err := ctx.RunCommandContext(cmd.Context(), fetchCmd); err != nil {
		return fmt.Errorf("git fetch: %w", err)
	}

	if strings.TrimSpace(branch) == "" {
		// No branch specified: pull the current branch.
		pullCmd := exec.Command("git", "pull")
		pullCmd.Dir = ctx.ProjectDir
		if _, err := ctx.RunCommandContext(cmd.Context(), pullCmd); err != nil {
			return fmt.Errorf("git pull: %w", err)
		}
		return nil
	}

	checkoutCmd := exec.Command("git", "checkout", strings.TrimSpace(branch))
	checkoutCmd.Dir = ctx.ProjectDir
	if _, err := ctx.RunCommandContext(cmd.Context(), checkoutCmd); err != nil {
		return fmt.Errorf("git checkout %s: %w", branch, err)
	}

	pullCmd := exec.Command("git", "pull")
	pullCmd.Dir = ctx.ProjectDir
	if _, err := ctx.RunCommandContext(cmd.Context(), pullCmd); err != nil {
		return fmt.Errorf("git pull: %w", err)
	}
	return nil
}
