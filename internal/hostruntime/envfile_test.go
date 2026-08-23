package hostruntime

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestSetRuntimeEnvReplacesExactName(t *testing.T) {
	path := filepath.Join(t.TempDir(), ".env")
	if err := os.WriteFile(path, []byte("A=\"old\"\nAB=\"keep\"\n"), 0o640); err != nil {
		t.Fatal(err)
	}
	if err := SetRuntimeEnv(path, "A", "new$value"); err != nil {
		t.Fatal(err)
	}
	contents, _ := os.ReadFile(path)
	if string(contents) != "AB=\"keep\"\nA=\"new$$value\"\n" {
		t.Fatalf("contents = %q", contents)
	}
}

func TestSyncComposeEnvRemovesStaleManagedValues(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, ".env")
	data := filepath.Join(root, "application.json")
	_ = os.WriteFile(path, []byte("USER_VALUE=yes\n# cloud-compose application: OLD\nOLD=\"gone\"\n"), 0o640)
	_ = os.WriteFile(data, []byte(`{"NEW":"value"}`), 0o640)
	if err := SyncComposeEnv(path, data); err != nil {
		t.Fatal(err)
	}
	contents, _ := os.ReadFile(path)
	if strings.Contains(string(contents), "OLD") || !strings.Contains(string(contents), "USER_VALUE=yes") || !strings.Contains(string(contents), "NEW=\"value\"") {
		t.Fatalf("contents = %q", contents)
	}
}
