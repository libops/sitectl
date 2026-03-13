package component

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/libops/sitectl/pkg/config"
)

func TestDrupalConfigSetDeleteMapEntriesAcrossFiles(t *testing.T) {
	t.Parallel()

	set := NewDrupalConfigSet("config/sync", map[string][]byte{
		"one.yml":   []byte("roles:\n  fedoraadmin: '0'\n  editor: '1'\n"),
		"two.yml":   []byte("nested:\n  roles:\n    fedoraadmin: '0'\n"),
		"three.yml": []byte("roles:\n  editor: '1'\n"),
	})

	err := set.DeleteMapEntries(MapEntryMatch{Key: "fedoraadmin", Value: "0"})
	if err != nil {
		t.Fatalf("DeleteMapEntries() error = %v", err)
	}

	if strings.Contains(string(set.files["one.yml"]), "fedoraadmin") {
		t.Fatalf("expected fedoraadmin removed from one.yml, got:\n%s", string(set.files["one.yml"]))
	}
	if strings.Contains(string(set.files["two.yml"]), "fedoraadmin") {
		t.Fatalf("expected fedoraadmin removed from two.yml, got:\n%s", string(set.files["two.yml"]))
	}
	if !strings.Contains(string(set.files["three.yml"]), "editor") {
		t.Fatalf("expected unrelated content preserved in three.yml, got:\n%s", string(set.files["three.yml"]))
	}
}

func TestReplaceStringsTransformApplyAcrossFiles(t *testing.T) {
	t.Parallel()

	set := NewDrupalConfigSet("config/sync", map[string][]byte{
		"alpha.yml": []byte("uri: fedora\nlabel: fedora\n"),
		"beta.yml":  []byte("uri: fedora\n"),
	})

	transform := ReplaceStringsTransform{
		Replacements: []StringReplacement{
			{Old: "fedora", New: "gs-production"},
		},
	}

	if err := transform.Apply(set); err != nil {
		t.Fatalf("Apply() error = %v", err)
	}

	for name, data := range set.files {
		if strings.Contains(string(data), "fedora") {
			t.Fatalf("expected replacement applied in %s, got:\n%s", name, string(data))
		}
		if !strings.Contains(string(data), "gs-production") {
			t.Fatalf("expected new value present in %s, got:\n%s", name, string(data))
		}
	}
}

func TestDrupalConfigSetSaveRemovesDeletedFilesAndWritesUpdates(t *testing.T) {
	t.Parallel()

	projectDir := t.TempDir()
	configDir := filepath.Join(projectDir, "config", "sync")
	if err := os.MkdirAll(configDir, 0o755); err != nil {
		t.Fatalf("MkdirAll() error = %v", err)
	}

	if err := os.WriteFile(filepath.Join(configDir, "keep.yml"), []byte("uri: fedora\n"), 0o644); err != nil {
		t.Fatalf("WriteFile(keep.yml) error = %v", err)
	}
	if err := os.WriteFile(filepath.Join(configDir, "remove.yml"), []byte("remove: true\n"), 0o644); err != nil {
		t.Fatalf("WriteFile(remove.yml) error = %v", err)
	}

	ctx := &config.Context{
		DockerHostType: config.ContextLocal,
		ProjectDir:     projectDir,
	}

	set, err := LoadDrupalConfigSet(ctx, configDir)
	if err != nil {
		t.Fatalf("LoadDrupalConfigSet() error = %v", err)
	}

	set.DeleteFiles("remove.yml")
	set.UpsertFile("keep.yml", []byte("uri: gs-production\n"))
	set.UpsertFile("added.yml", []byte("enabled: true\n"))

	if err := set.Save(ctx); err != nil {
		t.Fatalf("Save() error = %v", err)
	}

	updated, err := os.ReadFile(filepath.Join(configDir, "keep.yml"))
	if err != nil {
		t.Fatalf("ReadFile(keep.yml) error = %v", err)
	}
	if string(updated) != "uri: gs-production\n" {
		t.Fatalf("expected updated keep.yml, got:\n%s", string(updated))
	}

	if _, err := os.Stat(filepath.Join(configDir, "remove.yml")); !os.IsNotExist(err) {
		t.Fatalf("expected remove.yml deleted, stat err = %v", err)
	}

	added, err := os.ReadFile(filepath.Join(configDir, "added.yml"))
	if err != nil {
		t.Fatalf("ReadFile(added.yml) error = %v", err)
	}
	if string(added) != "enabled: true\n" {
		t.Fatalf("expected added.yml written, got:\n%s", string(added))
	}
}

func TestDeleteMapEntriesTransformApply(t *testing.T) {
	t.Parallel()

	set := NewDrupalConfigSet("config/sync", map[string][]byte{
		"example.yml": []byte("roles:\n  fedoraadmin: '0'\n  manager: '1'\n"),
	})

	transform := DeleteMapEntriesTransform{
		Matches: []MapEntryMatch{
			{Key: "fedoraadmin", Value: "0"},
		},
	}

	if err := transform.Apply(set); err != nil {
		t.Fatalf("Apply() error = %v", err)
	}

	result := string(set.files["example.yml"])
	if strings.Contains(result, "fedoraadmin") {
		t.Fatalf("expected fedoraadmin removed, got:\n%s", result)
	}
	if !strings.Contains(result, "manager") {
		t.Fatalf("expected other entries preserved, got:\n%s", result)
	}
}
