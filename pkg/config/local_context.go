package config

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

type InputFunc func(question ...string) (string, error)

type LocalContextCreateOptions struct {
	Name                string
	DefaultName         string
	Site                string
	DefaultSite         string
	Plugin              string
	DefaultPlugin       string
	ProjectDir          string
	DefaultProjectDir   string
	ProjectName         string
	DefaultProjectName  string
	Environment         string
	DockerSocket        string
	SetDefault          bool
	ConfirmOverwrite    bool
	Input               InputFunc
	ProjectDirValidator func(string) error
	ContextNamePrompt   []string
	ProjectDirPrompt    []string
	OverwritePrompt     []string
}

func PromptAndSaveLocalContext(opts LocalContextCreateOptions) (*Context, error) {
	input := opts.Input
	if input == nil {
		input = GetInput
	}

	existing, err := GetContext(opts.Name)
	if err != nil {
		if !errors.Is(err, ErrContextNotFound) {
			return nil, err
		}
		existing = Context{}
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
		if !errors.Is(err, ErrContextNotFound) {
			return nil, err
		}
		existing = Context{}
	}
	if existing.DockerSocket != "" && opts.ConfirmOverwrite {
		prompt := opts.OverwritePrompt
		if len(prompt) == 0 {
			prompt = []string{"The context already exists. Do you want to overwrite it? [y/N]: "}
		}
		overwrite, err := input(prompt...)
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
	site := firstNonEmpty(opts.Site, existing.Site, opts.DefaultSite, projectName, name)
	plugin := firstNonEmpty(opts.Plugin, existing.Plugin, opts.DefaultPlugin, "core")
	environment := firstNonEmpty(opts.Environment, existing.Environment, "local")
	dockerSocket := GetDefaultLocalDockerSocket(firstNonEmpty(opts.DockerSocket, existing.DockerSocket, "/var/run/docker.sock"))

	ctx := &Context{
		Name:           name,
		Site:           site,
		Plugin:         plugin,
		DockerHostType: ContextLocal,
		Environment:    environment,
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

	baseName := firstNonEmpty(existing.Name, opts.DefaultName)
	if strings.TrimSpace(baseName) == "" {
		return "", fmt.Errorf("context name cannot be empty")
	}

	exists, err := ContextExists(baseName)
	if err != nil {
		return "", err
	}
	if !exists {
		return baseName, nil
	}

	defaultName, err := nextAvailableContextName(baseName)
	if err != nil {
		return "", err
	}
	prompt := opts.ContextNamePrompt
	if len(prompt) == 0 {
		prompt = []string{fmt.Sprintf("Context name [%s]: ", defaultName)}
	} else {
		prompt = append(append([]string{}, prompt[:len(prompt)-1]...), fmt.Sprintf(prompt[len(prompt)-1], defaultName))
	}
	value, err := input(prompt...)
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
		projectDir, err := expandAndCleanProjectDir(strings.TrimSpace(opts.ProjectDir))
		if err != nil {
			return "", err
		}
		if err := localProjectDirValidator(opts)(projectDir); err != nil {
			return "", err
		}
		return projectDir, nil
	}

	cwd, err := os.Getwd()
	if err != nil {
		return "", err
	}
	defaultDir := firstNonEmpty(existing.ProjectDir, opts.DefaultProjectDir, cwd)
	prompt := opts.ProjectDirPrompt
	if len(prompt) == 0 {
		prompt = []string{fmt.Sprintf("Full directory path to the project (directory where docker-compose.yml is located) [%s]: ", defaultDir)}
	} else {
		prompt = append(append([]string{}, prompt[:len(prompt)-1]...), fmt.Sprintf(prompt[len(prompt)-1], defaultDir))
	}
	for {
		value, err := input(prompt...)
		if err != nil {
			return "", err
		}
		value = strings.TrimSpace(value)

		candidate := defaultDir
		if value != "" {
			candidate = value
		}

		projectDir, err := expandAndCleanProjectDir(candidate)
		if err != nil {
			return "", err
		}
		if err := localProjectDirValidator(opts)(projectDir); err != nil {
			prompt = append([]string{fmt.Sprintf("Directory validation failed: %v", err), ""}, prompt...)
			continue
		}
		return projectDir, nil
	}
}

func localProjectDirValidator(opts LocalContextCreateOptions) func(string) error {
	if opts.ProjectDirValidator != nil {
		return opts.ProjectDirValidator
	}
	return validateLocalProjectDir
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

func validateLocalProjectDir(projectDir string) error {
	info, err := os.Stat(projectDir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return fmt.Errorf("stat project directory %q: %w", projectDir, err)
	}
	if !info.IsDir() {
		return fmt.Errorf("project directory %q exists and is not a directory", projectDir)
	}

	entries, err := os.ReadDir(projectDir)
	if err != nil {
		return fmt.Errorf("read project directory %q: %w", projectDir, err)
	}
	if len(entries) > 0 {
		return fmt.Errorf("project directory %q must not exist or must be empty", projectDir)
	}
	return nil
}

func ValidateExistingComposeProjectDir(projectDir string) error {
	info, err := os.Stat(projectDir)
	if err != nil {
		if os.IsNotExist(err) {
			return fmt.Errorf("project directory %q does not exist", projectDir)
		}
		return fmt.Errorf("stat project directory %q: %w", projectDir, err)
	}
	if !info.IsDir() {
		return fmt.Errorf("project directory %q exists and is not a directory", projectDir)
	}
	if !LooksLikeComposeProject(projectDir) {
		return fmt.Errorf("project directory %q does not look like a docker compose project", projectDir)
	}
	return nil
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
