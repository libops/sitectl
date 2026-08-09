package job

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"strings"

	"github.com/libops/sitectl/pkg/config"
	"github.com/pkg/sftp"
)

func RemoveContextHostPath(runCtx context.Context, ctx *config.Context, path string) {
	if ctx == nil || strings.TrimSpace(path) == "" {
		return
	}
	_, _ = ctx.RunQuietCommandContext(runCtx, exec.Command("rm", "-f", path)) // #nosec G204 -- sitectl intentionally removes a caller-selected context path without invoking a shell.
}

func DownloadContextFile(ctx *config.Context, sourcePath, localPath string) error {
	return DownloadContextFileContext(context.Background(), ctx, sourcePath, localPath)
}

// DownloadContextFileContext copies a context-host file to a private local
// file and actively closes remote transports when the operation is canceled.
func DownloadContextFileContext(runCtx context.Context, ctx *config.Context, sourcePath, localPath string) error {
	if ctx == nil {
		return fmt.Errorf("context is required")
	}
	if runCtx == nil {
		runCtx = context.Background()
	}
	if err := runCtx.Err(); err != nil {
		return err
	}
	if ctx.DockerHostType == config.ContextLocal {
		sourceFile, err := os.Open(sourcePath) // #nosec G304 -- sitectl intentionally downloads a caller-selected local project file.
		if err != nil {
			return err
		}
		defer sourceFile.Close()
		return writeLocalFileContext(runCtx, localPath, sourceFile)
	}

	sshClient, err := ctx.DialSSH()
	if err != nil {
		return err
	}
	defer sshClient.Close()
	stopCancellation := closeTransfersOnCancellation(runCtx, sshClient.Close)
	defer stopCancellation()

	sftpClient, err := sftp.NewClient(sshClient)
	if err != nil {
		return err
	}
	defer sftpClient.Close()

	sourceFile, err := sftpClient.Open(sourcePath)
	if err != nil {
		return err
	}
	defer sourceFile.Close()

	return writeLocalFileContext(runCtx, localPath, sourceFile)
}

// UploadContextFile copies a private local file to the selected context
// without publishing a partial destination after cancellation or failure.
func UploadContextFile(runCtx context.Context, ctx *config.Context, localPath, destinationPath string) error {
	if ctx == nil {
		return fmt.Errorf("context is required")
	}
	if runCtx == nil {
		runCtx = context.Background()
	}
	if err := runCtx.Err(); err != nil {
		return err
	}
	sourceFile, err := os.Open(localPath) // #nosec G304 -- source is an explicit caller-selected local artifact.
	if err != nil {
		return err
	}
	defer sourceFile.Close()

	if ctx.DockerHostType == config.ContextLocal {
		if err := os.MkdirAll(filepath.Dir(destinationPath), 0o700); err != nil {
			return err
		}
		destination, err := os.OpenFile(destinationPath, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600) // #nosec G304 -- destination is an explicit context artifact path.
		if err != nil {
			return err
		}
		committed := false
		defer func() {
			_ = destination.Close()
			if !committed {
				_ = os.Remove(destinationPath)
			}
		}()
		if _, err := copyContext(runCtx, destination, sourceFile); err != nil {
			return err
		}
		if err := runCtx.Err(); err != nil {
			return err
		}
		if err := destination.Sync(); err != nil {
			return err
		}
		if err := runCtx.Err(); err != nil {
			return err
		}
		if err := destination.Close(); err != nil {
			return err
		}
		committed = true
		return nil
	}

	sshClient, err := ctx.DialSSH()
	if err != nil {
		return err
	}
	defer sshClient.Close()
	stopCancellation := closeTransfersOnCancellation(runCtx, sshClient.Close)
	defer stopCancellation()
	sftpClient, err := sftp.NewClient(sshClient)
	if err != nil {
		return err
	}
	defer sftpClient.Close()
	if err := sftpClient.MkdirAll(path.Dir(destinationPath)); err != nil {
		return err
	}
	temporaryPath, err := transferTemporaryPath(destinationPath)
	if err != nil {
		return err
	}
	destination, err := sftpClient.OpenFile(temporaryPath, os.O_CREATE|os.O_EXCL|os.O_WRONLY)
	if err != nil {
		return err
	}
	committed := false
	defer func() {
		_ = destination.Close()
		if !committed {
			_ = sftpClient.Remove(temporaryPath)
		}
	}()
	if err := sftpClient.Chmod(temporaryPath, 0o600); err != nil {
		return err
	}
	if _, err := copyContext(runCtx, destination, sourceFile); err != nil {
		return err
	}
	if err := runCtx.Err(); err != nil {
		return err
	}
	if err := destination.Sync(); err != nil {
		return err
	}
	if err := runCtx.Err(); err != nil {
		return err
	}
	if err := destination.Close(); err != nil {
		return err
	}
	if err := runCtx.Err(); err != nil {
		return err
	}
	if _, err := sftpClient.Lstat(destinationPath); err == nil {
		return fmt.Errorf("refusing to overwrite existing context file %q", destinationPath)
	} else if !os.IsNotExist(err) {
		return fmt.Errorf("inspect context upload destination %q: %w", destinationPath, err)
	}
	if err := sftpClient.Rename(temporaryPath, destinationPath); err != nil {
		return err
	}
	committed = true
	return nil
}

func transferTemporaryPath(destination string) (string, error) {
	identifier := make([]byte, 16)
	if _, err := rand.Read(identifier); err != nil {
		return "", err
	}
	return destination + ".sitectl-transfer-" + hex.EncodeToString(identifier), nil
}

func EnsureDirOnContext(ctx *config.Context, dir string) error {
	if ctx == nil {
		return fmt.Errorf("context is required")
	}
	accessor, err := config.NewFileAccessor(ctx)
	if err != nil {
		return err
	}
	defer accessor.Close()
	return accessor.MkdirAll(dir)
}

func writeLocalFileContext(runCtx context.Context, path string, r io.Reader) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
		return err
	}
	dst, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600) // #nosec G304 -- destination is an explicit caller-selected download target.
	if err != nil {
		return err
	}
	committed := false
	defer func() {
		_ = dst.Close()
		if !committed {
			_ = os.Remove(path)
		}
	}()
	if _, err := copyContext(runCtx, dst, r); err != nil {
		return err
	}
	if err := runCtx.Err(); err != nil {
		return err
	}
	if err := dst.Sync(); err != nil {
		return err
	}
	if err := runCtx.Err(); err != nil {
		return err
	}
	if err := dst.Close(); err != nil {
		return err
	}
	committed = true
	return nil
}

func copyContext(runCtx context.Context, destination io.Writer, source io.Reader) (int64, error) {
	buffer := make([]byte, 128*1024)
	var written int64
	for {
		if err := runCtx.Err(); err != nil {
			return written, err
		}
		read, readErr := source.Read(buffer)
		if read > 0 {
			count, writeErr := destination.Write(buffer[:read])
			written += int64(count)
			if writeErr != nil {
				return written, writeErr
			}
			if count != read {
				return written, io.ErrShortWrite
			}
		}
		if readErr == io.EOF {
			return written, nil
		}
		if readErr != nil {
			return written, readErr
		}
	}
}

func closeTransfersOnCancellation(runCtx context.Context, closers ...func() error) func() {
	done := make(chan struct{})
	go func() {
		select {
		case <-runCtx.Done():
			for _, closeTransfer := range closers {
				_ = closeTransfer()
			}
		case <-done:
		}
	}()
	return func() { close(done) }
}
