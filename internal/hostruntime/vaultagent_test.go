package hostruntime

import (
	"os"
	"path/filepath"
	"testing"
)

func TestVaultAgentEnabledIsStrict(t *testing.T) {
	for _, value := range []string{"1", "TRUE", "yes", ""} {
		if _, err := vaultAgentEnabled(value); err == nil {
			t.Fatalf("expected %q to fail", value)
		}
	}
	if enabled, err := vaultAgentEnabled("true"); err != nil || !enabled {
		t.Fatalf("true = %v, %v", enabled, err)
	}
}

func TestRequireSingleRegularRejectsSymlink(t *testing.T) {
	root := t.TempDir()
	target := filepath.Join(root, "target")
	if err := os.WriteFile(target, []byte("data"), 0o700); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(root, "link")
	if err := os.Symlink(target, link); err != nil {
		t.Fatal(err)
	}
	if err := requireSingleRegular(link, true); err == nil {
		t.Fatal("expected symlink to fail")
	}
}
