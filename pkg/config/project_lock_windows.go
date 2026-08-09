//go:build windows

package config

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
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

func localProjectMutationLockPath(projectIdentity string) (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("resolve home directory for local project lock: %w", err)
	}
	home = filepath.Clean(home)
	if err := validateWindowsProjectMutationLockDirectory(home); err != nil {
		return "", err
	}
	baseDirectory := filepath.Join(home, ".sitectl")
	if err := ensureWindowsProjectMutationLockDirectory(baseDirectory); err != nil {
		return "", err
	}
	lockDirectory := filepath.Join(baseDirectory, "locks")
	if err := ensureWindowsProjectMutationLockDirectory(lockDirectory); err != nil {
		return "", err
	}
	return filepath.Join(lockDirectory, projectMutationLockFilename(projectIdentity)), nil
}

func ensureWindowsProjectMutationLockDirectory(directory string) error {
	if err := os.Mkdir(directory, 0o700); err != nil && !os.IsExist(err) {
		return fmt.Errorf("create local project lock directory %q: %w", directory, err)
	}
	return validateWindowsProjectMutationLockDirectory(directory)
}

func validateWindowsProjectMutationLockDirectory(directory string) error {
	pathPointer, err := windows.UTF16PtrFromString(directory)
	if err != nil {
		return fmt.Errorf("encode local project lock directory %q: %w", directory, err)
	}
	handle, err := windows.CreateFile(
		pathPointer,
		windows.READ_CONTROL|windows.FILE_READ_ATTRIBUTES,
		windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE,
		nil,
		windows.OPEN_EXISTING,
		windows.FILE_FLAG_BACKUP_SEMANTICS|windows.FILE_FLAG_OPEN_REPARSE_POINT,
		0,
	)
	if err != nil {
		return fmt.Errorf("open local project lock directory %q without following reparse points: %w", directory, err)
	}
	defer func() { _ = windows.CloseHandle(handle) }()
	return validateWindowsProjectMutationLockHandle(handle, directory, true)
}

func validateWindowsProjectMutationLockHandle(handle windows.Handle, name string, wantDirectory bool) error {
	var info windows.ByHandleFileInformation
	if err := windows.GetFileInformationByHandle(handle, &info); err != nil {
		return fmt.Errorf("inspect local project lock path %q: %w", name, err)
	}
	if info.FileAttributes&windows.FILE_ATTRIBUTE_REPARSE_POINT != 0 {
		return fmt.Errorf("local project lock path %q must not be a reparse point", name)
	}
	isDirectory := info.FileAttributes&windows.FILE_ATTRIBUTE_DIRECTORY != 0
	if isDirectory != wantDirectory {
		return fmt.Errorf("local project lock path %q has the wrong file type", name)
	}
	descriptor, err := windows.GetSecurityInfo(handle, windows.SE_FILE_OBJECT, windows.OWNER_SECURITY_INFORMATION)
	if err != nil {
		return fmt.Errorf("inspect local project lock owner for %q: %w", name, err)
	}
	if descriptor == nil {
		return fmt.Errorf("local project lock path %q has no security descriptor", name)
	}
	owner, _, err := descriptor.Owner()
	if err != nil {
		return fmt.Errorf("read local project lock owner for %q: %w", name, err)
	}
	token, err := windows.OpenCurrentProcessToken()
	if err != nil {
		return fmt.Errorf("open current process token for project lock: %w", err)
	}
	defer func() { _ = token.Close() }()
	user, err := token.GetTokenUser()
	if err != nil {
		return fmt.Errorf("read current user for project lock: %w", err)
	}
	if owner == nil || user.User.Sid == nil || !owner.Equals(user.User.Sid) {
		return fmt.Errorf("local project lock path %q must be owned by the current user", name)
	}
	return nil
}

func openWindowsProjectMutationLock(lockPath string) (*os.File, error) {
	pathPointer, err := windows.UTF16PtrFromString(lockPath)
	if err != nil {
		return nil, fmt.Errorf("encode local project lock path %q: %w", lockPath, err)
	}
	handle, err := windows.CreateFile(
		pathPointer,
		windows.GENERIC_READ|windows.GENERIC_WRITE|windows.READ_CONTROL,
		windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE,
		nil,
		windows.OPEN_ALWAYS,
		windows.FILE_ATTRIBUTE_NORMAL|windows.FILE_FLAG_OPEN_REPARSE_POINT,
		0,
	)
	if err != nil {
		return nil, fmt.Errorf("open local project lock without following reparse points: %w", err)
	}
	if err := validateWindowsProjectMutationLockHandle(handle, lockPath, false); err != nil {
		_ = windows.CloseHandle(handle)
		return nil, err
	}
	file := os.NewFile(uintptr(handle), lockPath)
	if file == nil {
		_ = windows.CloseHandle(handle)
		return nil, fmt.Errorf("open local project lock: invalid handle")
	}
	return file, nil
}

func acquireLocalProjectMutationLock(runCtx, lockContext context.Context, lockPath string) (*ProjectMutationLock, error) {
	file, err := openWindowsProjectMutationLock(lockPath)
	if err != nil {
		return nil, err
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
