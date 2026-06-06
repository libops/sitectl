package component

import (
	"path/filepath"
	"strings"

	"github.com/spf13/cobra"
)

const DefaultDrupalRootfs = "."

// CodebaseRootfsFlagAnnotation marks a command flag as the CLI flag that
// receives the generic codebase_rootfs RPC parameter.
const CodebaseRootfsFlagAnnotation = "sitectl.libops.dev/codebase-rootfs-flag"

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

// MarkCodebaseRootfsFlag marks flagName as the command's codebase rootfs flag
// for RPC argv reconstruction.
func MarkCodebaseRootfsFlag(cmd *cobra.Command, flagName string) {
	if cmd == nil {
		return
	}
	flagName = strings.TrimPrefix(strings.TrimSpace(flagName), "--")
	if flagName == "" {
		return
	}
	flag := cmd.Flags().Lookup(flagName)
	if flag == nil {
		return
	}
	if flag.Annotations == nil {
		flag.Annotations = map[string][]string{}
	}
	flag.Annotations[CodebaseRootfsFlagAnnotation] = []string{"true"}
}

// AddDrupalRootfsFlag adds the standard Drupal rootfs flag to cmd.
func AddDrupalRootfsFlag(cmd *cobra.Command, target *string, defaultValue string) {
	if defaultValue == "" {
		defaultValue = DefaultDrupalRootfs
	}
	cmd.Flags().StringVar(target, "drupal-rootfs", defaultValue, "Drupal rootfs relative to --path. Used to resolve composer.json and config/sync")
	MarkCodebaseRootfsFlag(cmd, "drupal-rootfs")
}
