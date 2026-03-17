package validate

import (
	"fmt"
	"path/filepath"
	"strings"

	"github.com/libops/sitectl/pkg/config"
)

func CoreValidators(cfg *config.Config) []Validator {
	return []Validator{
		requiredFieldsValidator,
		crossContextValidator(cfg),
		projectDirValidator,
		composeProjectValidator,
		contextFilesValidator,
		overrideSymlinkValidator,
		dockerAccessValidator,
	}
}

func requiredFieldsValidator(ctx *config.Context) ([]Result, error) {
	if ctx == nil {
		return []Result{{Name: "context", Status: StatusFailed, Detail: "context is nil"}}, nil
	}

	results := []Result{
		requiredStringResult("site", ctx.Site),
		requiredStringResult("plugin", ctx.Plugin),
		requiredStringResult("environment", ctx.Environment),
		requiredStringResult("type", string(ctx.DockerHostType)),
		requiredStringResult("project-dir", ctx.ProjectDir),
		requiredStringResult("project-name", ctx.ProjectName),
		requiredStringResult("compose-project-name", ctx.EffectiveComposeProjectName()),
		requiredStringResult("compose-network", ctx.EffectiveComposeNetwork()),
	}
	if ctx.DockerHostType == config.ContextRemote {
		results = append(results,
			requiredStringResult("ssh-hostname", ctx.SSHHostname),
			requiredStringResult("ssh-user", ctx.SSHUser),
			requiredUintResult("ssh-port", ctx.SSHPort),
			requiredStringResult("ssh-key", ctx.SSHKeyPath),
			requiredStringResult("docker-socket", ctx.DockerSocket),
		)
	} else {
		results = append(results, requiredStringResult("docker-socket", ctx.DockerSocket))
	}
	return results, nil
}

func crossContextValidator(cfg *config.Config) Validator {
	return func(ctx *config.Context) ([]Result, error) {
		if ctx == nil || cfg == nil {
			return nil, nil
		}
		results := []Result{}

		siteEnvMatches := 0
		localProjectDirMatches := 0
		for _, candidate := range cfg.Contexts {
			if strings.EqualFold(candidate.Site, ctx.Site) && strings.EqualFold(candidate.Environment, ctx.Environment) {
				siteEnvMatches++
			}
			if ctx.DockerHostType == config.ContextLocal &&
				candidate.DockerHostType == config.ContextLocal &&
				samePath(candidate.ProjectDir, ctx.ProjectDir) {
				localProjectDirMatches++
			}
		}

		if siteEnvMatches > 1 {
			results = append(results, Result{
				Name:   "site-environment-uniqueness",
				Status: StatusFailed,
				Detail: fmt.Sprintf("multiple contexts share site=%q environment=%q", ctx.Site, ctx.Environment),
			})
		} else {
			results = append(results, Result{Name: "site-environment-uniqueness", Status: StatusOK})
		}

		if ctx.DockerHostType == config.ContextLocal {
			if localProjectDirMatches > 1 {
				results = append(results, Result{
					Name:   "local-project-dir-uniqueness",
					Status: StatusFailed,
					Detail: fmt.Sprintf("multiple local contexts point at %q", ctx.ProjectDir),
				})
			} else {
				results = append(results, Result{Name: "local-project-dir-uniqueness", Status: StatusOK})
			}
		}
		return results, nil
	}
}

func projectDirValidator(ctx *config.Context) ([]Result, error) {
	if ctx == nil {
		return nil, nil
	}
	exists, err := ctx.ProjectDirExists()
	if err != nil {
		return []Result{{Name: "project-directory", Status: StatusFailed, Detail: err.Error()}}, nil
	}
	if !exists {
		return []Result{{Name: "project-directory", Status: StatusFailed, Detail: fmt.Sprintf("project directory %q does not exist", ctx.ProjectDir)}}, nil
	}
	return []Result{{Name: "project-directory", Status: StatusOK}}, nil
}

func composeProjectValidator(ctx *config.Context) ([]Result, error) {
	if ctx == nil {
		return nil, nil
	}
	ok, err := ctx.HasComposeProject()
	if err != nil {
		return []Result{{Name: "compose-project", Status: StatusFailed, Detail: err.Error()}}, nil
	}
	if !ok {
		return []Result{{Name: "compose-project", Status: StatusFailed, Detail: "no docker compose file found in project directory"}}, nil
	}
	return []Result{{Name: "compose-project", Status: StatusOK}}, nil
}

func contextFilesValidator(ctx *config.Context) ([]Result, error) {
	if ctx == nil {
		return nil, nil
	}
	results := []Result{}
	for _, file := range ctx.ComposeFile {
		exists, err := ctx.FileExists(ctx.ResolveProjectPath(file))
		if err != nil {
			results = append(results, Result{Name: "compose-file", Status: StatusFailed, Detail: err.Error()})
			continue
		}
		if !exists {
			results = append(results, Result{Name: "compose-file", Status: StatusFailed, Detail: fmt.Sprintf("%s not found", file)})
			continue
		}
		results = append(results, Result{Name: "compose-file", Status: StatusOK, Detail: file})
	}
	for _, file := range ctx.EnvFile {
		exists, err := ctx.FileExists(ctx.ResolveProjectPath(file))
		if err != nil {
			results = append(results, Result{Name: "env-file", Status: StatusFailed, Detail: err.Error()})
			continue
		}
		if !exists {
			results = append(results, Result{Name: "env-file", Status: StatusFailed, Detail: fmt.Sprintf("%s not found", file)})
			continue
		}
		results = append(results, Result{Name: "env-file", Status: StatusOK, Detail: file})
	}
	return results, nil
}

func overrideSymlinkValidator(ctx *config.Context) ([]Result, error) {
	if ctx == nil || ctx.DockerHostType != config.ContextLocal {
		return nil, nil
	}
	if err := ctx.ValidateTrackedComposeOverrideSymlink(); err != nil {
		return []Result{{Name: "compose-override-symlink", Status: StatusFailed, Detail: err.Error()}}, nil
	}
	return []Result{{Name: "compose-override-symlink", Status: StatusOK}}, nil
}

func dockerAccessValidator(ctx *config.Context) ([]Result, error) {
	if ctx == nil {
		return nil, nil
	}
	results := []Result{}
	if ctx.DockerHostType == config.ContextLocal {
		if !config.IsDockerSocketAlive(ctx.DockerSocket) {
			results = append(results, Result{
				Name:    "docker-socket",
				Status:  StatusFailed,
				Detail:  fmt.Sprintf("docker socket %q is not reachable", ctx.DockerSocket),
				FixHint: "start Docker or update the context docker-socket",
			})
			return results, nil
		}
		results = append(results, Result{Name: "docker-socket", Status: StatusOK, Detail: ctx.DockerSocket})
	}

	if err := ctx.ValidateComposeAccess(); err != nil {
		results = append(results, Result{
			Name:    "docker-compose-access",
			Status:  StatusFailed,
			Detail:  err.Error(),
			FixHint: "verify the project directory, docker socket, compose files, env files, and docker permissions",
		})
		return results, nil
	}
	results = append(results, Result{Name: "docker-compose-access", Status: StatusOK})
	return results, nil
}

func requiredStringResult(name, value string) Result {
	if strings.TrimSpace(value) == "" {
		return Result{Name: name, Status: StatusFailed, Detail: fmt.Sprintf("%s is required", name)}
	}
	return Result{Name: name, Status: StatusOK, Detail: strings.TrimSpace(value)}
}

func requiredUintResult(name string, value uint) Result {
	if value == 0 {
		return Result{Name: name, Status: StatusFailed, Detail: fmt.Sprintf("%s is required", name)}
	}
	return Result{Name: name, Status: StatusOK, Detail: fmt.Sprintf("%d", value)}
}

func samePath(a, b string) bool {
	a = filepath.Clean(strings.TrimSpace(a))
	b = filepath.Clean(strings.TrimSpace(b))
	return a != "" && b != "" && a == b
}
