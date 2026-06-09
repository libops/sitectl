package component

import (
	"fmt"
	"log/slog"
	"path/filepath"
	"sort"
	"strings"

	yaml "gopkg.in/yaml.v3"
)

// ComposeProject is an editable Docker Compose document.
type ComposeProject struct {
	doc       *YAMLDocument
	root      map[string]any
	rootDirty bool
}

// ComposeDefinitions contains reusable Docker Compose sections owned by a
// service component.
type ComposeDefinitions struct {
	Services map[string]any `json:"services,omitempty" yaml:"services,omitempty"`
	Networks map[string]any `json:"networks,omitempty" yaml:"networks,omitempty"`
	Volumes  map[string]any `json:"volumes,omitempty" yaml:"volumes,omitempty"`
	Secrets  map[string]any `json:"secrets,omitempty" yaml:"secrets,omitempty"`
	Configs  map[string]any `json:"configs,omitempty" yaml:"configs,omitempty"`
}

// ParseComposeProject parses a full Docker Compose document.
func ParseComposeProject(data []byte) (*ComposeProject, error) {
	doc, err := LoadYAMLDocument(data)
	if err != nil {
		return nil, fmt.Errorf("load compose yaml: %w", err)
	}
	return &ComposeProject{doc: doc, rootDirty: true}, nil
}

// ParseComposeDefinitions parses service component compose definitions.
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

// Bytes marshals the compose project back to YAML.
func (c *ComposeProject) Bytes() ([]byte, error) {
	if c != nil && c.doc != nil {
		return c.doc.Bytes()
	}
	data, err := yaml.Marshal(c.root)
	if err != nil {
		return nil, fmt.Errorf("marshal compose yaml: %w", err)
	}
	return data, nil
}

// RemoveService removes a service and any references to it from depends_on.
func (c *ComposeProject) RemoveService(name string) bool {
	services, err := c.services()
	if err != nil {
		slog.Warn("read compose services", "service", name, "error", err)
		return false
	}
	if services == nil {
		return false
	}
	if _, ok := services[name]; !ok {
		return false
	}

	c.applyDocumentMutationBestEffort(func(doc *YAMLDocument) {
		deleteDocumentSectionEntry(doc, "services", name)
		removeDocumentServiceDependencyReferences(doc, name)
	})
	return true
}

// AddDefinitions merges service component definitions into the project.
func (c *ComposeProject) AddDefinitions(defs *ComposeDefinitions) {
	if defs == nil {
		return
	}
	c.applyDocumentMutationBestEffort(func(doc *YAMLDocument) {
		mergeIntoDocumentSection(doc, "services", defs.Services)
		mergeIntoDocumentSection(doc, "networks", defs.Networks)
		mergeIntoDocumentSection(doc, "volumes", defs.Volumes)
		mergeIntoDocumentSection(doc, "secrets", defs.Secrets)
		mergeIntoDocumentSection(doc, "configs", defs.Configs)
	})
}

// ApplyRules applies YAML rules against the compose project.
func (c *ComposeProject) ApplyRules(rules []YAMLRule) error {
	if c == nil || len(rules) == 0 {
		return nil
	}
	for _, rule := range rules {
		switch rule.Op {
		case OpSet, OpRestore:
			err := c.applyDocumentMutation(func(doc *YAMLDocument) error {
				return doc.SetValue(rule.Path, rule.Value)
			})
			if err != nil {
				return fmt.Errorf("apply compose rule %q at %q: %w", rule.Op, rule.Path, err)
			}
		case OpDelete:
			err := c.applyDocumentMutation(func(doc *YAMLDocument) error {
				return doc.DeletePath(rule.Path)
			})
			if err != nil {
				return fmt.Errorf("apply compose rule %q at %q: %w", rule.Op, rule.Path, err)
			}
		default:
			return fmt.Errorf("apply compose rule %q at %q: unsupported operation", rule.Op, rule.Path)
		}
	}
	return nil
}

// SetDefinition sets one top-level compose section entry.
func (c *ComposeProject) SetDefinition(section, name string, value any) {
	if c == nil || strings.TrimSpace(section) == "" || strings.TrimSpace(name) == "" {
		return
	}
	c.applyDocumentMutationBestEffort(func(doc *YAMLDocument) {
		setDocumentSectionEntry(doc, section, name, value)
	})
}

// DeleteDefinition removes one top-level compose section entry.
func (c *ComposeProject) DeleteDefinition(section, name string) bool {
	if c == nil || strings.TrimSpace(section) == "" || strings.TrimSpace(name) == "" {
		return false
	}
	root, err := c.rootMap()
	if err != nil {
		slog.Warn("read compose definitions", "section", section, "name", name, "error", err)
		return false
	}
	target := nestedMap(root[section])
	if target == nil {
		return false
	}
	if _, ok := target[name]; !ok {
		return false
	}
	c.applyDocumentMutationBestEffort(func(doc *YAMLDocument) {
		deleteDocumentSectionEntry(doc, section, name)
	})
	return true
}

// Definition returns one compose definition by section and name.
func (d *ComposeDefinitions) Definition(section, name string) (any, bool) {
	if d == nil {
		return nil, false
	}
	entries := d.section(section)
	if entries == nil {
		return nil, false
	}
	value, ok := entries[name]
	return value, ok
}

func (d *ComposeDefinitions) section(section string) map[string]any {
	switch section {
	case "services":
		return d.Services
	case "networks":
		return d.Networks
	case "volumes":
		return d.Volumes
	case "secrets":
		return d.Secrets
	case "configs":
		return d.Configs
	default:
		return nil
	}
}

// PruneUnusedResources removes unused volumes, networks, secrets, and configs.
func (c *ComposeProject) PruneUnusedResources() {
	root, err := c.rootMap()
	if err != nil {
		slog.Warn("read compose resources", "error", err)
		return
	}
	removals := map[string][]string{}
	for _, section := range []string{"volumes", "networks", "secrets", "configs"} {
		entries := nestedMap(root[section])
		if len(entries) == 0 {
			continue
		}

		used := usedResources(root, section)
		for name := range entries {
			if !used[name] {
				removals[section] = append(removals[section], name)
			}
		}
	}
	if len(removals) == 0 {
		return
	}
	c.applyDocumentMutationBestEffort(func(doc *YAMLDocument) {
		for section, names := range removals {
			for _, name := range names {
				deleteDocumentSectionEntry(doc, section, name)
			}
		}
	})
}

func (c *ComposeProject) services() (map[string]any, error) {
	root, err := c.rootMap()
	if err != nil {
		return nil, err
	}
	services := nestedMap(root["services"])
	if services == nil {
		return nil, nil
	}
	root["services"] = services
	return services, nil
}

func (c *ComposeProject) applyDocumentMutation(mutate func(*YAMLDocument) error) error {
	if c == nil || mutate == nil {
		return nil
	}
	if c.doc == nil {
		doc, err := LoadYAMLDocument(nil)
		if err != nil {
			return fmt.Errorf("load empty compose document: %w", err)
		}
		c.doc = doc
	}
	if err := mutate(c.doc); err != nil {
		return err
	}
	c.rootDirty = true
	return nil
}

func (c *ComposeProject) applyDocumentMutationBestEffort(mutate func(*YAMLDocument)) {
	if err := c.applyDocumentMutation(func(doc *YAMLDocument) error {
		mutate(doc)
		return nil
	}); err != nil {
		slog.Warn("apply compose document mutation", "error", err)
	}
}

func (c *ComposeProject) rootMap() (map[string]any, error) {
	if c == nil {
		return nil, nil
	}
	if !c.rootDirty && c.root != nil {
		return c.root, nil
	}
	if c.doc == nil {
		if c.root == nil {
			c.root = map[string]any{}
		}
		c.rootDirty = false
		return c.root, nil
	}
	data, err := c.doc.Bytes()
	if err != nil {
		return nil, fmt.Errorf("marshal compose document: %w", err)
	}
	root := map[string]any{}
	if err := yaml.Unmarshal(data, &root); err != nil {
		return nil, fmt.Errorf("unmarshal compose document: %w", err)
	}
	c.root = root
	c.rootDirty = false
	return c.root, nil
}

func usedResources(root map[string]any, section string) map[string]bool {
	used := map[string]bool{}
	for _, rawService := range nestedMap(root["services"]) {
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

func mergeIntoDocumentSection(doc *YAMLDocument, section string, entries map[string]any) {
	if doc == nil || len(entries) == 0 {
		return
	}
	target := ensureDocumentSection(doc, section)
	for _, name := range sortedComposeDefinitionNames(entries) {
		valueNode, err := yamlNodeForValue(entries[name])
		if err != nil {
			continue
		}
		setMappingValue(target, name, valueNode)
	}
}

func setDocumentSectionEntry(doc *YAMLDocument, section, name string, value any) {
	if doc == nil || strings.TrimSpace(section) == "" || strings.TrimSpace(name) == "" {
		return
	}
	valueNode, err := yamlNodeForValue(value)
	if err != nil {
		return
	}
	setMappingValue(ensureDocumentSection(doc, section), name, valueNode)
}

func deleteDocumentSectionEntry(doc *YAMLDocument, section, name string) bool {
	if doc == nil || strings.TrimSpace(section) == "" || strings.TrimSpace(name) == "" {
		return false
	}
	root := doc.mappingRoot()
	if root == nil {
		return false
	}
	target := mappingValue(root, section)
	if target == nil || target.Kind != yaml.MappingNode || mappingValue(target, name) == nil {
		return false
	}
	deleteMappingValue(target, name)
	if len(target.Content) == 0 {
		deleteMappingValue(root, section)
	}
	return true
}

func ensureDocumentSection(doc *YAMLDocument, section string) *yaml.Node {
	root := doc.ensureMappingRoot()
	target := mappingValue(root, section)
	if target == nil {
		target = &yaml.Node{
			Kind:    yaml.MappingNode,
			Tag:     "!!map",
			Content: []*yaml.Node{},
		}
		setMappingValue(root, section, target)
	}
	if target.Kind != yaml.MappingNode {
		target.Kind = yaml.MappingNode
		target.Tag = "!!map"
		target.Content = []*yaml.Node{}
	}
	return target
}

func sortedComposeDefinitionNames(entries map[string]any) []string {
	names := make([]string, 0, len(entries))
	for name := range entries {
		if strings.TrimSpace(name) != "" {
			names = append(names, name)
		}
	}
	sort.Strings(names)
	return names
}

func removeDocumentServiceDependencyReferences(doc *YAMLDocument, name string) {
	if doc == nil {
		return
	}
	root := doc.mappingRoot()
	if root == nil {
		return
	}
	services := mappingValue(root, "services")
	if services == nil || services.Kind != yaml.MappingNode {
		return
	}
	for i := 1; i < len(services.Content); i += 2 {
		service := services.Content[i]
		if service == nil || service.Kind != yaml.MappingNode {
			continue
		}
		dependsOn := mappingValue(service, "depends_on")
		switch {
		case dependsOn == nil:
			continue
		case dependsOn.Kind == yaml.SequenceNode:
			filtered := make([]*yaml.Node, 0, len(dependsOn.Content))
			for _, item := range dependsOn.Content {
				if item != nil && item.Kind == yaml.ScalarNode && item.Value == name {
					continue
				}
				filtered = append(filtered, item)
			}
			if len(filtered) == 0 {
				deleteMappingValue(service, "depends_on")
			} else {
				dependsOn.Content = filtered
			}
		case dependsOn.Kind == yaml.MappingNode:
			deleteMappingValue(dependsOn, name)
			if len(dependsOn.Content) == 0 {
				deleteMappingValue(service, "depends_on")
			}
		}
	}
}
