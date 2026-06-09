package plugin

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/libops/sitectl/pkg/config"
)

type ProjectDiscoveryFunc func(projectDir string) (*config.ProjectClaim, error)

type ComposeProjectDiscovery struct {
	RequiredServices          []string
	ForbiddenServices         []string
	RequiredComposerPackages  []string
	ForbiddenComposerPackages []string
	ComposerFiles             []string
	Reason                    string
}

type projectDetectionResult struct {
	Claimed    bool   `json:"claimed" yaml:"claimed"`
	Plugin     string `json:"plugin,omitempty" yaml:"plugin,omitempty"`
	ProjectDir string `json:"project_dir,omitempty" yaml:"project-dir,omitempty"`
	Reason     string `json:"reason,omitempty" yaml:"reason,omitempty"`
}

func (s *SDK) SetProjectDiscovery(discovery ProjectDiscoveryFunc) {
	if s == nil {
		return
	}
	s.projectDiscovery = discovery
}

func (s *SDK) SetComposeProjectDiscovery(spec ComposeProjectDiscovery) {
	if s == nil {
		return
	}
	s.SetProjectDiscovery(func(projectDir string) (*config.ProjectClaim, error) {
		return claimComposeProject(s.Metadata.Name, projectDir, spec)
	})
}

func (s *SDK) detectOwnProject(projectDir string) (*config.ProjectClaim, error) {
	if s == nil || s.projectDiscovery == nil {
		return nil, nil
	}
	projectDir = filepath.Clean(strings.TrimSpace(projectDir))
	if projectDir == "" {
		projectDir = "."
	}
	if resolved, err := filepath.Abs(projectDir); err == nil {
		projectDir = resolved
	}
	claim, err := s.projectDiscovery(projectDir)
	if err != nil || claim == nil {
		return claim, err
	}
	if strings.TrimSpace(claim.Plugin) == "" {
		claim.Plugin = s.Metadata.Name
	}
	if strings.TrimSpace(claim.ProjectDir) == "" {
		claim.ProjectDir = projectDir
	}
	return claim, nil
}

func (s *SDK) detectProjectOwner(projectDir, requestedPlugin string) (*config.ProjectClaim, error) {
	if claim, err := s.detectOwnProject(projectDir); err != nil {
		return nil, err
	} else if claimSupportsRequestedPlugin(claim, requestedPlugin) {
		return claim, nil
	}
	return DetectProjectOwner(projectDir, requestedPlugin)
}

func DetectProjectOwner(projectDir, requestedPlugin string) (*config.ProjectClaim, error) {
	projectDir = filepath.Clean(strings.TrimSpace(projectDir))
	if projectDir == "" {
		return nil, nil
	}
	if resolved, err := filepath.Abs(projectDir); err == nil {
		projectDir = resolved
	}

	installed := DiscoverInstalledLightweight()
	sort.SliceStable(installed, func(i, j int) bool {
		return installed[i].Name < installed[j].Name
	})
	for _, discovered := range installed {
		if strings.TrimSpace(requestedPlugin) != "" && discovered.Name != requestedPlugin && !ContextPluginSupports(discovered.Name, requestedPlugin) {
			continue
		}
		claim, ok, err := detectInstalledPluginProject(discovered, projectDir)
		if err != nil {
			if strings.TrimSpace(requestedPlugin) != "" {
				return nil, err
			}
			continue
		}
		if !ok || !claimSupportsRequestedPlugin(claim, requestedPlugin) {
			continue
		}
		return claim, nil
	}
	return nil, nil
}

func detectInstalledPluginProject(discovered InstalledPlugin, projectDir string) (*config.ProjectClaim, bool, error) {
	req, err := NewProjectDetectRequest(projectDir)
	if err != nil {
		return nil, false, err
	}
	resp, err := runPluginRPCPath(discovered.Name, discovered.Path, req, pluginRPCPathOptions{})
	if err != nil {
		if IsRPCErrorCode(err, RPCErrorCodeNotRegistered) {
			return nil, false, nil
		}
		return nil, false, fmt.Errorf("detect project with plugin %q: %w", discovered.Name, err)
	}
	var result projectDetectionResult
	if err := json.Unmarshal(resp.Result, &result); err != nil || !result.Claimed {
		if err != nil {
			return nil, false, fmt.Errorf("parse project detection from plugin %q: %w", discovered.Name, err)
		}
		return nil, false, nil
	}
	pluginName := strings.TrimSpace(result.Plugin)
	if pluginName == "" {
		pluginName = discovered.Name
	}
	claim := &config.ProjectClaim{
		Plugin:     pluginName,
		ProjectDir: strings.TrimSpace(result.ProjectDir),
		Reason:     strings.TrimSpace(result.Reason),
	}
	if claim.ProjectDir == "" {
		claim.ProjectDir = projectDir
	}
	return claim, true, nil
}

func claimSupportsRequestedPlugin(claim *config.ProjectClaim, requestedPlugin string) bool {
	if claim == nil {
		return false
	}
	requestedPlugin = strings.TrimSpace(requestedPlugin)
	if requestedPlugin == "" {
		return true
	}
	return ContextPluginSupports(strings.TrimSpace(claim.Plugin), requestedPlugin)
}

func claimComposeProject(pluginName, projectDir string, spec ComposeProjectDiscovery) (*config.ProjectClaim, error) {
	services := stringSet(config.DetectComposeServices(projectDir))
	if len(spec.RequiredServices) > 0 && !containsAll(services, spec.RequiredServices) {
		return nil, nil
	}
	if containsAny(services, spec.ForbiddenServices) {
		return nil, nil
	}

	packages, err := readComposerPackages(projectDir, spec.ComposerFiles)
	if err != nil {
		return nil, err
	}
	if len(spec.RequiredComposerPackages) > 0 && !containsAll(packages, spec.RequiredComposerPackages) {
		return nil, nil
	}
	if containsAny(packages, spec.ForbiddenComposerPackages) {
		return nil, nil
	}
	if len(spec.RequiredServices) == 0 && len(spec.RequiredComposerPackages) == 0 {
		return nil, nil
	}

	reason := strings.TrimSpace(spec.Reason)
	if reason == "" {
		reason = defaultProjectDiscoveryReason(spec)
	}
	return &config.ProjectClaim{
		Plugin:     strings.TrimSpace(pluginName),
		ProjectDir: projectDir,
		Reason:     reason,
	}, nil
}

func defaultProjectDiscoveryReason(spec ComposeProjectDiscovery) string {
	parts := make([]string, 0, 2)
	if len(spec.RequiredServices) > 0 {
		parts = append(parts, "services "+strings.Join(spec.RequiredServices, ", "))
	}
	if len(spec.RequiredComposerPackages) > 0 {
		parts = append(parts, "composer packages "+strings.Join(spec.RequiredComposerPackages, ", "))
	}
	return strings.Join(parts, "; ")
}

func readComposerPackages(projectDir string, files []string) (map[string]bool, error) {
	if len(files) == 0 {
		files = []string{
			"composer.json",
			"drupal/rootfs/var/www/drupal/composer.json",
		}
	}
	packages := map[string]bool{}
	for _, rel := range files {
		path := filepath.Join(projectDir, filepath.Clean(rel))
		data, err := os.ReadFile(path) // #nosec G304 -- path is constrained to plugin-declared composer files inside the selected project.
		if err != nil {
			if os.IsNotExist(err) {
				continue
			}
			return nil, fmt.Errorf("read composer file %q: %w", path, err)
		}
		var doc struct {
			Require    map[string]json.RawMessage `json:"require"`
			RequireDev map[string]json.RawMessage `json:"require-dev"`
		}
		if err := json.Unmarshal(data, &doc); err != nil {
			return nil, fmt.Errorf("parse composer file %q: %w", path, err)
		}
		for name := range doc.Require {
			packages[strings.TrimSpace(name)] = true
		}
		for name := range doc.RequireDev {
			packages[strings.TrimSpace(name)] = true
		}
	}
	return packages, nil
}

func stringSet(values []string) map[string]bool {
	out := make(map[string]bool, len(values))
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value != "" {
			out[value] = true
		}
	}
	return out
}

func containsAll(haystack map[string]bool, needles []string) bool {
	for _, needle := range needles {
		if !haystack[strings.TrimSpace(needle)] {
			return false
		}
	}
	return true
}

func containsAny(haystack map[string]bool, needles []string) bool {
	for _, needle := range needles {
		if haystack[strings.TrimSpace(needle)] {
			return true
		}
	}
	return false
}
