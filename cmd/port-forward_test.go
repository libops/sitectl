package cmd

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/libops/sitectl/pkg/config"
	"github.com/libops/sitectl/pkg/docker"
)

func TestParsePortForwardSpec(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name    string
		value   string
		want    portForwardSpec
		wantErr string
	}{
		{name: "valid", value: "8080:omeka-s:80", want: portForwardSpec{localPort: 8080, service: "omeka-s", remotePort: 80}},
		{name: "missing service", value: "8080::80", wantErr: "service"},
		{name: "extra field", value: "8080:app:80:tcp", wantErr: "expected format"},
		{name: "invalid local", value: "zero:app:80", wantErr: "local port"},
		{name: "local out of range", value: "65536:app:80", wantErr: "between 1 and 65535"},
		{name: "invalid remote", value: "8080:app:http", wantErr: "remote port"},
		{name: "remote out of range", value: "8080:app:0", wantErr: "between 1 and 65535"},
	}
	for _, test := range tests {
		test := test
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			got, err := parsePortForwardSpec(test.value)
			if test.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), test.wantErr) {
					t.Fatalf("parsePortForwardSpec(%q) error = %v, want %q", test.value, err, test.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("parsePortForwardSpec(%q) error = %v", test.value, err)
			}
			if !reflect.DeepEqual(got, test.want) {
				t.Fatalf("parsePortForwardSpec(%q) = %#v, want %#v", test.value, got, test.want)
			}
		})
	}
}

func TestPortForwardListenAddressIsIPv4Loopback(t *testing.T) {
	t.Parallel()
	address := portForwardListenAddress(8080)
	host, port, err := net.SplitHostPort(address)
	if err != nil {
		t.Fatalf("SplitHostPort(%q) error = %v", address, err)
	}
	if host != "127.0.0.1" || port != "8080" {
		t.Fatalf("portForwardListenAddress() = %q, want 127.0.0.1:8080", address)
	}
	if ip := net.ParseIP(host); ip == nil || !ip.IsLoopback() {
		t.Fatalf("listener host %q is not loopback", host)
	}
}

func TestUseContainerExecPortForward(t *testing.T) {
	t.Parallel()
	local := &config.Context{DockerHostType: config.ContextLocal}
	remote := &config.Context{DockerHostType: config.ContextRemote}
	if !useContainerExecPortForward("darwin", local) {
		t.Fatal("expected local macOS context to use container exec")
	}
	if !useContainerExecPortForward("windows", local) {
		t.Fatal("expected local Windows context to use container exec")
	}
	if useContainerExecPortForward("linux", local) {
		t.Fatal("expected local Linux context to use direct container networking")
	}
	if useContainerExecPortForward("darwin", remote) {
		t.Fatal("expected remote context to use SSH forwarding")
	}
}

type fakePortForwardExecutor struct {
	opts     docker.ExecOptions
	exitCode int
	err      error
	run      func(docker.ExecOptions) error
}

func (f *fakePortForwardExecutor) Exec(_ context.Context, opts docker.ExecOptions) (int, error) {
	f.opts = opts
	if f.run != nil {
		if err := f.run(opts); err != nil {
			return -1, err
		}
	}
	return f.exitCode, f.err
}

func TestForwardContainerExecStreamsThroughBusyBoxNC(t *testing.T) {
	t.Parallel()
	server, client := net.Pipe()
	t.Cleanup(func() { _ = client.Close() })
	executor := &fakePortForwardExecutor{
		run: func(opts docker.ExecOptions) error {
			request := make([]byte, 4)
			if _, err := io.ReadFull(opts.Stdin, request); err != nil {
				return err
			}
			if string(request) != "ping" {
				return errors.New("unexpected request")
			}
			_, err := opts.Stdout.Write([]byte("pong"))
			return err
		},
	}
	done := make(chan struct{})
	go func() {
		defer close(done)
		forwardContainerExec(context.Background(), executor, server, "project-omeka-s-1", 80, io.Discard)
	}()

	if err := client.SetDeadline(time.Now().Add(5 * time.Second)); err != nil {
		t.Fatal(err)
	}
	if _, err := client.Write([]byte("ping")); err != nil {
		t.Fatalf("write request: %v", err)
	}
	response := make([]byte, 4)
	if _, err := io.ReadFull(client, response); err != nil {
		t.Fatalf("read response: %v", err)
	}
	if string(response) != "pong" {
		t.Fatalf("response = %q, want pong", response)
	}
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("container exec forward did not stop")
	}

	wantCmd := []string{"busybox", "nc", "127.0.0.1", "80"}
	if executor.opts.Container != "project-omeka-s-1" || !reflect.DeepEqual(executor.opts.Cmd, wantCmd) {
		t.Fatalf("exec options = container %q command %#v, want %q %#v", executor.opts.Container, executor.opts.Cmd, "project-omeka-s-1", wantCmd)
	}
	if !executor.opts.AttachStdin || !executor.opts.AttachStdout || !executor.opts.AttachStderr {
		t.Fatalf("exec stream attachments = stdin:%v stdout:%v stderr:%v", executor.opts.AttachStdin, executor.opts.AttachStdout, executor.opts.AttachStderr)
	}
}

func TestForwardContainerExecExplainsBusyBoxRequirement(t *testing.T) {
	t.Parallel()
	server, client := net.Pipe()
	defer client.Close()
	executor := &fakePortForwardExecutor{err: errors.New("executable file not found")}
	var stderr bytes.Buffer
	forwardContainerExec(context.Background(), executor, server, "project-app-1", 80, &stderr)
	if got := stderr.String(); !strings.Contains(got, "BusyBox nc") || !strings.Contains(got, "executable file not found") {
		t.Fatalf("forward error = %q, want clear BusyBox nc requirement", got)
	}
}

func TestForwardContainerExecReportsNonzeroExit(t *testing.T) {
	t.Parallel()
	server, client := net.Pipe()
	defer client.Close()
	executor := &fakePortForwardExecutor{exitCode: 17}
	var stderr bytes.Buffer
	forwardContainerExec(context.Background(), executor, server, "project-app-1", 80, &stderr)
	if got := stderr.String(); !strings.Contains(got, "status 17") || !strings.Contains(got, "BusyBox nc") {
		t.Fatalf("forward error = %q, want nonzero status and BusyBox nc requirement", got)
	}
}
