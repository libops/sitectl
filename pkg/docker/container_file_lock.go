package docker

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"path"
	"strings"
	"sync"
	"time"

	dockertypes "github.com/docker/docker/api/types"
	dockercontainer "github.com/docker/docker/api/types/container"
	"github.com/docker/docker/pkg/stdcopy"
)

const (
	containerFileLockHandshake       = "sitectl-container-file-lock-acquired"
	containerFileLockHeartbeat       = 5 * time.Second
	containerFileLockReleaseTimeout  = 10 * time.Second
	containerFileLockMaxErrorMessage = 4096
	containerFileLockScript          = `( IFS= read -r -t 0 _ </dev/null ); status=$?
if [ "$status" -gt 1 ]; then
  printf 'container shell does not support read timeouts\n' >&2
  exit 64
fi
printf '` + containerFileLockHandshake + `\n'
while IFS= read -r -t 30 message; do
  [ "$message" = release ] && exit 0
done
exit 0`
)

var ErrContainerFileLockHeld = errors.New("container file lock is already held")

// ContainerFileLockOptions identifies an advisory lock file inside a container.
type ContainerFileLockOptions struct {
	Container string
	Path      string
}

// ContainerFileLock holds an exclusive advisory lock through a connected
// container exec process. Release must be called when the protected operation
// finishes. A renewable lease releases an abandoned holder after 30 seconds.
type ContainerFileLock struct {
	exec       containerFileLockAPI
	execID     string
	response   *dockertypes.HijackedResponse
	stdout     *io.PipeReader
	outputDone <-chan error
	context    context.Context
	cancel     context.CancelFunc

	heartbeatDone chan struct{}
	writeMu       sync.Mutex
	stateMu       sync.Mutex
	heartbeatErr  error
	once          sync.Once
	releaseErr    error
}

type containerFileLockAPI interface {
	ContainerExecCreate(ctx context.Context, containerID string, options dockercontainer.ExecOptions) (dockercontainer.ExecCreateResponse, error)
	ContainerExecAttach(ctx context.Context, execID string, options dockercontainer.ExecAttachOptions) (dockertypes.HijackedResponse, error)
	ContainerExecInspect(ctx context.Context, execID string) (dockercontainer.ExecInspect, error)
}

// AcquireContainerFileLock takes a non-blocking exclusive flock inside the
// selected container. The lock path's parent directory must already exist. The
// container must provide flock and a shell whose read builtin supports -t.
func (d *DockerClient) AcquireContainerFileLock(ctx context.Context, options ContainerFileLockOptions) (*ContainerFileLock, error) {
	if ctx == nil {
		return nil, fmt.Errorf("context is nil")
	}
	if d == nil || d.CLI == nil {
		return nil, fmt.Errorf("docker client is unavailable")
	}
	container := strings.TrimSpace(options.Container)
	lockPath := path.Clean(strings.TrimSpace(options.Path))
	if container == "" {
		return nil, fmt.Errorf("container is required for container file lock")
	}
	if !path.IsAbs(lockPath) || lockPath == "/" || strings.ContainsRune(lockPath, '\x00') {
		return nil, fmt.Errorf("container file lock path %q must be an absolute file path", options.Path)
	}
	execAPI, ok := d.CLI.(containerFileLockAPI)
	if !ok {
		return nil, fmt.Errorf("docker client does not support container exec locking")
	}

	execResponse, err := execAPI.ContainerExecCreate(ctx, container, dockercontainer.ExecOptions{
		AttachStdin:  true,
		AttachStdout: true,
		AttachStderr: true,
		Cmd:          []string{"flock", "-n", lockPath, "sh", "-c", containerFileLockScript},
	})
	if err != nil {
		return nil, fmt.Errorf("create container file lock holder: %w", err)
	}
	response, err := execAPI.ContainerExecAttach(ctx, execResponse.ID, dockercontainer.ExecAttachOptions{})
	if err != nil {
		return nil, fmt.Errorf("attach container file lock holder: %w", err)
	}
	stdoutReader, stdoutWriter := io.Pipe()
	stderr := &boundedLockErrorWriter{remaining: containerFileLockMaxErrorMessage}
	outputDone := make(chan error, 1)
	go func() {
		_, copyErr := stdcopy.StdCopy(stdoutWriter, stderr, response.Reader)
		_ = stdoutWriter.CloseWithError(copyErr)
		outputDone <- copyErr
	}()
	stopContextClose := context.AfterFunc(ctx, response.Close)
	line, readErr := bufio.NewReader(stdoutReader).ReadString('\n')
	if strings.TrimSpace(line) != containerFileLockHandshake {
		stopContextClose()
		response.Close()
		_ = stdoutReader.Close()
		inspection, inspectErr := inspectContainerFileLock(execAPI, execResponse.ID)
		if inspectErr != nil {
			return nil, errors.Join(containerFileLockAcquireError(line, stderr.String(), readErr, -1), inspectErr)
		}
		if inspection.ExitCode == 1 {
			return nil, fmt.Errorf("%w: %s", ErrContainerFileLockHeld, lockPath)
		}
		return nil, containerFileLockAcquireError(line, stderr.String(), readErr, inspection.ExitCode)
	}
	stopContextClose()
	lockContext, cancel := context.WithCancel(ctx)
	lock := &ContainerFileLock{
		exec:          execAPI,
		execID:        execResponse.ID,
		response:      &response,
		stdout:        stdoutReader,
		outputDone:    outputDone,
		context:       lockContext,
		cancel:        cancel,
		heartbeatDone: make(chan struct{}),
	}
	go lock.heartbeat()
	return lock, nil
}

func inspectContainerFileLock(execAPI containerFileLockAPI, execID string) (dockercontainer.ExecInspect, error) {
	inspectCtx, cancel := context.WithTimeout(context.Background(), containerFileLockReleaseTimeout)
	defer cancel()
	inspection, err := execAPI.ContainerExecInspect(inspectCtx, execID)
	if err != nil {
		return dockercontainer.ExecInspect{}, fmt.Errorf("inspect container file lock holder: %w", err)
	}
	return inspection, nil
}

func containerFileLockAcquireError(stdout, stderr string, readErr error, exitCode int) error {
	detail := strings.TrimSpace(stderr)
	if detail == "" {
		detail = strings.TrimSpace(stdout)
	}
	if detail == "" {
		detail = "lock holder exited before acquisition"
	}
	if readErr != nil && !errors.Is(readErr, io.EOF) {
		return fmt.Errorf("acquire container file lock: %s (exit code %d): %w", detail, exitCode, readErr)
	}
	return fmt.Errorf("acquire container file lock: %s (exit code %d)", detail, exitCode)
}

// Context is canceled if the parent context is canceled or the lock heartbeat
// is lost. Callers should use it for every operation protected by the lock.
func (l *ContainerFileLock) Context() context.Context {
	if l == nil || l.context == nil {
		return context.Background()
	}
	return l.context
}

func (l *ContainerFileLock) heartbeat() {
	defer close(l.heartbeatDone)
	ticker := time.NewTicker(containerFileLockHeartbeat)
	defer ticker.Stop()
	for {
		select {
		case <-l.context.Done():
			return
		case <-ticker.C:
			if err := l.writeMessage("heartbeat"); err != nil {
				l.stateMu.Lock()
				l.heartbeatErr = fmt.Errorf("renew container file lock lease: %w", err)
				l.stateMu.Unlock()
				l.cancel()
				return
			}
		}
	}
}

func (l *ContainerFileLock) writeMessage(message string) error {
	l.writeMu.Lock()
	defer l.writeMu.Unlock()
	_ = l.response.Conn.SetWriteDeadline(time.Now().Add(containerFileLockHeartbeat))
	_, err := io.WriteString(l.response.Conn, message+"\n")
	return err
}

// Release relinquishes the lock. It is safe to call more than once.
func (l *ContainerFileLock) Release() error {
	if l == nil {
		return nil
	}
	l.once.Do(func() {
		l.cancel()
		<-l.heartbeatDone
		writeErr := l.writeMessage("release")
		closeWriteErr := l.response.CloseWrite()

		var outputErr error
		select {
		case outputErr = <-l.outputDone:
		case <-time.After(containerFileLockReleaseTimeout):
			outputErr = fmt.Errorf("timed out waiting for container file lock holder to exit")
		}
		l.response.Close()
		_ = l.stdout.Close()
		inspection, inspectErr := inspectContainerFileLock(l.exec, l.execID)
		var exitErr error
		if inspectErr == nil && (inspection.Running || inspection.ExitCode != 0) {
			exitErr = fmt.Errorf("container file lock holder exited with code %d (running: %t)", inspection.ExitCode, inspection.Running)
		}
		l.stateMu.Lock()
		heartbeatErr := l.heartbeatErr
		l.stateMu.Unlock()
		l.releaseErr = errors.Join(
			wrapContainerFileLockIOError("signal lock holder", writeErr),
			wrapContainerFileLockIOError("close lock holder input", closeWriteErr),
			wrapContainerFileLockIOError("read lock holder output", outputErr),
			heartbeatErr,
			inspectErr,
			exitErr,
		)
	})
	return l.releaseErr
}

func wrapContainerFileLockIOError(operation string, err error) error {
	if err == nil || isIgnorableExecStreamError(err) {
		return nil
	}
	return fmt.Errorf("%s: %w", operation, err)
}

type boundedLockErrorWriter struct {
	buffer    bytes.Buffer
	remaining int
}

func (w *boundedLockErrorWriter) Write(data []byte) (int, error) {
	written := len(data)
	if w.remaining > 0 {
		chunk := data
		if len(chunk) > w.remaining {
			chunk = chunk[:w.remaining]
		}
		_, _ = w.buffer.Write(chunk)
		w.remaining -= len(chunk)
	}
	return written, nil
}

func (w *boundedLockErrorWriter) String() string {
	return w.buffer.String()
}
