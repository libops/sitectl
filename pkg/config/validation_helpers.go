package config

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/kballard/go-shellquote"
	"github.com/pkg/sftp"
)

func IsDockerSocketAlive(socket string) bool {
	return isDockerSocketAlive(socket)
}

func (c *Context) FileExists(path string) (bool, error) {
	if c == nil {
		return false, fmt.Errorf("context is nil")
	}
	if strings.TrimSpace(path) == "" {
		return false, nil
	}

	if c.DockerHostType == ContextLocal {
		_, err := os.Stat(path)
		if os.IsNotExist(err) {
			return false, nil
		}
		return err == nil, err
	}

	client, err := c.DialSSH()
	if err != nil {
		return false, err
	}
	defer client.Close()

	sftpClient, err := sftp.NewClient(client)
	if err != nil {
		return false, err
	}
	defer sftpClient.Close()

	_, err = sftpClient.Stat(path)
	if err != nil {
		return false, nil
	}
	return true, nil
}

func (c *Context) ResolveProjectPath(path string) string {
	if strings.TrimSpace(path) == "" {
		return ""
	}
	if filepath.IsAbs(path) {
		return filepath.Clean(path)
	}
	return filepath.Join(c.ProjectDir, path)
}

func (c *Context) HasComposeProject() (bool, error) {
	if c == nil {
		return false, fmt.Errorf("context is nil")
	}
	for _, candidate := range composeProjectCandidates {
		exists, err := c.FileExists(filepath.Join(c.ProjectDir, candidate))
		if err != nil {
			return false, err
		}
		if exists {
			return true, nil
		}
	}
	return false, nil
}

func (c *Context) ValidateComposeAccess() error {
	if c == nil {
		return fmt.Errorf("context is nil")
	}
	cmdArgs := []string{"docker", "compose"}
	for _, file := range c.ComposeFile {
		cmdArgs = append(cmdArgs, "-f", file)
	}
	for _, env := range c.EnvFile {
		cmdArgs = append(cmdArgs, "--env-file", env)
	}
	cmdArgs = append(cmdArgs, "ps")
	shellCmd := shellquote.Join(cmdArgs...) + " >/dev/null 2>&1"
	_, err := c.RunQuietCommand(exec.Command("sh", "-lc", shellCmd))
	return err
}
