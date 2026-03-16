package component

import (
	"path/filepath"
	"testing"

	"github.com/spf13/cobra"
)

func TestResolveDrupalLayoutDefaultsToProjectRoot(t *testing.T) {
	t.Parallel()

	layout := ResolveDrupalLayout("/tmp/project", "")
	if layout.Root != filepath.Clean("/tmp/project") {
		t.Fatalf("expected project root, got %q", layout.Root)
	}
	if layout.ComposerJSONPath() != filepath.Join("/tmp/project", "composer.json") {
		t.Fatalf("unexpected composer path %q", layout.ComposerJSONPath())
	}
	if layout.ConfigSyncDir() != filepath.Join("/tmp/project", "config", "sync") {
		t.Fatalf("unexpected config path %q", layout.ConfigSyncDir())
	}
}

func TestResolveDrupalLayoutUsesRelativeRootfs(t *testing.T) {
	t.Parallel()

	layout := ResolveDrupalLayout("/tmp/project", "drupal/rootfs/var/www/drupal")
	expectedRoot := filepath.Join("/tmp/project", "drupal", "rootfs", "var", "www", "drupal")
	if layout.Root != expectedRoot {
		t.Fatalf("expected %q, got %q", expectedRoot, layout.Root)
	}
	if layout.ComposerJSONPath() != filepath.Join(expectedRoot, "composer.json") {
		t.Fatalf("unexpected composer path %q", layout.ComposerJSONPath())
	}
	if layout.ConfigSyncDir() != filepath.Join(expectedRoot, "config", "sync") {
		t.Fatalf("unexpected config path %q", layout.ConfigSyncDir())
	}
}

func TestAddDrupalRootfsFlagUsesDefault(t *testing.T) {
	t.Parallel()

	var value string
	cmd := &cobra.Command{Use: "test"}
	AddDrupalRootfsFlag(cmd, &value, "")

	flag := cmd.Flags().Lookup("drupal-rootfs")
	if flag == nil {
		t.Fatal("expected drupal-rootfs flag")
		return
	}
	if flag.DefValue != DefaultDrupalRootfs {
		t.Fatalf("expected default %q, got %q", DefaultDrupalRootfs, flag.DefValue)
	}
}
