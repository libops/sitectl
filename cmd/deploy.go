package cmd

import (
	"fmt"
	"log/slog"
	"os"
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
  3. git pull --ff-only for the current upstream branch (unless --skip-git is set)
  4. docker compose pull (unless --no-pull is set)
  5. docker compose up -d --remove-orphans
  6. Plugin post-up hooks (if the context plugin registers a deploy runner)

The --branch flag checks out a branch before the git pull step.
If omitted, sitectl updates the current branch when it has a git upstream.

Examples:
  sitectl deploy                         # Deploy the current upstream branch
  sitectl deploy --branch main           # Switch to main and deploy
  sitectl deploy --skip-git              # Restart services without pulling git changes
  sitectl deploy --context prod          # Deploy on a specific context`,
	RunE: func(cmd *cobra.Command, args []string) error {
		contextName, err := resolveContextName(cmd)
		if err != nil {
			return err
		}
		ctx, err := config.GetContext(contextName)
		if err != nil {
			return err
		}

		pluginName := strings.TrimSpace(ctx.Plugin)
		hasDeployHooks, err := pluginHasDeployHooks(cmd, contextName, pluginName)
		if err != nil {
			return err
		}

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
			slog.Debug("running git update", "context", contextName, "branch", strings.TrimSpace(deployBranch))
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
	deployCmd.Flags().StringVar(&deployBranch, "branch", "", "Git branch to check out during the deploy (default: current branch)")
	deployCmd.Flags().BoolVar(&deployNoPull, "no-pull", false, "Skip docker compose pull before bringing services up")
	deployCmd.Flags().BoolVar(&deploySkipGit, "skip-git", false, "Skip the git fetch/checkout step")
	deployCmd.GroupID = "workflow"
	RootCmd.AddCommand(deployCmd)
}

// pluginHasDeployHooks checks whether the context plugin has registered deploy hooks
// using the lightweight plugin discovery metadata.
func pluginHasDeployHooks(_ *cobra.Command, _ string, pluginName string) (bool, error) {
	if pluginName == "" || pluginName == "core" {
		return false, nil
	}
	installed, err := installedPluginWithMetadata(pluginName)
	if err != nil {
		return false, err
	}
	return installed.CanDeploy, nil
}

// invokeDeployHook calls the deploy hook on the context plugin over RPC.
func invokeDeployHook(cmd *cobra.Command, contextName, pluginName, hook string) error {
	req, err := plugin.NewDeployRunRequest(hook)
	if err != nil {
		return err
	}
	req.Context = contextName
	resp, err := pluginSDK.InvokePluginRPC(pluginName, req, plugin.CommandExecOptions{
		Context:    cmd.Context(),
		Stderr:     cmd.ErrOrStderr(),
		LiveStderr: true,
	})
	if strings.TrimSpace(resp.Output) != "" {
		if _, printErr := fmt.Fprint(cmd.OutOrStdout(), resp.Output); printErr != nil && err == nil {
			err = printErr
		}
	}
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
	if len(args) > 0 && args[0] == "up" {
		envValues, messages, err := ctx.PrepareComposeUpPortOverride()
		if err != nil {
			return err
		}
		for _, message := range messages {
			fmt.Fprintln(cmd.ErrOrStderr(), message)
		}
		c.Env = config.AppendEnvOverrides(os.Environ(), envValues)
	}
	_, err := ctx.RunCommandContext(cmd.Context(), c)
	return err
}

// runGitUpdate fast-forwards the checkout from its configured upstream branch.
func runGitUpdate(cmd *cobra.Command, ctx config.Context, branch string) error {
	command := ctx.GitSyncShellCommand(branch)
	syncCmd := exec.Command("bash", "-lc", command) // #nosec G204 -- command text is assembled from context values using shell quoting.
	syncCmd.Dir = ctx.ProjectDir
	_, err := ctx.RunCommandContext(cmd.Context(), syncCmd)
	return err
}
