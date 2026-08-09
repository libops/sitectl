//go:build !windows

package config

import (
	"context"
	"errors"
	"fmt"
	"os"
	"syscall"
	"time"

	"golang.org/x/sys/unix"
)

func localProjectMutationLockIdentity(projectDir string) (string, error) {
	directory, err := os.Open(projectDir)
	if err != nil {
		return "", fmt.Errorf("open local project directory for mutation lock identity: %w", err)
	}
	defer func() { _ = directory.Close() }()
	info, err := directory.Stat()
	if err != nil {
		return "", fmt.Errorf("inspect local project directory for mutation lock identity: %w", err)
	}
	if !info.IsDir() {
		return "", fmt.Errorf("local project path %q is not a directory", projectDir)
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		return "", fmt.Errorf("local project directory %q does not expose a stable device and inode", projectDir)
	}
	return fmt.Sprintf("unix:%x:%x", uint64(stat.Dev), uint64(stat.Ino)), nil
}

func acquireLocalProjectMutationLock(runCtx, lockContext context.Context, lockPath string) (*ProjectMutationLock, error) {
	file, err := os.OpenFile(lockPath, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, fmt.Errorf("open local project lock: %w", err)
	}
	ticker := time.NewTicker(100 * time.Millisecond)
	defer ticker.Stop()
	for {
		err := unix.Flock(int(file.Fd()), unix.LOCK_EX|unix.LOCK_NB)
		if err == nil {
			break
		}
		if !errors.Is(err, syscall.EWOULDBLOCK) && !errors.Is(err, syscall.EAGAIN) {
			_ = file.Close()
			return nil, fmt.Errorf("acquire local project lock: %w", err)
		}
		select {
		case <-runCtx.Done():
			_ = file.Close()
			return nil, runCtx.Err()
		case <-ticker.C:
		}
	}
	return &ProjectMutationLock{context: lockContext, close: func() {
		_ = unix.Flock(int(file.Fd()), unix.LOCK_UN)
		_ = file.Close()
	}}, nil
}
