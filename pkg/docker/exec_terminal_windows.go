//go:build windows

package docker

import (
	"context"
	"fmt"
	"os"
	"runtime"
	"sync"

	"golang.org/x/sys/windows"
)

var cancelSynchronousIO = windows.NewLazySystemDLL("kernel32.dll").NewProc("CancelSynchronousIo")

func newFileExecInputController(file *os.File) (*execInputController, error) {
	process, err := windows.GetCurrentProcess()
	if err != nil {
		return nil, fmt.Errorf("get current process for exec input: %w", err)
	}
	var duplicate windows.Handle
	if err := windows.DuplicateHandle(
		process,
		windows.Handle(file.Fd()),
		process,
		&duplicate,
		0,
		false,
		windows.DUPLICATE_SAME_ACCESS,
	); err != nil {
		return nil, fmt.Errorf("duplicate exec input handle: %w", err)
	}
	duplicatedFile := os.NewFile(uintptr(duplicate), file.Name()+" (sitectl exec)")
	if duplicatedFile == nil {
		_ = windows.CloseHandle(duplicate)
		return nil, fmt.Errorf("open duplicated exec input handle")
	}
	reader := &windowsExecInputReader{input: duplicatedFile, handle: duplicate}
	var closeOnce sync.Once
	return &execInputController{
		Reader: reader,
		cancel: reader.Cancel,
		close: func() {
			closeOnce.Do(func() {
				reader.Cancel()
				_ = duplicatedFile.Close()
			})
		},
	}, nil
}

// windowsExecInputReader pins each read to one OS thread. Cancel can therefore
// interrupt both console ReadConsole calls and overlapped pipe/file reads
// without closing the process-wide stdin handle.
type windowsExecInputReader struct {
	input  *os.File
	handle windows.Handle

	mu       sync.Mutex
	canceled bool
	thread   windows.Handle
}

func (r *windowsExecInputReader) Read(buffer []byte) (int, error) {
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()

	thread, err := windows.OpenThread(windows.THREAD_TERMINATE, false, windows.GetCurrentThreadId())
	if err != nil {
		return 0, fmt.Errorf("open exec input thread: %w", err)
	}
	r.mu.Lock()
	if r.canceled {
		r.mu.Unlock()
		_ = windows.CloseHandle(thread)
		return 0, errExecInputCanceled
	}
	r.thread = thread
	r.mu.Unlock()

	n, readErr := r.input.Read(buffer)
	r.mu.Lock()
	r.thread = 0
	canceled := r.canceled
	r.mu.Unlock()
	_ = windows.CloseHandle(thread)
	if canceled {
		return 0, errExecInputCanceled
	}
	return n, readErr
}

func (r *windowsExecInputReader) Cancel() {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.canceled {
		return
	}
	r.canceled = true
	// CancelIoEx handles Go's overlapped pipe/file reads. Console input uses
	// synchronous ReadConsole, which is canceled on its pinned reader thread.
	_ = windows.CancelIoEx(r.handle, nil)
	if r.thread != 0 {
		_, _, _ = cancelSynchronousIO.Call(uintptr(r.thread))
	}
}

func watchExecTerminalResizes(context.Context, func() error) func() {
	// Windows consoles do not expose SIGWINCH. The initial size is still sent
	// by prepareExecTerminal; subsequent resizes are unavailable through this
	// API and must not leave a background watcher behind.
	return func() {}
}
