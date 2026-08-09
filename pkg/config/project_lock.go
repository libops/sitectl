package config

import (
	"bufio"
	"bytes"
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
	"strings"
	"sync"
	"time"

	"github.com/kballard/go-shellquote"
)

type projectMutationLockContextKey struct{}

type projectMutationLockContextValue struct {
	requestedPath string
	lockPath      string
	remote        bool
}

const (
	remoteProjectLockReleaseTimeout = 5 * time.Second
	remoteProjectLockCloseTimeout   = time.Second
)

// ProjectMutationLock serializes a complete mutating operation for one
// Compose project. Its Context marks nested sitectl calls as already protected.
type ProjectMutationLock struct {
	context context.Context
	stdin   io.WriteCloser
	wait    func() error
	close   func()
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
		return &ProjectMutationLock{context: runCtx}, nil
	}
	var identityPath string
	if remote {
		var err error
		identityPath, err = c.canonicalRemoteProjectMutationLockIdentity(runCtx)
		if err != nil {
			return nil, err
		}
	} else {
		var err error
		identityPath, err = localProjectMutationLockIdentity(requestedPath)
		if err != nil {
			return nil, err
		}
	}
	lockPath := projectMutationLockPath(identityPath, remote)
	if held.remote == remote && held.lockPath == lockPath {
		return &ProjectMutationLock{context: runCtx}, nil
	}
	lockContext := context.WithValue(runCtx, projectMutationLockContextKey{}, projectMutationLockContextValue{
		requestedPath: requestedPath,
		lockPath:      lockPath,
		remote:        remote,
	})
	if remote {
		return c.acquireRemoteProjectMutationLock(runCtx, lockContext, lockPath)
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
	defer accessor.Close()
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

func projectMutationLockPath(projectIdentity string, remote bool) string {
	identity := strings.TrimSpace(projectIdentity)
	if remote {
		identity = path.Clean(strings.ReplaceAll(identity, `\`, "/"))
	}
	digest := sha256.Sum256([]byte(identity))
	name := "sitectl-project-" + hex.EncodeToString(digest[:16]) + ".lock"
	if remote {
		return path.Join("/tmp", name)
	}
	return filepath.Join(os.TempDir(), name)
}

func (c *Context) acquireRemoteProjectMutationLock(runCtx, lockContext context.Context, lockPath string) (*ProjectMutationLock, error) {
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
	var stderr bytes.Buffer
	session.Stderr = &stderr
	if err := session.Start(shellquote.Join("flock", "--exclusive", lockPath, "cat")); err != nil {
		_ = session.Close()
		_ = client.Close()
		return nil, fmt.Errorf("start remote project lock: %w", err)
	}
	var closeOnce sync.Once
	closeResources := func() { closeOnce.Do(func() { _ = session.Close(); _ = client.Close() }) }
	lock := &ProjectMutationLock{
		context: lockContext,
		stdin:   stdin,
		wait: func() error {
			return waitForRemoteProjectLockRelease(session.Wait, closeResources, remoteProjectLockReleaseTimeout, remoteProjectLockCloseTimeout)
		},
		close: closeResources,
	}
	if err := completeProjectMutationLockHandshake(runCtx, stdin, stdout); err != nil {
		// Acquisition cancellation must interrupt a remote flock that is still
		// waiting. Once the handshake succeeds, only Release ends the lock so a
		// caller can keep rollback/recovery protected after command cancellation.
		closeResources()
		_ = lock.Release()
		return nil, fmt.Errorf("acquire remote project lock: %w: %s", err, strings.TrimSpace(stderr.String()))
	}
	return lock, nil
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
