package config

import (
	"os"
	"path/filepath"
	"strings"
)

var composeProjectCandidates = []string{
	"docker-compose.yml",
	"docker-compose.yaml",
	"compose.yml",
	"compose.yaml",
}

func LooksLikeComposeProject(projectDir string) bool {
	projectDir = filepath.Clean(strings.TrimSpace(projectDir))
	if projectDir == "" {
		return false
	}
	for _, name := range composeProjectCandidates {
		if _, err := os.Stat(filepath.Join(projectDir, name)); err == nil {
			return true
		}
	}
	return false
}

func FindLocalContextByProjectDir(projectDir string) (*Context, error) {
	projectDir = filepath.Clean(strings.TrimSpace(projectDir))
	if projectDir == "" {
		return nil, nil
	}
	if resolved, err := filepath.EvalSymlinks(projectDir); err == nil {
		projectDir = resolved
	}
	cfg, err := Load()
	if err != nil {
		return nil, err
	}
	for i := range cfg.Contexts {
		ctx := &cfg.Contexts[i]
		if ctx.DockerHostType != ContextLocal {
			continue
		}
		storedDir := filepath.Clean(strings.TrimSpace(ctx.ProjectDir))
		if resolved, err := filepath.EvalSymlinks(storedDir); err == nil {
			storedDir = resolved
		}
		if storedDir == projectDir {
			return ctx, nil
		}
	}
	return nil, nil
}

func DiscoverCurrentContext() (*Context, error) {
	cwd, err := os.Getwd()
	if err != nil {
		return nil, err
	}
	return FindLocalContextByProjectDir(cwd)
}
