package config

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

type InputFunc func(question ...string) (string, error)

type LocalContextCreateOptions struct {
	Name             string
	DefaultName      string
	ProjectDir       string
	DefaultProjectDir string
	ProjectName      string
	DefaultProjectName string
	DockerSocket     string
	SetDefault       bool
	ConfirmOverwrite bool
	Input            InputFunc
}

func PromptAndSaveLocalContext(opts LocalContextCreateOptions) (*Context, error) {
	input := opts.Input
	if input == nil {
		input = GetInput
	}

	existing, err := GetContext(opts.Name)
	if err != nil {
		return nil, err
	}

	name, err := resolveLocalContextName(existing, opts, input)
	if err != nil {
		return nil, err
	}
	if name == "" {
		return nil, fmt.Errorf("context name cannot be empty")
	}

	existing, err = GetContext(name)
	if err != nil {
		return nil, err
	}
	if existing.DockerSocket != "" && opts.ConfirmOverwrite {
		overwrite, err := input("The context already exists. Do you want to overwrite it? [y/N]: ")
		if err != nil {
			return nil, err
		}
		if !isAffirmative(overwrite) {
			return nil, fmt.Errorf("context creation cancelled")
		}
	}

	projectDir, err := resolveLocalProjectDir(existing, opts, input)
	if err != nil {
		return nil, err
	}
	if projectDir == "" {
		return nil, fmt.Errorf("project directory cannot be empty")
	}

	projectName := firstNonEmpty(opts.ProjectName, existing.ProjectName, opts.DefaultProjectName, "docker-compose")
	dockerSocket := GetDefaultLocalDockerSocket(firstNonEmpty(opts.DockerSocket, existing.DockerSocket, "/var/run/docker.sock"))

	ctx := &Context{
		Name:           name,
		DockerHostType: ContextLocal,
		DockerSocket:   dockerSocket,
		ProjectName:    projectName,
		ProjectDir:     projectDir,
	}

	if err := SaveContext(ctx, opts.SetDefault); err != nil {
		return nil, err
	}

	return ctx, nil
}

func resolveLocalContextName(existing Context, opts LocalContextCreateOptions, input InputFunc) (string, error) {
	if strings.TrimSpace(opts.Name) != "" {
		return strings.TrimSpace(opts.Name), nil
	}

	defaultName, err := nextAvailableContextName(firstNonEmpty(existing.Name, opts.DefaultName))
	if err != nil {
		return "", err
	}
	value, err := input(fmt.Sprintf("Context name [%s]: ", defaultName))
	if err != nil {
		return "", err
	}
	value = strings.TrimSpace(value)
	if value == "" {
		return defaultName, nil
	}
	return value, nil
}

func nextAvailableContextName(base string) (string, error) {
	base = strings.TrimSpace(base)
	if base == "" {
		return "", nil
	}

	exists, err := ContextExists(base)
	if err != nil {
		return "", err
	}
	if !exists {
		return base, nil
	}

	for i := 2; ; i++ {
		candidate := fmt.Sprintf("%s-%d", base, i)
		exists, err := ContextExists(candidate)
		if err != nil {
			return "", err
		}
		if !exists {
			return candidate, nil
		}
	}
}

func resolveLocalProjectDir(existing Context, opts LocalContextCreateOptions, input InputFunc) (string, error) {
	if strings.TrimSpace(opts.ProjectDir) != "" {
		return expandAndCleanProjectDir(strings.TrimSpace(opts.ProjectDir))
	}

	cwd, err := os.Getwd()
	if err != nil {
		return "", err
	}
	defaultDir := firstNonEmpty(existing.ProjectDir, opts.DefaultProjectDir, cwd)
	value, err := input(fmt.Sprintf("Full directory path to the project (directory where docker-compose.yml is located) [%s]: ", defaultDir))
	if err != nil {
		return "", err
	}
	value = strings.TrimSpace(value)
	if value == "" {
		return expandAndCleanProjectDir(defaultDir)
	}
	return expandAndCleanProjectDir(value)
}

func expandAndCleanProjectDir(value string) (string, error) {
	value = strings.TrimSpace(value)
	if value == "" {
		return "", nil
	}
	if value == "~" {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", fmt.Errorf("resolve home directory: %w", err)
		}
		return filepath.Clean(home), nil
	}
	if strings.HasPrefix(value, "~/") {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", fmt.Errorf("resolve home directory: %w", err)
		}
		value = filepath.Join(home, strings.TrimPrefix(value, "~/"))
	}
	return filepath.Clean(value), nil
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return value
		}
	}
	return ""
}

func isAffirmative(value string) bool {
	value = strings.TrimSpace(strings.ToLower(value))
	return value == "y" || value == "yes"
}
