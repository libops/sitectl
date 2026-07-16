package plugin

import (
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestLocalTemplateCheckoutRejectsNonEmptyProject(t *testing.T) {
	projectDir := t.TempDir()
	marker := filepath.Join(projectDir, "existing")
	if err := os.WriteFile(marker, []byte("keep\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	sdk := NewSDK(Metadata{Name: "omeka-s", Version: "1.0.0"})
	created, err := sdk.ensureLocalComposeTemplateCheckout(context.Background(), io.Discard, ComposeCreateRequest{
		TemplateRepo: "git@github.com:libops/omeka-s.git",
	}, projectDir)
	if err == nil || !strings.Contains(err.Error(), string(CheckoutSourceExisting)) {
		t.Fatalf("ensureLocalComposeTemplateCheckout() error = %v", err)
	}
	if created {
		t.Fatal("ensureLocalComposeTemplateCheckout() created = true, want false")
	}
	if _, err := os.Stat(marker); err != nil {
		t.Fatalf("existing project was modified: %v", err)
	}
}

func TestLocalTemplateCheckoutRejectsSymlinkProjectDirectory(t *testing.T) {
	target := t.TempDir()
	projectDir := filepath.Join(t.TempDir(), "site")
	if err := os.Symlink(target, projectDir); err != nil {
		t.Fatal(err)
	}

	sdk := NewSDK(Metadata{Name: "omeka-s", Version: "1.0.0"})
	created, err := sdk.ensureLocalComposeTemplateCheckout(context.Background(), io.Discard, ComposeCreateRequest{
		TemplateRepo: "git@github.com:libops/omeka-s.git",
	}, projectDir)
	if err == nil || !strings.Contains(err.Error(), "real directory") {
		t.Fatalf("ensureLocalComposeTemplateCheckout() error = %v", err)
	}
	if created {
		t.Fatal("ensureLocalComposeTemplateCheckout() created = true, want false")
	}
	if info, err := os.Lstat(projectDir); err != nil || info.Mode()&os.ModeSymlink == 0 {
		t.Fatalf("project symlink was modified: info = %v, err = %v", info, err)
	}
}

func TestLocalTemplateCloneFailureCleansNewProjectDirectory(t *testing.T) {
	originalRunner := runGitCommandContext
	t.Cleanup(func() {
		runGitCommandContext = originalRunner
	})
	projectDir := filepath.Join(t.TempDir(), "site")
	cloneErr := errors.New("clone failed")
	runGitCommandContext = func(_ context.Context, _, _ io.Writer, name string, args ...string) error {
		if name != "git" || len(args) == 0 || args[0] != "clone" {
			t.Fatalf("unexpected command: %s %v", name, args)
		}
		if err := os.WriteFile(filepath.Join(projectDir, "partial"), []byte("partial\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		return cloneErr
	}

	sdk := NewSDK(Metadata{Name: "omeka-s", Version: "1.0.0"})
	created, err := sdk.ensureLocalComposeTemplateCheckout(context.Background(), io.Discard, ComposeCreateRequest{
		TemplateRepo: "git@github.com:libops/omeka-s.git",
	}, projectDir)
	if err == nil || !strings.Contains(err.Error(), cloneErr.Error()) {
		t.Fatalf("ensureLocalComposeTemplateCheckout() error = %v", err)
	}
	if created {
		t.Fatal("ensureLocalComposeTemplateCheckout() created = true, want false")
	}
	if _, err := os.Lstat(projectDir); !os.IsNotExist(err) {
		t.Fatalf("partial checkout remains after clone failure: %v", err)
	}
}

func TestLocalTemplateCloneHonorsCancellation(t *testing.T) {
	originalRunner := runGitCommandContext
	t.Cleanup(func() {
		runGitCommandContext = originalRunner
	})
	projectDir := filepath.Join(t.TempDir(), "site")
	runCtx, cancel := context.WithCancel(context.Background())
	runGitCommandContext = func(ctx context.Context, _, _ io.Writer, name string, args ...string) error {
		if name != "git" || len(args) == 0 || args[0] != "clone" {
			t.Fatalf("unexpected command: %s %v", name, args)
		}
		cancel()
		return ctx.Err()
	}

	sdk := NewSDK(Metadata{Name: "omeka-s", Version: "1.0.0"})
	created, err := sdk.ensureLocalComposeTemplateCheckout(runCtx, io.Discard, ComposeCreateRequest{
		TemplateRepo: "git@github.com:libops/omeka-s.git",
	}, projectDir)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("ensureLocalComposeTemplateCheckout() error = %v, want context cancellation", err)
	}
	if created {
		t.Fatal("ensureLocalComposeTemplateCheckout() created = true, want false")
	}
	if _, err := os.Lstat(projectDir); !os.IsNotExist(err) {
		t.Fatalf("cancelled checkout remains: %v", err)
	}
}
