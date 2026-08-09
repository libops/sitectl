//go:build !windows

package docker

import (
	"context"
	"errors"
	"log/slog"
	"os"
	"os/signal"
	"sync"
	"syscall"

	"golang.org/x/sys/unix"
)

func newFileExecInputController(file *os.File) (*execInputController, error) {
	cancelReader, cancelWriter, err := os.Pipe()
	if err != nil {
		return nil, err
	}
	reader := &pollExecInputReader{input: file, cancel: cancelReader}
	var cancelOnce sync.Once
	var closeOnce sync.Once
	return &execInputController{
		Reader: reader,
		cancel: func() { cancelOnce.Do(func() { _ = cancelWriter.Close() }) },
		close: func() {
			closeOnce.Do(func() {
				cancelOnce.Do(func() { _ = cancelWriter.Close() })
				_ = cancelReader.Close()
			})
		},
	}, nil
}

type pollExecInputReader struct {
	input  *os.File
	cancel *os.File
}

func (r *pollExecInputReader) Read(buffer []byte) (int, error) {
	pollFDs := []unix.PollFd{
		{Fd: int32(r.input.Fd()), Events: unix.POLLIN},
		{Fd: int32(r.cancel.Fd()), Events: unix.POLLIN},
	}
	for {
		_, err := unix.Poll(pollFDs, -1)
		if errors.Is(err, syscall.EINTR) {
			continue
		}
		if err != nil {
			return 0, err
		}
		if pollFDs[1].Revents != 0 {
			return 0, errExecInputCanceled
		}
		if pollFDs[0].Revents&(unix.POLLIN|unix.POLLHUP|unix.POLLERR|unix.POLLNVAL) != 0 {
			return r.input.Read(buffer)
		}
	}
}

func watchExecTerminalResizes(ctx context.Context, resize func() error) func() {
	resizeSignals := make(chan os.Signal, 1)
	signal.Notify(resizeSignals, syscall.SIGWINCH)
	resizeDone := make(chan struct{})
	var stopOnce sync.Once
	go func() {
		for {
			select {
			case <-resizeSignals:
				if err := resize(); err != nil && ctx.Err() == nil {
					slog.Warn("resize exec terminal", "err", err)
				}
			case <-ctx.Done():
				return
			case <-resizeDone:
				return
			}
		}
	}()
	return func() {
		stopOnce.Do(func() {
			signal.Stop(resizeSignals)
			close(resizeDone)
		})
	}
}
