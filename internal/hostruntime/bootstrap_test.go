package hostruntime

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestPublishAndValidateMarker(t *testing.T) {
	directory := t.TempDir()
	path := filepath.Join(directory, "ready")
	uid, gid := uint32(os.Geteuid()), uint32(os.Getegid())
	if err := publishMarkerOwned(path, true, uid, gid); err != nil {
		t.Fatal(err)
	}
	if !markerValidFor(path, true, uid, gid) {
		t.Fatal("published marker is invalid")
	}
	if err := os.WriteFile(path, []byte("wrong\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if markerValidFor(path, true, uid, gid) {
		t.Fatal("invalid payload reported ready")
	}
}

func TestConsumeFreshMarkerChecksIdentity(t *testing.T) {
	root := t.TempDir()
	directory := filepath.Join(root, ".cloud-compose")
	if err := os.Mkdir(directory, 0o700); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(directory, "fresh-filesystem")
	if err := os.WriteFile(path, []byte("fresh\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	uid, gid := uint32(os.Geteuid()), uint32(os.Getegid())
	if err := consumeFreshMarkerOwned(path, "v1:gcp-disk-id:2", uid, gid); err == nil {
		t.Fatal("expected identity mismatch")
	}
	if err := consumeFreshMarkerOwned(path, "fresh", uid, gid); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Lstat(path); !os.IsNotExist(err) {
		t.Fatal("fresh marker was not consumed")
	}
}

func TestValidateSystemdOptionsRejectsUnrelatedUnits(t *testing.T) {
	if err := validateSystemdOptions(systemdDefaults(SystemdOptions{Unit: "ssh.service"})); err == nil {
		t.Fatal("expected unrelated unit to fail")
	}
	if err := validateSystemdOptions(SystemdOptions{Unit: "cloud-compose.service", Timeout: time.Second, Poll: time.Second, Heartbeat: time.Second}); err != nil {
		t.Fatal(err)
	}
}
