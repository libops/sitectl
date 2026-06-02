package plugin

import (
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"

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
	CanDeploy         bool
	CanConverge       bool
	CanSet            bool
	CanValidate       bool
	Includes          []string
	CreateDefinitions []CreateSpec
	DeployDefinitions []DeploySpec
}

var builtinTemplateRepos = map[string]string{
	"isle": "https://github.com/islandora-devops/isle-site-template",
}

var installedDiscoveryCache = struct {
	sync.Mutex
	full        map[string][]InstalledPlugin
	lightweight map[string][]InstalledPlugin
}{
	full:        map[string][]InstalledPlugin{},
	lightweight: map[string][]InstalledPlugin{},
}

func DiscoverInstalled() []InstalledPlugin {
	return DiscoverInstalledFromPath(os.Getenv("PATH"))
}

func DiscoverInstalledLightweight() []InstalledPlugin {
	return DiscoverInstalledLightweightFromPath(os.Getenv("PATH"))
}

func DiscoverInstalledFromPath(pathEnv string) []InstalledPlugin {
	started := time.Now()
	if cached, ok := cachedInstalledPlugins(pathEnv, false); ok {
		slog.Debug("completed full plugin discovery from cache", "count", len(cached), "duration", time.Since(started))
		return cached
	}

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

	slog.Debug("completed full plugin discovery", "count", len(discovered), "duration", time.Since(started))
	storeInstalledPlugins(pathEnv, false, discovered)
	return discovered
}

func DiscoverInstalledLightweightFromPath(pathEnv string) []InstalledPlugin {
	started := time.Now()
	if cached, ok := cachedInstalledPlugins(pathEnv, true); ok {
		slog.Debug("completed lightweight plugin discovery from cache", "count", len(cached), "duration", time.Since(started))
		return cached
	}

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
			discovered = append(discovered, InstalledPlugin{
				Name:        pluginName,
				BinaryName:  name,
				Path:        path,
				Description: fmt.Sprintf("the %s plugin", pluginName),
			})
		}
	}

	slog.Debug("completed lightweight plugin discovery", "count", len(discovered), "duration", time.Since(started))
	storeInstalledPlugins(pathEnv, true, discovered)
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
	started := time.Now()
	info := InstalledPlugin{
		Name:        pluginName,
		BinaryName:  binaryName,
		Path:        pluginPath,
		Description: fmt.Sprintf("the %s plugin", pluginName),
	}
	if repo := builtinTemplateRepos[pluginName]; repo != "" {
		info.TemplateRepo = repo
	}

	cmd := exec.Command(pluginPath, "__plugin-metadata") // #nosec G702 -- pluginPath comes from PATH discovery and must execute the installed sitectl-* plugin binary to read its metadata.
	output, err := cmd.Output()
	if err != nil {
		slog.Debug("plugin metadata command failed", "plugin", pluginName, "path", pluginPath, "duration", time.Since(started), "err", err)
		return info
	}

	var parsed InstalledPlugin
	if err := yaml.Unmarshal(output, &parsed); err != nil {
		slog.Debug("plugin metadata unmarshal failed", "plugin", pluginName, "path", pluginPath, "duration", time.Since(started), "err", err)
		return info
	}
	for i := range parsed.CreateDefinitions {
		parsed.CreateDefinitions[i] = normalizeCreateSpec(parsed.CreateDefinitions[i])
	}
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
	if parsed.TemplateRepo == "" {
		if spec, ok := defaultCreateDefinition(parsed.CreateDefinitions); ok && strings.TrimSpace(spec.DockerComposeRepo) != "" {
			parsed.TemplateRepo = spec.DockerComposeRepo
		}
	}
	if !parsed.CanCreate {
		parsed.CanCreate = len(parsed.CreateDefinitions) > 0
	}
	if !parsed.CanDeploy {
		parsed.CanDeploy = len(parsed.DeployDefinitions) > 0
	}
	slog.Debug("inspected plugin metadata", "plugin", pluginName, "path", pluginPath, "can_create", parsed.CanCreate, "can_deploy", parsed.CanDeploy, "can_converge", parsed.CanConverge, "can_set", parsed.CanSet, "can_validate", parsed.CanValidate, "includes", len(parsed.Includes), "create_definitions", len(parsed.CreateDefinitions), "deploy_definitions", len(parsed.DeployDefinitions), "duration", time.Since(started))
	return parsed
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

func cachedInstalledPlugins(pathEnv string, lightweight bool) ([]InstalledPlugin, bool) {
	installedDiscoveryCache.Lock()
	defer installedDiscoveryCache.Unlock()
	var values []InstalledPlugin
	var ok bool
	if lightweight {
		values, ok = installedDiscoveryCache.lightweight[pathEnv]
	} else {
		values, ok = installedDiscoveryCache.full[pathEnv]
	}
	if !ok {
		return nil, false
	}
	return cloneInstalledPlugins(values), true
}

func storeInstalledPlugins(pathEnv string, lightweight bool, values []InstalledPlugin) {
	installedDiscoveryCache.Lock()
	defer installedDiscoveryCache.Unlock()
	if lightweight {
		installedDiscoveryCache.lightweight[pathEnv] = cloneInstalledPlugins(values)
		return
	}
	installedDiscoveryCache.full[pathEnv] = cloneInstalledPlugins(values)
}

func cloneInstalledPlugins(values []InstalledPlugin) []InstalledPlugin {
	if len(values) == 0 {
		return nil
	}
	out := make([]InstalledPlugin, len(values))
	for i, value := range values {
		out[i] = value
		out[i].Includes = append([]string{}, value.Includes...)
		out[i].CreateDefinitions = cloneCreateSpecs(value.CreateDefinitions)
		out[i].DeployDefinitions = append([]DeploySpec{}, value.DeployDefinitions...)
	}
	return out
}

func cloneCreateSpecs(values []CreateSpec) []CreateSpec {
	if len(values) == 0 {
		return nil
	}
	out := make([]CreateSpec, len(values))
	for i, value := range values {
		out[i] = value
		out[i].DockerComposeBuild = append([]string{}, value.DockerComposeBuild...)
		out[i].DockerComposeInit = append([]string{}, value.DockerComposeInit...)
		out[i].DockerComposeUp = append([]string{}, value.DockerComposeUp...)
		out[i].DockerComposeDown = append([]string{}, value.DockerComposeDown...)
		out[i].DockerComposeRollout = append([]string{}, value.DockerComposeRollout...)
	}
	return out
}
