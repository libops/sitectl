package cmd

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"slices"

	"github.com/libops/sitectl/pkg/config"
	"github.com/libops/sitectl/pkg/helpers"
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
  - 'compose build' automatically adds '--pull' if not already specified
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
		filteredArgs, sitectlContext, err := helpers.GetContextFromArgs(cmd, args)
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
		if len(filteredArgs) == 0 || !slices.Contains(validCommands, filteredArgs[0]) {
			helpers.ExitOnError(fmt.Errorf("unknown docker compose command: %s", filteredArgs[0]))
		}

		context, err := config.GetContext(sitectlContext)
		if err != nil {
			return err
		}

		if context.DockerHostType == config.ContextLocal {
			path := filepath.Join(context.ProjectDir, "docker-compose.yml")
			_, err = os.Stat(path)
			if err != nil {
				helpers.ExitOnError(fmt.Errorf("docker-compose.yml not found at %s: %v", path, err))
			}
		}

		// consider adding a flag to not do this
		// but this seems like a nice default for ISLE projects
		if filteredArgs[0] == "up" && !slices.Contains(filteredArgs, "-d") && !slices.Contains(filteredArgs, "--detach") {
			filteredArgs = append(filteredArgs, "-d", "--remove-orphans")
		}
		if filteredArgs[0] == "build" && !slices.Contains(filteredArgs, "--pull") {
			filteredArgs = append(filteredArgs, "--pull")
		}

		cmdArgs := []string{"compose"}

		// Add compose file paths if specified
		for _, file := range context.ComposeFile {
			cmdArgs = append(cmdArgs, "-f", file)
		}

		// Add env file paths if specified
		for _, env := range context.EnvFile {
			cmdArgs = append(cmdArgs, "--env-file", env)
		}

		cmdArgs = append(cmdArgs, filteredArgs...)
		c := exec.Command("docker", cmdArgs...)
		c.Dir = context.ProjectDir
		_, err = context.RunCommand(c)
		if err != nil {
			return err
		}

		return nil
	},
}

func init() {
	RootCmd.AddCommand(composeCmd)
}
