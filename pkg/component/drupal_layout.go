package component

import (
	"path/filepath"

	"github.com/spf13/cobra"
)

const DefaultDrupalRootfs = "."

type DrupalLayout struct {
	Root string
}

func ResolveDrupalLayout(projectRoot, drupalRoot string) DrupalLayout {
	root := drupalRoot
	if root == "" {
		root = DefaultDrupalRootfs
	} else if !filepath.IsAbs(root) {
		root = filepath.Join(projectRoot, root)
	}
	if root == DefaultDrupalRootfs {
		root = projectRoot
	}

	return DrupalLayout{
		Root: filepath.Clean(root),
	}
}

func (l DrupalLayout) ComposerJSONPath() string {
	return filepath.Join(l.Root, "composer.json")
}

func (l DrupalLayout) ConfigSyncDir() string {
	return filepath.Join(l.Root, "config", "sync")
}

func AddDrupalRootfsFlag(cmd *cobra.Command, target *string, defaultValue string) {
	if defaultValue == "" {
		defaultValue = DefaultDrupalRootfs
	}
	cmd.Flags().StringVar(target, "drupal-rootfs", defaultValue, "Drupal rootfs relative to --path. Used to resolve composer.json and config/sync")
}
