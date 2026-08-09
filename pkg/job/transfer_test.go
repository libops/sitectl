package job

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"testing"

	"github.com/libops/sitectl/pkg/config"
)

func TestContextFileTransfersFailBeforePublishingWhenCanceled(t *testing.T) {
	t.Parallel()

	directory := t.TempDir()
	source := filepath.Join(directory, "source")
	if err := os.WriteFile(source, []byte("customer database"), 0o600); err != nil {
		t.Fatal(err)
	}
	ctx := &config.Context{DockerHostType: config.ContextLocal}
	runCtx, cancel := context.WithCancel(context.Background())
	cancel()

	download := filepath.Join(directory, "download")
	if err := DownloadContextFileContext(runCtx, ctx, source, download); !errors.Is(err, context.Canceled) {
		t.Fatalf("DownloadContextFileContext() error = %v, want context cancellation", err)
	}
	if _, err := os.Stat(download); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("canceled download published destination: %v", err)
	}

	upload := filepath.Join(directory, "upload")
	if err := UploadContextFile(runCtx, ctx, source, upload); !errors.Is(err, context.Canceled) {
		t.Fatalf("UploadContextFile() error = %v, want context cancellation", err)
	}
	if _, err := os.Stat(upload); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("canceled upload published destination: %v", err)
	}
}

func TestContextFileDownloadRemovesPartialFileWhenCanceledMidCopy(t *testing.T) {
	t.Parallel()

	directory := t.TempDir()
	destination := filepath.Join(directory, "partial")
	runCtx, cancel := context.WithCancel(context.Background())
	reader := &cancelAfterFirstRead{cancel: cancel, payload: []byte("first transfer chunk")}

	err := writeLocalFileContext(runCtx, destination, reader)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("writeLocalFileContext() error = %v, want context cancellation", err)
	}
	if _, err := os.Stat(destination); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("canceled transfer retained partial destination: %v", err)
	}
}

func TestContextFileDownloadDoesNotOverwriteExistingDestination(t *testing.T) {
	t.Parallel()

	destination := filepath.Join(t.TempDir(), "existing")
	if err := os.WriteFile(destination, []byte("keep me"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := writeLocalFileContext(context.Background(), destination, bytes.NewBufferString("replacement")); err == nil {
		t.Fatal("writeLocalFileContext() overwrote an existing destination")
	}
	contents, err := os.ReadFile(destination)
	if err != nil {
		t.Fatal(err)
	}
	if string(contents) != "keep me" {
		t.Fatalf("existing destination changed to %q", contents)
	}
}

type cancelAfterFirstRead struct {
	cancel  context.CancelFunc
	payload []byte
	read    bool
}

func (r *cancelAfterFirstRead) Read(destination []byte) (int, error) {
	if r.read {
		return 0, io.EOF
	}
	r.read = true
	count := copy(destination, r.payload)
	r.cancel()
	return count, nil
}
