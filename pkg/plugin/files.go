package plugin

import (
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

type FileAccessor struct {
	ctx            *config.Context
	ssh            *ssh.Client
	sftp           *sftp.Client
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
	return NewFileAccessor(ctx)
}

func NewFileAccessor(ctx *config.Context) (*FileAccessor, error) {
	accessor := &FileAccessor{
		ctx:            ctx,
		readFileCache:  map[string][]byte{},
		readDirCache:   map[string][]os.FileInfo{},
		listFilesCache: map[string][]string{},
	}
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
		data, err = io.ReadAll(file)
	}
	if err != nil {
		return nil, err
	}
	a.storeFile(path, data)
	return append([]byte(nil), data...), nil
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
	a.mu.Lock()
	if entries, ok := a.readDirCache[root]; ok {
		a.mu.Unlock()
		return entries, nil
	}
	a.mu.Unlock()

	var entries []os.FileInfo
	if a == nil || a.ctx == nil || a.ctx.DockerHostType == config.ContextLocal {
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
