package plugin

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// PluginMetadata is the JSON metadata payload returned by plugin.metadata RPC.
type PluginMetadata struct {
	// ProtocolVersion is the metadata payload version. The RPCResponse envelope
	// carrying this payload has its own protocol_version check.
	ProtocolVersion   int          `json:"protocol_version,omitempty" yaml:"protocol_version,omitempty"`
	Name              string       `json:"name" yaml:"name"`
	BinaryName        string       `json:"binary_name,omitempty" yaml:"binary_name,omitempty"`
	Version           string       `json:"version,omitempty" yaml:"version,omitempty"`
	Description       string       `json:"description,omitempty" yaml:"description,omitempty"`
	Author            string       `json:"author,omitempty" yaml:"author,omitempty"`
	TemplateRepo      string       `json:"template_repo,omitempty" yaml:"template_repo,omitempty"`
	CanCreate         bool         `json:"can_create,omitempty" yaml:"can_create,omitempty"`
	CanDeploy         bool         `json:"can_deploy,omitempty" yaml:"can_deploy,omitempty"`
	CanDebug          bool         `json:"can_debug,omitempty" yaml:"can_debug,omitempty"`
	CanConverge       bool         `json:"can_converge,omitempty" yaml:"can_converge,omitempty"`
	CanSet            bool         `json:"can_set,omitempty" yaml:"can_set,omitempty"`
	CanValidate       bool         `json:"can_validate,omitempty" yaml:"can_validate,omitempty"`
	CanHealthcheck    bool         `json:"can_healthcheck,omitempty" yaml:"can_healthcheck,omitempty"`
	CanVerify         bool         `json:"can_verify,omitempty" yaml:"can_verify,omitempty"`
	Includes          []string     `json:"includes,omitempty" yaml:"includes,omitempty"`
	CreateDefinitions []CreateSpec `json:"create_definitions,omitempty" yaml:"create_definitions,omitempty"`
	DeployDefinitions []DeploySpec `json:"deploy_definitions,omitempty" yaml:"deploy_definitions,omitempty"`
}

// InstalledPlugin is the local registry record for a discovered plugin.
type InstalledPlugin struct {
	ProtocolVersion   int          `json:"protocol_version,omitempty" yaml:"protocol_version,omitempty"`
	Name              string       `json:"name" yaml:"name"`
	BinaryName        string       `json:"binary_name,omitempty" yaml:"binary_name,omitempty"`
	Path              string       `json:"path,omitempty" yaml:"path,omitempty"`
	Version           string       `json:"version,omitempty" yaml:"version,omitempty"`
	Description       string       `json:"description,omitempty" yaml:"description,omitempty"`
	Author            string       `json:"author,omitempty" yaml:"author,omitempty"`
	TemplateRepo      string       `json:"template_repo,omitempty" yaml:"template_repo,omitempty"`
	CanCreate         bool         `json:"can_create,omitempty" yaml:"can_create,omitempty"`
	CanDeploy         bool         `json:"can_deploy,omitempty" yaml:"can_deploy,omitempty"`
	CanDebug          bool         `json:"can_debug,omitempty" yaml:"can_debug,omitempty"`
	CanConverge       bool         `json:"can_converge,omitempty" yaml:"can_converge,omitempty"`
	CanSet            bool         `json:"can_set,omitempty" yaml:"can_set,omitempty"`
	CanValidate       bool         `json:"can_validate,omitempty" yaml:"can_validate,omitempty"`
	CanHealthcheck    bool         `json:"can_healthcheck,omitempty" yaml:"can_healthcheck,omitempty"`
	CanVerify         bool         `json:"can_verify,omitempty" yaml:"can_verify,omitempty"`
	Includes          []string     `json:"includes,omitempty" yaml:"includes,omitempty"`
	CreateDefinitions []CreateSpec `json:"create_definitions,omitempty" yaml:"create_definitions,omitempty"`
	DeployDefinitions []DeploySpec `json:"deploy_definitions,omitempty" yaml:"deploy_definitions,omitempty"`
	MetadataError     string       `json:"metadata_error,omitempty" yaml:"metadata_error,omitempty"`
}

var builtinTemplateRepos = map[string]string{
	"isle": "https://github.com/islandora-devops/isle-site-template",
}

const maxConcurrentPluginInspections = 8

var installedPluginMetadataTimeout = 5 * time.Second

// installedDiscoveryCache is intentionally process-local. sitectl is normally
// short-lived; long-lived hosts can call InvalidateInstalledDiscoveryCache to
// pick up plugin upgrades.
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

// InvalidateInstalledDiscoveryCache clears process-local plugin discovery
// results so subsequent discovery sees PATH and plugin metadata changes.
func InvalidateInstalledDiscoveryCache() {
	installedDiscoveryCache.Lock()
	defer installedDiscoveryCache.Unlock()
	installedDiscoveryCache.full = map[string][]InstalledPlugin{}
	installedDiscoveryCache.lightweight = map[string][]InstalledPlugin{}
}

func DiscoverInstalledFromPath(pathEnv string) []InstalledPlugin {
	started := time.Now()
	if cached, ok := cachedInstalledPlugins(pathEnv, false); ok {
		slog.Debug("completed full plugin discovery from cache", "count", len(cached), "duration", time.Since(started))
		return cached
	}

	discovered := inspectInstalledPlugins(discoverInstalledPathEntries(pathEnv))

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

	discovered := buildInstalledPlugins(discoverInstalledPathEntries(pathEnv), func(pluginName, binaryName, path string) InstalledPlugin {
		return InstalledPlugin{
			Name:        pluginName,
			BinaryName:  binaryName,
			Path:        path,
			Description: fmt.Sprintf("the %s plugin", pluginName),
		}
	})

	slog.Debug("completed lightweight plugin discovery", "count", len(discovered), "duration", time.Since(started))
	storeInstalledPlugins(pathEnv, true, discovered)
	return discovered
}

type installedPluginPathEntry struct {
	pluginName string
	binaryName string
	path       string
}

func discoverInstalledPathEntries(pathEnv string) []installedPluginPathEntry {
	seen := map[string]bool{}
	discovered := make([]installedPluginPathEntry, 0)

	for _, dir := range filepath.SplitList(pathEnv) {
		entries, err := os.ReadDir(dir)
		if err != nil {
			continue
		}

		for _, entry := range entries {
			if entry.IsDir() {
				continue
			}
			binaryName := entry.Name()
			if !strings.HasPrefix(binaryName, "sitectl-") || binaryName == "sitectl" {
				continue
			}

			pluginName := strings.TrimPrefix(binaryName, "sitectl-")
			if pluginName == "" || seen[pluginName] {
				continue
			}
			seen[pluginName] = true

			path := filepath.Join(dir, binaryName)
			discovered = append(discovered, installedPluginPathEntry{
				pluginName: pluginName,
				binaryName: binaryName,
				path:       path,
			})
		}
	}

	return discovered
}

func buildInstalledPlugins(entries []installedPluginPathEntry, build func(pluginName, binaryName, path string) InstalledPlugin) []InstalledPlugin {
	discovered := make([]InstalledPlugin, 0, len(entries))
	for _, entry := range entries {
		discovered = append(discovered, build(entry.pluginName, entry.binaryName, entry.path))
	}
	return discovered
}

func inspectInstalledPlugins(entries []installedPluginPathEntry) []InstalledPlugin {
	discovered := make([]InstalledPlugin, len(entries))
	if len(entries) == 0 {
		return discovered
	}

	workerCount := min(len(entries), maxConcurrentPluginInspections)
	jobs := make(chan int)
	var wg sync.WaitGroup
	wg.Add(workerCount)
	for range workerCount {
		go func() {
			defer wg.Done()
			for index := range jobs {
				entry := entries[index]
				discovered[index] = inspectInstalledPlugin(entry.pluginName, entry.binaryName, entry.path)
			}
		}()
	}
	for index := range entries {
		jobs <- index
	}
	close(jobs)
	wg.Wait()
	return discovered
}

// FindInstalled returns metadata for a sitectl plugin discovered on PATH.
//
// Executables named sitectl-* on PATH are treated as trusted plugins; invoking
// them uses the same trust boundary as running those plugin binaries directly.
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

	req := NewRPCRequest(MethodPluginMetadata)
	metadataCtx, cancel := context.WithTimeout(context.Background(), installedPluginMetadataTimeout)
	defer cancel()
	resp, err := runPluginRPCPath(pluginName, pluginPath, req, pluginRPCPathOptions{
		CommandExecOptions: CommandExecOptions{Context: metadataCtx},
	})
	if err != nil {
		slog.Debug("plugin metadata rpc failed", "plugin", pluginName, "path", pluginPath, "duration", time.Since(started), "err", err)
		info.MetadataError = err.Error()
		return info
	}
	var metadata PluginMetadata
	if err := json.Unmarshal(resp.Result, &metadata); err != nil {
		slog.Debug("plugin metadata result unmarshal failed", "plugin", pluginName, "path", pluginPath, "duration", time.Since(started), "err", err)
		info.MetadataError = err.Error()
		return info
	}
	// runPluginRPCPath has already validated the transport envelope version.
	// This check validates the metadata payload schema carried inside it.
	if metadata.ProtocolVersion != 0 && metadata.ProtocolVersion != RPCProtocolVersion {
		slog.Debug("plugin metadata protocol mismatch", "plugin", pluginName, "path", pluginPath, "protocol_version", metadata.ProtocolVersion)
		info.MetadataError = rpcProtocolVersionMismatchMessage(metadata.ProtocolVersion)
		return info
	}
	for i := range metadata.CreateDefinitions {
		metadata.CreateDefinitions[i] = normalizeCreateSpec(metadata.CreateDefinitions[i])
	}
	parsed := installedPluginFromMetadata(metadata, info)
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
	slog.Debug("inspected plugin metadata", "plugin", pluginName, "path", pluginPath, "can_create", parsed.CanCreate, "can_deploy", parsed.CanDeploy, "can_debug", parsed.CanDebug, "can_converge", parsed.CanConverge, "can_set", parsed.CanSet, "can_validate", parsed.CanValidate, "can_healthcheck", parsed.CanHealthcheck, "can_verify", parsed.CanVerify, "includes", len(parsed.Includes), "create_definitions", len(parsed.CreateDefinitions), "deploy_definitions", len(parsed.DeployDefinitions), "duration", time.Since(started))
	return parsed
}

func installedPluginFromMetadata(metadata PluginMetadata, defaults InstalledPlugin) InstalledPlugin {
	installed := InstalledPlugin{
		ProtocolVersion:   metadata.ProtocolVersion,
		Name:              metadata.Name,
		BinaryName:        metadata.BinaryName,
		Path:              defaults.Path,
		Version:           metadata.Version,
		Description:       metadata.Description,
		Author:            metadata.Author,
		TemplateRepo:      metadata.TemplateRepo,
		CanCreate:         metadata.CanCreate,
		CanDeploy:         metadata.CanDeploy,
		CanDebug:          metadata.CanDebug,
		CanConverge:       metadata.CanConverge,
		CanSet:            metadata.CanSet,
		CanValidate:       metadata.CanValidate,
		CanHealthcheck:    metadata.CanHealthcheck,
		CanVerify:         metadata.CanVerify,
		Includes:          append([]string{}, metadata.Includes...),
		CreateDefinitions: append([]CreateSpec{}, metadata.CreateDefinitions...),
		DeployDefinitions: append([]DeploySpec{}, metadata.DeployDefinitions...),
	}
	if installed.Name == "" {
		installed.Name = defaults.Name
	}
	if installed.BinaryName == "" {
		installed.BinaryName = defaults.BinaryName
	}
	if installed.Description == "" {
		installed.Description = defaults.Description
	}
	if installed.TemplateRepo == "" {
		installed.TemplateRepo = defaults.TemplateRepo
	}
	return installed
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
		out[i].InitArtifacts = append([]InitArtifact{}, value.InitArtifacts...)
		out[i].Images = append([]ComposeImageSpec{}, value.Images...)
		out[i].DockerComposeUp = append([]string{}, value.DockerComposeUp...)
		out[i].DockerComposeDown = append([]string{}, value.DockerComposeDown...)
		out[i].DockerComposeRollout = append([]string{}, value.DockerComposeRollout...)
	}
	return out
}
