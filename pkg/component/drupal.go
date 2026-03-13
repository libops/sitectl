package component

import (
	"bytes"
	"fmt"
	"path/filepath"
	"sort"
	"strings"

	"github.com/libops/sitectl/pkg/config"
	yaml "gopkg.in/yaml.v3"
)

type DrupalConfigSet struct {
	root  string
	files map[string][]byte
}

type StringReplacement struct {
	Old string
	New string
}

type MapEntryMatch struct {
	Key   string
	Value string
}

func LoadDrupalConfigSet(ctx *config.Context, root string) (*DrupalConfigSet, error) {
	files, err := ctx.ListFiles(root)
	if err != nil {
		return nil, fmt.Errorf("list drupal config files: %w", err)
	}

	configFiles := map[string][]byte{}
	for _, rel := range files {
		if filepath.Ext(rel) != ".yml" && filepath.Ext(rel) != ".yaml" {
			continue
		}
		fullPath := filepath.Join(root, rel)
		data, err := ctx.ReadFile(fullPath)
		if err != nil {
			return nil, fmt.Errorf("read drupal config file %q: %w", fullPath, err)
		}
		configFiles[filepath.ToSlash(rel)] = data
	}

	return &DrupalConfigSet{
		root:  root,
		files: configFiles,
	}, nil
}

func NewDrupalConfigSet(root string, files map[string][]byte) *DrupalConfigSet {
	cloned := map[string][]byte{}
	for name, data := range files {
		cloned[name] = bytes.Clone(data)
	}
	return &DrupalConfigSet{root: root, files: cloned}
}

func (d *DrupalConfigSet) DeleteFiles(names ...string) {
	for _, name := range names {
		delete(d.files, filepath.ToSlash(name))
	}
}

func (d *DrupalConfigSet) UpsertFile(name string, data []byte) {
	d.files[filepath.ToSlash(name)] = bytes.Clone(data)
}

func (d *DrupalConfigSet) ReplaceString(old, new string) {
	for name, data := range d.files {
		d.files[name] = []byte(strings.ReplaceAll(string(data), old, new))
	}
}

func (d *DrupalConfigSet) DeleteMapEntries(match MapEntryMatch) error {
	for name, data := range d.files {
		var node yaml.Node
		if err := yaml.Unmarshal(data, &node); err != nil {
			return fmt.Errorf("unmarshal drupal config file %q: %w", name, err)
		}
		if deleteMapEntries(&node, match) {
			updated, err := yaml.Marshal(&node)
			if err != nil {
				return fmt.Errorf("marshal drupal config file %q: %w", name, err)
			}
			d.files[name] = updated
		}
	}
	return nil
}

func (d *DrupalConfigSet) Save(ctx *config.Context) error {
	existing, err := ctx.ListFiles(d.root)
	if err != nil {
		return fmt.Errorf("list existing drupal config files: %w", err)
	}

	existingSet := map[string]bool{}
	for _, rel := range existing {
		normalized := filepath.ToSlash(rel)
		existingSet[normalized] = true
		if _, ok := d.files[normalized]; !ok {
			if err := ctx.RemoveFile(filepath.Join(d.root, normalized)); err != nil {
				return fmt.Errorf("remove drupal config file %q: %w", normalized, err)
			}
		}
	}

	names := make([]string, 0, len(d.files))
	for name := range d.files {
		names = append(names, name)
	}
	sort.Strings(names)

	for _, name := range names {
		if err := ctx.WriteFile(filepath.Join(d.root, name), d.files[name]); err != nil {
			return fmt.Errorf("write drupal config file %q: %w", name, err)
		}
		if !existingSet[name] {
			existingSet[name] = true
		}
	}

	return nil
}

func deleteMapEntries(node *yaml.Node, match MapEntryMatch) bool {
	changed := false

	switch node.Kind {
	case yaml.DocumentNode:
		for _, child := range node.Content {
			if deleteMapEntries(child, match) {
				changed = true
			}
		}
	case yaml.MappingNode:
		filtered := make([]*yaml.Node, 0, len(node.Content))
		for i := 0; i < len(node.Content); i += 2 {
			keyNode := node.Content[i]
			valueNode := node.Content[i+1]
			if keyNode.Value == match.Key && scalarValue(valueNode) == match.Value {
				changed = true
				continue
			}
			if deleteMapEntries(valueNode, match) {
				changed = true
			}
			filtered = append(filtered, keyNode, valueNode)
		}
		if changed {
			node.Content = filtered
		}
	case yaml.SequenceNode:
		for _, child := range node.Content {
			if deleteMapEntries(child, match) {
				changed = true
			}
		}
	}

	return changed
}

func scalarValue(node *yaml.Node) string {
	if node.Kind != yaml.ScalarNode {
		return ""
	}
	return node.Value
}
