package hostruntime

import (
	"os"
	"path/filepath"
	"testing"
)

func TestScaffoldApplicationDefaultsCreatesDeclaredSecrets(t *testing.T) {
	project := t.TempDir()
	compose := `secrets:
  password:
    file: ./secrets/PASSWORD
  salt:
    file: ./secrets/DRUPAL_DEFAULT_SALT
  uid:
    file: ./secrets/UID
services:
  app:
    image: example
`
	if err := os.WriteFile(filepath.Join(project, "compose.yaml"), []byte(compose), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := scaffoldApplicationDefaults(Application{ProjectDir: project}); err != nil {
		t.Fatalf("scaffoldApplicationDefaults() error = %v", err)
	}
	for _, name := range []string{"PASSWORD", "DRUPAL_DEFAULT_SALT", "UID"} {
		info, err := os.Stat(filepath.Join(project, "secrets", name))
		if err != nil || info.Size() == 0 {
			t.Fatalf("secret %s was not created: %v", name, err)
		}
	}
	password := filepath.Join(project, "secrets", "PASSWORD")
	before, _ := os.ReadFile(password)
	if err := scaffoldApplicationDefaults(Application{ProjectDir: project}); err != nil {
		t.Fatal(err)
	}
	after, _ := os.ReadFile(password)
	if string(after) != string(before) {
		t.Fatal("existing secret changed during idempotent scaffold")
	}
}

func TestComposeSecretFilesRejectsEscape(t *testing.T) {
	project := t.TempDir()
	compose := "secrets:\n  password:\n    file: ../../outside\n"
	if err := os.WriteFile(filepath.Join(project, "compose.yaml"), []byte(compose), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := composeSecretFiles(project); err == nil {
		t.Fatal("expected escaped secret path to fail")
	}
}
