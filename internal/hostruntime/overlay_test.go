package hostruntime

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

func TestMountOverlaysWithoutVolumesIsNoOp(t *testing.T) {
	options := OverlayOptions{
		VolumesRoot: filepath.Join(t.TempDir(), "missing-volumes"),
		LowerRoot:   filepath.Join(t.TempDir(), "missing-lower"),
	}
	if err := MountOverlays(context.Background(), options); err != nil {
		t.Fatalf("MountOverlays() error = %v", err)
	}
}

func TestEnsureOverlayDirectoryRejectsSymlinkTraversal(t *testing.T) {
	root := t.TempDir()
	outside := t.TempDir()
	if err := os.Symlink(outside, filepath.Join(root, "redirect")); err != nil {
		t.Fatal(err)
	}
	if err := ensureOverlayDirectory(filepath.Join(root, "redirect", "upper"), root); err == nil {
		t.Fatal("expected symlink traversal to fail")
	}
	if _, err := os.Stat(filepath.Join(outside, "upper")); !os.IsNotExist(err) {
		t.Fatal("directory was created through the rejected symlink")
	}
}

func TestEmptyOverlayDirectoryRemovesOnlyChildren(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "state"), []byte("data"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := emptyOverlayDirectory(root); err != nil {
		t.Fatal(err)
	}
	entries, err := os.ReadDir(root)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 0 {
		t.Fatalf("entries = %v", entries)
	}
}

func TestVolumeNamePattern(t *testing.T) {
	for _, value := range []string{"mariadb_data", "site-1.cache"} {
		if !volumeNamePattern.MatchString(value) {
			t.Fatalf("expected %q to pass", value)
		}
	}
	for _, value := range []string{"", "../escape", "space name"} {
		if volumeNamePattern.MatchString(value) {
			t.Fatalf("expected %q to fail", value)
		}
	}
}
