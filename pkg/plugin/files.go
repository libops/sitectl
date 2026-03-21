package plugin

import (
	"context"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"sync"

	"github.com/libops/sitectl/pkg/config"
	"github.com/pkg/sftp"
	"golang.org/x/crypto/ssh"
)

const maxRemoteReadBytes int64 = 4 << 20
const remoteReadConcurrency = 8

type FileAccessor struct {
	ctx            *config.Context
	ssh            *ssh.Client
	sftp           *sftp.Client
	ownsSSH        bool
	mu             sync.Mutex
	readFileCache  map[string][]byte
	readDirCache   map[string][]os.FileInfo
	listFilesCache map[string][]string
}

func (s *SDK) GetFileAccessor() (*FileAccessor, error) {
	ctx, err := s.GetContext()
	if err != nil {
		return nil, err
	}
	if ctx == nil || ctx.DockerHostType == config.ContextLocal {
		return NewFileAccessor(ctx)
	}
	sshClient, err := s.getSSHClient()
	if err != nil {
		return nil, err
	}
	return NewFileAccessorWithSSH(ctx, sshClient, false)
}

func NewFileAccessor(ctx *config.Context) (*FileAccessor, error) {
	return NewFileAccessorWithSSH(ctx, nil, true)
}

func NewFileAccessorWithSSH(ctx *config.Context, client *ssh.Client, ownsSSH bool) (*FileAccessor, error) {
	accessor := &FileAccessor{
		ctx:            ctx,
		ownsSSH:        ownsSSH,
		readFileCache:  map[string][]byte{},
		readDirCache:   map[string][]os.FileInfo{},
		listFilesCache: map[string][]string{},
	}
	if ctx == nil || ctx.DockerHostType == config.ContextLocal {
		return accessor, nil
	}
	if client == nil {
		var err error
		client, err = ctx.DialSSH()
		if err != nil {
			return nil, err
		}
	}
	sftpClient, err := sftp.NewClient(client)
	if err != nil {
		if ownsSSH {
			client.Close()
		}
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
	if a.ssh != nil && a.ownsSSH {
		return a.ssh.Close()
	}
	return nil
}

func (a *FileAccessor) ReadFile(path string) ([]byte, error) {
	return a.ReadFileContext(context.Background(), path)
}

func (a *FileAccessor) ReadFileContext(ctx context.Context, path string) ([]byte, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if data, ok := a.cachedFile(path); ok {
		return data, nil
	}

	var data []byte
	var err error
	if a == nil || a.ctx == nil || a.ctx.DockerHostType == config.ContextLocal {
		data, err = os.ReadFile(path)
	} else {
		file, openErr := a.sftp.Open(path)
		if openErr != nil {
			return nil, openErr
		}
		defer file.Close()
		data, err = readAllLimited(file, maxRemoteReadBytes)
	}
	if err != nil {
		return nil, err
	}
	a.storeFile(path, data)
	return append([]byte(nil), data...), nil
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
		if path == "" {
			continue
		}
		if data, ok := a.cachedFile(path); ok {
			results[path] = data
			continue
		}
		missing = append(missing, path)
	}

	if len(missing) == 0 {
		return results, nil
	}

	if a == nil || a.ctx == nil || a.ctx.DockerHostType == config.ContextLocal {
		for _, path := range missing {
			data, err := os.ReadFile(path)
			if err != nil {
				return nil, err
			}
			a.storeFile(path, data)
			results[path] = append([]byte(nil), data...)
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
					file, err := a.sftp.Open(path)
					if err != nil {
						out <- readResult{path: path, err: err}
						cancel()
						return
					}
					data, err := readAllLimited(file, maxRemoteReadBytes)
					file.Close()
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
		a.storeFile(result.path, result.data)
		results[result.path] = append([]byte(nil), result.data...)
	}
	if firstErr != nil {
		return nil, firstErr
	}
	return results, nil
}

func (a *FileAccessor) ListFiles(root string) ([]string, error) {
	if files, ok := a.cachedList(root); ok {
		return files, nil
	}

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
		if err == nil {
			a.storeList(root, files)
		}
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
	a.storeList(root, files)
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

func (a *FileAccessor) MatchFilesInDir(root, pattern string) ([]string, error) {
	matches := []string{}

	entries, err := a.readDir(root)
	if err != nil {
		return nil, err
	}
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		ok, err := filepath.Match(pattern, entry.Name())
		if err != nil {
			return nil, err
		}
		if ok {
			matches = append(matches, filepath.Join(root, entry.Name()))
		}
	}
	sort.Strings(matches)
	return matches, nil
}

func (a *FileAccessor) readDir(root string) ([]os.FileInfo, error) {
	if a == nil {
		return nil, os.ErrInvalid
	}
	a.mu.Lock()
	if entries, ok := a.readDirCache[root]; ok {
		a.mu.Unlock()
		return entries, nil
	}
	a.mu.Unlock()

	var entries []os.FileInfo
	if a.ctx == nil || a.ctx.DockerHostType == config.ContextLocal {
		dirEntries, err := os.ReadDir(root)
		if err != nil {
			return nil, err
		}
		fileInfos := make([]os.FileInfo, 0, len(dirEntries))
		for _, entry := range dirEntries {
			info, infoErr := entry.Info()
			if infoErr != nil {
				return nil, infoErr
			}
			fileInfos = append(fileInfos, info)
		}
		entries = fileInfos
	} else {
		var err error
		entries, err = a.sftp.ReadDir(root)
		if err != nil {
			return nil, err
		}
	}

	a.mu.Lock()
	a.readDirCache[root] = entries
	a.mu.Unlock()
	return entries, nil
}

func (a *FileAccessor) cachedFile(path string) ([]byte, bool) {
	if a == nil {
		return nil, false
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	data, ok := a.readFileCache[path]
	if !ok {
		return nil, false
	}
	return append([]byte(nil), data...), true
}

func (a *FileAccessor) storeFile(path string, data []byte) {
	if a == nil {
		return
	}
	a.mu.Lock()
	a.readFileCache[path] = append([]byte(nil), data...)
	a.mu.Unlock()
}

func (a *FileAccessor) cachedList(root string) ([]string, bool) {
	if a == nil {
		return nil, false
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	files, ok := a.listFilesCache[root]
	if !ok {
		return nil, false
	}
	out := make([]string, len(files))
	copy(out, files)
	return out, true
}

func (a *FileAccessor) storeList(root string, files []string) {
	if a == nil {
		return
	}
	out := make([]string, len(files))
	copy(out, files)
	a.mu.Lock()
	a.listFilesCache[root] = out
	a.mu.Unlock()
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
