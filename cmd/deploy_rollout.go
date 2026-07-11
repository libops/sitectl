package cmd

import (
	"fmt"
	"os"
	"os/exec"
	"strings"

	"github.com/libops/sitectl/pkg/config"
	"github.com/spf13/cobra"
)

var deployRunComposeRollout = runDeployComposeRollout

func pluginComposeRollout(pluginName string) ([]string, bool, error) {
	pluginName = strings.TrimSpace(pluginName)
	if pluginName == "" || pluginName == "core" {
		return nil, false, nil
	}
	installed, err := installedPluginWithMetadata(pluginName)
	if err != nil {
		return nil, false, err
	}
	if len(installed.CreateDefinitions) == 0 {
		return nil, false, nil
	}
	spec := installed.CreateDefinitions[0]
	for _, candidate := range installed.CreateDefinitions {
		if candidate.Default {
			spec = candidate
			break
		}
	}
	if len(spec.DockerComposeRollout) == 0 {
		return nil, false, nil
	}
	return append([]string{}, spec.DockerComposeRollout...), true, nil
}

func runDeployComposeRollout(cmd *cobra.Command, ctx *config.Context, commands []string, noPull bool) error {
	for _, commandText := range commands {
		commandText = strings.TrimSpace(commandText)
		if commandText == "" || (noPull && isDockerComposeSubcommand(commandText, "pull")) {
			continue
		}

		commandText = ctx.DockerComposeShellCommand(commandText)
		fmt.Fprintf(cmd.OutOrStdout(), "Running %s\n", commandText)
		command := exec.Command("bash", "-lc", commandText) // #nosec G204 -- commands come from trusted plugin create metadata.
		command.Dir = ctx.ProjectDir
		if isDockerComposeSubcommand(commandText, "up") {
			envValues, messages, err := ctx.PrepareComposeUpPortOverride()
			if err != nil {
				return err
			}
			for _, message := range messages {
				fmt.Fprintln(cmd.ErrOrStderr(), message)
			}
			command.Env = config.AppendEnvOverrides(os.Environ(), envValues)
		}
		if _, err := ctx.RunCommandContext(cmd.Context(), command); err != nil {
			return fmt.Errorf("run %s: %w", commandText, err)
		}
	}
	return nil
}

// splitLeadingComposePreparationCommands separates the pull-and-build
// preparation prefix from commands that require the deployment outage window.
// Blank entries in the prefix are ignored because the rollout runner ignores
// them as well.
func splitLeadingComposePreparationCommands(commands []string) ([]string, []string) {
	var preparation []string
	index := 0
	for index < len(commands) {
		command := strings.TrimSpace(commands[index])
		if command == "" {
			index++
			continue
		}
		if !isDockerComposePreparationCommand(command) {
			break
		}
		preparation = append(preparation, commands[index])
		index++
	}
	return preparation, append([]string{}, commands[index:]...)
}

// isDockerComposePreparationCommand accepts shell lists only when every
// command in the list is a Compose pull or build. This keeps a compound
// build-and-up or build-and-migrate command inside the outage window.
func isDockerComposePreparationCommand(command string) bool {
	command = strings.ReplaceAll(command, "&&", "||")
	for _, command := range strings.Split(command, "||") {
		command = strings.TrimSpace(command)
		if command == "" || strings.Contains(command, "$(") || strings.ContainsAny(command, "\r\n;&|`") {
			return false
		}
		if !isDockerComposeSubcommand(command, "pull") && !isDockerComposeSubcommand(command, "build") {
			return false
		}
	}
	return true
}

// isDockerComposeSubcommand classifies a command by the first Compose
// subcommand. This deliberately does not treat `docker compose build --pull`
// as a pull command when deploy --no-pull is set.
func isDockerComposeSubcommand(command, want string) bool {
	fields := strings.Fields(strings.TrimSpace(command))
	if len(fields) < 3 || fields[0] != "docker" || fields[1] != "compose" {
		return false
	}
	for index := 2; index < len(fields); index++ {
		field := fields[index]
		if !strings.HasPrefix(field, "-") {
			return field == want
		}
		if composeGlobalOptionTakesValue(field) && !strings.Contains(field, "=") {
			index++
		}
	}
	return false
}

func composeGlobalOptionTakesValue(option string) bool {
	switch option {
	case "--ansi", "--env-file", "-f", "--file", "--parallel", "--profile", "--progress", "--project-directory", "-p", "--project-name":
		return true
	default:
		return false
	}
}
