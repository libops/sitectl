package hostruntime

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestAcquireLockRejectsSymbolicLink(t *testing.T) {
	directory := t.TempDir()
	target := filepath.Join(directory, "target")
	if err := os.WriteFile(target, []byte("unchanged"), 0o600); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(directory, "lock")
	if err := os.Symlink(target, link); err != nil {
		t.Fatal(err)
	}

	lock, err := AcquireLock(context.Background(), link)
	if err == nil {
		_ = lock.Close()
		t.Fatal("AcquireLock accepted a symbolic-link target")
	}
	contents, readErr := os.ReadFile(target)
	if readErr != nil {
		t.Fatal(readErr)
	}
	if string(contents) != "unchanged" {
		t.Fatalf("symbolic-link target changed to %q", contents)
	}
}

func TestAcquireLockHonorsContextWhileContended(t *testing.T) {
	path := filepath.Join(t.TempDir(), "lifecycle.lock")
	first, err := AcquireLock(context.Background(), path)
	if err != nil {
		t.Fatal(err)
	}
	defer first.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 150*time.Millisecond)
	defer cancel()
	second, err := AcquireLock(ctx, path)
	if err == nil {
		_ = second.Close()
		t.Fatal("AcquireLock succeeded while the lock was held")
	}
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("AcquireLock error = %v, want context deadline", err)
	}
}

func TestAcquireLockValidatesConfiguredTimeout(t *testing.T) {
	t.Setenv("CLOUD_COMPOSE_LIFECYCLE_LOCK_TIMEOUT_SECONDS", "0")
	lock, err := AcquireLock(context.Background(), filepath.Join(t.TempDir(), "lifecycle.lock"))
	if err == nil {
		_ = lock.Close()
		t.Fatal("AcquireLock accepted an invalid timeout")
	}
	if !strings.Contains(err.Error(), "integer from 1 through 43200") {
		t.Fatalf("AcquireLock error = %v, want timeout validation", err)
	}
}
