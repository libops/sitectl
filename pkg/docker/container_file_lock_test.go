package docker

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"net"
	"reflect"
	"strings"
	"sync"
	"testing"

	dockertypes "github.com/docker/docker/api/types"
	dockercontainer "github.com/docker/docker/api/types/container"
	"github.com/docker/docker/pkg/stdcopy"
)

func TestAcquireContainerFileLockHoldsUntilRelease(t *testing.T) {
	api := &fakeContainerFileLockAPI{}
	client := &DockerClient{CLI: api}
	lockPath := "/var/lock/sitectl/example.lock"

	lock, err := client.AcquireContainerFileLock(context.Background(), ContainerFileLockOptions{
		Container: "example-solr-1",
		Path:      lockPath,
	})
	if err != nil {
		t.Fatalf("AcquireContainerFileLock() error = %v", err)
	}
	api.mu.Lock()
	options := api.options
	running := api.running
	api.mu.Unlock()
	wantCommand := []string{"flock", "-n", lockPath, "sh", "-c", containerFileLockScript}
	if !reflect.DeepEqual(options.Cmd, wantCommand) {
		t.Fatalf("lock command = %#v, want %#v", options.Cmd, wantCommand)
	}
	if !options.AttachStdin || !options.AttachStdout || !options.AttachStderr || options.Tty || !running {
		t.Fatalf("lock holder options/state = %+v, running=%t", options, running)
	}
	if err := lock.Release(); err != nil {
		t.Fatalf("Release() error = %v", err)
	}
	if err := lock.Release(); err != nil {
		t.Fatalf("second Release() error = %v", err)
	}
	api.mu.Lock()
	running = api.running
	exitCode := api.exitCode
	api.mu.Unlock()
	if running || exitCode != 0 {
		t.Fatalf("released lock holder running=%t exit=%d", running, exitCode)
	}
}

func TestAcquireContainerFileLockReportsHeldLock(t *testing.T) {
	api := &fakeContainerFileLockAPI{forceBusy: true}
	client := &DockerClient{CLI: api}
	_, err := client.AcquireContainerFileLock(context.Background(), ContainerFileLockOptions{
		Container: "example-solr-1",
		Path:      "/var/lock/sitectl/example.lock",
	})
	if !errors.Is(err, ErrContainerFileLockHeld) {
		t.Fatalf("AcquireContainerFileLock() error = %v, want ErrContainerFileLockHeld", err)
	}
}

func TestAcquireContainerFileLockValidatesPath(t *testing.T) {
	client := &DockerClient{CLI: &fakeContainerFileLockAPI{}}
	if _, err := client.AcquireContainerFileLock(context.Background(), ContainerFileLockOptions{Container: "example", Path: "relative.lock"}); err == nil {
		t.Fatal("AcquireContainerFileLock() accepted a relative path")
	}
}

type fakeContainerFileLockAPI struct {
	mu        sync.Mutex
	options   dockercontainer.ExecOptions
	forceBusy bool
	running   bool
	exitCode  int
}

func (f *fakeContainerFileLockAPI) ContainerInspect(context.Context, string) (dockercontainer.InspectResponse, error) {
	return dockercontainer.InspectResponse{}, nil
}

func (f *fakeContainerFileLockAPI) ContainerList(context.Context, dockercontainer.ListOptions) ([]dockercontainer.Summary, error) {
	return nil, nil
}

func (f *fakeContainerFileLockAPI) ContainerExecCreate(_ context.Context, _ string, options dockercontainer.ExecOptions) (dockercontainer.ExecCreateResponse, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.options = options
	return dockercontainer.ExecCreateResponse{ID: "lock-exec"}, nil
}

func (f *fakeContainerFileLockAPI) ContainerExecAttach(_ context.Context, _ string, _ dockercontainer.ExecAttachOptions) (dockertypes.HijackedResponse, error) {
	clientConnection, serverConnection := net.Pipe()
	f.mu.Lock()
	busy := f.forceBusy
	if !busy {
		f.running = true
		f.exitCode = 0
	}
	f.mu.Unlock()
	go func() {
		defer serverConnection.Close()
		if busy {
			f.mu.Lock()
			f.running = false
			f.exitCode = 1
			f.mu.Unlock()
			return
		}
		if _, err := fmt.Fprintln(stdcopy.NewStdWriter(serverConnection, stdcopy.Stdout), containerFileLockHandshake); err != nil {
			return
		}
		reader := bufio.NewReader(serverConnection)
		for {
			message, err := reader.ReadString('\n')
			if err != nil || strings.TrimSpace(message) == "release" {
				break
			}
		}
		f.mu.Lock()
		f.running = false
		f.exitCode = 0
		f.mu.Unlock()
	}()
	return dockertypes.NewHijackedResponse(clientConnection, "application/vnd.docker.raw-stream"), nil
}

func (f *fakeContainerFileLockAPI) ContainerExecInspect(context.Context, string) (dockercontainer.ExecInspect, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return dockercontainer.ExecInspect{ExecID: "lock-exec", Running: f.running, ExitCode: f.exitCode}, nil
}
