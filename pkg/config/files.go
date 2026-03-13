package config

import (
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"github.com/pkg/sftp"
)

// ReadFile reads a file from the context, supporting local and remote paths.
func (c *Context) ReadFile(filename string) ([]byte, error) {
	if c.DockerHostType == ContextLocal {
		return os.ReadFile(filename)
	}

	client, err := c.DialSSH()
	if err != nil {
		return nil, fmt.Errorf("dial ssh: %w", err)
	}
	defer client.Close()

	sftpClient, err := sftp.NewClient(client)
	if err != nil {
		return nil, fmt.Errorf("create sftp client: %w", err)
	}
	defer sftpClient.Close()

	remoteFile, err := sftpClient.Open(filename)
	if err != nil {
		return nil, fmt.Errorf("open remote file %q: %w", filename, err)
	}
	defer remoteFile.Close()

	data, err := io.ReadAll(remoteFile)
	if err != nil {
		return nil, fmt.Errorf("read remote file %q: %w", filename, err)
	}

	return data, nil
}

// WriteFile writes a file to the context, creating parent directories as needed.
func (c *Context) WriteFile(filename string, data []byte) error {
	if c.DockerHostType == ContextLocal {
		if err := os.MkdirAll(filepath.Dir(filename), 0o755); err != nil {
			return fmt.Errorf("create parent directories for %q: %w", filename, err)
		}
		return os.WriteFile(filename, data, 0o644)
	}

	client, err := c.DialSSH()
	if err != nil {
		return fmt.Errorf("dial ssh: %w", err)
	}
	defer client.Close()

	sftpClient, err := sftp.NewClient(client)
	if err != nil {
		return fmt.Errorf("create sftp client: %w", err)
	}
	defer sftpClient.Close()

	if err := mkdirAllRemote(sftpClient, filepath.Dir(filename)); err != nil {
		return err
	}

	remoteFile, err := sftpClient.Create(filename)
	if err != nil {
		return fmt.Errorf("create remote file %q: %w", filename, err)
	}
	defer remoteFile.Close()

	if _, err := remoteFile.Write(data); err != nil {
		return fmt.Errorf("write remote file %q: %w", filename, err)
	}

	return nil
}

// RemoveFile removes a file from the context.
func (c *Context) RemoveFile(filename string) error {
	if c.DockerHostType == ContextLocal {
		if err := os.Remove(filename); err != nil && !os.IsNotExist(err) {
			return err
		}
		return nil
	}

	client, err := c.DialSSH()
	if err != nil {
		return fmt.Errorf("dial ssh: %w", err)
	}
	defer client.Close()

	sftpClient, err := sftp.NewClient(client)
	if err != nil {
		return fmt.Errorf("create sftp client: %w", err)
	}
	defer sftpClient.Close()

	if err := sftpClient.Remove(filename); err != nil && !isSFTPNotExist(err) {
		return fmt.Errorf("remove remote file %q: %w", filename, err)
	}

	return nil
}

// ListFiles lists files under a directory relative to the directory root.
func (c *Context) ListFiles(root string) ([]string, error) {
	if c.DockerHostType == ContextLocal {
		return listLocalFiles(root)
	}

	client, err := c.DialSSH()
	if err != nil {
		return nil, fmt.Errorf("dial ssh: %w", err)
	}
	defer client.Close()

	sftpClient, err := sftp.NewClient(client)
	if err != nil {
		return nil, fmt.Errorf("create sftp client: %w", err)
	}
	defer sftpClient.Close()

	walker := sftpClient.Walk(root)
	files := []string{}
	for walker.Step() {
		if err := walker.Err(); err != nil {
			return nil, fmt.Errorf("walk remote path %q: %w", walker.Path(), err)
		}
		if walker.Stat().IsDir() {
			continue
		}
		rel, err := filepath.Rel(root, walker.Path())
		if err != nil {
			return nil, fmt.Errorf("get relative path for %q: %w", walker.Path(), err)
		}
		files = append(files, filepath.ToSlash(rel))
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
		if err := client.Mkdir(current); err != nil && !isSFTPExist(err) {
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
