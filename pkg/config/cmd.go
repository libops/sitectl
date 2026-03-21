package config

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"strings"
	"sync"

	"github.com/kballard/go-shellquote"
	"golang.org/x/crypto/ssh"
	"golang.org/x/term"
)

func (c *Context) RunCommand(cmd *exec.Cmd) (string, error) {
	return c.runCommandContext(context.Background(), cmd, true)
}

func (c *Context) RunQuietCommand(cmd *exec.Cmd) (string, error) {
	return c.runCommandContext(context.Background(), cmd, false)
}

func (c *Context) RunCommandContext(ctx context.Context, cmd *exec.Cmd) (string, error) {
	return c.runCommandContext(ctx, cmd, true)
}

func (c *Context) RunQuietCommandContext(ctx context.Context, cmd *exec.Cmd) (string, error) {
	return c.runCommandContext(ctx, cmd, false)
}

func (c *Context) runCommandContext(ctx context.Context, cmd *exec.Cmd, printOutput bool) (string, error) {
	runCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	var output strings.Builder
	if c.DockerHostType == ContextLocal {
		cmd = exec.CommandContext(runCtx, cmd.Path, cmd.Args[1:]...)
		cmd.Env = os.Environ()
		if printOutput {
			cmd.Stdin = os.Stdin
		}
		cmd.Dir = c.ProjectDir
		stdoutPipe, err := cmd.StdoutPipe()
		if err != nil {
			return "", fmt.Errorf("error creating stdout pipe for command %s: %v", cmd.String(), err)
		}
		if printOutput {
			cmd.Stderr = os.Stderr
		} else {
			cmd.Stderr = io.Discard
		}
		if err := cmd.Start(); err != nil {
			return "", fmt.Errorf("error starting command %s: %v", cmd.String(), err)
		}
		scanner := bufio.NewScanner(stdoutPipe)
		for scanner.Scan() {
			line := scanner.Text()
			if printOutput {
				fmt.Println(line)
			}
			output.WriteString(line)
			output.WriteString("\n")
		}
		if err := scanner.Err(); err != nil {
			slog.Error("Error reading stdout", "err", err)
		}
		if err := cmd.Wait(); err != nil {
			return "", fmt.Errorf("error waiting for command %s: %v", cmd.String(), err)
		}
		return strings.TrimRight(output.String(), "\n"), nil
	}

	sshClient, err := c.DialSSH()
	if err != nil {
		return "", fmt.Errorf("error establishing SSH connection: %v", err)
	}

	remoteCmd := fmt.Sprintf("cd %s && ", shellquote.Join(c.ProjectDir))
	remoteCmd += shellquote.Join(cmd.Args...)

	slog.Info("Running remote command", "host", c.SSHHostname, "cmd", remoteCmd)
	session, err := sshClient.NewSession()
	if err != nil {
		_ = sshClient.Close()
		return "", fmt.Errorf("error creating SSH session: %v", err)
	}

	// closeOnce ensures session and client are closed exactly once,
	// whether by the watchdog goroutine (context cancellation) or by deferred cleanup.
	var closeOnce sync.Once
	closeResources := func() {
		_ = session.Close()
		_ = sshClient.Close()
	}
	defer closeOnce.Do(closeResources)
	go func() {
		<-runCtx.Done()
		closeOnce.Do(closeResources)
	}()

	if printOutput {
		modes := ssh.TerminalModes{
			ssh.ECHO:          0,
			ssh.TTY_OP_ISPEED: 14400,
			ssh.TTY_OP_OSPEED: 14400,
		}
		width, height, err := term.GetSize(int(os.Stdin.Fd()))
		if err != nil {
			width = 80
			height = 40
		}
		if err := session.RequestPty("xterm", width, height, modes); err != nil {
			return "", fmt.Errorf("error requesting pseudo terminal: %w", err)
		}

		if term.IsTerminal(int(os.Stdin.Fd())) {
			oldState, err := term.MakeRaw(int(os.Stdin.Fd()))
			if err != nil {
				return "", fmt.Errorf("failed to set terminal to raw mode: %v", err)
			}
			defer func() {
				if err := term.Restore(int(os.Stdin.Fd()), oldState); err != nil {
					slog.Error("Unable to return terminal to original state.", "err", err)
				}
			}()
		}

		session.Stdin = os.Stdin
	}
	stdoutPipe, err := session.StdoutPipe()
	if err != nil {
		return "", fmt.Errorf("error obtaining stdout pipe: %v", err)
	}
	stderrPipe, err := session.StderrPipe()
	if err != nil {
		return "", fmt.Errorf("error obtaining stderr pipe: %v", err)
	}
	combined := io.MultiReader(stdoutPipe, stderrPipe)

	// call ssh foo@host.tld "remoteCmd"
	if err := session.Start(remoteCmd); err != nil {
		return "", fmt.Errorf("error starting remote command %q: %v", remoteCmd, err)
	}

	// save the output from the command so we can return it
	buf := make([]byte, 1024)
	for {
		n, err := combined.Read(buf)
		if n > 0 {
			chunk := string(buf[:n])
			if printOutput {
				fmt.Print(chunk)
			}
			output.WriteString(chunk)
		}
		if err != nil {
			if err == io.EOF {
				break
			}
			slog.Error("Error reading remote output", "err", err)
			break
		}
	}

	if err = session.Wait(); err != nil {
		// do not mark error on sigint
		if exitErr, ok := err.(*ssh.ExitError); ok && exitErr.ExitStatus() == 130 {
			return output.String(), nil
		}
		return "", fmt.Errorf("error waiting for remote command %q: %v", remoteCmd, err)
	}

	return output.String(), nil
}
