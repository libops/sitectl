//go:build linux

package docker

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"net"
	"os"
	"reflect"
	"testing"
	"time"

	dockercontainer "github.com/docker/docker/api/types/container"
	"golang.org/x/sys/unix"
)

type recordingExecResizer struct {
	sizes []dockercontainer.ResizeOptions
}

func (r *recordingExecResizer) ContainerExecResize(_ context.Context, _ string, size dockercontainer.ResizeOptions) error {
	r.sizes = append(r.sizes, size)
	return nil
}

func TestInteractiveExecReturnsAndRestoresPTYWhenRemoteOutputEnds(t *testing.T) {
	master, slave := openTestPTY(t)
	defer master.Close()
	defer slave.Close()
	if err := unix.IoctlSetWinsize(int(slave.Fd()), unix.TIOCSWINSZ, &unix.Winsize{Row: 41, Col: 103}); err != nil {
		t.Fatalf("set PTY size: %v", err)
	}
	before, err := unix.IoctlGetTermios(int(slave.Fd()), unix.TCGETS)
	if err != nil {
		t.Fatalf("read original PTY state: %v", err)
	}

	resizer := &recordingExecResizer{}
	opts := ExecOptions{AttachStdin: true, AttachStdout: true, Tty: true, Stdin: slave, Stdout: io.Discard, Stderr: io.Discard}
	restore, err := prepareExecTerminal(context.Background(), resizer, "exec-id", opts)
	if err != nil {
		t.Fatalf("prepareExecTerminal() error = %v", err)
	}
	if len(resizer.sizes) != 1 || resizer.sizes[0].Width != 103 || resizer.sizes[0].Height != 41 {
		t.Fatalf("initial resize = %#v, want 103x41", resizer.sizes)
	}

	client, server := net.Pipe()
	defer client.Close()
	input, err := newExecInputController(slave)
	if err != nil {
		t.Fatalf("newExecInputController() error = %v", err)
	}
	defer input.Close()
	go func() {
		_, _ = io.WriteString(server, "remote complete\n")
		_ = server.Close()
	}()
	done := make(chan error, 1)
	go func() {
		done <- copyDockerExecStreams(context.Background(), client, bufio.NewReader(client), func() { _ = client.Close() }, opts, input)
	}()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("copyDockerExecStreams() error = %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("interactive exec waited for PTY stdin EOF after remote output ended")
	}

	restore()
	after, err := unix.IoctlGetTermios(int(slave.Fd()), unix.TCGETS)
	if err != nil {
		t.Fatalf("read restored PTY state: %v", err)
	}
	if !reflect.DeepEqual(after, before) {
		t.Fatalf("PTY state was not restored: got %#v, want %#v", after, before)
	}
	if _, err := master.Write([]byte("next command\n")); err != nil {
		t.Fatalf("write next PTY input: %v", err)
	}
	poll := []unix.PollFd{{Fd: int32(slave.Fd()), Events: unix.POLLIN}}
	if ready, err := unix.Poll(poll, 250); err != nil || ready != 1 {
		t.Fatalf("next PTY input was consumed by a lingering exec reader: ready=%d err=%v", ready, err)
	}
	buffer := make([]byte, 64)
	read, err := slave.Read(buffer)
	if err != nil || string(buffer[:read]) != "next command\n" {
		t.Fatalf("next PTY input = %q, %v; want preserved input", buffer[:read], err)
	}
}

func openTestPTY(t *testing.T) (*os.File, *os.File) {
	t.Helper()
	master, err := os.OpenFile("/dev/ptmx", os.O_RDWR|unix.O_NOCTTY, 0)
	if err != nil {
		t.Skipf("open /dev/ptmx: %v", err)
	}
	if err := unix.IoctlSetPointerInt(int(master.Fd()), unix.TIOCSPTLCK, 0); err != nil {
		master.Close()
		t.Fatalf("unlock PTY: %v", err)
	}
	number, err := unix.IoctlGetInt(int(master.Fd()), unix.TIOCGPTN)
	if err != nil {
		master.Close()
		t.Fatalf("get PTY number: %v", err)
	}
	slave, err := os.OpenFile(fmt.Sprintf("/dev/pts/%d", number), os.O_RDWR|unix.O_NOCTTY, 0)
	if err != nil {
		master.Close()
		t.Fatalf("open PTY slave: %v", err)
	}
	return master, slave
}
