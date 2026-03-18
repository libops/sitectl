package plugin

import (
	"bufio"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

type InstalledPlugin struct {
	Name         string
	BinaryName   string
	Path         string
	Version      string
	Description  string
	Author       string
	TemplateRepo string
	CanCreate    bool
}

var builtinTemplateRepos = map[string]string{
	"isle": "https://github.com/islandora-devops/isle-site-template",
}

func DiscoverInstalled() []InstalledPlugin {
	return DiscoverInstalledFromPath(os.Getenv("PATH"))
}

func DiscoverInstalledFromPath(pathEnv string) []InstalledPlugin {
	seen := map[string]bool{}
	discovered := make([]InstalledPlugin, 0)

	for _, dir := range filepath.SplitList(pathEnv) {
		entries, err := os.ReadDir(dir)
		if err != nil {
			continue
		}

		for _, entry := range entries {
			if entry.IsDir() {
				continue
			}
			name := entry.Name()
			if !strings.HasPrefix(name, "sitectl-") || name == "sitectl" {
				continue
			}

			pluginName := strings.TrimPrefix(name, "sitectl-")
			if pluginName == "" || seen[pluginName] {
				continue
			}
			seen[pluginName] = true

			path := filepath.Join(dir, name)
			discovered = append(discovered, inspectInstalledPlugin(pluginName, name, path))
		}
	}

	return discovered
}

func inspectInstalledPlugin(pluginName, binaryName, pluginPath string) InstalledPlugin {
	info := InstalledPlugin{
		Name:        pluginName,
		BinaryName:  binaryName,
		Path:        pluginPath,
		Description: fmt.Sprintf("the %s plugin", pluginName),
		CanCreate:   pluginSupportsCreate(pluginPath),
	}
	if repo := builtinTemplateRepos[pluginName]; repo != "" {
		info.TemplateRepo = repo
	}

	cmd := exec.Command(pluginPath, "plugin-info")
	output, err := cmd.Output()
	if err != nil {
		return info
	}

	parsed := ParsePluginInfoOutput(string(output))
	if parsed.Name == "" {
		parsed.Name = pluginName
	}
	if parsed.BinaryName == "" {
		parsed.BinaryName = binaryName
	}
	if parsed.Path == "" {
		parsed.Path = pluginPath
	}
	if parsed.Description == "" {
		parsed.Description = info.Description
	}
	if parsed.TemplateRepo == "" {
		parsed.TemplateRepo = info.TemplateRepo
	}
	if !parsed.CanCreate {
		parsed.CanCreate = info.CanCreate
	}

	return parsed
}

func pluginSupportsCreate(pluginPath string) bool {
	cmd := exec.Command(pluginPath, "create", "--help")
	if output, err := cmd.CombinedOutput(); err == nil {
		return true
	} else if strings.Contains(strings.ToLower(string(output)), "unknown command") {
		return false
	}
	return false
}

func ParsePluginInfoOutput(output string) InstalledPlugin {
	var info InstalledPlugin

	scanner := bufio.NewScanner(strings.NewReader(output))
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}
		key, value, ok := strings.Cut(line, ":")
		if !ok {
			continue
		}
		key = strings.TrimSpace(strings.ToLower(key))
		value = strings.TrimSpace(value)

		switch key {
		case "name":
			info.Name = value
		case "version":
			info.Version = value
		case "description":
			info.Description = value
		case "author":
			info.Author = value
		case "template-repo":
			info.TemplateRepo = value
		}
	}

	return info
}
