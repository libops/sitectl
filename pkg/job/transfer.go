package job

import (
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
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
	if ctx == nil {
		return fmt.Errorf("context is required")
	}
	if ctx.DockerHostType == config.ContextLocal {
		sourceFile, err := os.Open(sourcePath) // #nosec G304 -- sitectl intentionally downloads a caller-selected local project file.
		if err != nil {
			return err
		}
		defer sourceFile.Close()
		return writeLocalFile(localPath, sourceFile)
	}

	sshClient, err := ctx.DialSSH()
	if err != nil {
		return err
	}
	defer sshClient.Close()

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

	return writeLocalFile(localPath, sourceFile)
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

func writeLocalFile(path string, r io.Reader) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
		return err
	}
	dst, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o600) // #nosec G304 -- destination is an explicit caller-selected download target.
	if err != nil {
		return err
	}
	defer dst.Close()
	_, err = io.Copy(dst, r)
	return err
}
