package hostruntime

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"
)

func TestRunMariaDBBackupsPublishesValidArtifact(t *testing.T) {
	root := t.TempDir()
	bin := filepath.Join(root, "bin")
	if err := os.Mkdir(bin, 0o755); err != nil {
		t.Fatal(err)
	}
	writeExecutable(t, filepath.Join(bin, "systemctl"), "#!/bin/sh\nexit 0\n")
	writeExecutable(t, filepath.Join(bin, "sitectl"), "#!/bin/sh\nwhile [ $# -gt 0 ]; do if [ \"$1\" = --output ]; then shift; printf dump | gzip >\"$1\"; exit; fi; shift; done\nexit 2\n")
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	manifest := Manifest{"app": {Name: "app", SitectlContextName: "app"}}
	options := BackupOptions{
		Root: filepath.Join(root, "backups"), RetentionDays: 14, LockPath: filepath.Join(root, "lifecycle.lock"),
		Now: func() time.Time { return time.Date(2026, 8, 23, 0, 0, 0, 0, time.UTC) }, Sitectl: filepath.Join(bin, "sitectl"),
	}
	if err := RunMariaDBBackups(context.Background(), manifest, options); err != nil {
		t.Fatalf("RunMariaDBBackups() error = %v", err)
	}
	artifact := filepath.Join(options.Root, "app", "20260823-app.sql.gz")
	if !validGzip(artifact) {
		t.Fatalf("backup is not a valid gzip artifact: %s", artifact)
	}
}

func TestBuildCoverageRejectsBindOutsideManagedRoots(t *testing.T) {
	config := composeConfig{Services: map[string]struct {
		Volumes []composeMount `json:"volumes"`
	}{"web": {Volumes: []composeMount{{Type: "bind", Source: "/etc", Target: "/data"}}}}}
	dump := filepath.Join(t.TempDir(), "dump.sql.gz")
	writeGzip(t, dump)
	_, err := buildCoverage(Application{Name: "app", ProjectDir: "/mnt/disks/data/app"}, config, dump, BackupOptions{DataRoot: "/mnt/disks/data", VolumesRoot: "/mnt/disks/volumes"})
	if err == nil {
		t.Fatal("expected unmanaged bind source to fail")
	}
}

func TestValidateBackupReceiptRejectsIncompleteCoverage(t *testing.T) {
	path := filepath.Join(t.TempDir(), "receipt.json")
	receipt := map[string]any{
		"schema_version": 1, "kind": "cloud-compose.offhost-backup-receipt", "operation_id": "op",
		"manifest_sha256": "digest", "status": "succeeded", "encrypted": true, "off_host": true,
		"completed_at": "2026-08-23T00:00:00Z", "remote_id": "remote", "coverage": map[string]bool{"database": true},
	}
	contents, _ := json.Marshal(receipt)
	if err := os.WriteFile(path, contents, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := validateBackupReceipt(path, "op", "digest"); err == nil {
		t.Fatal("expected incomplete coverage to fail")
	}
}

func writeGzip(t *testing.T, path string) {
	t.Helper()
	bin := t.TempDir()
	script := filepath.Join(bin, "write")
	writeExecutable(t, script, "#!/bin/sh\nprintf dump | gzip >\"$1\"\n")
	command := exec.Command(script, path)
	if err := command.Run(); err != nil {
		t.Fatal(err)
	}
}
