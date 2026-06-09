package plugin

import (
	"bytes"
	"io"
	"os"
	"strings"
	"testing"
)

func TestProgressLineReturnsNoopForNonTerminalOutput(t *testing.T) {
	var out bytes.Buffer
	progress := NewProgressLine(&out, "Waiting", "service starting")

	progress.Report("Retrying", "")
	progress.Close()

	if out.String() != "" {
		t.Fatalf("expected non-terminal progress to be silent, got %q", out.String())
	}
}

func TestProgressLineClearsLineBeforeEachRender(t *testing.T) {
	reader, writer, err := os.Pipe()
	if err != nil {
		t.Fatalf("Pipe() error = %v", err)
	}
	defer reader.Close()

	progress := &ProgressLine{
		out:    writer,
		frames: []string{"-"},
		title:  "Waiting for healthcheck retry 1",
		detail: "service starting",
	}
	progress.renderLocked()
	progress.title = "OK"
	progress.detail = ""
	progress.renderLocked()
	if err := writer.Close(); err != nil {
		t.Fatalf("close pipe writer: %v", err)
	}

	data, err := io.ReadAll(reader)
	if err != nil {
		t.Fatalf("ReadAll(pipe) error = %v", err)
	}
	got := string(data)
	want := "\r\x1b[2K- Waiting for healthcheck retry 1 - service starting\r\x1b[2K- OK"
	if got != want {
		t.Fatalf("rendered progress = %q, want %q", got, want)
	}
	if strings.Count(got, "\r\x1b[2K") != 2 {
		t.Fatalf("expected each render to clear the line, got %q", got)
	}
}
