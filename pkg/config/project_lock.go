package config

import (
	"bufio"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/kballard/go-shellquote"
	"github.com/pkg/sftp"
)

type projectMutationLockContextKey struct{}

type projectMutationLockLossContextKey struct{}

type projectMutationLockLossState struct {
	lost     atomic.Bool
	done     chan struct{}
	doneOnce sync.Once
}

func newProjectMutationLockLossState() *projectMutationLockLossState {
	return &projectMutationLockLossState{done: make(chan struct{})}
}

func (s *projectMutationLockLossState) markLost() {
	if s == nil {
		return
	}
	s.lost.Store(true)
	s.doneOnce.Do(func() { close(s.done) })
}

type projectMutationLockContextValue struct {
	requestedPath string
	lockPath      string
	remote        bool
}

const (
	remoteProjectLockReleaseTimeout = 5 * time.Second
	remoteProjectLockCloseTimeout   = time.Second
)

// ErrProjectMutationLockLost reports that a remote lock process exited before
// the holder began releasing it. Callers must stop mutating the project because
// subsequent operations are no longer fenced from another operator.
var ErrProjectMutationLockLost = errors.New("project mutation lock was lost")

// ProjectMutationLockContextLost reports whether the remote lock process for a
// held mutation-lock context exited unexpectedly. It remains true even when an
// earlier caller cancellation is already the context's cancellation cause.
func ProjectMutationLockContextLost(runCtx context.Context) bool {
	if runCtx == nil {
		return false
	}
	state, _ := runCtx.Value(projectMutationLockLossContextKey{}).(*projectMutationLockLossState)
	return state != nil && state.lost.Load()
}

// ProjectMutationLockFenceContext preserves lock-context values and ordinary
// cancellation recovery while cancelling only if the physical remote lock is
// lost. It is intended for rollback-capable transports that must remain usable
// after a caller cancels an operation.
func ProjectMutationLockFenceContext(runCtx context.Context) context.Context {
	if runCtx == nil {
		runCtx = context.Background()
	}
	state, _ := runCtx.Value(projectMutationLockLossContextKey{}).(*projectMutationLockLossState)
	return projectMutationLockFenceContext{Context: context.WithoutCancel(runCtx), lossState: state}
}

type projectMutationLockFenceContext struct {
	context.Context
	lossState *projectMutationLockLossState
}

func (c projectMutationLockFenceContext) Done() <-chan struct{} {
	if c.lossState == nil {
		return nil
	}
	return c.lossState.done
}

func (c projectMutationLockFenceContext) Err() error {
	if c.lossState != nil && c.lossState.lost.Load() {
		return context.Canceled
	}
	return nil
}

// ProjectMutationLock serializes a complete mutating operation for one
// Compose project. Its Context marks nested sitectl calls as already protected.
type ProjectMutationLock struct {
	context context.Context
	stdin   io.WriteCloser
	wait    func() error
	close   func()
	begin   func()
	once    sync.Once
	err     error
}

func (l *ProjectMutationLock) Context() context.Context {
	if l == nil || l.context == nil {
		return context.Background()
	}
	return l.context
}

func (l *ProjectMutationLock) Release() error {
	if l == nil {
		return nil
	}
	l.once.Do(func() {
		if l.begin != nil {
			l.begin()
		}
		var closeInputErr error
		if l.stdin != nil {
			closeInputErr = l.stdin.Close()
		}
		var waitErr error
		if l.wait != nil {
			waitErr = l.wait()
		}
		if l.close != nil {
			l.close()
		}
		l.err = errors.Join(closeInputErr, waitErr)
	})
	return l.err
}

// AcquireProjectMutationLock waits for the project-scoped operator lock. Local
// contexts derive the lock name from the opened directory's filesystem identity
// so path aliases converge, then use the platform advisory file-lock API.
// Remote contexts hold flock through a direct `flock ... cat` process; no
// inline program is evaluated.
// The acquisition context bounds waiting, but a successful lock remains held
// until Release so cancellation recovery can finish while still serialized.
func (c *Context) AcquireProjectMutationLock(runCtx context.Context) (*ProjectMutationLock, error) {
	if c == nil {
		return nil, fmt.Errorf("context is nil")
	}
	if runCtx == nil {
		runCtx = context.Background()
	}
	if strings.TrimSpace(c.ProjectDir) == "" {
		return nil, fmt.Errorf("project directory is required for mutation lock")
	}
	remote := c.DockerHostType == ContextRemote
	requestedPath := projectMutationLockRequestedPath(c.ProjectDir, remote)
	held, _ := runCtx.Value(projectMutationLockContextKey{}).(projectMutationLockContextValue)
	// Remote recovery can reacquire through the same context after the caller's
	// cancellation without another SSH round trip. Local requests still resolve
	// file identity so a relative path cannot become reentrant after cwd changes.
	if remote && held.remote && held.requestedPath == requestedPath && held.lockPath != "" {
		if ProjectMutationLockContextLost(runCtx) {
			return nil, ErrProjectMutationLockLost
		}
		return &ProjectMutationLock{context: runCtx}, nil
	}
	var (
		identityPath string
		lockPath     string
	)
	if remote {
		var err error
		identityPath, err = c.canonicalRemoteProjectMutationLockIdentity(runCtx)
		if err != nil {
			return nil, err
		}
		lockPath, err = c.remoteProjectMutationLockPath(runCtx, identityPath)
		if err != nil {
			return nil, err
		}
	} else {
		var err error
		identityPath, err = localProjectMutationLockIdentity(requestedPath)
		if err != nil {
			return nil, err
		}
		lockPath, err = localProjectMutationLockPath(identityPath)
		if err != nil {
			return nil, err
		}
	}
	if held.remote == remote && held.lockPath == lockPath {
		return &ProjectMutationLock{context: runCtx}, nil
	}
	lockContext := context.WithValue(runCtx, projectMutationLockContextKey{}, projectMutationLockContextValue{
		requestedPath: requestedPath,
		lockPath:      lockPath,
		remote:        remote,
	})
	if remote {
		lossState := newProjectMutationLockLossState()
		lockContext = context.WithValue(lockContext, projectMutationLockLossContextKey{}, lossState)
		return c.acquireRemoteProjectMutationLock(runCtx, lockContext, lockPath, lossState)
	}
	return acquireLocalProjectMutationLock(runCtx, lockContext, lockPath)
}

func projectMutationLockRequestedPath(projectDir string, remote bool) string {
	projectDir = strings.TrimSpace(projectDir)
	if remote {
		return path.Clean(strings.ReplaceAll(projectDir, `\`, "/"))
	}
	return filepath.Clean(projectDir)
}

func (c *Context) canonicalRemoteProjectMutationLockIdentity(runCtx context.Context) (string, error) {
	if err := runCtx.Err(); err != nil {
		return "", err
	}
	accessor, err := c.NewFileAccessor()
	if err != nil {
		return "", fmt.Errorf("open remote project directory for mutation lock: %w", err)
	}
	defer func() { _ = accessor.Close() }()
	identity, err := canonicalRemoteProjectMutationLockIdentity(accessor, c.ProjectDir)
	if err != nil {
		return "", err
	}
	if err := runCtx.Err(); err != nil {
		return "", err
	}
	return identity, nil
}

func canonicalRemoteProjectMutationLockIdentity(accessor projectFileInspector, projectDir string) (string, error) {
	identity, err := accessor.RealPath(projectMutationLockRequestedPath(projectDir, true))
	if err != nil {
		return "", fmt.Errorf("resolve remote project directory for mutation lock: %w", err)
	}
	identity = path.Clean(strings.TrimSpace(identity))
	if identity == "." || !path.IsAbs(identity) {
		return "", fmt.Errorf("remote project directory resolved to invalid path %q", identity)
	}
	return identity, nil
}

func projectMutationLockFilename(projectIdentity string) string {
	identity := strings.TrimSpace(projectIdentity)
	digest := sha256.Sum256([]byte(identity))
	return "project-" + hex.EncodeToString(digest[:16]) + ".lock"
}

func (c *Context) remoteProjectMutationLockPath(runCtx context.Context, projectIdentity string) (returnPath string, returnErr error) {
	if err := runCtx.Err(); err != nil {
		return "", err
	}
	accessor, err := c.NewFileAccessor()
	if err != nil {
		return "", fmt.Errorf("open remote lock directory: %w", err)
	}
	defer func() {
		returnErr = errors.Join(returnErr, accessor.Close())
	}()
	if accessor.sftp == nil || accessor.ssh == nil {
		return "", fmt.Errorf("remote lock directory requires SSH and SFTP")
	}
	home, err := accessor.sftp.RealPath(".")
	if err != nil {
		return "", fmt.Errorf("resolve remote home directory for project lock: %w", err)
	}
	home = path.Clean(strings.TrimSpace(home))
	if !path.IsAbs(home) {
		return "", fmt.Errorf("remote home directory resolved to invalid path %q", home)
	}
	uid, err := remoteProjectMutationLockUID(runCtx, accessor)
	if err != nil {
		return "", err
	}
	homeInfo, err := accessor.sftp.Lstat(home)
	if err != nil {
		return "", fmt.Errorf("inspect remote home directory for project lock: %w", err)
	}
	if err := validateRemoteProjectMutationLockDirectory(home, homeInfo, uid, false); err != nil {
		return "", err
	}
	baseDirectory := path.Join(home, ".sitectl")
	if err := ensureRemoteProjectMutationLockDirectory(accessor.sftp, baseDirectory, uid, false); err != nil {
		return "", err
	}
	lockDirectory := path.Join(baseDirectory, "locks")
	if err := ensureRemoteProjectMutationLockDirectory(accessor.sftp, lockDirectory, uid, true); err != nil {
		return "", err
	}
	resolvedLockDirectory, err := accessor.sftp.RealPath(lockDirectory)
	if err != nil {
		return "", fmt.Errorf("resolve remote project lock directory: %w", err)
	}
	if path.Clean(resolvedLockDirectory) != lockDirectory {
		return "", fmt.Errorf("remote project lock directory %q must not resolve through a symlink", lockDirectory)
	}
	lockPath := path.Join(lockDirectory, projectMutationLockFilename(projectIdentity))
	if err := ensureRemoteProjectMutationLockFile(accessor.sftp, lockPath, uid); err != nil {
		return "", err
	}
	if err := runCtx.Err(); err != nil {
		return "", err
	}
	return lockPath, nil
}

type boundedProjectMutationLockOutput struct {
	maximum  int
	data     []byte
	exceeded bool
}

func (b *boundedProjectMutationLockOutput) Write(data []byte) (int, error) {
	originalLength := len(data)
	remaining := b.maximum - len(b.data)
	if remaining > 0 {
		if len(data) > remaining {
			data = data[:remaining]
		}
		b.data = append(b.data, data...)
	}
	if originalLength > remaining {
		b.exceeded = true
	}
	return originalLength, nil
}

func remoteProjectMutationLockUID(runCtx context.Context, accessor *FileAccessor) (uint32, error) {
	session, err := accessor.ssh.NewSession()
	if err != nil {
		return 0, fmt.Errorf("create remote user identity session: %w", err)
	}
	defer func() { _ = session.Close() }()
	stdout := &boundedProjectMutationLockOutput{maximum: 64}
	stderr := &boundedProjectMutationLockOutput{maximum: 1024}
	session.Stdout = stdout
	session.Stderr = stderr
	if err := session.Start(shellquote.Join("id", "-u")); err != nil {
		return 0, fmt.Errorf("start remote user identity command: %w", err)
	}
	done := make(chan error, 1)
	go func() { done <- session.Wait() }()
	select {
	case <-runCtx.Done():
		_ = session.Close()
		<-done
		return 0, runCtx.Err()
	case err := <-done:
		if err != nil {
			return 0, fmt.Errorf("resolve remote user identity: %w: %s", err, strings.TrimSpace(string(stderr.data)))
		}
	}
	if stdout.exceeded {
		return 0, fmt.Errorf("remote user identity output exceeded %d bytes", stdout.maximum)
	}
	value, err := strconv.ParseUint(strings.TrimSpace(string(stdout.data)), 10, 32)
	if err != nil {
		return 0, fmt.Errorf("parse remote user identity: %w", err)
	}
	return uint32(value), nil
}

func remoteProjectMutationLockOwner(info os.FileInfo) (uint32, error) {
	stat, ok := info.Sys().(*sftp.FileStat)
	if !ok {
		return 0, fmt.Errorf("remote path %q does not expose an owner", info.Name())
	}
	return stat.UID, nil
}

func validateRemoteProjectMutationLockDirectory(directory string, info os.FileInfo, uid uint32, private bool) error {
	if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
		return fmt.Errorf("remote project lock directory %q must be a real directory", directory)
	}
	owner, err := remoteProjectMutationLockOwner(info)
	if err != nil {
		return err
	}
	if owner != uid {
		return fmt.Errorf("remote project lock directory %q is owned by uid %d, want uid %d", directory, owner, uid)
	}
	if private {
		if info.Mode().Perm() != 0o700 {
			return fmt.Errorf("remote project lock directory %q has permissions %04o, want 0700", directory, info.Mode().Perm())
		}
	} else if info.Mode().Perm()&0o022 != 0 {
		return fmt.Errorf("remote project lock parent %q must not be group- or world-writable", directory)
	}
	return nil
}

func ensureRemoteProjectMutationLockDirectory(client *sftp.Client, directory string, uid uint32, private bool) error {
	info, err := client.Lstat(directory)
	if err != nil {
		if !os.IsNotExist(err) {
			return fmt.Errorf("inspect remote project lock directory %q: %w", directory, err)
		}
		created := false
		if err := client.Mkdir(directory); err != nil && !os.IsExist(err) {
			if _, statErr := client.Lstat(directory); statErr != nil {
				return fmt.Errorf("create remote project lock directory %q: %w", directory, err)
			}
		} else if err == nil {
			created = true
		}
		if created {
			if err := client.Chmod(directory, 0o700); err != nil {
				return fmt.Errorf("set remote project lock directory %q permissions: %w", directory, err)
			}
		}
		info, err = client.Lstat(directory)
		if err != nil {
			return fmt.Errorf("inspect created remote project lock directory %q: %w", directory, err)
		}
	}
	return validateRemoteProjectMutationLockDirectory(directory, info, uid, private)
}

func ensureRemoteProjectMutationLockFile(client *sftp.Client, lockPath string, uid uint32) error {
	file, createErr := client.OpenFile(lockPath, os.O_CREATE|os.O_EXCL|os.O_WRONLY)
	if createErr == nil {
		chmodErr := file.Chmod(0o600)
		closeErr := file.Close()
		if err := errors.Join(chmodErr, closeErr); err != nil {
			return fmt.Errorf("initialize remote project lock file %q: %w", lockPath, err)
		}
	}
	info, statErr := client.Lstat(lockPath)
	if statErr != nil {
		if createErr != nil {
			return errors.Join(fmt.Errorf("prepare remote project lock file %q: %w", lockPath, createErr), fmt.Errorf("inspect remote project lock file: %w", statErr))
		}
		return fmt.Errorf("inspect remote project lock file %q: %w", lockPath, statErr)
	}
	// Some SFTP servers do not preserve an EEXIST sentinel. A successful lstat
	// of the protected path is sufficient after the owner/type checks below.
	if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
		return fmt.Errorf("remote project lock path %q must be a regular file, not a symlink or other file", lockPath)
	}
	owner, err := remoteProjectMutationLockOwner(info)
	if err != nil {
		return err
	}
	if owner != uid {
		return fmt.Errorf("remote project lock path %q is owned by uid %d, want uid %d", lockPath, owner, uid)
	}
	if info.Mode().Perm() != 0o600 {
		return fmt.Errorf("remote project lock path %q has permissions %04o, want 0600", lockPath, info.Mode().Perm())
	}
	return nil
}

func (c *Context) acquireRemoteProjectMutationLock(runCtx, lockContext context.Context, lockPath string, lossState *projectMutationLockLossState) (*ProjectMutationLock, error) {
	client, err := c.DialSSH()
	if err != nil {
		return nil, fmt.Errorf("connect for remote project lock: %w", err)
	}
	session, err := client.NewSession()
	if err != nil {
		_ = client.Close()
		return nil, fmt.Errorf("create remote project lock session: %w", err)
	}
	stdin, err := session.StdinPipe()
	if err != nil {
		_ = session.Close()
		_ = client.Close()
		return nil, fmt.Errorf("open remote project lock input: %w", err)
	}
	stdout, err := session.StdoutPipe()
	if err != nil {
		_ = session.Close()
		_ = client.Close()
		return nil, fmt.Errorf("open remote project lock output: %w", err)
	}
	stderr := &boundedProjectMutationLockOutput{maximum: 64 << 10}
	session.Stderr = stderr
	if err := session.Start(shellquote.Join("flock", "--exclusive", lockPath, "cat")); err != nil {
		_ = session.Close()
		_ = client.Close()
		return nil, fmt.Errorf("start remote project lock: %w", err)
	}
	waitResult := newProjectMutationWaitResult(session.Wait)
	releaseStarted := make(chan struct{})
	releaseState := &atomic.Uint32{}
	var releaseOnce sync.Once
	beginRelease := func() {
		releaseOnce.Do(func() {
			releaseState.CompareAndSwap(0, 1)
			close(releaseStarted)
		})
	}
	monitoredContext, cancelLockContext := context.WithCancelCause(lockContext)
	var closeOnce sync.Once
	closeResources := func() { closeOnce.Do(func() { _ = session.Close(); _ = client.Close() }) }
	lock := &ProjectMutationLock{
		context: monitoredContext,
		stdin:   stdin,
		begin:   beginRelease,
		wait: func() error {
			return waitForRemoteProjectLockRelease(waitResult.Wait, closeResources, remoteProjectLockReleaseTimeout, remoteProjectLockCloseTimeout)
		},
		close: closeResources,
	}
	if err := completeProjectMutationLockHandshake(runCtx, stdin, stdout); err != nil {
		// Acquisition cancellation must interrupt a remote flock that is still
		// waiting. Once the handshake succeeds, only Release ends the lock so a
		// caller can keep rollback/recovery protected after command cancellation.
		closeResources()
		_ = lock.Release()
		return nil, fmt.Errorf("acquire remote project lock: %w: %s", err, strings.TrimSpace(string(stderr.data)))
	}
	go monitorRemoteProjectMutationLock(waitResult, releaseStarted, releaseState, lossState, cancelLockContext)
	return lock, nil
}

type projectMutationWaitResult struct {
	done chan struct{}
	err  error
}

func newProjectMutationWaitResult(wait func() error) *projectMutationWaitResult {
	result := &projectMutationWaitResult{done: make(chan struct{})}
	go func() {
		result.err = wait()
		close(result.done)
	}()
	return result
}

func (r *projectMutationWaitResult) Wait() error {
	if r == nil {
		return nil
	}
	<-r.done
	return r.err
}

func monitorRemoteProjectMutationLock(waitResult *projectMutationWaitResult, releaseStarted <-chan struct{}, releaseState *atomic.Uint32, lossState *projectMutationLockLossState, cancel context.CancelCauseFunc) {
	select {
	case <-releaseStarted:
		return
	case <-waitResult.done:
	}
	if !releaseState.CompareAndSwap(0, 2) {
		return
	}
	lossState.markLost()
	waitErr := waitResult.Wait()
	if waitErr == nil {
		cancel(ErrProjectMutationLockLost)
		return
	}
	cancel(fmt.Errorf("%w: remote lock process exited: %v", ErrProjectMutationLockLost, waitErr))
}

func waitForRemoteProjectLockRelease(wait func() error, forceClose func(), releaseTimeout, closeTimeout time.Duration) error {
	done := make(chan error, 1)
	go func() { done <- wait() }()
	timer := time.NewTimer(releaseTimeout)
	defer timer.Stop()
	select {
	case err := <-done:
		return err
	case <-timer.C:
		forceClose()
	}

	// Give the forcibly closed SSH channel a short bounded interval to join.
	// The timeout remains an error even if Wait now returns: the normal release
	// handshake did not complete and the operator should know it was aborted.
	closeTimer := time.NewTimer(closeTimeout)
	defer closeTimer.Stop()
	select {
	case <-done:
	case <-closeTimer.C:
	}
	return fmt.Errorf("remote project lock did not release within %s; forcibly closed the SSH session", releaseTimeout)
}

func completeProjectMutationLockHandshake(runCtx context.Context, stdin io.Writer, stdout io.Reader) error {
	var nonce [16]byte
	if _, err := rand.Read(nonce[:]); err != nil {
		return fmt.Errorf("generate project lock handshake: %w", err)
	}
	want := hex.EncodeToString(nonce[:])
	if _, err := io.WriteString(stdin, want+"\n"); err != nil {
		return err
	}
	result := make(chan struct {
		line string
		err  error
	}, 1)
	go func() {
		line, err := bufio.NewReader(stdout).ReadString('\n')
		result <- struct {
			line string
			err  error
		}{line: line, err: err}
	}()
	select {
	case <-runCtx.Done():
		return runCtx.Err()
	case got := <-result:
		if got.err != nil {
			return got.err
		}
		if strings.TrimSpace(got.line) != want {
			return fmt.Errorf("unexpected project lock handshake %q", strings.TrimSpace(got.line))
		}
		return nil
	}
}
