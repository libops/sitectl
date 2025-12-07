package cmd

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"slices"

	"github.com/libops/sitectl/internal/utils"
	"github.com/libops/sitectl/pkg/config"
	"github.com/spf13/cobra"
)

var composeCmd = &cobra.Command{
	Use:                "compose [command...]",
	DisableFlagParsing: true,
	Args:               cobra.ArbitraryArgs,
	Short:              "Run docker compose commands",
	Long: `Run docker compose commands

This command wraps docker compose and automatically applies the correct profile and project directory
based on the current context. All docker compose commands and flags are supported.

Automatic behaviors:
  - 'compose up' automatically adds '-d --remove-orphans' if not already specified
  - 'compose build' automatically adds '--pull' if not already specified

Examples:
  sitectl compose up                    # Start containers in detached mode
  sitectl compose down                  # Stop and remove containers
  sitectl compose logs -f nginx         # Follow nginx container logs
  sitectl compose ps                    # List running containers
  sitectl compose exec -it nginx bash   # Open shell in nginx container
  sitectl compose --context prod up     # Start containers on prod context`,
	RunE: func(cmd *cobra.Command, args []string) error {
		// since we're disabling flag parsing to make easy passing of flags to docker compose
		// handle the context flag
		filteredArgs, siteCtx, err := utils.GetContextFromArgs(cmd, args)
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
			utils.ExitOnError(fmt.Errorf("unknown docker compose command: %s", filteredArgs[0]))
		}

		context, err := config.GetContext(siteCtx)
		if err != nil {
			return err
		}

		if context.DockerHostType == config.ContextLocal {
			path := filepath.Join(context.ProjectDir, "docker-compose.yml")
			_, err = os.Stat(path)
			if err != nil {
				utils.ExitOnError(fmt.Errorf("docker-compose.yml not found at %s: %v", path, err))
			}
		}

		// consider adding a flag to not do this
		// but this seems like a nice default for compose projects
		if filteredArgs[0] == "up" && !slices.Contains(filteredArgs, "-d") && !slices.Contains(filteredArgs, "--detach") {
			filteredArgs = append(filteredArgs, "-d", "--remove-orphans")
		}
		if filteredArgs[0] == "build" && !slices.Contains(filteredArgs, "--pull") {
			filteredArgs = append(filteredArgs, "--pull")
		}

		cmdArgs := []string{
			"compose",
			"--profile",
			context.Profile,
		}

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
