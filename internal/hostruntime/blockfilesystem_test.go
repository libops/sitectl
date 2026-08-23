package hostruntime

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestReconcileFstabReplacesOnlyManagedEntries(t *testing.T) {
	options := filesystemDefaults(FilesystemOptions{
		DataDevice:    "/dev/disk/by-id/data",
		VolumesDevice: "/dev/disk/by-id/volumes",
		OverlayDevice: "/dev/disk/by-id/prod",
	})
	input := strings.Join([]string{
		"UUID=root / ext4 defaults 0 1",
		fstabBeginMarker,
		"old /mnt/disks/data ext4 defaults 0 2",
		fstabEndMarker,
		"",
	}, "\n")
	got, err := reconcileFstab(input, options, "", "")
	if err != nil {
		t.Fatal(err)
	}
	for _, expected := range []string{
		"UUID=root / ext4 defaults 0 1",
		"/dev/disk/by-id/data\t/mnt/disks/data\text4",
		"/dev/disk/by-id/volumes\t/mnt/disks/volumes\text4",
		"/dev/disk/by-id/prod\t/mnt/disks/prod-readonly\text4\tro",
	} {
		if !strings.Contains(got, expected) {
			t.Fatalf("reconciled fstab missing %q:\n%s", expected, got)
		}
	}
}

func TestReconcileFstabRejectsUnmanagedMountConflict(t *testing.T) {
	options := filesystemDefaults(FilesystemOptions{DataDevice: "/dev/data", VolumesDevice: "/dev/volumes"})
	_, err := reconcileFstab("operator /mnt/disks/data ext4 defaults 0 2\n", options, "", "")
	if err == nil {
		t.Fatal("expected unmanaged target conflict")
	}
}

func TestDigitalOceanMountUsesProviderNaming(t *testing.T) {
	got, err := digitalOceanMount("/dev/disk/by-id/scsi-0DO_Volume_cc-do-isle-123-data")
	if err != nil {
		t.Fatal(err)
	}
	if got != "/mnt/cc_do_isle_123_data" {
		t.Fatalf("mount = %q", got)
	}
	if got, err := digitalOceanMount("/dev/disk/by-id/ordinary"); err != nil || got != "" {
		t.Fatalf("ordinary device = %q, %v", got, err)
	}
}

func TestUnitHasExactMountRejectsDuplicateFields(t *testing.T) {
	if !unitHasExactMount("[Mount]\nWhat=/dev/data\nWhere=/mnt/data\n", "/dev/data", "/mnt/data") {
		t.Fatal("expected exact unit to pass")
	}
	if unitHasExactMount("[Mount]\nWhat=/dev/data\nWhat=/dev/other\nWhere=/mnt/data\n", "/dev/data", "/mnt/data") {
		t.Fatal("expected duplicate What to fail")
	}
}

func TestValidateFilesystemOptionsRejectsAliasedDevices(t *testing.T) {
	options := filesystemDefaults(FilesystemOptions{DataDevice: "/dev/data", VolumesDevice: "/dev/data"})
	if err := validateFilesystemOptions(options); err == nil {
		t.Fatal("expected aliased devices to fail")
	}
	options = filesystemDefaults(FilesystemOptions{DataDevice: "/dev/../dev/data", VolumesDevice: "/dev/volumes"})
	if err := validateFilesystemOptions(options); err == nil {
		t.Fatal("expected non-canonical device path to fail")
	}
	options = filesystemDefaults(FilesystemOptions{DataDevice: "/dev/data", VolumesDevice: "/dev/volumes", VolumesMount: "/mnt/disks/data/volumes"})
	if err := validateFilesystemOptions(options); err == nil {
		t.Fatal("expected overlapping mount paths to fail")
	}
}

func TestPublishFreshMarkerRecoversSafeEmptyDirectory(t *testing.T) {
	root := t.TempDir()
	if err := os.Chmod(root, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(root, ".cloud-compose"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := publishFreshMarkerOwned(root, "fresh", uint32(os.Geteuid()), uint32(os.Getegid())); err != nil {
		t.Fatal(err)
	}
	contents, err := os.ReadFile(filepath.Join(root, ".cloud-compose", "fresh-filesystem"))
	if err != nil {
		t.Fatal(err)
	}
	if string(contents) != "fresh\n" {
		t.Fatalf("marker = %q", contents)
	}
}

func TestMkdirAllNoSymlinkDoesNotTraverse(t *testing.T) {
	root := t.TempDir()
	outside := t.TempDir()
	if err := os.Symlink(outside, filepath.Join(root, "redirect")); err != nil {
		t.Fatal(err)
	}
	if err := mkdirAllNoSymlink(filepath.Join(root, "redirect", "child"), 0o755); err == nil {
		t.Fatal("expected symlink traversal to fail")
	}
	if _, err := os.Stat(filepath.Join(outside, "child")); !os.IsNotExist(err) {
		t.Fatal("directory was created through the rejected symlink")
	}
}
