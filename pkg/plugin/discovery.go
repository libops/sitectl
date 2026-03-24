package plugin

import (
	"bufio"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	yaml "gopkg.in/yaml.v3"
)

type InstalledPlugin struct {
	Name              string
	BinaryName        string
	Path              string
	Version           string
	Description       string
	Author            string
	TemplateRepo      string
	CanCreate         bool
	Includes          []string
	CreateDefinitions []CreateSpec
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

func FindInstalled(name string) (InstalledPlugin, bool) {
	for _, discovered := range DiscoverInstalled() {
		if discovered.Name == name {
			return discovered, true
		}
	}
	return InstalledPlugin{}, false
}

func inspectInstalledPlugin(pluginName, binaryName, pluginPath string) InstalledPlugin {
	createDefinitions := pluginCreateDefinitions(pluginPath)
	info := InstalledPlugin{
		Name:              pluginName,
		BinaryName:        binaryName,
		Path:              pluginPath,
		Description:       fmt.Sprintf("the %s plugin", pluginName),
		CanCreate:         len(createDefinitions) > 0,
		CreateDefinitions: createDefinitions,
	}
	if repo := builtinTemplateRepos[pluginName]; repo != "" {
		info.TemplateRepo = repo
	}
	if spec, ok := defaultCreateDefinition(createDefinitions); ok && strings.TrimSpace(spec.DockerComposeRepo) != "" {
		info.TemplateRepo = spec.DockerComposeRepo
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
	if len(parsed.CreateDefinitions) == 0 {
		parsed.CreateDefinitions = info.CreateDefinitions
	}

	return parsed
}

func pluginCreateDefinitions(pluginPath string) []CreateSpec {
	cmd := exec.Command(pluginPath, "__create", "list")
	output, err := cmd.Output()
	if err != nil {
		return nil
	}
	var specs []CreateSpec
	if err := yaml.Unmarshal(output, &specs); err != nil {
		return nil
	}
	for i := range specs {
		specs[i] = normalizeCreateSpec(specs[i])
	}
	return specs
}

func defaultCreateDefinition(specs []CreateSpec) (CreateSpec, bool) {
	if len(specs) == 0 {
		return CreateSpec{}, false
	}
	for _, spec := range specs {
		if spec.Default {
			return spec, true
		}
	}
	return specs[0], true
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
		case "includes":
			if value == "" {
				continue
			}
			for _, include := range strings.Split(value, ",") {
				include = strings.TrimSpace(include)
				if include == "" {
					continue
				}
				info.Includes = append(info.Includes, include)
			}
		}
	}

	return info
}
