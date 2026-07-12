package cmd

import (
	"context"
	"fmt"
	"io"
	"net"
	"os"
	"os/signal"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"syscall"

	"github.com/libops/sitectl/pkg/config"
	"github.com/libops/sitectl/pkg/docker"
	"github.com/spf13/cobra"
	"golang.org/x/crypto/ssh"
)

var portForwardCmd = &cobra.Command{
	Use:   "port-forward [LOCAL-PORT:SERVICE:REMOTE-PORT...]",
	Args:  cobra.MinimumNArgs(1),
	Short: "Forward one or more local ports to a service",
	Long: `
Access a Docker Compose service without publishing its port on the host.

Every listener is bound to 127.0.0.1. Remote contexts forward over SSH,
local Linux contexts use the container network directly, and local Docker
Desktop contexts stream through BusyBox nc inside the selected service
container.

As an example, from a local machine, accessing your stage context's traefik dashboard and solr admin UI
could be done by running this command in the terminal:

sitectl port-forward \
  8983:solr:8983 \
  8080:traefik:8080 \
  8161:activemq:8161 \
  --context stage

Then, while leaving the terminal open, in your web browser you can visit

http://localhost:8983/solr to see the solr admin UI
http://localhost:8080/dashboard to see the traefik dashboard (assuming it's enabled in your config)
http://localhost:8161/admin/queues.jsp to see ActiveMQ queues

Be sure to run Ctrl+c in your terminal when you are done to close the connection.
`,
	RunE: func(cmd *cobra.Command, args []string) error {
		c, err := resolveCurrentContext(cmd)
		if err != nil {
			return err
		}
		cli, err := docker.GetDockerCli(c)
		if err != nil {
			return err
		}
		defer cli.Close()

		listeners := make([]net.Listener, 0, len(args))
		ctx, stop := signal.NotifyContext(cmd.Context(), os.Interrupt, syscall.SIGHUP, syscall.SIGTERM)
		var wg sync.WaitGroup
		defer func() {
			stop()
			for _, listener := range listeners {
				_ = listener.Close()
			}
			_ = cli.Close()
			wg.Wait()
		}()

		for _, arg := range args {
			spec, err := parsePortForwardSpec(arg)
			if err != nil {
				return err
			}

			addr := portForwardListenAddress(spec.localPort)
			listener, err := net.Listen("tcp", addr)
			if err != nil {
				return fmt.Errorf("local port %d appears to be in use: %v", spec.localPort, err)
			}
			listeners = append(listeners, listener)

			containerName, err := cli.GetContainerNameContext(ctx, c, spec.service)
			if err != nil {
				return err
			}
			if strings.TrimSpace(containerName) == "" {
				return fmt.Errorf("service %q does not have a running container", spec.service)
			}

			var target string
			var transport string
			var forwardConnection func(net.Conn)
			if useContainerExecPortForward(runtime.GOOS, c) {
				target = fmt.Sprintf("%s:%d", spec.service, spec.remotePort)
				transport = "Docker exec"
				forwardConnection = func(localConn net.Conn) {
					forwardContainerExec(ctx, cli, localConn, containerName, spec.remotePort, cmd.ErrOrStderr())
				}
			} else {
				serviceIP, err := cli.GetServiceIp(ctx, c, containerName)
				if err != nil {
					return err
				}
				if strings.TrimSpace(serviceIP) == "" {
					return fmt.Errorf("service %q does not have an address on the Compose network", spec.service)
				}
				target = net.JoinHostPort(serviceIP, strconv.Itoa(spec.remotePort))
				transport = "the local Docker network"
				if cli.SshCli != nil {
					transport = "SSH"
				}
				forwardConnection = func(localConn net.Conn) {
					forward(ctx, cli.SshCli, localConn, target, cmd.ErrOrStderr())
				}
			}

			wg.Add(1)
			go func(listener net.Listener, localPort int, remoteTarget, via string, forwardConn func(net.Conn)) {
				defer wg.Done()
				fmt.Fprintf(cmd.OutOrStdout(), "Forwarding 127.0.0.1:%d -> %s via %s\n", localPort, remoteTarget, via)
				for {
					localConn, err := listener.Accept()
					if err != nil {
						if ctx.Err() != nil || isClosedNetworkError(err) {
							return
						}
						fmt.Fprintf(cmd.ErrOrStderr(), "error accepting connection on port %d: %v\n", localPort, err)
						stop()
						return
					}
					wg.Add(1)
					go func() {
						defer wg.Done()
						forwardConn(localConn)
					}()
				}
			}(listener, spec.localPort, target, transport, forwardConnection)
		}

		<-ctx.Done()
		fmt.Fprintln(cmd.OutOrStdout(), "Shutting down port forwards...")
		for _, listener := range listeners {
			if err := listener.Close(); err != nil {
				fmt.Fprintf(cmd.ErrOrStderr(), "error closing listener: %v\n", err)
			}
		}
		if err := cli.Close(); err != nil {
			fmt.Fprintf(cmd.ErrOrStderr(), "error closing docker connection: %v\n", err)
		}
		wg.Wait()
		return nil
	},
}

type portForwardSpec struct {
	localPort  int
	service    string
	remotePort int
}

func parsePortForwardSpec(value string) (portForwardSpec, error) {
	parts := strings.Split(value, ":")
	if len(parts) != 3 {
		return portForwardSpec{}, fmt.Errorf("invalid port forwarding spec %q: expected format LOCAL-PORT:SERVICE:REMOTE-PORT", value)
	}
	localPort, err := strconv.Atoi(parts[0])
	if err != nil {
		return portForwardSpec{}, fmt.Errorf("invalid local port %q: must be an integer", parts[0])
	}
	if localPort < 1 || localPort > 65535 {
		return portForwardSpec{}, fmt.Errorf("invalid local port %q: must be between 1 and 65535", parts[0])
	}
	service := strings.TrimSpace(parts[1])
	if service == "" {
		return portForwardSpec{}, fmt.Errorf("invalid service: must not be empty")
	}
	remotePort, err := strconv.Atoi(parts[2])
	if err != nil {
		return portForwardSpec{}, fmt.Errorf("invalid remote port %q: must be an integer", parts[2])
	}
	if remotePort < 1 || remotePort > 65535 {
		return portForwardSpec{}, fmt.Errorf("invalid remote port %q: must be between 1 and 65535", parts[2])
	}
	return portForwardSpec{localPort: localPort, service: service, remotePort: remotePort}, nil
}

func portForwardListenAddress(localPort int) string {
	return net.JoinHostPort("127.0.0.1", strconv.Itoa(localPort))
}

func useContainerExecPortForward(goos string, c *config.Context) bool {
	return c != nil && c.DockerHostType == config.ContextLocal && goos != "linux"
}

type portForwardContainerExecutor interface {
	Exec(context.Context, docker.ExecOptions) (int, error)
}

func forwardContainerExec(ctx context.Context, executor portForwardContainerExecutor, localConn net.Conn, containerName string, remotePort int, errw io.Writer) {
	defer localConn.Close()
	stopClose := context.AfterFunc(ctx, func() {
		_ = localConn.Close()
	})
	defer stopClose()

	exitCode, err := executor.Exec(ctx, docker.ExecOptions{
		Container:    containerName,
		Cmd:          []string{"busybox", "nc", "127.0.0.1", strconv.Itoa(remotePort)},
		AttachStdin:  true,
		AttachStdout: true,
		AttachStderr: true,
		Stdin:        localConn,
		Stdout:       localConn,
		Stderr:       errw,
	})
	if ctx.Err() != nil {
		return
	}
	if err != nil {
		fmt.Fprintf(errw, "container port forward failed: %v (service image must provide BusyBox nc)\n", err)
		return
	}
	if exitCode != 0 {
		fmt.Fprintf(errw, "container port forward exited with status %d (service image must provide BusyBox nc)\n", exitCode)
	}
}

func forward(ctx context.Context, client *ssh.Client, localConn net.Conn, remoteAddr string, errw io.Writer) {
	defer localConn.Close()
	remoteConn, err := dialPortForwardRemote(ctx, client, remoteAddr)
	if err != nil {
		fmt.Fprintf(errw, "failed to dial remote address %s: %v\n", remoteAddr, err)
		return
	}
	defer remoteConn.Close()

	ctxDone := make(chan struct{})
	go func() {
		select {
		case <-ctx.Done():
			_ = localConn.Close()
			_ = remoteConn.Close()
		case <-ctxDone:
		}
	}()
	defer close(ctxDone)

	errCh := make(chan error, 2)
	go copyPortForward(errCh, remoteConn, localConn)
	go copyPortForward(errCh, localConn, remoteConn)

	for i := 0; i < 2; i++ {
		if err := <-errCh; err != nil && ctx.Err() == nil && !isClosedNetworkError(err) {
			fmt.Fprintf(errw, "error while copying port forward traffic: %v\n", err)
		}
		_ = localConn.Close()
		_ = remoteConn.Close()
	}
}

func dialPortForwardRemote(ctx context.Context, client *ssh.Client, remoteAddr string) (net.Conn, error) {
	if client != nil {
		return client.Dial("tcp", remoteAddr)
	}
	var dialer net.Dialer
	return dialer.DialContext(ctx, "tcp", remoteAddr)
}

func copyPortForward(errCh chan<- error, dst net.Conn, src net.Conn) {
	_, err := io.Copy(dst, src)
	errCh <- err
}

func isClosedNetworkError(err error) bool {
	if err == nil {
		return false
	}
	message := strings.ToLower(err.Error())
	return strings.Contains(message, "closed network connection") ||
		strings.Contains(message, "use of closed network connection") ||
		strings.Contains(message, "operation canceled")
}

func init() {
	portForwardCmd.GroupID = "troubleshoot"
	RootCmd.AddCommand(portForwardCmd)
}
