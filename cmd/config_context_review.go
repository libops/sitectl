package cmd

import (
	"errors"
	"fmt"
	"os"
	"os/user"
	"path/filepath"
	"strconv"
	"strings"

	corecomponent "github.com/libops/sitectl/pkg/component"
	"github.com/libops/sitectl/pkg/config"
	"github.com/libops/sitectl/pkg/helpers"
	"github.com/spf13/cobra"
)

func reviewSetContext(cmd *cobra.Command, name string, input config.InputFunc) (*config.Context, bool, error) {
	if input == nil {
		input = config.GetInput
	}
	existing, err := config.GetContext(name)
	if err != nil {
		if !errors.Is(err, config.ErrContextNotFound) {
			return nil, false, err
		}
		existing = config.Context{}
	}
	yolo, err := cmd.Flags().GetBool("yolo")
	if err != nil {
		return nil, false, fmt.Errorf("get yolo flag: %w", err)
	}

	cwd, err := os.Getwd()
	if err != nil {
		return nil, false, fmt.Errorf("resolve current directory: %w", err)
	}
	reviewed := existing
	reviewed.Name = strings.TrimSpace(name)
	reviewed.ProjectDir, err = contextStringDecision(cmd, input, "project-dir", "Working path", "sitectl derives the site, environment, Compose identity, file locations, and target assumptions from this directory. Enter another path now, or press Ctrl-C, change directory, and run set-context again.", helpers.FirstNonEmpty(existing.ProjectDir, cwd), true, yolo)
	if err != nil {
		return nil, false, err
	}
	legacyProjectName, err := contextLegacyProjectName(cmd)
	if err != nil {
		return nil, false, err
	}

	targetDefault, err := contextFlagString(cmd, "type", string(existing.DockerHostType), string(config.ContextLocal))
	if err != nil {
		return nil, false, err
	}
	if targetDefault != string(config.ContextLocal) && targetDefault != string(config.ContextRemote) {
		return nil, false, fmt.Errorf("unknown context type %q", targetDefault)
	}
	if !yolo {
		targetDefault, err = corecomponent.PromptChoice("target machine", []corecomponent.Choice{
			{Value: string(config.ContextLocal), Label: "local", Help: "Send Docker operations to the socket on this machine."},
			{Value: string(config.ContextRemote), Label: "remote", Help: "Send Docker and filesystem operations to another machine over SSH."},
		}, targetDefault, contextComponentInput(input), strings.Split(corecomponent.RenderSection("Target machine", "This selects the machine whose containers, volumes, networks, and project files later commands can change."), "\n")...)
		if err != nil {
			return nil, false, err
		}
	}
	reviewed.DockerHostType = config.ContextType(targetDefault)

	pathBase := filepath.Base(filepath.Clean(reviewed.ProjectDir))
	reviewed.Site, err = contextStringDecision(cmd, input, "site", "Site identity", "Use the same site value for local, staging, and production contexts that represent one logical application site.", helpers.FirstNonEmpty(legacyProjectName, existing.Site, pathBase), true, yolo)
	if err != nil {
		return nil, false, err
	}
	environmentDefault := "local"
	if reviewed.DockerHostType == config.ContextRemote {
		environmentDefault = "remote"
	}
	reviewed.Environment, err = contextStringDecision(cmd, input, "environment", "Environment", "This label distinguishes deployments of the same site, such as local, staging, or prod.", helpers.FirstNonEmpty(existing.Environment, environmentDefault), true, yolo)
	if err != nil {
		return nil, false, err
	}
	reviewed.Plugin, err = contextStringDecision(cmd, input, "plugin", "Application owner", "The selected plugin supplies application-specific lifecycle, validation, and service behavior for this context.", helpers.FirstNonEmpty(existing.Plugin, "core"), true, yolo)
	if err != nil {
		return nil, false, err
	}
	detectedComposeProjectName := ""
	if reviewed.DockerHostType == config.ContextLocal {
		detectedComposeProjectName = config.DetectComposeProjectName(reviewed.ProjectDir)
	}
	reviewed.ComposeProjectName, err = contextStringDecision(cmd, input, "compose-project-name", "Compose project identity", "Docker Compose uses this value to label containers, volumes, and networks. Changing it targets a different Compose project.", helpers.FirstNonEmpty(legacyProjectName, existing.ComposeProjectName, detectedComposeProjectName, pathBase), true, yolo)
	if err != nil {
		return nil, false, err
	}
	detectedComposeNetwork := ""
	if reviewed.DockerHostType == config.ContextLocal {
		detectedComposeNetwork = config.DetectComposeNetworkName(reviewed.ProjectDir, reviewed.ComposeProjectName)
	}
	reviewed.ComposeNetwork, err = contextStringDecision(cmd, input, "compose-network", "Compose network", "sitectl uses this network to resolve and connect to services in the selected stack.", helpers.FirstNonEmpty(existing.ComposeNetwork, detectedComposeNetwork, reviewed.ComposeProjectName+"_default"), true, yolo)
	if err != nil {
		return nil, false, err
	}
	detectedDockerSocket := ""
	if reviewed.DockerHostType == config.ContextLocal {
		detectedDockerSocket = config.GetDefaultLocalDockerSocket("/var/run/docker.sock")
	}
	reviewed.DockerSocket, err = contextStringDecision(cmd, input, "docker-socket", "Docker socket", "This Unix socket is the Docker API endpoint on the target machine.", helpers.FirstNonEmpty(existing.DockerSocket, detectedDockerSocket, "/var/run/docker.sock"), true, yolo)
	if err != nil {
		return nil, false, err
	}

	reviewed.ComposeFile, err = contextStringListDecision(cmd, input, "compose-file", "Compose files", "These files are passed to Compose in order. Enter 'auto' to let Compose discover its standard file.", existing.ComposeFile, "auto", yolo)
	if err != nil {
		return nil, false, err
	}
	reviewed.EnvFile, err = contextStringListDecision(cmd, input, "env-file", "Environment files", "These files are passed to Compose in order and can change image tags, ports, and application configuration. Enter 'none' to pass no explicit env file.", existing.EnvFile, "none", yolo)
	if err != nil {
		return nil, false, err
	}

	reviewed.DatabaseService, err = contextStringDecision(cmd, input, "database-service", "Database service", "Shared database jobs execute inside this Compose service.", helpers.FirstNonEmpty(existing.DatabaseService, "mariadb"), true, yolo)
	if err != nil {
		return nil, false, err
	}
	reviewed.DatabaseUser, err = contextStringDecision(cmd, input, "database-user", "Database account", "Shared backup, restore, and sync jobs connect as this database user; the password is referenced separately.", helpers.FirstNonEmpty(existing.DatabaseUser, "root"), true, yolo)
	if err != nil {
		return nil, false, err
	}
	reviewed.DatabasePasswordSecret, err = contextStringDecision(cmd, input, "database-password-secret", "Database password secret", "This is the Compose secret name containing the password. The context stores only the reference.", helpers.FirstNonEmpty(existing.DatabasePasswordSecret, "DB_ROOT_PASSWORD"), true, yolo)
	if err != nil {
		return nil, false, err
	}
	reviewed.DatabaseName, err = contextStringDecision(cmd, input, "database-name", "Default database", "Shared database jobs operate on this schema unless a command selects another one.", helpers.FirstNonEmpty(existing.DatabaseName, "drupal_default"), true, yolo)
	if err != nil {
		return nil, false, err
	}

	if reviewed.DockerHostType == config.ContextRemote {
		if err := reviewSetContextSSH(cmd, &reviewed, existing, input, yolo); err != nil {
			return nil, false, err
		}
	} else {
		reviewed.SSHHostname = ""
		reviewed.SSHUser = ""
		reviewed.SSHPort = 0
		reviewed.SSHKeyPath = ""
	}

	cfg, err := config.Load()
	if err != nil {
		return nil, false, err
	}
	firstContext := strings.TrimSpace(cfg.CurrentContext) == ""
	setDefaultFallback := firstContext || strings.EqualFold(cfg.CurrentContext, reviewed.Name)
	defaultImplication := "When enabled, commands without --context and without a directory match operate on this environment."
	if firstContext {
		defaultImplication += " Because no fallback exists yet, the first saved context must become the default."
	}
	setDefault, err := contextBoolDecision(cmd, input, "default", "Default context", defaultImplication, setDefaultFallback, yolo)
	if err != nil {
		return nil, false, err
	}
	if firstContext && !setDefault {
		if yolo {
			setDefault = true
		} else {
			return nil, false, fmt.Errorf("the first saved context must be the default context")
		}
	}
	if !yolo {
		defaultSummary := "The current default context will remain unchanged."
		if setDefault {
			defaultSummary = "This context will be the default when no context or directory match is selected."
		}
		summary := fmt.Sprintf("Save context %q for %s on the %s target. Compose project %q will be operated as site %q, environment %q. %s", reviewed.Name, reviewed.ProjectDir, reviewed.DockerHostType, reviewed.ComposeProjectName, reviewed.Site, reviewed.Environment, defaultSummary)
		confirmed, confirmErr := corecomponent.PromptChoice("save context", []corecomponent.Choice{
			{Value: "yes", Label: "yes", Help: "Write this reviewed connection record to the sitectl config."},
			{Value: "no", Label: "no", Help: "Leave the sitectl config unchanged."},
		}, "yes", contextComponentInput(input), strings.Split(corecomponent.RenderSection("Review context", summary), "\n")...)
		if confirmErr != nil {
			return nil, false, confirmErr
		}
		if confirmed != "yes" {
			return nil, false, fmt.Errorf("context creation cancelled")
		}
	}
	return &reviewed, setDefault, nil
}

func reviewSetContextSSH(cmd *cobra.Command, reviewed *config.Context, existing config.Context, input config.InputFunc, yolo bool) error {
	var err error
	reviewed.SSHHostname, err = contextStringDecision(cmd, input, "ssh-hostname", "SSH hostname", "This host receives all remote Docker and filesystem operations for the context.", existing.SSHHostname, true, yolo)
	if err != nil {
		return err
	}
	currentUser := "root"
	if account, userErr := user.Current(); userErr == nil {
		currentUser = account.Username
	}
	reviewed.SSHUser, err = contextStringDecision(cmd, input, "ssh-user", "SSH account", "Remote commands and project-file changes run with this account's permissions.", helpers.FirstNonEmpty(existing.SSHUser, currentUser), true, yolo)
	if err != nil {
		return err
	}
	reviewed.SSHPort, err = contextUintDecision(cmd, input, "ssh-port", "SSH port", "This TCP port is used to establish the encrypted remote transport.", firstNonZero(existing.SSHPort, 22), yolo)
	if err != nil {
		return err
	}
	defaultKey := filepath.Join(os.Getenv("HOME"), ".ssh", "id_rsa")
	reviewed.SSHKeyPath, err = contextStringDecision(cmd, input, "ssh-key", "SSH identity", "This private-key file authenticates sitectl; its contents are never stored in the context.", helpers.FirstNonEmpty(existing.SSHKeyPath, defaultKey), true, yolo)
	return err
}

func contextStringDecision(cmd *cobra.Command, input config.InputFunc, flagName, title, implication, fallback string, required, yolo bool) (string, error) {
	value, err := contextFlagString(cmd, flagName, fallback)
	if err != nil {
		return "", err
	}
	if yolo {
		if required && strings.TrimSpace(value) == "" {
			return "", fmt.Errorf("--%s requires a value when --yolo is used", flagName)
		}
		return strings.TrimSpace(value), nil
	}
	prompt := strings.ReplaceAll(flagName, "-", " ") + ": "
	if strings.TrimSpace(value) != "" {
		prompt = fmt.Sprintf("%s [%s]: ", strings.ReplaceAll(flagName, "-", " "), value)
	}
	answer, err := input(append(strings.Split(corecomponent.RenderSection(title, implication), "\n"), "", corecomponent.RenderPromptLine(prompt))...)
	if err != nil {
		return "", err
	}
	if strings.TrimSpace(answer) == "" {
		answer = value
	}
	answer = strings.TrimSpace(answer)
	if required && answer == "" {
		return "", fmt.Errorf("%s cannot be empty", strings.ReplaceAll(flagName, "-", " "))
	}
	return answer, nil
}

func contextFlagString(cmd *cobra.Command, flagName string, fallbacks ...string) (string, error) {
	if flag := cmd.Flags().Lookup(flagName); flag != nil && flag.Changed {
		value, err := cmd.Flags().GetString(flagName)
		if err != nil {
			return "", fmt.Errorf("get %s flag: %w", flagName, err)
		}
		return strings.TrimSpace(value), nil
	}
	return helpers.FirstNonEmpty(fallbacks...), nil
}

func contextLegacyProjectName(cmd *cobra.Command) (string, error) {
	flag := cmd.Flags().Lookup("project-name")
	if flag == nil || !flag.Changed {
		return "", nil
	}
	value, err := cmd.Flags().GetString("project-name")
	if err != nil {
		return "", fmt.Errorf("get project-name flag: %w", err)
	}
	return strings.TrimSpace(value), nil
}

func contextStringListDecision(cmd *cobra.Command, input config.InputFunc, flagName, title, implication string, fallback []string, emptyWord string, yolo bool) ([]string, error) {
	values := append([]string{}, fallback...)
	if flag := cmd.Flags().Lookup(flagName); flag != nil && flag.Changed {
		var err error
		values, err = cmd.Flags().GetStringSlice(flagName)
		if err != nil {
			return nil, fmt.Errorf("get %s flag: %w", flagName, err)
		}
	}
	if yolo {
		return cleanContextList(values), nil
	}
	defaultValue := strings.Join(values, ", ")
	if defaultValue == "" {
		defaultValue = emptyWord
	}
	answer, err := input(append(strings.Split(corecomponent.RenderSection(title, implication), "\n"), "", corecomponent.RenderPromptLine(fmt.Sprintf("%s [%s]: ", strings.ReplaceAll(flagName, "-", " "), defaultValue)))...)
	if err != nil {
		return nil, err
	}
	answer = strings.TrimSpace(answer)
	if answer == "" {
		return cleanContextList(values), nil
	}
	if strings.EqualFold(answer, emptyWord) {
		return nil, nil
	}
	return cleanContextList(strings.Split(answer, ",")), nil
}

func cleanContextList(values []string) []string {
	cleaned := make([]string, 0, len(values))
	for _, value := range values {
		if value = strings.TrimSpace(value); value != "" {
			cleaned = append(cleaned, value)
		}
	}
	return cleaned
}

func contextUintDecision(cmd *cobra.Command, input config.InputFunc, flagName, title, implication string, fallback uint, yolo bool) (uint, error) {
	value := fallback
	if flag := cmd.Flags().Lookup(flagName); flag != nil && flag.Changed {
		var err error
		value, err = cmd.Flags().GetUint(flagName)
		if err != nil {
			return 0, fmt.Errorf("get %s flag: %w", flagName, err)
		}
	}
	if yolo {
		if value == 0 {
			return 0, fmt.Errorf("--%s requires a non-zero value when --yolo is used", flagName)
		}
		return value, nil
	}
	answer, err := input(append(strings.Split(corecomponent.RenderSection(title, implication), "\n"), "", corecomponent.RenderPromptLine(fmt.Sprintf("%s [%d]: ", strings.ReplaceAll(flagName, "-", " "), value)))...)
	if err != nil {
		return 0, err
	}
	if strings.TrimSpace(answer) == "" {
		return value, nil
	}
	parsed, err := strconv.ParseUint(strings.TrimSpace(answer), 10, 16)
	if err != nil || parsed == 0 {
		return 0, fmt.Errorf("invalid %s %q", strings.ReplaceAll(flagName, "-", " "), answer)
	}
	return uint(parsed), nil
}

func contextBoolDecision(cmd *cobra.Command, input config.InputFunc, flagName, title, implication string, fallback, yolo bool) (bool, error) {
	value := fallback
	if flag := cmd.Flags().Lookup(flagName); flag != nil && flag.Changed {
		var err error
		value, err = cmd.Flags().GetBool(flagName)
		if err != nil {
			return false, fmt.Errorf("get %s flag: %w", flagName, err)
		}
	}
	if yolo {
		return value, nil
	}
	defaultValue := "no"
	if value {
		defaultValue = "yes"
	}
	selected, err := corecomponent.PromptChoice(flagName, []corecomponent.Choice{
		{Value: "yes", Label: "yes", Help: "Use this context as the fallback target."},
		{Value: "no", Label: "no", Help: "Keep the current fallback target unchanged."},
	}, defaultValue, contextComponentInput(input), strings.Split(corecomponent.RenderSection(title, implication), "\n")...)
	return selected == "yes", err
}

func contextComponentInput(input config.InputFunc) corecomponent.InputFunc {
	return func(question ...string) (string, error) { return input(question...) }
}

func firstNonZero(values ...uint) uint {
	for _, value := range values {
		if value != 0 {
			return value
		}
	}
	return 0
}
