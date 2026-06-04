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
	Args:  cobra.ArbitraryArgs,
	Short: "Forward one or more local ports to a service",
	Long: `
Access remote context docker service ports.

For docker services running in remote contexts that do not have ports exposed on the host VM, accessing those services can be tricky.
The sitectl port-forward command can help in these situations.

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
		if runtime.GOOS != "linux" && c.DockerHostType == config.ContextLocal {
			return fmt.Errorf("port-forwarding on non-linux local contexts is not currently supported")
		}
		cli, err := docker.GetDockerCli(c)
		if err != nil {
			return err
		}
		defer cli.Close()

		listeners := make([]net.Listener, 0, len(args))
		ctx, stop := signal.NotifyContext(cmd.Context(), os.Interrupt, syscall.SIGHUP, syscall.SIGTERM)
		defer stop()
		var wg sync.WaitGroup

		for _, arg := range args {
			parts := strings.Split(arg, ":")
			if len(parts) != 3 {
				return fmt.Errorf("invalid port forwarding spec '%s': expected format LOCAL-PORT:SERVICE:REMOTE-PORT", arg)
			}
			localPortStr, service, remotePortStr := parts[0], parts[1], parts[2]

			localPort, err := strconv.Atoi(localPortStr)
			if err != nil {
				return fmt.Errorf("invalid local port '%s': must be an integer", localPortStr)
			}
			if localPort < 1 || localPort > 65535 {
				return fmt.Errorf("invalid local port '%s': must be between 1 and 65535", localPortStr)
			}
			remotePort, err := strconv.Atoi(remotePortStr)
			if err != nil {
				return fmt.Errorf("invalid remote port '%s': must be an integer", remotePortStr)
			}
			if remotePort < 1 || remotePort > 65535 {
				return fmt.Errorf("invalid remote port '%s': must be between 1 and 65535", remotePortStr)
			}

			addr := fmt.Sprintf("localhost:%d", localPort)
			listener, err := net.Listen("tcp", addr)
			if err != nil {
				return fmt.Errorf("local port %d appears to be in use: %v", localPort, err)
			}
			listeners = append(listeners, listener)

			containerName, err := cli.GetContainerNameContext(ctx, c, service)
			if err != nil {
				return err
			}
			serviceIp, err := cli.GetServiceIp(ctx, c, containerName)
			if err != nil {
				return err
			}

			remoteEndpoint := fmt.Sprintf("%s:%d", serviceIp, remotePort)
			wg.Add(1)
			go func(listener net.Listener, lp, remoteAddr string) {
				defer wg.Done()
				fmt.Fprintf(cmd.OutOrStdout(), "Forwarding localhost:%s -> %s via SSH\n", lp, remoteAddr)
				for {
					localConn, err := listener.Accept()
					if err != nil {
						if ctx.Err() != nil || isClosedNetworkError(err) {
							return
						}
						fmt.Fprintf(cmd.ErrOrStderr(), "error accepting connection on port %s: %v\n", lp, err)
						return
					}
					wg.Add(1)
					go func() {
						defer wg.Done()
						forward(ctx, cli.SshCli, localConn, remoteAddr, cmd.ErrOrStderr())
					}()
				}
			}(listener, localPortStr, remoteEndpoint)
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
