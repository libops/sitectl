package hostruntime

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestMigrateLegacyUnitsPreservesUnrelatedCollision(t *testing.T) {
	directory := t.TempDir()
	path := filepath.Join(directory, "vault-agent.service")
	if err := os.WriteFile(path, []byte("ExecStart=/usr/bin/vault agent -config=/operator.hcl\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := MigrateLegacyUnits(context.Background(), directory, nil, nil); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatal("unrelated unit was removed")
	}
}

func TestMigrateLegacyUnitsRemovesExactUnit(t *testing.T) {
	directory := t.TempDir()
	path := filepath.Join(directory, "internal-services.service")
	contents := "Description=Internal Services (Ping, Metrics, Power Management)\nWorkingDirectory=/mnt/disks/data/libops-internal\n"
	if err := os.WriteFile(path, []byte(contents), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", t.TempDir())
	if err := MigrateLegacyUnits(context.Background(), directory, nil, nil); err != nil && !strings.Contains(err.Error(), "systemctl") {
		t.Fatal(err)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatal("exact legacy unit remains")
	}
}
