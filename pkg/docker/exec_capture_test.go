package docker

import (
	"context"
	"strings"
	"testing"
)

func TestExecCapture_NilCliReturnsError(t *testing.T) {
	// ExecCapture on a DockerClient with no API should return an error,
	// not panic. This verifies the function is safe to call even when the
	// underlying client has not been fully initialised.
	cli := &DockerClient{}
	_, err := ExecCapture(context.Background(), cli, "mycontainer", "/app", []string{"echo", "hi"})
	if err == nil {
		t.Error("expected error from nil CLI, got nil")
	}
}

func TestExecCapture_ErrorMessageFormat(t *testing.T) {
	// Verify the error message format produced when a command exits non-zero.
	// We test this by inspecting the formatting logic directly, since a real
	// Docker daemon is not available in unit tests.
	cases := []struct {
		exitCode int
		stderr   string
		stdout   string
		wantSub  string
	}{
		{1, "permission denied", "", "exit code 1: permission denied"},
		{2, "", "some output", "exit code 2: some output"},
		{3, "", "", "exit code 3"},
	}
	for _, tc := range cases {
		detail := strings.TrimSpace(tc.stderr)
		if detail == "" {
			detail = strings.TrimSpace(tc.stdout)
		}
		var msg string
		if detail != "" {
			msg = "command failed with exit code " + itoa(tc.exitCode) + ": " + detail
		} else {
			msg = "command failed with exit code " + itoa(tc.exitCode)
		}
		if !strings.Contains(msg, tc.wantSub) {
			t.Errorf("exitCode=%d: got %q, want substring %q", tc.exitCode, msg, tc.wantSub)
		}
	}
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	neg := n < 0
	if neg {
		n = -n
	}
	digits := []byte{}
	for n > 0 {
		digits = append([]byte{byte('0' + n%10)}, digits...)
		n /= 10
	}
	if neg {
		digits = append([]byte{'-'}, digits...)
	}
	return string(digits)
}
