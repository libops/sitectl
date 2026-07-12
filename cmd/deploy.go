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
	deployRef     string
	deployNoPull  bool
	deploySkipGit bool
)

var deployCmd = &cobra.Command{
	Use:   "deploy",
	Short: "Deploy the active context: pull updates and restart services",
	Long: `Deploy the active context by orchestrating a full update cycle.

The deploy sequence runs:
  1. git pull --ff-only for the current upstream branch (unless --skip-git is set)
  2. Pull images and build application images while the current site is still
     running (explicit pulls are skipped when --no-pull is set)
  3. Plugin pre-down hooks (if the context plugin registers a deploy runner)
  4. docker compose down --remove-orphans
  5. The plugin's remaining application-aware rollout commands when declared;
     otherwise docker compose up -d --remove-orphans
  6. Plugin post-up hooks (if the context plugin registers a deploy runner)

The --branch flag fetches and checks out a named remote branch before a
fast-forward merge. The --ref flag fetches an exact remote ref (including a
pull-request ref or advertised commit) and checks it out detached without
rewriting a local branch. If both are omitted, sitectl updates the current
branch when it has a git upstream.

Examples:
  sitectl deploy                         # Deploy the current upstream branch
  sitectl deploy --branch main           # Switch to main and deploy
  sitectl deploy --ref refs/pull/123/head # Deploy an exact pull-request ref
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
		return runDeployCycle(cmd, contextName, ctx, pluginName, hasDeployHooks, deployCycleOptions{
			Branch:  deployBranch,
			Ref:     deployRef,
			NoPull:  deployNoPull,
			SkipGit: deploySkipGit,
		})
	},
}

type deployCycleOptions struct {
	Branch  string
	Ref     string
	NoPull  bool
	SkipGit bool
}

var (
	deployRunGitUpdate      = runGitUpdate
	deployRunGitRefUpdate   = runGitRefUpdate
	deployRunContextCompose = runContextCompose
	deployRunHook           = invokeDeployHook
	deployResolveRollout    = pluginComposeRollout
)

func runDeployCycle(cmd *cobra.Command, contextName string, ctx config.Context, pluginName string, hasDeployHooks bool, opts deployCycleOptions) error {
	// Update the checkout while the healthy site is still online. A fetch,
	// checkout, or pull failure therefore cannot turn an update failure into an
	// outage.
	if !opts.SkipGit {
		slog.Debug("running git update", "context", contextName, "branch", strings.TrimSpace(opts.Branch), "ref", strings.TrimSpace(opts.Ref))
		var err error
		if strings.TrimSpace(opts.Ref) != "" {
			err = deployRunGitRefUpdate(cmd, ctx, opts.Ref)
		} else {
			err = deployRunGitUpdate(cmd, ctx, opts.Branch)
		}
		if err != nil {
			return fmt.Errorf("git update failed: %w", err)
		}
	}

	// Resolve the rollout before stopping a healthy site. Plugin discovery and
	// metadata errors are deployment validation failures, not reasons to create
	// an outage.
	rolloutCommands, hasRollout, err := deployResolveRollout(pluginName)
	if err != nil {
		return fmt.Errorf("resolve compose rollout failed: %w", err)
	}
	preparationCommands, rolloutCommands := splitLeadingComposePreparationCommands(rolloutCommands)

	// Pull and build while the healthy site is still online. Registry,
	// connectivity, missing-image, and build failures must not turn an update
	// failure into an outage. The rollout runner still honors --no-pull while
	// allowing build preparation to run.
	if hasRollout {
		if len(preparationCommands) > 0 {
			slog.Debug("running plugin compose preparation", "context", contextName, "plugin", pluginName)
			if err := deployRunComposeRollout(cmd, &ctx, preparationCommands, opts.NoPull); err != nil {
				return fmt.Errorf("compose preparation failed: %w", err)
			}
		}
	} else if !opts.NoPull {
		slog.Debug("running compose pull preflight", "context", contextName)
		if err := deployRunContextCompose(cmd, ctx, []string{"pull"}); err != nil {
			return fmt.Errorf("compose pull preflight failed: %w", err)
		}
	}

	if hasDeployHooks {
		slog.Debug("running pre-down hooks", "context", contextName, "plugin", pluginName)
		if err := deployRunHook(cmd, contextName, pluginName, "pre-down"); err != nil {
			return fmt.Errorf("pre-down hook failed: %w", err)
		}
	}

	slog.Debug("running compose down", "context", contextName)
	if err := deployRunContextCompose(cmd, ctx, []string{"down", "--remove-orphans"}); err != nil {
		return fmt.Errorf("compose down failed: %w", err)
	}

	if hasRollout {
		slog.Debug("running plugin compose rollout", "context", contextName, "plugin", pluginName)
		if err := deployRunComposeRollout(cmd, &ctx, rolloutCommands, opts.NoPull); err != nil {
			return fmt.Errorf("compose rollout failed: %w", err)
		}
	} else {
		slog.Debug("running compose up", "context", contextName)
		if err := deployRunContextCompose(cmd, ctx, []string{"up", "-d", "--remove-orphans"}); err != nil {
			return fmt.Errorf("compose up failed: %w", err)
		}
	}

	if hasDeployHooks {
		slog.Debug("running post-up hooks", "context", contextName, "plugin", pluginName)
		if err := deployRunHook(cmd, contextName, pluginName, "post-up"); err != nil {
			return fmt.Errorf("post-up hook failed: %w", err)
		}
	}
	return nil
}

func init() {
	deployCmd.Flags().StringVar(&deployBranch, "branch", "", "Git branch to check out during the deploy (default: current branch)")
	deployCmd.Flags().StringVar(&deployRef, "ref", "", "Exact remote Git ref or advertised commit to fetch and deploy detached")
	deployCmd.MarkFlagsMutuallyExclusive("branch", "ref")
	deployCmd.Flags().BoolVar(&deployNoPull, "no-pull", false, "Skip explicit docker compose pull steps (build --pull is unaffected)")
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
		if shouldAutoReconcileComposeUp(args) {
			handled, err := maybeRunComposeReconcile(cmd, &ctx)
			if err != nil {
				return err
			}
			if handled {
				return nil
			}
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

func runGitRefUpdate(cmd *cobra.Command, ctx config.Context, ref string) error {
	command := ctx.GitSyncRefShellCommand(ref)
	syncCmd := exec.Command("bash", "-lc", command) // #nosec G204 -- command text is assembled from context values using shell quoting.
	syncCmd.Dir = ctx.ProjectDir
	_, err := ctx.RunCommandContext(cmd.Context(), syncCmd)
	return err
}
