package component

import (
	"bytes"
	"fmt"
	"path/filepath"
	"sort"
	"strings"

	"github.com/libops/sitectl/pkg/config"
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
		doc, err := LoadYAMLDocument(data)
		if err != nil {
			return fmt.Errorf("unmarshal drupal config file %q: %w", name, err)
		}

		matchExpression := matchingMapEntryExpression(match)
		variables := map[string]any{
			"key":   match.Key,
			"value": match.Value,
		}
		context, err := doc.evaluateWithValues(matchExpression, false, variables)
		if err != nil {
			return fmt.Errorf("find drupal config map entries in %q: %w", name, err)
		}
		if context.MatchingNodes.Len() == 0 {
			continue
		}
		if _, err := doc.evaluateWithValues(`del((`+matchExpression+`))`, false, variables); err != nil {
			return fmt.Errorf("delete drupal config map entries in %q: %w", name, err)
		}
		updated, err := doc.Bytes()
		if err != nil {
			return fmt.Errorf("marshal drupal config file %q: %w", name, err)
		}
		d.files[name] = updated
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

func matchingMapEntryExpression(match MapEntryMatch) string {
	path := `.. | select(kind == "map") | .[] | select((key | to_string | @base64) == ($key | @base64))`
	if match.Value == "" {
		return path + ` | select(kind != "scalar" or (to_string | trim | @base64) == ($value | @base64))`
	}
	return path + ` | select(kind == "scalar" and (to_string | trim | @base64) == ($value | @base64))`
}
