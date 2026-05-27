package cron

import (
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

type ComponentSpec struct {
	Name        string `yaml:"name"`
	Plugin      string `yaml:"plugin"`
	Description string `yaml:"description,omitempty"`
	Filename    string `yaml:"filename"`
}

func PruneArtifacts(root string, now time.Time, retentionDays int, preserveFirstOfMonth bool) error {
	if retentionDays <= 0 {
		return nil
	}
	root = filepath.Clean(root)
	rootFS, err := os.OpenRoot(root)
	if err != nil {
		return err
	}
	defer rootFS.Close()

	err = filepath.WalkDir(root, func(path string, d os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if d.IsDir() {
			return nil
		}
		info, err := d.Info()
		if err != nil {
			return err
		}
		if ShouldDeleteArtifact(info.ModTime(), now, retentionDays, preserveFirstOfMonth) {
			rel, err := filepath.Rel(root, path)
			if err != nil {
				return err
			}
			if err := rootFS.Remove(rel); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		return err
	}

	return removeEmptyDirs(root, rootFS)
}

func removeEmptyDirs(root string, rootFS *os.Root) error {
	var dirs []string
	if err := filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			dirs = append(dirs, path)
		}
		return nil
	}); err != nil {
		return err
	}

	sort.Slice(dirs, func(i, j int) bool { return len(dirs[i]) > len(dirs[j]) })
	for _, dir := range dirs {
		if dir == root {
			continue
		}
		rel, err := filepath.Rel(root, dir)
		if err != nil {
			return err
		}
		entries, err := fs.ReadDir(rootFS.FS(), rel)
		if err != nil {
			return err
		}
		if len(entries) == 0 {
			if err := rootFS.Remove(rel); err != nil {
				return err
			}
		}
	}
	return nil
}

func ShouldDeleteArtifact(modTime, now time.Time, retentionDays int, preserveFirstOfMonth bool) bool {
	if retentionDays <= 0 {
		return false
	}
	if preserveFirstOfMonth && modTime.UTC().Day() == 1 {
		return false
	}
	return now.Sub(modTime) > time.Duration(retentionDays)*24*time.Hour
}

func EnsureDatedDestination(root string, now time.Time) (string, error) {
	out := filepath.Join(root, now.Format("2006"), now.Format("01"), now.Format("02"))
	if err := os.MkdirAll(out, 0o750); err != nil {
		return "", err
	}
	return out, nil
}

func ValidateComponents(specs []ComponentSpec) error {
	seen := map[string]bool{}
	for _, spec := range specs {
		if strings.TrimSpace(spec.Name) == "" {
			return fmt.Errorf("cron component name is required")
		}
		if strings.TrimSpace(spec.Plugin) == "" {
			return fmt.Errorf("cron component %q plugin is required", spec.Name)
		}
		if strings.TrimSpace(spec.Filename) == "" {
			return fmt.Errorf("cron component %q filename is required", spec.Name)
		}
		if seen[spec.Name] {
			return fmt.Errorf("duplicate cron component %q", spec.Name)
		}
		seen[spec.Name] = true
	}
	return nil
}

func SortComponents(specs []ComponentSpec) {
	sort.Slice(specs, func(i, j int) bool {
		if specs[i].Plugin != specs[j].Plugin {
			return specs[i].Plugin < specs[j].Plugin
		}
		return specs[i].Name < specs[j].Name
	})
}

func FindComponent(specs []ComponentSpec, name string) (ComponentSpec, bool) {
	for _, spec := range specs {
		if strings.EqualFold(spec.Name, name) {
			return spec, true
		}
	}
	return ComponentSpec{}, false
}
