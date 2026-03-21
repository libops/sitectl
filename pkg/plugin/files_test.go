package plugin

import (
	"os"
	"path/filepath"
	"slices"
	"testing"

	"github.com/libops/sitectl/pkg/config"
)

func TestNewFileAccessorLocalReadListAndMatch(t *testing.T) {
	root := t.TempDir()

	if err := os.MkdirAll(filepath.Join(root, "nested"), 0o755); err != nil {
		t.Fatalf("MkdirAll() error = %v", err)
	}
	if err := os.WriteFile(filepath.Join(root, "alpha.yml"), []byte("alpha"), 0o644); err != nil {
		t.Fatalf("WriteFile(alpha) error = %v", err)
	}
	if err := os.WriteFile(filepath.Join(root, "nested", "beta.yml"), []byte("beta"), 0o644); err != nil {
		t.Fatalf("WriteFile(beta) error = %v", err)
	}
	if err := os.WriteFile(filepath.Join(root, "nested", "gamma.txt"), []byte("gamma"), 0o644); err != nil {
		t.Fatalf("WriteFile(gamma) error = %v", err)
	}

	ctx := &config.Context{DockerHostType: config.ContextLocal}
	accessor, err := NewFileAccessor(ctx)
	if err != nil {
		t.Fatalf("NewFileAccessor() error = %v", err)
	}
	defer accessor.Close()

	got, err := accessor.ReadFile(filepath.Join(root, "alpha.yml"))
	if err != nil {
		t.Fatalf("ReadFile() error = %v", err)
	}
	if string(got) != "alpha" {
		t.Fatalf("expected alpha content, got %q", string(got))
	}

	files, err := accessor.ListFiles(root)
	if err != nil {
		t.Fatalf("ListFiles() error = %v", err)
	}
	wantFiles := []string{"alpha.yml", "nested/beta.yml", "nested/gamma.txt"}
	if !slices.Equal(files, wantFiles) {
		t.Fatalf("unexpected files: got %v want %v", files, wantFiles)
	}

	matches, err := accessor.MatchFiles(root, "*.yml")
	if err != nil {
		t.Fatalf("MatchFiles() error = %v", err)
	}
	wantMatches := []string{
		filepath.Join(root, "alpha.yml"),
		filepath.Join(root, "nested", "beta.yml"),
	}
	if !slices.Equal(matches, wantMatches) {
		t.Fatalf("unexpected matches: got %v want %v", matches, wantMatches)
	}
}

func TestSDKGetFileAccessorUsesResolvedContext(t *testing.T) {
	tempHome := t.TempDir()
	t.Setenv("HOME", tempHome)

	ctx := config.Context{
		Name:           "museum",
		Site:           "museum",
		Plugin:         "isle",
		DockerHostType: config.ContextLocal,
		DockerSocket:   "/var/run/docker.sock",
		ProjectDir:     tempHome,
	}
	if err := config.SaveContext(&ctx, true); err != nil {
		t.Fatalf("SaveContext() error = %v", err)
	}

	sdk := NewSDK(Metadata{Name: "drupal"})
	sdk.Config.Context = "museum"

	accessor, err := sdk.GetFileAccessor()
	if err != nil {
		t.Fatalf("GetFileAccessor() error = %v", err)
	}
	defer accessor.Close()

	if accessor.ctx == nil {
		t.Fatal("expected accessor context to be set")
	}
	if accessor.ctx.Name != "museum" {
		t.Fatalf("unexpected accessor context %q", accessor.ctx.Name)
	}
}
