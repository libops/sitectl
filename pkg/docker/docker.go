package docker

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"syscall"

	dockercontainer "github.com/docker/docker/api/types/container"
	"github.com/docker/docker/api/types/filters"
	"github.com/docker/docker/client"
	"github.com/docker/docker/pkg/stdcopy"
	"github.com/libops/sitectl/pkg/config"
	"golang.org/x/crypto/ssh"
	"golang.org/x/term"
)

// DockerAPI abstracts the Docker client functionality needed by our package.
type DockerAPI interface {
	ContainerInspect(ctx context.Context, container string) (dockercontainer.InspectResponse, error)
	ContainerList(ctx context.Context, options dockercontainer.ListOptions) ([]dockercontainer.Summary, error)
}

type DockerClient struct {
	CLI        DockerAPI
	SshCli     *ssh.Client
	httpClient *http.Client
	ownsSSH    bool
}

func (d *DockerClient) Close() error {
	var firstErr error
	if d.SshCli != nil {
		if d.ownsSSH {
			if err := d.SshCli.Close(); err != nil && firstErr == nil {
				firstErr = err
			}
		}
	}
	if d.httpClient != nil {
		d.httpClient.CloseIdleConnections()
	}
	return firstErr
}

func GetDockerCli(activeCtx *config.Context) (*DockerClient, error) {
	if activeCtx.DockerHostType == config.ContextLocal {
		cli, err := client.NewClientWithOpts(
			client.WithHost("unix://"+activeCtx.DockerSocket),
			client.WithAPIVersionNegotiation(),
		)
		if err != nil {
			return nil, fmt.Errorf("error creating local Docker client: %v", err)
		}
		return &DockerClient{CLI: cli}, nil
	}
	sshConn, err := activeCtx.DialSSH()
	if err != nil {
		return nil, fmt.Errorf("error establishing SSH connection: %v", err)
	}
	return GetDockerCliWithSSH(activeCtx, sshConn, true)
}

func GetDockerCliWithSSH(activeCtx *config.Context, sshConn *ssh.Client, ownsSSH bool) (*DockerClient, error) {
	if activeCtx.DockerHostType == config.ContextLocal {
		cli, err := client.NewClientWithOpts(
			client.WithHost("unix://"+activeCtx.DockerSocket),
			client.WithAPIVersionNegotiation(),
		)
		if err != nil {
			return nil, fmt.Errorf("error creating local Docker client: %v", err)
		}
		return &DockerClient{CLI: cli}, nil
	}
	if sshConn == nil {
		return nil, fmt.Errorf("ssh client is required for remote docker context")
	}
	transport := &http.Transport{
		DialContext: func(ctx context.Context, network, addr string) (net.Conn, error) {
			return sshConn.Dial("unix", activeCtx.DockerSocket)
		},
	}
	httpClient := &http.Client{
		Transport: transport,
	}
	cli, err := client.NewClientWithOpts(
		client.WithHost("http://docker"),
		client.WithHTTPClient(httpClient),
		client.WithAPIVersionNegotiation(),
	)
	if err != nil {
		if ownsSSH {
			_ = sshConn.Close()
		}
		return nil, fmt.Errorf("error creating Docker client over SSH: %v", err)
	}
	return &DockerClient{
		CLI:        cli,
		SshCli:     sshConn,
		httpClient: httpClient,
		ownsSSH:    ownsSSH,
	}, nil
}

func GetSecret(ctx context.Context, cli DockerAPI, c *config.Context, containerName, secretName string) (string, error) {
	containerJSON, err := cli.ContainerInspect(ctx, containerName)
	if err != nil {
		return "", err
	}
	expectedTarget := filepath.Join("/run/secrets", secretName)
	for _, mount := range containerJSON.Mounts {
		if mount.Destination == expectedTarget {
			secretFilePath := filepath.Join(c.ProjectDir, "secrets", secretName)
			secret, err := c.ReadSmallFile(secretFilePath)
			if err != nil {
				return "", fmt.Errorf("read secret %q: %w", secretName, err)
			}
			return secret, nil
		}
	}
	return GetConfigEnv(ctx, cli, containerName, secretName)
}

// GetFirstSecretOrEnv returns the first mounted secret or environment variable
// available from names on containerName.
func GetFirstSecretOrEnv(ctx context.Context, cli DockerAPI, c *config.Context, containerName string, names ...string) (string, error) {
	var misses []string
	for _, name := range names {
		name = strings.TrimSpace(name)
		if name == "" {
			continue
		}
		secret, err := GetSecret(ctx, cli, c, containerName, name)
		if err == nil && strings.TrimSpace(secret) != "" {
			return secret, nil
		}
		if err != nil {
			misses = append(misses, fmt.Sprintf("%s (%v)", name, err))
		} else {
			misses = append(misses, name)
		}
	}
	if len(misses) == 0 {
		return "", fmt.Errorf("no secret or environment variable names provided")
	}
	return "", fmt.Errorf("none of these secrets or environment variables are available in container %s: %s", containerName, strings.Join(misses, "; "))
}

func GetConfigEnv(ctx context.Context, cli DockerAPI, containerName, envName string) (string, error) {
	containerJSON, err := cli.ContainerInspect(ctx, containerName)
	if err != nil {
		return "", fmt.Errorf("error inspecting container %s: %v", containerName, err)
	}
	for _, env := range containerJSON.Config.Env {
		parts := strings.SplitN(env, "=", 2)
		if len(parts) == 2 && parts[0] == envName {
			return parts[1], nil
		}
	}
	return "", fmt.Errorf("environment variable %q not found in container %s", envName, containerName)
}

func (d *DockerClient) GetServiceIp(ctx context.Context, c *config.Context, containerName string) (string, error) {
	containerJSON, err := d.CLI.ContainerInspect(ctx, containerName)
	if err != nil {
		return "", fmt.Errorf("error inspecting container %q: %v", containerName, err)
	}
	networkName := c.EffectiveComposeNetwork()
	if network, ok := containerJSON.NetworkSettings.Networks[networkName]; ok {
		return network.IPAddress, nil
	}
	if len(containerJSON.NetworkSettings.Networks) == 1 {
		for _, network := range containerJSON.NetworkSettings.Networks {
			return network.IPAddress, nil
		}
	}
	available := make([]string, 0, len(containerJSON.NetworkSettings.Networks))
	for name := range containerJSON.NetworkSettings.Networks {
		available = append(available, name)
	}
	sort.Strings(available)
	return "", fmt.Errorf("network %q not found in container %q (available: %s)", networkName, containerName, strings.Join(available, ", "))
}

func (d *DockerClient) GetContainerName(c *config.Context, service string) (string, error) {
	return d.GetContainerNameContext(context.Background(), c, service)
}

func (d *DockerClient) GetContainerNameContext(ctx context.Context, c *config.Context, service string) (string, error) {
	// Define the filters based on the Docker Compose labels.
	filterArgs := filters.NewArgs()
	filterArgs.Add("label", "com.docker.compose.project="+c.EffectiveComposeProjectName())
	filterArgs.Add("label", "com.docker.compose.service="+service)

	slog.Debug("Querying docker", "filters", filterArgs)
	containers, err := d.CLI.ContainerList(ctx, dockercontainer.ListOptions{Filters: filterArgs})
	if err != nil {
		return "", err
	}

	// Print the container names.
	for _, container := range containers {
		for _, name := range container.Names {
			slog.Debug("Got container", "name", name)
			return name, nil
		}
	}

	return "", nil
}

// ExecOptions holds options for executing a command in a container
type ExecOptions struct {
	// Container is the container ID or name
	Container string

	// Cmd is the command to execute
	Cmd []string

	// Env is additional environment variables
	Env []string

	// WorkingDir is the working directory
	WorkingDir string

	// User to run as
	User string

	// AttachStdin attaches stdin
	AttachStdin bool

	// AttachStdout attaches stdout
	AttachStdout bool

	// AttachStderr attaches stderr
	AttachStderr bool

	// Tty allocates a pseudo-TTY
	Tty bool

	// Stdin is the input stream
	Stdin io.Reader

	// Stdout is the output stream
	Stdout io.Writer

	// Stderr is the error stream
	Stderr io.Writer
}

// Exec executes a command in a container using the DockerClient
func (d *DockerClient) Exec(ctx context.Context, opts ExecOptions) (int, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	// Set defaults
	if opts.Stdout == nil {
		opts.Stdout = os.Stdout
	}
	if opts.Stderr == nil {
		opts.Stderr = os.Stderr
	}
	if opts.Stdin == nil {
		opts.Stdin = os.Stdin
	}
	var input *execInputController
	if opts.AttachStdin {
		var err error
		input, err = newExecInputController(opts.Stdin)
		if err != nil {
			return -1, err
		}
		defer input.Close()
	}

	// Get the underlying client (type assert to *client.Client)
	cli, ok := d.CLI.(*client.Client)
	if !ok {
		return -1, fmt.Errorf("CLI is not a *client.Client")
	}

	// Create exec instance
	execConfig := dockercontainer.ExecOptions{
		AttachStdin:  opts.AttachStdin,
		AttachStdout: opts.AttachStdout,
		AttachStderr: opts.AttachStderr,
		Tty:          opts.Tty,
		Cmd:          opts.Cmd,
		Env:          opts.Env,
		WorkingDir:   opts.WorkingDir,
		User:         opts.User,
	}

	execID, err := cli.ContainerExecCreate(ctx, opts.Container, execConfig)
	if err != nil {
		return -1, fmt.Errorf("failed to create exec: %w", err)
	}

	// Attach to exec
	resp, err := cli.ContainerExecAttach(ctx, execID.ID, dockercontainer.ExecStartOptions{
		Tty: opts.Tty,
	})
	if err != nil {
		return -1, fmt.Errorf("failed to attach to exec: %w", err)
	}
	defer resp.Close()

	restoreTerminal, err := prepareExecTerminal(ctx, cli, execID.ID, opts)
	if err != nil {
		return -1, err
	}
	defer restoreTerminal()

	if err := copyDockerExecStreams(ctx, resp.Conn, resp.Reader, resp.Close, opts, input); err != nil {
		return -1, err
	}

	if ctx.Err() != nil {
		return -1, ctx.Err()
	}

	// Get exit code
	inspectResp, err := cli.ContainerExecInspect(ctx, execID.ID)
	if err != nil {
		return -1, fmt.Errorf("failed to inspect exec: %w", err)
	}

	return inspectResp.ExitCode, nil
}

type execTTYResizer interface {
	ContainerExecResize(context.Context, string, dockercontainer.ResizeOptions) error
}

func prepareExecTerminal(ctx context.Context, resizer execTTYResizer, execID string, opts ExecOptions) (func(), error) {
	file, ok := opts.Stdin.(*os.File)
	if !opts.Tty || !opts.AttachStdin || !ok || !term.IsTerminal(int(file.Fd())) {
		return func() {}, nil
	}

	var restoreOnce sync.Once
	restore := func() {}
	oldState, err := term.MakeRaw(int(file.Fd()))
	if err != nil {
		slog.Warn("put exec terminal in raw mode", "err", err)
	} else {
		restore = func() {
			restoreOnce.Do(func() {
				if err := term.Restore(int(file.Fd()), oldState); err != nil {
					slog.Error("restore exec terminal", "err", err)
				}
			})
		}
	}

	resize := func() error {
		width, height, err := term.GetSize(int(file.Fd()))
		if err != nil {
			return err
		}
		return resizer.ContainerExecResize(ctx, execID, dockercontainer.ResizeOptions{Height: uint(height), Width: uint(width)})
	}
	if err := resize(); err != nil {
		slog.Warn("set initial exec terminal size", "err", err)
	}

	stopResizeWatcher := watchExecTerminalResizes(ctx, resize)

	return func() {
		stopResizeWatcher()
		restore()
	}, nil
}

func copyDockerExecStreams(ctx context.Context, conn net.Conn, output io.Reader, closeStream func(), opts ExecOptions, input *execInputController) error {
	inputDone := make(chan error, 1)
	if opts.AttachStdin {
		go func() {
			_, err := io.Copy(conn, input.Reader)
			if err == nil || isIgnorableExecStreamError(err) {
				if closer, ok := conn.(interface{ CloseWrite() error }); ok {
					_ = closer.CloseWrite()
				}
			}
			inputDone <- err
		}()
	} else {
		inputDone = nil
	}

	outputDone := make(chan error, 1)
	go func() {
		if opts.Tty {
			_, err := io.Copy(opts.Stdout, output)
			outputDone <- err
			return
		}
		_, err := stdcopy.StdCopy(opts.Stdout, opts.Stderr, output)
		outputDone <- err
	}()

	for {
		select {
		case err := <-outputDone:
			// Remote output EOF owns the attach lifecycle. Closing the Docker
			// stream and interrupting a pollable stdin prevents an interactive
			// exec from waiting forever for terminal EOF after the process exits.
			closeStream()
			if input != nil && inputDone != nil {
				input.Cancel()
				<-inputDone
			}
			if ctx.Err() != nil {
				return ctx.Err()
			}
			if err != nil && !isIgnorableExecStreamError(err) {
				return fmt.Errorf("failed to copy exec output: %w", err)
			}
			return nil
		case err := <-inputDone:
			inputDone = nil
			if err != nil && !isIgnorableExecStreamError(err) {
				closeStream()
				return fmt.Errorf("failed to copy exec input: %w", err)
			}
		case <-ctx.Done():
			closeStream()
			if input != nil && inputDone != nil {
				input.Cancel()
				<-inputDone
			}
			return ctx.Err()
		}
	}
}

var errExecInputCanceled = errors.New("exec input canceled")

type execInputController struct {
	Reader io.Reader
	cancel func()
	close  func()
}

func (c *execInputController) Cancel() {
	if c != nil && c.cancel != nil {
		c.cancel()
	}
}

func (c *execInputController) Close() {
	if c != nil && c.close != nil {
		c.close()
	}
}

func newExecInputController(input io.Reader) (*execInputController, error) {
	if file, ok := input.(*os.File); ok {
		return newFileExecInputController(file)
	}
	if closer, ok := input.(io.ReadCloser); ok {
		var closeOnce sync.Once
		closeInput := func() { closeOnce.Do(func() { _ = closer.Close() }) }
		return &execInputController{Reader: input, cancel: closeInput, close: closeInput}, nil
	}
	switch input.(type) {
	case *bytes.Buffer, *bytes.Reader, *strings.Reader:
		return &execInputController{Reader: input, cancel: func() {}, close: func() {}}, nil
	default:
		return nil, fmt.Errorf("attached exec stdin %T cannot be canceled safely", input)
	}
}

func isIgnorableExecStreamError(err error) bool {
	if err == nil || err == io.EOF {
		return true
	}
	if errors.Is(err, syscall.EPIPE) {
		return true
	}
	message := strings.ToLower(err.Error())
	return strings.Contains(message, "broken pipe") ||
		strings.Contains(message, "closed network connection") ||
		strings.Contains(message, "use of closed network connection")
}

// ExecSimple executes a simple command and returns the exit code
func (d *DockerClient) ExecSimple(ctx context.Context, containerID string, cmd []string) (int, error) {
	return d.Exec(ctx, ExecOptions{
		Container:    containerID,
		Cmd:          cmd,
		AttachStdout: true,
		AttachStderr: true,
	})
}

// ExecInteractive executes an interactive command with TTY
func (d *DockerClient) ExecInteractive(ctx context.Context, containerID string, cmd []string) (int, error) {
	return d.Exec(ctx, ExecOptions{
		Container:    containerID,
		Cmd:          cmd,
		AttachStdin:  true,
		AttachStdout: true,
		AttachStderr: true,
		Tty:          true,
	})
}

// GetDatabaseUris constructs MySQL and SSH connection URIs for database tools like Sequel Ace
// Returns: mysqlURI, sshURI, error
func GetDatabaseUris(c *config.Context) (string, string, error) {
	ctx := context.Background()

	// Get Docker client
	dockerCli, err := GetDockerCli(c)
	if err != nil {
		return "", "", fmt.Errorf("failed to get docker client: %w", err)
	}
	defer dockerCli.Close()

	return getDatabaseURIsWithClient(ctx, dockerCli, c)
}

func getDatabaseURIsWithClient(ctx context.Context, dockerCli *DockerClient, c *config.Context) (string, string, error) {
	dbHost := "127.0.0.1"

	// Get the database container name
	containerName, err := dockerCli.GetContainerNameContext(ctx, c, c.DatabaseService)
	if err != nil {
		return "", "", fmt.Errorf("failed to get %s container: %w", c.DatabaseService, err)
	}
	if containerName == "" {
		return "", "", fmt.Errorf("%s container not found", c.DatabaseService)
	}

	if c.DockerHostType == config.ContextRemote {
		dbHost, err = dockerCli.GetServiceIp(ctx, c, containerName)
		if err != nil {
			return "", "", fmt.Errorf("failed to resolve %s service IP: %w", c.DatabaseService, err)
		}
		if dbHost == "" {
			return "", "", fmt.Errorf("resolved empty IP for %s service", c.DatabaseService)
		}
	}

	// Get database password from container environment
	password, err := GetSecret(ctx, dockerCli.CLI, c, containerName, c.DatabasePasswordSecret)
	if err != nil {
		return "", "", fmt.Errorf("failed to get database password from %s: %w", c.DatabasePasswordSecret, err)
	}

	mysqlURI := url.URL{
		Scheme: "mysql",
		User:   url.UserPassword(c.DatabaseUser, password),
		Host:   net.JoinHostPort(dbHost, strconv.Itoa(3306)),
		Path:   "/" + c.DatabaseName,
	}

	return mysqlURI.String(), c.GetSshUri(), nil
}
