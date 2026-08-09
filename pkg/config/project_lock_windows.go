//go:build windows

package config

import (
	"context"
	"errors"
	"fmt"
	"os"
	"time"

	"golang.org/x/sys/windows"
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
	var identity windows.ByHandleFileInformation
	if err := windows.GetFileInformationByHandle(windows.Handle(directory.Fd()), &identity); err != nil {
		return "", fmt.Errorf("read local project directory file identity: %w", err)
	}
	fileIndex := uint64(identity.FileIndexHigh)<<32 | uint64(identity.FileIndexLow)
	return fmt.Sprintf("windows:%08x:%016x", identity.VolumeSerialNumber, fileIndex), nil
}

func acquireLocalProjectMutationLock(runCtx, lockContext context.Context, lockPath string) (*ProjectMutationLock, error) {
	file, err := os.OpenFile(lockPath, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, fmt.Errorf("open local project lock: %w", err)
	}
	handle := windows.Handle(file.Fd())
	overlapped := &windows.Overlapped{}
	ticker := time.NewTicker(100 * time.Millisecond)
	defer ticker.Stop()
	for {
		err = windows.LockFileEx(
			handle,
			windows.LOCKFILE_EXCLUSIVE_LOCK|windows.LOCKFILE_FAIL_IMMEDIATELY,
			0,
			1,
			0,
			overlapped,
		)
		if err == nil {
			break
		}
		if !errors.Is(err, windows.ERROR_LOCK_VIOLATION) {
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
		_ = windows.UnlockFileEx(handle, 0, 1, 0, overlapped)
		_ = file.Close()
	}}, nil
}
