package cmd

import (
	"fmt"
	"strings"

	"github.com/kballard/go-shellquote"
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

		if err := runLifecycleCommandList(cmd, ctx, commandText, nil, true); err != nil {
			return err
		}
	}
	return nil
}

func validateDeployComposeRollout(ctx *config.Context, commands []string) error {
	for _, commandText := range commands {
		commandText = strings.TrimSpace(commandText)
		if commandText == "" {
			continue
		}
		_, plans, err := planLifecycleCommandList(ctx, commandText)
		if err != nil {
			return fmt.Errorf("validate compose rollout command %q: %w", commandText, err)
		}
		if err := validateLifecycleProjectScripts(ctx, commandText, plans); err != nil {
			return err
		}
	}
	return nil
}

// validateDeployRolloutFinalStart rejects plugin rollout metadata that could
// finish without a Compose project start. Preparation-only and
// conditional final commands must fail before Git or runtime state changes.
func validateDeployRolloutFinalStart(ctx *config.Context, commands []string) error {
	finalCommand := ""
	for _, commandText := range commands {
		if strings.TrimSpace(commandText) != "" {
			finalCommand = strings.TrimSpace(commandText)
		}
	}
	if finalCommand == "" {
		return fmt.Errorf("plugin compose rollout must include an unconditional final start after preparation")
	}

	list, plans, err := planLifecycleCommandList(ctx, finalCommand)
	if err != nil {
		return fmt.Errorf("validate final compose rollout command %q: %w", finalCommand, err)
	}
	if len(list.commands) != 1 || len(plans) != 1 || !plans[0].composeUp {
		return fmt.Errorf("plugin compose rollout must end with one unconditional start command; final command %q does not", finalCommand)
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
	fields, err := shellquote.Split(strings.TrimSpace(command))
	if err != nil {
		return false
	}
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
