package config

import (
	"context"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"github.com/pkg/sftp"
	"golang.org/x/crypto/ssh"
)

const maxRemoteReadBytes int64 = 4 << 20
const remoteReadConcurrency = 8

type FileAccessor struct {
	ctx     *Context
	ssh     *ssh.Client
	sftp    *sftp.Client
	ownsSSH bool
}

func (c *Context) NewFileAccessor() (*FileAccessor, error) {
	return NewFileAccessor(c)
}

func NewFileAccessor(ctx *Context) (*FileAccessor, error) {
	return NewFileAccessorWithSSH(ctx, nil, true)
}

func NewFileAccessorWithSSH(ctx *Context, sshClient *ssh.Client, ownsSSH bool) (*FileAccessor, error) {
	accessor := &FileAccessor{ctx: ctx, ownsSSH: ownsSSH}
	if ctx == nil || ctx.DockerHostType == ContextLocal {
		return accessor, nil
	}
	if sshClient == nil {
		var err error
		sshClient, err = ctx.DialSSH()
		if err != nil {
			return nil, err
		}
	}
	sftpClient, err := sftp.NewClient(sshClient)
	if err != nil {
		if ownsSSH {
			sshClient.Close()
		}
		return nil, err
	}
	accessor.ssh = sshClient
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
	if a.ssh != nil && a.ownsSSH {
		return a.ssh.Close()
	}
	return nil
}

func (a *FileAccessor) ReadFile(filename string) ([]byte, error) {
	return a.ReadFileContext(context.Background(), filename)
}

func (a *FileAccessor) ReadFileContext(ctx context.Context, filename string) ([]byte, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if a == nil || a.ctx == nil || a.ctx.DockerHostType == ContextLocal {
		return os.ReadFile(filename)
	}
	remoteFile, err := a.sftp.Open(filename)
	if err != nil {
		return nil, err
	}
	defer remoteFile.Close()
	return readAllLimited(remoteFile, maxRemoteReadBytes)
}

func (a *FileAccessor) ReadFiles(paths []string) (map[string][]byte, error) {
	return a.ReadFilesContext(context.Background(), paths)
}

func (a *FileAccessor) ReadFilesContext(ctx context.Context, paths []string) (map[string][]byte, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	results := make(map[string][]byte, len(paths))
	missing := make([]string, 0, len(paths))

	for _, path := range paths {
		if strings.TrimSpace(path) == "" {
			continue
		}
		missing = append(missing, path)
	}
	if len(missing) == 0 {
		return results, nil
	}

	if a == nil || a.ctx == nil || a.ctx.DockerHostType == ContextLocal {
		for _, path := range missing {
			data, err := os.ReadFile(path)
			if err != nil {
				return nil, err
			}
			results[path] = data
		}
		return results, nil
	}

	type readResult struct {
		path string
		data []byte
		err  error
	}

	workers := remoteReadConcurrency
	if len(missing) < workers {
		workers = len(missing)
	}
	if workers < 1 {
		workers = 1
	}

	jobs := make(chan string, len(missing))
	out := make(chan readResult, len(missing))
	var wg sync.WaitGroup

	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for {
				select {
				case <-ctx.Done():
					return
				case path, ok := <-jobs:
					if !ok {
						return
					}
					if err := ctx.Err(); err != nil {
						out <- readResult{path: path, err: err}
						return
					}
					remoteFile, err := a.sftp.Open(path)
					if err != nil {
						out <- readResult{path: path, err: err}
						cancel()
						return
					}
					data, err := readAllLimited(remoteFile, maxRemoteReadBytes)
					remoteFile.Close()
					out <- readResult{path: path, data: data, err: err}
					if err != nil {
						cancel()
						return
					}
				}
			}
		}()
	}

enqueue:
	for _, path := range missing {
		if err := ctx.Err(); err != nil {
			break
		}
		select {
		case <-ctx.Done():
			break enqueue
		case jobs <- path:
		}
	}
	close(jobs)

	go func() {
		wg.Wait()
		close(out)
	}()

	var firstErr error
	for result := range out {
		if result.err != nil && firstErr == nil {
			firstErr = result.err
			cancel()
			continue
		}
		if result.err != nil {
			continue
		}
		results[result.path] = result.data
	}
	if firstErr != nil {
		return nil, firstErr
	}
	return results, nil
}

func (a *FileAccessor) WriteFile(filename string, data []byte) error {
	if a == nil || a.ctx == nil || a.ctx.DockerHostType == ContextLocal {
		if err := os.MkdirAll(filepath.Dir(filename), 0o755); err != nil {
			return err
		}
		return os.WriteFile(filename, data, 0o644)
	}
	if err := mkdirAllRemote(a.sftp, filepath.Dir(filename)); err != nil {
		return err
	}
	remoteFile, err := a.sftp.Create(filename)
	if err != nil {
		return err
	}
	defer remoteFile.Close()
	_, err = remoteFile.Write(data)
	return err
}

func (a *FileAccessor) MkdirAll(path string) error {
	if strings.TrimSpace(path) == "" {
		return nil
	}
	if a == nil || a.ctx == nil || a.ctx.DockerHostType == ContextLocal {
		return os.MkdirAll(path, 0o755)
	}
	return mkdirAllRemote(a.sftp, path)
}

func (a *FileAccessor) RemoveFile(filename string) error {
	if a == nil || a.ctx == nil || a.ctx.DockerHostType == ContextLocal {
		if err := os.Remove(filename); err != nil && !os.IsNotExist(err) {
			return err
		}
		return nil
	}
	if err := a.sftp.Remove(filename); err != nil && !isSFTPNotExist(err) {
		return err
	}
	return nil
}

func (a *FileAccessor) RemoveAll(path string) error {
	if strings.TrimSpace(path) == "" {
		return nil
	}
	if a == nil || a.ctx == nil || a.ctx.DockerHostType == ContextLocal {
		if err := os.RemoveAll(path); err != nil && !os.IsNotExist(err) {
			return err
		}
		return nil
	}

	entries := []string{}
	walker := a.sftp.Walk(path)
	for walker.Step() {
		if err := walker.Err(); err != nil {
			if isSFTPNotExist(err) {
				return nil
			}
			return err
		}
		entries = append(entries, walker.Path())
	}
	if len(entries) == 0 {
		return nil
	}

	for i := len(entries) - 1; i >= 0; i-- {
		entry := entries[i]
		info, err := a.sftp.Stat(entry)
		if err != nil {
			if isSFTPNotExist(err) {
				continue
			}
			return err
		}
		if info.IsDir() {
			if err := a.sftp.RemoveDirectory(entry); err != nil && !isSFTPNotExist(err) {
				return err
			}
			continue
		}
		if err := a.sftp.Remove(entry); err != nil && !isSFTPNotExist(err) {
			return err
		}
	}
	return nil
}

func (a *FileAccessor) ListFiles(root string) ([]string, error) {
	if a == nil || a.ctx == nil || a.ctx.DockerHostType == ContextLocal {
		return listLocalFiles(root)
	}
	walker := a.sftp.Walk(root)
	files := []string{}
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

func (a *FileAccessor) FileExists(path string) (bool, error) {
	if a == nil || a.ctx == nil {
		return false, nil
	}
	if strings.TrimSpace(path) == "" {
		return false, nil
	}
	if a.ctx.DockerHostType == ContextLocal {
		_, err := os.Stat(path)
		if os.IsNotExist(err) {
			return false, nil
		}
		return err == nil, err
	}
	_, err := a.sftp.Stat(path)
	if err != nil {
		return false, nil
	}
	return true, nil
}

func (a *FileAccessor) Stat(path string) (fs.FileInfo, error) {
	if a == nil || a.ctx == nil || a.ctx.DockerHostType == ContextLocal {
		return os.Stat(path)
	}
	return a.sftp.Stat(path)
}

func (a *FileAccessor) UploadFile(source, destination string) error {
	localFile, err := os.Open(source)
	if err != nil {
		return err
	}
	defer localFile.Close()

	if a == nil || a.ctx == nil || a.ctx.DockerHostType == ContextLocal {
		if err := os.MkdirAll(filepath.Dir(destination), 0o755); err != nil {
			return err
		}
		remoteFile, err := os.Create(destination)
		if err != nil {
			return err
		}
		defer remoteFile.Close()
		_, err = io.Copy(remoteFile, localFile)
		return err
	}

	if err := mkdirAllRemote(a.sftp, filepath.Dir(destination)); err != nil {
		return err
	}
	remoteFile, err := a.sftp.Create(destination)
	if err != nil {
		return err
	}
	defer remoteFile.Close()
	_, err = io.Copy(remoteFile, localFile)
	return err
}

func readAllLimited(r io.Reader, limit int64) ([]byte, error) {
	limited := io.LimitReader(r, limit+1)
	data, err := io.ReadAll(limited)
	if err != nil {
		return nil, err
	}
	if int64(len(data)) > limit {
		return nil, fmt.Errorf("remote file exceeds %d bytes", limit)
	}
	return data, nil
}
