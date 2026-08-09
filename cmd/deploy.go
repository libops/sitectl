package cmd

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"slices"
	"strings"
	"time"

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

The deploy sequence validates the complete rollout before changing the checkout.
Production services must run image-backed application code: a successful Git
checkout necessarily changes files immediately for services that bind-mount the
project source. After validation, sitectl runs:
  1. A fast-forward-only Git sync, or a verified exact-ref checkout, unless
     --skip-git is set
  2. Pull images and build application images while the current site is still
     running (explicit pulls are skipped when --no-pull is set)
  3. Plugin pre-down hooks (if the context plugin registers a deploy runner)
  4. docker compose down --remove-orphans
  5. The plugin's remaining application-aware rollout commands when declared;
     otherwise docker compose up -d --remove-orphans
  6. Plugin post-up hooks (if the context plugin registers a deploy runner)

If a failure occurs after shutdown begins, sitectl makes one bounded direct
Docker Compose start and readiness check from the current checkout while the
project mutation lock is still held. Recovery does not roll back Git or
application data.

The --branch flag fetches a named remote branch, proves an existing local target
is its ancestor, and then checks out the fetched commit. The --ref flag fetches
an exact remote ref (including a pull-request ref or advertised commit) and
checks it out detached without rewriting a local branch. If both are omitted,
sitectl updates the current branch when it has a git upstream.

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
	deployRunGitUpdate       = runGitUpdate
	deployRunGitRefUpdate    = runGitRefUpdate
	deployRunContextCompose  = runContextCompose
	deployRunRecoveryCompose = runContextComposeDirect
	deployRunHook            = invokeDeployHook
	deployResolveRollout     = pluginComposeRollout
	deployValidateContext    = validateDeployContextPrerequisites
	deployAcquireProjectLock = func(runCtx context.Context, ctx *config.Context) (*config.ProjectMutationLock, error) {
		return ctx.AcquireProjectMutationLock(runCtx)
	}
)

const (
	deployRecoveryTimeout     = 10 * time.Minute
	deployRecoveryWaitTimeout = 9 * time.Minute
)

func runDeployCycle(cmd *cobra.Command, contextName string, ctx config.Context, pluginName string, hasDeployHooks bool, opts deployCycleOptions) (returnErr error) {
	lock, err := deployAcquireProjectLock(cmd.Context(), &ctx)
	if err != nil {
		return fmt.Errorf("acquire project mutation lock: %w", err)
	}
	originalContext := cmd.Context()
	cmd.SetContext(lock.Context())
	defer func() {
		cmd.SetContext(originalContext)
		returnErr = errors.Join(returnErr, lock.Release())
	}()
	if err := deployValidateContext(&ctx); err != nil {
		return fmt.Errorf("validate deploy context: %w", err)
	}

	// Resolve and validate every rollout entry before Git can change code that a
	// running service might have bind-mounted from the checkout.
	rolloutCommands, hasRollout, err := deployResolveRollout(pluginName)
	if err != nil {
		return fmt.Errorf("resolve compose rollout failed: %w", err)
	}
	if hasRollout {
		if err := validateDeployComposeRollout(&ctx, rolloutCommands); err != nil {
			return err
		}
	}
	preparationCommands, rolloutCommands := splitLeadingComposePreparationCommands(rolloutCommands)
	if hasRollout {
		if err := validateDeployRolloutFinalStart(&ctx, rolloutCommands); err != nil {
			return err
		}
	}

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
		// Git can replace a previously validated checked-in lifecycle script.
		// Revalidate the complete rollout against the fetched checkout before
		// preparation or an outage can begin.
		if hasRollout {
			if err := validateDeployComposeRollout(&ctx, rolloutCommands); err != nil {
				return fmt.Errorf("validate fetched compose rollout: %w", err)
			}
		}
	}

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
		return recoverDeployAfterOutage(cmd, contextName, ctx, fmt.Errorf("compose down failed: %w", err))
	}

	if hasRollout {
		slog.Debug("running plugin compose rollout", "context", contextName, "plugin", pluginName)
		if err := deployRunComposeRollout(cmd, &ctx, rolloutCommands, opts.NoPull); err != nil {
			return recoverDeployAfterOutage(cmd, contextName, ctx, fmt.Errorf("compose rollout failed: %w", err))
		}
	} else {
		slog.Debug("running compose up", "context", contextName)
		if err := deployRunContextCompose(cmd, ctx, []string{"up", "-d", "--remove-orphans"}); err != nil {
			return recoverDeployAfterOutage(cmd, contextName, ctx, fmt.Errorf("compose up failed: %w", err))
		}
	}

	if hasDeployHooks {
		slog.Debug("running post-up hooks", "context", contextName, "plugin", pluginName)
		if err := deployRunHook(cmd, contextName, pluginName, "post-up"); err != nil {
			return recoverDeployAfterOutage(cmd, contextName, ctx, fmt.Errorf("post-up hook failed: %w", err))
		}
	}
	return nil
}

// recoverDeployAfterOutage makes one direct, bounded start and readiness
// attempt before the caller releases the project mutation lock. WithoutCancel
// preserves the lock marker after an interrupted deploy.
func recoverDeployAfterOutage(cmd *cobra.Command, contextName string, ctx config.Context, deployErr error) error {
	recoveryContext, cancel := context.WithTimeout(context.WithoutCancel(cmd.Context()), deployRecoveryTimeout)
	defer cancel()
	recoveryCommand := *cmd
	recoveryCommand.SetContext(recoveryContext)

	slog.Warn("deploy failed after shutdown began; attempting bounded direct Compose start and readiness check", "context", contextName, "timeout", deployRecoveryTimeout)
	waitTimeoutSeconds := fmt.Sprintf("%d", deployRecoveryWaitTimeout/time.Second)
	if recoveryErr := deployRunRecoveryCompose(&recoveryCommand, ctx, []string{"up", "-d", "--remove-orphans", "--wait", "--wait-timeout", waitTimeoutSeconds}); recoveryErr != nil {
		return errors.Join(
			deployErr,
			fmt.Errorf("bounded direct Compose recovery start or readiness check failed; no automatic Git or application-data rollback was attempted: %w", recoveryErr),
		)
	}
	if _, err := fmt.Fprintln(cmd.ErrOrStderr(), "sitectl: deploy failed after shutdown began; a bounded direct Docker Compose start and readiness check completed from the current checkout. No automatic Git or application-data rollback was attempted."); err != nil {
		return errors.Join(deployErr, fmt.Errorf("report deploy recovery result: %w", err))
	}
	return deployErr
}

func validateDeployContextPrerequisites(ctx *config.Context) error {
	if ctx == nil || strings.TrimSpace(ctx.ProjectDir) == "" {
		return fmt.Errorf("project directory is required")
	}
	hasProject, err := ctx.HasComposeProject()
	if err != nil {
		return fmt.Errorf("inspect Compose project: %w", err)
	}
	if !hasProject {
		return fmt.Errorf("no Compose project file found in %s", ctx.ProjectDir)
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
	return runContextComposeMode(cmd, ctx, args, true)
}

// runContextComposeDirect executes exactly one Compose operation without
// invoking plugin reconciliation. Deploy recovery uses this path because a
// failed rollout must not recursively enter another lifecycle workflow.
func runContextComposeDirect(cmd *cobra.Command, ctx config.Context, args []string) error {
	return runContextComposeMode(cmd, ctx, args, false)
}

func runContextComposeMode(cmd *cobra.Command, ctx config.Context, args []string, allowReconcile bool) error {
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

	// Auto-add -d --remove-orphans for up if not already present.
	if len(args) > 0 && args[0] == "up" {
		if !slices.Contains(args, "-d") && !slices.Contains(args, "--detach") {
			args = append(args, "-d", "--remove-orphans")
		}
		if allowReconcile && shouldAutoReconcileComposeUp(args) {
			handled, err := maybeRunComposeReconcile(cmd, &ctx)
			if err != nil {
				return err
			}
			if handled {
				return nil
			}
		}
	}
	commandName := ""
	if len(args) > 0 {
		commandName = args[0]
	}
	cmdArgs := []string{"compose"}
	cmdArgs = append(cmdArgs, ctx.DockerComposeGlobalArgsForCommand(commandName)...)
	args = ctx.DockerComposeSubcommandArgs(args)
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
	return ctx.SyncGitCheckout(cmd.Context(), cmd.OutOrStdout(), branch)
}

func runGitRefUpdate(cmd *cobra.Command, ctx config.Context, ref string) error {
	return ctx.SyncGitRefCheckout(cmd.Context(), cmd.OutOrStdout(), ref)
}
