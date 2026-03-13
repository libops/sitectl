package component

import (
	"fmt"
	"path/filepath"
	"strings"

	yaml "gopkg.in/yaml.v3"
)

type ComposeProject struct {
	root map[string]any
}

type ComposeDefinitions struct {
	Services map[string]any
	Networks map[string]any
	Volumes  map[string]any
	Secrets  map[string]any
	Configs  map[string]any
}

func ParseComposeProject(data []byte) (*ComposeProject, error) {
	root := map[string]any{}
	if len(data) == 0 {
		return &ComposeProject{root: root}, nil
	}
	if err := yaml.Unmarshal(data, &root); err != nil {
		return nil, fmt.Errorf("unmarshal compose yaml: %w", err)
	}
	return &ComposeProject{root: root}, nil
}

func ParseComposeDefinitions(data []byte) (*ComposeDefinitions, error) {
	root := map[string]any{}
	if err := yaml.Unmarshal(data, &root); err != nil {
		return nil, fmt.Errorf("unmarshal compose definitions: %w", err)
	}
	return &ComposeDefinitions{
		Services: nestedMap(root["services"]),
		Networks: nestedMap(root["networks"]),
		Volumes:  nestedMap(root["volumes"]),
		Secrets:  nestedMap(root["secrets"]),
		Configs:  nestedMap(root["configs"]),
	}, nil
}

func (c *ComposeProject) Bytes() ([]byte, error) {
	data, err := yaml.Marshal(c.root)
	if err != nil {
		return nil, fmt.Errorf("marshal compose yaml: %w", err)
	}
	return data, nil
}

func (c *ComposeProject) RemoveService(name string) bool {
	services := c.services()
	if services == nil {
		return false
	}
	if _, ok := services[name]; !ok {
		return false
	}

	delete(services, name)
	c.removeServiceDependencyReferences(name)
	return true
}

func (c *ComposeProject) AddDefinitions(defs *ComposeDefinitions) {
	if defs == nil {
		return
	}
	mergeIntoSection(c.root, "services", defs.Services)
	mergeIntoSection(c.root, "networks", defs.Networks)
	mergeIntoSection(c.root, "volumes", defs.Volumes)
	mergeIntoSection(c.root, "secrets", defs.Secrets)
	mergeIntoSection(c.root, "configs", defs.Configs)
}

func (c *ComposeProject) PruneUnusedResources() {
	for _, section := range []string{"volumes", "networks", "secrets", "configs"} {
		entries := nestedMap(c.root[section])
		if len(entries) == 0 {
			continue
		}

		used := c.usedResources(section)
		for name := range entries {
			if !used[name] {
				delete(entries, name)
			}
		}
		if len(entries) == 0 {
			delete(c.root, section)
			continue
		}
		c.root[section] = entries
	}
}

func (c *ComposeProject) services() map[string]any {
	services := nestedMap(c.root["services"])
	if services == nil {
		return nil
	}
	c.root["services"] = services
	return services
}

func (c *ComposeProject) removeServiceDependencyReferences(name string) {
	for _, rawService := range c.services() {
		service, ok := rawService.(map[string]any)
		if !ok {
			continue
		}
		switch dependsOn := service["depends_on"].(type) {
		case []any:
			filtered := make([]any, 0, len(dependsOn))
			for _, item := range dependsOn {
				if str, ok := item.(string); ok && str == name {
					continue
				}
				filtered = append(filtered, item)
			}
			if len(filtered) == 0 {
				delete(service, "depends_on")
			} else {
				service["depends_on"] = filtered
			}
		case map[string]any:
			delete(dependsOn, name)
			if len(dependsOn) == 0 {
				delete(service, "depends_on")
			}
		}
	}
}

func (c *ComposeProject) usedResources(section string) map[string]bool {
	used := map[string]bool{}
	for _, rawService := range c.services() {
		service, ok := rawService.(map[string]any)
		if !ok {
			continue
		}
		switch section {
		case "volumes":
			for _, name := range serviceVolumeRefs(service["volumes"]) {
				used[name] = true
			}
		case "networks":
			for _, name := range serviceNetworkRefs(service["networks"]) {
				used[name] = true
			}
		case "secrets":
			for _, name := range serviceObjectRefs(service["secrets"]) {
				used[name] = true
			}
		case "configs":
			for _, name := range serviceObjectRefs(service["configs"]) {
				used[name] = true
			}
		}
	}
	return used
}

func serviceVolumeRefs(raw any) []string {
	items, ok := raw.([]any)
	if !ok {
		return nil
	}
	names := []string{}
	for _, item := range items {
		switch volume := item.(type) {
		case string:
			source := strings.SplitN(volume, ":", 2)[0]
			if isNamedVolume(source) {
				names = append(names, source)
			}
		case map[string]any:
			volumeType, _ := volume["type"].(string)
			source, _ := volume["source"].(string)
			if source == "" {
				continue
			}
			if volumeType == "" || volumeType == "volume" {
				names = append(names, source)
			}
		}
	}
	return names
}

func serviceNetworkRefs(raw any) []string {
	switch networks := raw.(type) {
	case []any:
		names := []string{}
		for _, item := range networks {
			if name, ok := item.(string); ok {
				names = append(names, name)
			}
		}
		return names
	case map[string]any:
		names := make([]string, 0, len(networks))
		for name := range networks {
			names = append(names, name)
		}
		return names
	default:
		return nil
	}
}

func serviceObjectRefs(raw any) []string {
	items, ok := raw.([]any)
	if !ok {
		return nil
	}
	names := []string{}
	for _, item := range items {
		switch value := item.(type) {
		case string:
			names = append(names, value)
		case map[string]any:
			if source, ok := value["source"].(string); ok && source != "" {
				names = append(names, source)
			}
		}
	}
	return names
}

func isNamedVolume(source string) bool {
	if source == "" {
		return false
	}
	if strings.HasPrefix(source, "/") || strings.HasPrefix(source, "./") || strings.HasPrefix(source, "../") || strings.HasPrefix(source, "~/") {
		return false
	}
	if filepath.IsAbs(source) {
		return false
	}
	return true
}

func nestedMap(value any) map[string]any {
	if value == nil {
		return nil
	}
	if m, ok := value.(map[string]any); ok {
		return m
	}
	return nil
}

func mergeIntoSection(root map[string]any, key string, entries map[string]any) {
	if len(entries) == 0 {
		return
	}
	target := nestedMap(root[key])
	if target == nil {
		target = map[string]any{}
	}
	for name, value := range entries {
		target[name] = value
	}
	root[key] = target
}
