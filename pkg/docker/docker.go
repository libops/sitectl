package docker

import (
	"context"
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

	dockercontainer "github.com/docker/docker/api/types/container"
	"github.com/docker/docker/api/types/filters"
	"github.com/docker/docker/client"
	"github.com/docker/docker/pkg/stdcopy"
	"github.com/libops/sitectl/pkg/config"
	"golang.org/x/crypto/ssh"
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
			sshConn.Close()
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

	// Copy output
	errCh := make(chan error, 1)
	go func() {
		if opts.Tty {
			// For TTY, copy directly
			_, err := io.Copy(opts.Stdout, resp.Reader)
			errCh <- err
		} else {
			// For non-TTY, demux stdout/stderr
			_, err := stdcopy.StdCopy(opts.Stdout, opts.Stderr, resp.Reader)
			errCh <- err
		}
	}()

	// Wait for completion
	if err := <-errCh; err != nil && err != io.EOF {
		return -1, fmt.Errorf("failed to copy output: %w", err)
	}

	// Get exit code
	inspectResp, err := cli.ContainerExecInspect(ctx, execID.ID)
	if err != nil {
		return -1, fmt.Errorf("failed to inspect exec: %w", err)
	}

	return inspectResp.ExitCode, nil
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
