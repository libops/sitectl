package hostruntime

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strconv"
	"time"

	"golang.org/x/sys/unix"
)

type Lock struct{ file *os.File }

// AcquireLock obtains an exclusive host lifecycle lock without following a
// symbolic-link target. The environment-configured timeout bounds contention.
func AcquireLock(ctx context.Context, path string) (*Lock, error) {
	timeout, err := lifecycleLockTimeout()
	if err != nil {
		return nil, err
	}
	fd, err := unix.Open(path, unix.O_CREAT|unix.O_RDWR|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0o640)
	if err != nil {
		return nil, fmt.Errorf("open host lifecycle lock: %w", err)
	}
	file := os.NewFile(uintptr(fd), path)
	info, err := file.Stat()
	if err != nil {
		_ = file.Close()
		return nil, fmt.Errorf("inspect host lifecycle lock: %w", err)
	}
	if !info.Mode().IsRegular() {
		_ = file.Close()
		return nil, fmt.Errorf("host lifecycle lock is not a regular file: %s", path)
	}

	deadline := time.NewTimer(timeout)
	defer deadline.Stop()
	retry := time.NewTicker(100 * time.Millisecond)
	defer retry.Stop()
	for {
		err = unix.Flock(fd, unix.LOCK_EX|unix.LOCK_NB)
		if err == nil {
			return &Lock{file: file}, nil
		}
		if !errors.Is(err, unix.EWOULDBLOCK) {
			_ = file.Close()
			return nil, fmt.Errorf("acquire host lifecycle lock: %w", err)
		}
		select {
		case <-ctx.Done():
			_ = file.Close()
			return nil, fmt.Errorf("acquire host lifecycle lock: %w", ctx.Err())
		case <-deadline.C:
			_ = file.Close()
			return nil, fmt.Errorf("timed out after %s waiting for host lifecycle lock", timeout)
		case <-retry.C:
		}
	}
}

func lifecycleLockTimeout() (time.Duration, error) {
	value := os.Getenv("CLOUD_COMPOSE_LIFECYCLE_LOCK_TIMEOUT_SECONDS")
	if value == "" {
		return 15 * time.Minute, nil
	}
	seconds, err := strconv.Atoi(value)
	if err != nil || seconds < 1 || seconds > 43200 {
		return 0, fmt.Errorf("CLOUD_COMPOSE_LIFECYCLE_LOCK_TIMEOUT_SECONDS must be an integer from 1 through 43200")
	}
	return time.Duration(seconds) * time.Second, nil
}

func (l *Lock) Close() error {
	if l == nil || l.file == nil {
		return nil
	}
	unlockErr := unix.Flock(int(l.file.Fd()), unix.LOCK_UN)
	closeErr := l.file.Close()
	if unlockErr != nil {
		return unlockErr
	}
	return closeErr
}
