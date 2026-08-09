package docker

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"strings"
)

// ExecCapture runs a command in a container and returns its stdout.
// stderr is used as the error detail when the command exits non-zero.
// workingDir may be empty to use the container's default.
func ExecCapture(ctx context.Context, cli *DockerClient, container, workingDir string, cmd []string) (string, error) {
	return execCapture(ctx, cli, container, workingDir, cmd, nil)
}

// ExecCaptureWithInput runs a command in a container with input attached and
// returns its stdout. The input is streamed directly to the command's stdin.
func ExecCaptureWithInput(ctx context.Context, cli *DockerClient, container, workingDir string, cmd []string, input io.Reader) (string, error) {
	return execCapture(ctx, cli, container, workingDir, cmd, input)
}

func execCapture(ctx context.Context, cli *DockerClient, container, workingDir string, cmd []string, input io.Reader) (string, error) {
	var stdout bytes.Buffer
	var stderr bytes.Buffer

	exitCode, err := cli.Exec(ctx, ExecOptions{
		Container:    container,
		Cmd:          cmd,
		WorkingDir:   workingDir,
		AttachStdin:  input != nil,
		AttachStdout: true,
		AttachStderr: true,
		Stdin:        input,
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

// IsExecutableNotFound reports whether a container exec failed because its
// requested executable is not installed. Callers can use this to select a
// compatible binary without hiding operational failures from an installed one.
func IsExecutableNotFound(err error) bool {
	if err == nil {
		return false
	}
	message := strings.ToLower(err.Error())
	return strings.Contains(message, "executable file not found") ||
		strings.Contains(message, "not found in $path")
}
