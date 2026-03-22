package config

import (
	"fmt"
	"io/fs"
	"path/filepath"
	"strings"

	"github.com/pkg/sftp"
)

// ReadFile reads a file from the context, supporting local and remote paths.
func (c *Context) ReadFile(filename string) ([]byte, error) {
	accessor, err := c.NewFileAccessor()
	if err != nil {
		return nil, fmt.Errorf("create file accessor: %w", err)
	}
	defer accessor.Close()
	data, err := accessor.ReadFile(filename)
	if err != nil {
		return nil, fmt.Errorf("read file %q: %w", filename, err)
	}
	return data, nil
}

// WriteFile writes a file to the context, creating parent directories as needed.
func (c *Context) WriteFile(filename string, data []byte) error {
	accessor, err := c.NewFileAccessor()
	if err != nil {
		return fmt.Errorf("create file accessor: %w", err)
	}
	defer accessor.Close()
	if err := accessor.WriteFile(filename, data); err != nil {
		return fmt.Errorf("write file %q: %w", filename, err)
	}
	return nil
}

// RemoveFile removes a file from the context.
func (c *Context) RemoveFile(filename string) error {
	accessor, err := c.NewFileAccessor()
	if err != nil {
		return fmt.Errorf("create file accessor: %w", err)
	}
	defer accessor.Close()
	if err := accessor.RemoveFile(filename); err != nil {
		return fmt.Errorf("remove file %q: %w", filename, err)
	}
	return nil
}

// ListFiles lists files under a directory relative to the directory root.
func (c *Context) ListFiles(root string) ([]string, error) {
	accessor, err := c.NewFileAccessor()
	if err != nil {
		return nil, fmt.Errorf("create file accessor: %w", err)
	}
	defer accessor.Close()
	files, err := accessor.ListFiles(root)
	if err != nil {
		return nil, fmt.Errorf("list files under %q: %w", root, err)
	}
	return files, nil
}

func listLocalFiles(root string) ([]string, error) {
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
	if err != nil {
		return nil, err
	}

	return files, nil
}

func mkdirAllRemote(client *sftp.Client, dir string) error {
	if dir == "." || dir == "" || dir == "/" {
		return nil
	}

	parts := strings.Split(filepath.Clean(dir), string(filepath.Separator))
	current := ""
	if strings.HasPrefix(dir, string(filepath.Separator)) {
		current = string(filepath.Separator)
	}

	for _, part := range parts {
		if part == "" || part == "." {
			continue
		}
		current = filepath.Join(current, part)
		info, err := client.Stat(current)
		if err == nil {
			if info.IsDir() {
				continue
			}
			return fmt.Errorf("remote path %q exists and is not a directory", current)
		}
		if err := client.Mkdir(current); err != nil && !isSFTPExist(err) {
			info, statErr := client.Stat(current)
			if statErr == nil && info.IsDir() {
				continue
			}
			return fmt.Errorf("create remote directory %q: %w", current, err)
		}
	}

	return nil
}

func isSFTPNotExist(err error) bool {
	return strings.Contains(strings.ToLower(err.Error()), "no such file")
}

func isSFTPExist(err error) bool {
	return strings.Contains(strings.ToLower(err.Error()), "file exists")
}
