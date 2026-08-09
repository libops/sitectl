package docker

import (
	"archive/tar"
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"path"
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
	lockPath := "/var/lock/sitectl/example;touch-not-executed.lock"

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
	copyDestination := api.copyDestination
	copyName := api.copyName
	copyMode := api.copyMode
	copyContent := append([]byte(nil), api.copyContent...)
	calls := append([]string(nil), api.calls...)
	api.mu.Unlock()
	if len(options.Cmd) != 3 {
		t.Fatalf("lock command = %#v, want sh PROGRAM LOCK_PATH", options.Cmd)
	}
	programPath := options.Cmd[1]
	wantCommand := []string{"sh", programPath, lockPath}
	if !reflect.DeepEqual(options.Cmd, wantCommand) {
		t.Fatalf("lock command = %#v, want %#v", options.Cmd, wantCommand)
	}
	if path.Dir(programPath) != containerFileLockProgramDir || !strings.HasPrefix(path.Base(programPath), containerFileLockProgramPrefix) || !strings.HasSuffix(programPath, ".sh") {
		t.Fatalf("staged lock program path = %q", programPath)
	}
	if copyDestination != containerFileLockProgramDir || copyName != path.Base(programPath) || copyMode != 0o444 || !bytes.Equal(copyContent, containerFileLockProgram) {
		t.Fatalf("staged lock program destination=%q name=%q mode=%#o content_match=%t", copyDestination, copyName, copyMode, bytes.Equal(copyContent, containerFileLockProgram))
	}
	if wantCalls := []string{"create", "copy", "attach"}; !reflect.DeepEqual(calls, wantCalls) {
		t.Fatalf("lock setup calls = %q, want %q", calls, wantCalls)
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

func TestContainerFileLockProgramDefinesStableProtocol(t *testing.T) {
	t.Parallel()

	for _, fragment := range []string{
		`flock -n "$lock_path"`,
		`read -r -t 30 message`,
		containerFileLockHandshake,
		`[ "$message" = release ]`,
	} {
		if !bytes.Contains(containerFileLockProgram, []byte(fragment)) {
			t.Fatalf("container lock program is missing protocol fragment %q", fragment)
		}
	}
	if bytes.Contains(containerFileLockProgram, []byte("sh -c")) {
		t.Fatal("container lock program contains an inline shell command")
	}
}

type fakeContainerFileLockAPI struct {
	mu              sync.Mutex
	options         dockercontainer.ExecOptions
	forceBusy       bool
	running         bool
	exitCode        int
	copyDestination string
	copyName        string
	copyMode        int64
	copyContent     []byte
	calls           []string
}

func (f *fakeContainerFileLockAPI) CopyToContainer(_ context.Context, _ string, destination string, content io.Reader, _ dockercontainer.CopyToContainerOptions) error {
	archiveReader := tar.NewReader(content)
	header, err := archiveReader.Next()
	if err != nil {
		return fmt.Errorf("read staged lock program header: %w", err)
	}
	program, err := io.ReadAll(archiveReader)
	if err != nil {
		return fmt.Errorf("read staged lock program: %w", err)
	}
	if _, err := archiveReader.Next(); err != io.EOF {
		return fmt.Errorf("staged lock program archive contains additional entries")
	}
	f.mu.Lock()
	f.copyDestination = destination
	f.copyName = header.Name
	f.copyMode = header.Mode
	f.copyContent = program
	f.calls = append(f.calls, "copy")
	f.mu.Unlock()
	return nil
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
	f.calls = append(f.calls, "create")
	return dockercontainer.ExecCreateResponse{ID: "lock-exec"}, nil
}

func (f *fakeContainerFileLockAPI) ContainerExecAttach(_ context.Context, _ string, _ dockercontainer.ExecAttachOptions) (dockertypes.HijackedResponse, error) {
	clientConnection, serverConnection := net.Pipe()
	f.mu.Lock()
	f.calls = append(f.calls, "attach")
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
