package docker

import (
	"bytes"
	"context"
	"fmt"
	"strings"
)

// ExecCapture runs a command in a container and returns its stdout.
// stderr is used as the error detail when the command exits non-zero.
// workingDir may be empty to use the container's default.
func ExecCapture(ctx context.Context, cli *DockerClient, container, workingDir string, cmd []string) (string, error) {
	var stdout bytes.Buffer
	var stderr bytes.Buffer

	exitCode, err := cli.Exec(ctx, ExecOptions{
		Container:    container,
		Cmd:          cmd,
		WorkingDir:   workingDir,
		AttachStdout: true,
		AttachStderr: true,
		Stdout:       &stdout,
		Stderr:       &stderr,
	})
	if err != nil {
		return "", err
	}
	if exitCode != 0 {
		detail := strings.TrimSpace(stderr.String())
		if detail == "" {
			detail = strings.TrimSpace(stdout.String())
		}
		if detail != "" {
			return "", fmt.Errorf("command failed with exit code %d: %s", exitCode, detail)
		}
		return "", fmt.Errorf("command failed with exit code %d", exitCode)
	}
	return strings.TrimSpace(stdout.String()), nil
}
