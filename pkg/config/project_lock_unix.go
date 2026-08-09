//go:build !windows

package config

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
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

func localProjectMutationLockPath(projectIdentity string) (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("resolve home directory for local project lock: %w", err)
	}
	home = filepath.Clean(home)
	homeInfo, err := os.Lstat(home)
	if err != nil {
		return "", fmt.Errorf("inspect home directory for local project lock: %w", err)
	}
	uid := uint32(os.Geteuid())
	if err := validateLocalProjectMutationLockDirectory(home, homeInfo, uid, false); err != nil {
		return "", err
	}
	baseDirectory := filepath.Join(home, ".sitectl")
	if err := ensureLocalProjectMutationLockDirectory(baseDirectory, uid, false); err != nil {
		return "", err
	}
	lockDirectory := filepath.Join(baseDirectory, "locks")
	if err := ensureLocalProjectMutationLockDirectory(lockDirectory, uid, true); err != nil {
		return "", err
	}
	return filepath.Join(lockDirectory, projectMutationLockFilename(projectIdentity)), nil
}

func ensureLocalProjectMutationLockDirectory(directory string, uid uint32, private bool) error {
	info, err := os.Lstat(directory)
	if err != nil {
		if !os.IsNotExist(err) {
			return fmt.Errorf("inspect local project lock directory %q: %w", directory, err)
		}
		if err := os.Mkdir(directory, 0o700); err != nil && !os.IsExist(err) {
			return fmt.Errorf("create local project lock directory %q: %w", directory, err)
		}
		info, err = os.Lstat(directory)
		if err != nil {
			return fmt.Errorf("inspect created local project lock directory %q: %w", directory, err)
		}
	}
	return validateLocalProjectMutationLockDirectory(directory, info, uid, private)
}

func validateLocalProjectMutationLockDirectory(directory string, info os.FileInfo, uid uint32, private bool) error {
	if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
		return fmt.Errorf("local project lock directory %q must be a real directory", directory)
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		return fmt.Errorf("local project lock directory %q does not expose an owner", directory)
	}
	if !localProjectMutationLockDirectoryOwnerAllowed(uint32(stat.Uid), uid, private) {
		if !private {
			return fmt.Errorf("local project lock directory %q is owned by uid %d, want uid %d or root", directory, stat.Uid, uid)
		}
		return fmt.Errorf("local project lock directory %q is owned by uid %d, want uid %d", directory, stat.Uid, uid)
	}
	if private {
		if info.Mode().Perm() != 0o700 {
			return fmt.Errorf("local project lock directory %q has permissions %04o, want 0700", directory, info.Mode().Perm())
		}
	} else if info.Mode().Perm()&0o022 != 0 {
		return fmt.Errorf("local project lock parent %q must not be group- or world-writable", directory)
	}
	return nil
}

// A root-owned, non-writable home is a safe parent for user-owned sitectl
// state. Private state and lock directories must still belong to the operator.
func localProjectMutationLockDirectoryOwnerAllowed(owner, uid uint32, private bool) bool {
	return owner == uid || (!private && owner == 0)
}

func openLocalProjectMutationLock(lockPath string) (*os.File, error) {
	fd, err := unix.Open(lockPath, unix.O_CREAT|unix.O_RDWR|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0o600)
	if err != nil {
		return nil, fmt.Errorf("open local project lock without following symlinks: %w", err)
	}
	file := os.NewFile(uintptr(fd), lockPath)
	if file == nil {
		_ = unix.Close(fd)
		return nil, fmt.Errorf("open local project lock: invalid file descriptor")
	}
	info, err := file.Stat()
	if err != nil {
		_ = file.Close()
		return nil, fmt.Errorf("inspect opened local project lock: %w", err)
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok || !info.Mode().IsRegular() || uint32(stat.Uid) != uint32(os.Geteuid()) || info.Mode().Perm() != 0o600 {
		_ = file.Close()
		return nil, fmt.Errorf("local project lock %q must be an owner-only regular file owned by the current user", lockPath)
	}
	return file, nil
}

func acquireLocalProjectMutationLock(runCtx, lockContext context.Context, lockPath string) (*ProjectMutationLock, error) {
	file, err := openLocalProjectMutationLock(lockPath)
	if err != nil {
		return nil, err
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
