package cmd

import (
	"fmt"
	"os"
	"os/exec"
	"slices"

	"github.com/libops/sitectl/pkg/config"
	"github.com/spf13/cobra"
)

var composeCmd = &cobra.Command{
	Use:                "compose COMMAND",
	DisableFlagParsing: true,
	Args:               cobra.ArbitraryArgs,
	Short:              "Run docker compose commands on sitectl contexts",
	Long: `Run docker compose commands on sitectl contexts.

This command wraps docker compose and automatically applies compose files, env files, and project directory
based on the current context. All docker compose commands and flags are supported.

Automatic behaviors:
  - 'compose up' automatically adds '-d --remove-orphans' if not already specified
  - Compose file paths (-f flags) are injected from context.ComposeFile setting
  - Env file paths (--env-file flags) are injected from context.EnvFile setting
  - Working directory is set to context.ProjectDir

Examples:
  sitectl compose up                    # Start containers in detached mode
  sitectl compose down                  # Stop and remove containers
  sitectl compose logs -f drupal        # Follow drupal container logs
  sitectl compose ps                    # List running containers
  sitectl compose exec -it drupal bash      # Open shell in drupal container
  sitectl compose --context prod up     # Start containers on prod context`,
	RunE: func(cmd *cobra.Command, args []string) error {
		// since we're disabling flag parsing to make easy passing of flags to docker compose
		// handle the context flag
		filteredArgs, sitectlContext, err := getContextFromArgs(cmd, args)
		if err != nil {
			return err
		}

		validCommands := []string{
			"attach",
			"build",
			"commit",
			"config",
			"cp",
			"create",
			"down",
			"events",
			"exec",
			"export",
			"images",
			"kill",
			"logs",
			"ls",
			"pause",
			"port",
			"ps",
			"pull",
			"push",
			"restart",
			"rm",
			"run",
			"scale",
			"start",
			"stats",
			"stop",
			"top",
			"unpause",
			"up",
			"version",
			"wait",
			"watch",
			"-h",
			"--help",
		}
		if len(filteredArgs) == 0 {
			return fmt.Errorf("missing docker compose command")
		}
		if !slices.Contains(validCommands, filteredArgs[0]) {
			return fmt.Errorf("unknown docker compose command: %s", filteredArgs[0])
		}

		context, err := config.GetContext(sitectlContext)
		if err != nil {
			return err
		}

		if context.DockerHostType == config.ContextLocal {
			hasComposeProject, err := context.HasComposeProject()
			if err != nil {
				return fmt.Errorf("failed to inspect compose project in %s: %w", context.ProjectDir, err)
			}
			if !hasComposeProject {
				return fmt.Errorf("no compose project file found in %s (expected one of docker-compose.yml, docker-compose.yaml, compose.yml, compose.yaml)", context.ProjectDir)
			}
			if err := context.EnsureTrackedComposeOverrideSymlink(); err != nil {
				return err
			}
		}

		// consider adding a flag to not do this
		// but this seems like a nice default for ISLE projects
		if filteredArgs[0] == "up" && !slices.Contains(filteredArgs, "-d") && !slices.Contains(filteredArgs, "--detach") {
			filteredArgs = append(filteredArgs, "-d", "--remove-orphans")
		}
		filteredArgs = context.DockerComposeSubcommandArgs(filteredArgs)
		if isComposeUpCommand(filteredArgs) {
			handled, err := maybeRunComposeReconcile(cmd, &context)
			if err != nil {
				return err
			}
			if handled {
				return nil
			}
		}

		var envValues map[string]string
		if isComposeUpCommand(filteredArgs) {
			var messages []string
			envValues, messages, err = context.PrepareComposeUpPortOverride()
			if err != nil {
				return err
			}
			for _, message := range messages {
				fmt.Fprintln(cmd.ErrOrStderr(), message)
			}
		}

		cmdArgs := []string{"compose"}
		cmdArgs = append(cmdArgs, context.DockerComposeGlobalArgsForCommand(filteredArgs[0])...)

		cmdArgs = append(cmdArgs, filteredArgs...)
		c := exec.Command("docker", cmdArgs...)
		c.Dir = context.ProjectDir
		if isComposeUpCommand(filteredArgs) {
			c.Env = config.AppendEnvOverrides(os.Environ(), envValues)
		}
		_, err = context.RunCommandContext(cmd.Context(), c)
		if err != nil {
			return err
		}

		return nil
	},
}

func isComposeUpCommand(args []string) bool {
	if len(args) == 0 {
		return false
	}
	return args[0] == "up"
}

func init() {
	composeCmd.GroupID = "ops"
	RootCmd.AddCommand(composeCmd)
}
