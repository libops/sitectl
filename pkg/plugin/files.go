package plugin

import (
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"sort"

	"github.com/libops/sitectl/pkg/config"
	"github.com/pkg/sftp"
	"golang.org/x/crypto/ssh"
)

type FileAccessor struct {
	ctx  *config.Context
	ssh  *ssh.Client
	sftp *sftp.Client
}

func (s *SDK) GetFileAccessor() (*FileAccessor, error) {
	ctx, err := s.GetContext()
	if err != nil {
		return nil, err
	}
	return NewFileAccessor(ctx)
}

func NewFileAccessor(ctx *config.Context) (*FileAccessor, error) {
	accessor := &FileAccessor{ctx: ctx}
	if ctx == nil || ctx.DockerHostType == config.ContextLocal {
		return accessor, nil
	}
	client, err := ctx.DialSSH()
	if err != nil {
		return nil, err
	}
	sftpClient, err := sftp.NewClient(client)
	if err != nil {
		client.Close()
		return nil, err
	}
	accessor.ssh = client
	accessor.sftp = sftpClient
	return accessor, nil
}

func (a *FileAccessor) Close() error {
	if a == nil {
		return nil
	}
	if a.sftp != nil {
		_ = a.sftp.Close()
	}
	if a.ssh != nil {
		return a.ssh.Close()
	}
	return nil
}

func (a *FileAccessor) ReadFile(path string) ([]byte, error) {
	if a == nil || a.ctx == nil || a.ctx.DockerHostType == config.ContextLocal {
		return os.ReadFile(path)
	}
	file, err := a.sftp.Open(path)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	return io.ReadAll(file)
}

func (a *FileAccessor) ListFiles(root string) ([]string, error) {
	if a == nil || a.ctx == nil || a.ctx.DockerHostType == config.ContextLocal {
		files := []string{}
		err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if d.IsDir() {
				return nil
			}
			rel, err := filepath.Rel(root, path)
			if err != nil {
				return err
			}
			files = append(files, filepath.ToSlash(rel))
			return nil
		})
		return files, err
	}
	files := []string{}
	walker := a.sftp.Walk(root)
	for walker.Step() {
		if err := walker.Err(); err != nil {
			return nil, err
		}
		if walker.Stat().IsDir() {
			continue
		}
		rel, err := filepath.Rel(root, walker.Path())
		if err != nil {
			return nil, err
		}
		files = append(files, filepath.ToSlash(rel))
	}
	return files, nil
}

func (a *FileAccessor) MatchFiles(root, pattern string) ([]string, error) {
	files, err := a.ListFiles(root)
	if err != nil {
		return nil, err
	}
	matches := []string{}
	for _, rel := range files {
		ok, err := filepath.Match(pattern, filepath.Base(rel))
		if err != nil {
			return nil, err
		}
		if ok {
			matches = append(matches, filepath.Join(root, rel))
		}
	}
	sort.Strings(matches)
	return matches, nil
}
