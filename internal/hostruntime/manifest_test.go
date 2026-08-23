package hostruntime

import (
	"os"
	"path/filepath"
	"testing"
)

func TestLoadManifestValidatesAndSortsApplications(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "apps.json")
	contents := `{
  "zeta":{"name":"zeta","docker_compose_repo":"https://github.com/libops/zeta","docker_compose_branch":"main","project_dir":"` + filepath.Join(root, "zeta") + `","compose_project_name":"zeta","sitectl_context_name":"zeta"},
  "alpha":{"name":"alpha","docker_compose_repo":"https://github.com/libops/alpha","docker_compose_branch":"main","project_dir":"` + filepath.Join(root, "alpha") + `","compose_project_name":"alpha","sitectl_context_name":"alpha"}
}`
	if err := os.WriteFile(path, []byte(contents), 0o600); err != nil {
		t.Fatal(err)
	}
	manifest, err := LoadManifest(path, root)
	if err != nil {
		t.Fatalf("LoadManifest() error = %v", err)
	}
	names := manifest.Names()
	if len(names) != 2 || names[0] != "alpha" || names[1] != "zeta" {
		t.Fatalf("Names() = %v", names)
	}
}

func TestLoadManifestRejectsProjectOutsideDataRoot(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "apps.json")
	contents := `{"app":{"name":"app","docker_compose_repo":"https://example.com/app","docker_compose_branch":"main","project_dir":"/tmp/app","compose_project_name":"app","sitectl_context_name":"app"}}`
	if err := os.WriteFile(path, []byte(contents), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadManifest(path, root); err == nil {
		t.Fatal("expected an out-of-bound project directory to fail")
	}
}

func TestLoadManifestRejectsSymlink(t *testing.T) {
	root := t.TempDir()
	target := filepath.Join(root, "target")
	if err := os.WriteFile(target, []byte(`{}`), 0o600); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(root, "apps.json")
	if err := os.Symlink(target, path); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadManifest(path, root); err == nil {
		t.Fatal("expected a symlink manifest to fail")
	}
}

func TestApplicationCommands(t *testing.T) {
	app := Application{InitCommands: []string{"init"}, RolloutCommands: []string{"rollout"}}
	commands, err := app.Commands("rollout")
	if err != nil || len(commands) != 1 || commands[0] != "rollout" {
		t.Fatalf("Commands() = %v, %v", commands, err)
	}
	if _, err := app.Commands("invalid"); err == nil {
		t.Fatal("expected invalid lifecycle to fail")
	}
}
