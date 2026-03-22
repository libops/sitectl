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
	_, _ = ctx.RunQuietCommandContext(runCtx, exec.Command("rm", "-f", path))
}

func DownloadContextFile(ctx *config.Context, sourcePath, localPath string) error {
	if ctx == nil {
		return fmt.Errorf("context is required")
	}
	if ctx.DockerHostType == config.ContextLocal {
		sourceFile, err := os.Open(sourcePath)
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
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	dst, err := os.Create(path)
	if err != nil {
		return err
	}
	defer dst.Close()
	_, err = io.Copy(dst, r)
	return err
}
