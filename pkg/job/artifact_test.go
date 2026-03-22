package job

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/libops/sitectl/pkg/config"
)

func TestResolveRecentArtifactReusesExistingFile(t *testing.T) {
	root := t.TempDir()
	now := time.Date(2026, 3, 22, 10, 0, 0, 0, time.UTC)
	existing := DatedArtifactPath(root, "drupal.sql.gz", now.Add(-24*time.Hour))
	if err := os.MkdirAll(filepath.Dir(existing), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(existing, []byte("ok"), 0o644); err != nil {
		t.Fatal(err)
	}

	ctx := &config.Context{DockerHostType: config.ContextLocal}
	produced := false
	got, err := ResolveRecentArtifact(ctx, root, "drupal.sql.gz", false, now, func(path string) error {
		produced = true
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if got != existing {
		t.Fatalf("got %q, want %q", got, existing)
	}
	if produced {
		t.Fatal("expected existing artifact to be reused")
	}
}

func TestResolveRecentArtifactProducesWhenMissingOrFresh(t *testing.T) {
	root := t.TempDir()
	now := time.Date(2026, 3, 22, 10, 0, 0, 0, time.UTC)
	ctx := &config.Context{DockerHostType: config.ContextLocal}

	for _, fresh := range []bool{false, true} {
		produced := ""
		got, err := ResolveRecentArtifact(ctx, root, "fcrepo.sql.gz", fresh, now, func(path string) error {
			produced = path
			return nil
		})
		if err != nil {
			t.Fatal(err)
		}
		want := DatedArtifactPath(root, "fcrepo.sql.gz", now)
		if got != want {
			t.Fatalf("fresh=%v got %q, want %q", fresh, got, want)
		}
		if produced != want {
			t.Fatalf("fresh=%v produced %q, want %q", fresh, produced, want)
		}
	}
}
