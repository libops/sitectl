package plugin

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path"
	"strings"
	"sync"
	"time"

	"github.com/libops/sitectl/pkg/config"
	"github.com/pkg/sftp"
	"golang.org/x/crypto/ssh"
)

const (
	remoteTemplateCleanupTimeout     = 30 * time.Second
	remoteTemplateCommandOutputLimit = 64 << 10
)

type limitedRemoteCommandBuffer struct {
	data []byte
}

func (b *limitedRemoteCommandBuffer) Write(data []byte) (int, error) {
	originalLength := len(data)
	remaining := remoteTemplateCommandOutputLimit - len(b.data)
	if remaining > 0 {
		if len(data) > remaining {
			data = data[:remaining]
		}
		b.data = append(b.data, data...)
	}
	return originalLength, nil
}

func (b *limitedRemoteCommandBuffer) String() string {
	return string(b.data)
}

type remoteTemplateFile interface {
	io.Reader
	io.Writer
	io.Closer
	Chmod(os.FileMode) error
	Stat() (os.FileInfo, error)
	Sync() error
}

type remoteTemplateConnection interface {
	Close() error
	Run(context.Context, io.Writer, io.Writer, ...string) (string, error)
	Lstat(string) (os.FileInfo, error)
	ReadDir(string) ([]os.FileInfo, error)
	Open(string) (remoteTemplateFile, error)
	OpenFile(string, int) (remoteTemplateFile, error)
	MkdirAll(string) error
	Mkdir(string) error
	Chmod(string, os.FileMode) error
	Remove(string) error
	Rename(string, string) error
}

var openRemoteTemplateConnection = newSSHRemoteTemplateConnection

type sshRemoteTemplateConnection struct {
	ssh           *ssh.Client
	sftp          *sftp.Client
	closed        chan struct{}
	closeOnce     sync.Once
	sftpCloseOnce sync.Once
}

func newSSHRemoteTemplateConnection(runCtx context.Context, ctx *config.Context) (remoteTemplateConnection, error) {
	if ctx == nil {
		return nil, fmt.Errorf("context is nil")
	}
	if runCtx == nil {
		runCtx = context.Background()
	}
	if err := runCtx.Err(); err != nil {
		return nil, err
	}
	sshClient, err := ctx.DialSSH()
	if err != nil {
		return nil, fmt.Errorf("establish SSH connection: %w", err)
	}
	sftpClient, err := sftp.NewClient(sshClient)
	if err != nil {
		_ = sshClient.Close()
		return nil, fmt.Errorf("establish SFTP connection: %w", err)
	}
	connection := &sshRemoteTemplateConnection{
		ssh:    sshClient,
		sftp:   sftpClient,
		closed: make(chan struct{}),
	}
	if done := runCtx.Done(); done != nil {
		go func() {
			select {
			case <-done:
				_ = connection.closeSFTP()
			case <-connection.closed:
			}
		}()
	}
	return connection, nil
}

func (c *sshRemoteTemplateConnection) Close() error {
	if c == nil {
		return nil
	}
	var closeErr error
	c.closeOnce.Do(func() {
		close(c.closed)
		closeErr = errors.Join(c.closeSFTP(), c.ssh.Close())
	})
	return closeErr
}

func (c *sshRemoteTemplateConnection) closeSFTP() error {
	var closeErr error
	c.sftpCloseOnce.Do(func() {
		closeErr = c.sftp.Close()
	})
	return closeErr
}

func (c *sshRemoteTemplateConnection) Run(runCtx context.Context, stdout, stderr io.Writer, args ...string) (string, error) {
	if runCtx == nil {
		runCtx = context.Background()
	}
	if err := runCtx.Err(); err != nil {
		return "", err
	}
	if len(args) == 0 {
		return "", fmt.Errorf("remote command cannot be empty")
	}
	session, err := c.ssh.NewSession()
	if err != nil {
		return "", remoteTemplateContextError(runCtx, fmt.Errorf("create SSH session: %w", err))
	}
	var closeOnce sync.Once
	closeSession := func() {
		_ = session.Close()
	}
	commandDone := make(chan struct{})
	defer close(commandDone)
	defer closeOnce.Do(closeSession)
	if done := runCtx.Done(); done != nil {
		go func() {
			select {
			case <-done:
				closeOnce.Do(closeSession)
			case <-commandDone:
			}
		}()
	}

	var stdoutBuffer limitedRemoteCommandBuffer
	var stderrBuffer limitedRemoteCommandBuffer
	if stdout == nil {
		session.Stdout = &stdoutBuffer
	} else {
		session.Stdout = stdout
	}
	if stderr == nil {
		session.Stderr = &stderrBuffer
	} else {
		session.Stderr = stderr
	}

	// SSH exec transports a command string rather than an argv array, so retain
	// argument boundaries with POSIX quoting without adding a Bash dependency.
	err = session.Run(shellJoin(args))
	output := strings.TrimRight(stdoutBuffer.String(), "\n")
	if err == nil {
		return output, nil
	}
	if contextErr := runCtx.Err(); contextErr != nil {
		return output, contextErr
	}
	if detail := strings.TrimSpace(stderrBuffer.String()); detail != "" {
		return output, fmt.Errorf("%s: %w", detail, err)
	}
	return output, err
}

func (c *sshRemoteTemplateConnection) Lstat(name string) (os.FileInfo, error) {
	return c.sftp.Lstat(name)
}

func (c *sshRemoteTemplateConnection) ReadDir(name string) ([]os.FileInfo, error) {
	return c.sftp.ReadDir(name)
}

func (c *sshRemoteTemplateConnection) Open(name string) (remoteTemplateFile, error) {
	return c.sftp.Open(name)
}

func (c *sshRemoteTemplateConnection) OpenFile(name string, flag int) (remoteTemplateFile, error) {
	return c.sftp.OpenFile(name, flag)
}

func (c *sshRemoteTemplateConnection) MkdirAll(directory string) error {
	if directory == "" || directory == "." || directory == "/" {
		return nil
	}
	return c.sftp.MkdirAll(directory)
}

func (c *sshRemoteTemplateConnection) Mkdir(directory string) error {
	return c.sftp.Mkdir(directory)
}

func (c *sshRemoteTemplateConnection) Chmod(name string, mode os.FileMode) error {
	return c.sftp.Chmod(name, mode)
}

func (c *sshRemoteTemplateConnection) Remove(name string) error {
	return c.sftp.Remove(name)
}

func (c *sshRemoteTemplateConnection) Rename(oldName, newName string) error {
	return c.sftp.Rename(oldName, newName)
}

func remoteTemplateContextError(runCtx context.Context, err error) error {
	if runCtx != nil && runCtx.Err() != nil {
		return runCtx.Err()
	}
	return err
}

func remoteProjectDirectoryState(runCtx context.Context, connection remoteTemplateConnection, projectDir string) (bool, bool, error) {
	if err := runCtx.Err(); err != nil {
		return false, false, err
	}
	info, err := connection.Lstat(projectDir)
	if err != nil {
		if os.IsNotExist(err) {
			return false, false, nil
		}
		return false, false, remoteTemplateContextError(runCtx, fmt.Errorf("inspect remote project directory %q: %w", projectDir, err))
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
		return true, false, fmt.Errorf("remote project directory %q must be a real directory, not a symlink or other file", projectDir)
	}
	entries, err := connection.ReadDir(projectDir)
	if err != nil {
		return false, false, remoteTemplateContextError(runCtx, fmt.Errorf("read remote project directory %q: %w", projectDir, err))
	}
	return true, len(entries) > 0, nil
}

func inspectRemoteTemplateCheckout(runCtx context.Context, connection remoteTemplateConnection, projectDir string) (templateCheckoutMetadata, error) {
	gitPath := path.Join(projectDir, ".git")
	gitInfo, err := connection.Lstat(gitPath)
	if err != nil {
		if os.IsNotExist(err) {
			return templateCheckoutMetadata{}, fmt.Errorf("remote template checkout has no Git history")
		}
		return templateCheckoutMetadata{}, remoteTemplateContextError(runCtx, fmt.Errorf("inspect remote template Git history: %w", err))
	}
	if gitInfo.Mode()&os.ModeSymlink != 0 || !gitInfo.IsDir() {
		return templateCheckoutMetadata{}, fmt.Errorf("remote template checkout has no Git history")
	}
	commit, err := connection.Run(runCtx, nil, nil, "git", "-C", projectDir, "rev-parse", "--verify", "HEAD^{commit}")
	if err != nil {
		return templateCheckoutMetadata{}, fmt.Errorf("resolve remote template commit: %w", err)
	}
	commit = strings.TrimSpace(commit)
	if !templateCommitPattern.MatchString(commit) {
		return templateCheckoutMetadata{}, fmt.Errorf("resolve remote template commit: git returned an invalid object id")
	}
	metadata := templateCheckoutMetadata{Commit: strings.ToLower(commit)}

	libopsPath := path.Join(projectDir, ".libops")
	info, err := connection.Lstat(libopsPath)
	if err != nil {
		if os.IsNotExist(err) {
			return metadata, nil
		}
		return templateCheckoutMetadata{}, remoteTemplateContextError(runCtx, fmt.Errorf("inspect remote template metadata directory: %w", err))
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
		return templateCheckoutMetadata{}, fmt.Errorf("template metadata path %q must be a real directory, not a symlink or other file", ".libops")
	}
	lockPath := path.Join(projectDir, path.Clean(templateLockPath))
	if _, lockErr := connection.Lstat(lockPath); lockErr == nil {
		return templateCheckoutMetadata{}, fmt.Errorf("source template must not contain %q; sitectl creates that downstream provenance file", templateLockPath)
	} else if !os.IsNotExist(lockErr) {
		return templateCheckoutMetadata{}, remoteTemplateContextError(runCtx, fmt.Errorf("inspect %s: %w", templateLockPath, lockErr))
	}

	contract, present, err := readRemoteTemplateMetadataFile(runCtx, connection, projectDir, templateContractPath, maxTemplateContractBytes)
	if err != nil {
		return templateCheckoutMetadata{}, err
	}
	if present {
		metadata.Contract = contract
		metadata.ComponentDefaultsRevision, err = validateTemplateContract(contract)
		if err != nil {
			return templateCheckoutMetadata{}, err
		}
	}
	revisionData, present, err := readRemoteTemplateMetadataFile(runCtx, connection, projectDir, componentDefaultsRevisionPath, maxComponentRevisionBytes)
	if err != nil {
		return templateCheckoutMetadata{}, err
	}
	if present {
		revision, revisionErr := validateComponentDefaultsRevision(string(revisionData))
		if revisionErr != nil {
			return templateCheckoutMetadata{}, fmt.Errorf("validate %s: %w", componentDefaultsRevisionPath, revisionErr)
		}
		if metadata.ComponentDefaultsRevision != "" && metadata.ComponentDefaultsRevision != revision {
			return templateCheckoutMetadata{}, fmt.Errorf("component defaults revision differs between %s and %s", templateContractPath, componentDefaultsRevisionPath)
		}
		metadata.ComponentDefaultsRevision = revision
	}
	return metadata, nil
}

func readRemoteTemplateMetadataFile(runCtx context.Context, connection remoteTemplateConnection, projectDir, relativePath string, maximum int64) ([]byte, bool, error) {
	if err := runCtx.Err(); err != nil {
		return nil, false, err
	}
	remotePath := path.Join(projectDir, path.Clean(relativePath))
	info, err := connection.Lstat(remotePath)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, false, nil
		}
		return nil, false, remoteTemplateContextError(runCtx, fmt.Errorf("inspect %s: %w", relativePath, err))
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
		return nil, false, fmt.Errorf("template metadata path %q must be a regular file, not a symlink or other file", relativePath)
	}
	if info.Size() > maximum {
		return nil, false, fmt.Errorf("template metadata path %q exceeds %d bytes", relativePath, maximum)
	}
	file, err := connection.Open(remotePath)
	if err != nil {
		return nil, false, remoteTemplateContextError(runCtx, fmt.Errorf("read %s: %w", relativePath, err))
	}
	openedInfo, statErr := file.Stat()
	if statErr != nil {
		_ = file.Close()
		return nil, false, remoteTemplateContextError(runCtx, fmt.Errorf("inspect opened %s: %w", relativePath, statErr))
	}
	if !openedInfo.Mode().IsRegular() || openedInfo.Size() > maximum {
		_ = file.Close()
		if openedInfo.Size() > maximum {
			return nil, false, fmt.Errorf("template metadata path %q exceeds %d bytes", relativePath, maximum)
		}
		return nil, false, fmt.Errorf("template metadata path %q must be a regular file, not a symlink or other file", relativePath)
	}
	data, readErr := io.ReadAll(io.LimitReader(file, maximum+1))
	closeErr := file.Close()
	if readErr != nil {
		return nil, false, remoteTemplateContextError(runCtx, fmt.Errorf("read %s: %w", relativePath, readErr))
	}
	if closeErr != nil {
		return nil, false, remoteTemplateContextError(runCtx, fmt.Errorf("close %s: %w", relativePath, closeErr))
	}
	if int64(len(data)) > maximum {
		return nil, false, fmt.Errorf("template metadata path %q exceeds %d bytes", relativePath, maximum)
	}
	if err := runCtx.Err(); err != nil {
		return nil, false, err
	}
	return data, true, nil
}

func finalizeRemoteTemplateCheckout(runCtx context.Context, connection remoteTemplateConnection, projectDir, branch string, lock []byte) error {
	temporaryPath, err := prepareRemoteTemplateLock(runCtx, connection, projectDir, lock)
	if err != nil {
		return err
	}
	published := false
	defer func() {
		if !published {
			_ = connection.Remove(temporaryPath)
		}
	}()

	gitPath := path.Join(projectDir, ".git")
	if _, err := connection.Run(runCtx, io.Discard, nil, "rm", "-rf", "--", gitPath); err != nil {
		return fmt.Errorf("remove remote template Git history: %w", err)
	}
	if _, err := connection.Lstat(gitPath); err == nil {
		return fmt.Errorf("remove remote template Git history: path still exists")
	} else if !os.IsNotExist(err) {
		return remoteTemplateContextError(runCtx, fmt.Errorf("verify remote template Git history removal: %w", err))
	}

	initArgs := []string{"git", "-C", projectDir, "init"}
	if strings.TrimSpace(branch) != "" {
		initArgs = append(initArgs, "-b", branch)
	}
	if _, err := connection.Run(runCtx, io.Discard, nil, initArgs...); err != nil {
		return fmt.Errorf("initialize fresh remote Git repository: %w", err)
	}
	lockPath := path.Join(projectDir, path.Clean(templateLockPath))
	if err := connection.Rename(temporaryPath, lockPath); err != nil {
		return remoteTemplateContextError(runCtx, fmt.Errorf("publish remote template lock: %w", err))
	}
	published = true
	return nil
}

func prepareRemoteTemplateLock(runCtx context.Context, connection remoteTemplateConnection, projectDir string, lock []byte) (temporaryPath string, err error) {
	if err := runCtx.Err(); err != nil {
		return "", err
	}
	libopsPath := path.Join(projectDir, ".libops")
	info, err := connection.Lstat(libopsPath)
	if err != nil {
		if !os.IsNotExist(err) {
			return "", remoteTemplateContextError(runCtx, fmt.Errorf("inspect remote template lock directory: %w", err))
		}
		if err := connection.Mkdir(libopsPath); err != nil {
			return "", remoteTemplateContextError(runCtx, fmt.Errorf("create remote template lock directory: %w", err))
		}
		if err := connection.Chmod(libopsPath, 0o750); err != nil {
			return "", remoteTemplateContextError(runCtx, fmt.Errorf("set remote template lock directory permissions: %w", err))
		}
		info, err = connection.Lstat(libopsPath)
		if err != nil {
			return "", remoteTemplateContextError(runCtx, fmt.Errorf("inspect created remote template lock directory: %w", err))
		}
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
		return "", fmt.Errorf("template lock directory must be a real directory, not a symlink or other file")
	}
	lockPath := path.Join(projectDir, path.Clean(templateLockPath))
	if _, lockErr := connection.Lstat(lockPath); lockErr == nil {
		return "", fmt.Errorf("source template must not contain %q; sitectl creates that downstream provenance file", templateLockPath)
	} else if !os.IsNotExist(lockErr) {
		return "", remoteTemplateContextError(runCtx, fmt.Errorf("inspect remote template lock path: %w", lockErr))
	}

	var random [16]byte
	if _, err := rand.Read(random[:]); err != nil {
		return "", fmt.Errorf("generate remote template lock name: %w", err)
	}
	temporaryPath = path.Join(libopsPath, ".template.lock.yaml.tmp-"+hex.EncodeToString(random[:]))
	file, err := connection.OpenFile(temporaryPath, os.O_CREATE|os.O_EXCL|os.O_WRONLY)
	if err != nil {
		return "", remoteTemplateContextError(runCtx, fmt.Errorf("create temporary remote template lock: %w", err))
	}
	closed := false
	defer func() {
		if !closed {
			_ = file.Close()
		}
		if err != nil {
			_ = connection.Remove(temporaryPath)
		}
	}()
	if err = file.Chmod(0o600); err != nil {
		return "", remoteTemplateContextError(runCtx, fmt.Errorf("set temporary remote template lock permissions: %w", err))
	}
	if _, err = io.Copy(file, bytes.NewReader(lock)); err != nil {
		return "", remoteTemplateContextError(runCtx, fmt.Errorf("write temporary remote template lock: %w", err))
	}
	if err = file.Sync(); err != nil && !isSFTPOperationUnsupported(err) {
		return "", remoteTemplateContextError(runCtx, fmt.Errorf("sync temporary remote template lock: %w", err))
	}
	if err = file.Chmod(0o644); err != nil {
		return "", remoteTemplateContextError(runCtx, fmt.Errorf("set remote template lock permissions: %w", err))
	}
	if err = file.Close(); err != nil {
		return "", remoteTemplateContextError(runCtx, fmt.Errorf("close temporary remote template lock: %w", err))
	}
	closed = true
	if err = runCtx.Err(); err != nil {
		return "", err
	}
	return temporaryPath, nil
}

func cleanupOwnedRemoteTemplateCheckout(connection remoteTemplateConnection, projectDir string, cause error) error {
	cleanupCtx, cancel := context.WithTimeout(context.Background(), remoteTemplateCleanupTimeout)
	defer cancel()
	if _, err := connection.Run(cleanupCtx, io.Discard, nil, "rm", "-rf", "--", projectDir); err != nil {
		return errors.Join(cause, fmt.Errorf("clean up failed remote template checkout: %w", err))
	}
	return cause
}

func isSFTPOperationUnsupported(err error) bool {
	var statusErr *sftp.StatusError
	if !errors.As(err, &statusErr) {
		return false
	}
	return statusErr.FxCode() == sftp.ErrSSHFxOpUnsupported
}
