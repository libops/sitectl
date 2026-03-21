package cmd

import (
	"strings"
	"testing"
)

func TestEvaluateLogConfigDetectsUnboundedJSONFileLogs(t *testing.T) {
	rotated, external, hint := evaluateLogConfig("json-file", map[string]string{})
	if rotated {
		t.Fatal("expected json-file without limits not to be rotated")
	}
	if external {
		t.Fatal("expected json-file without limits not to be external")
	}
	if !strings.Contains(hint, "max-size") {
		t.Fatalf("expected rotation hint, got %q", hint)
	}
}

func TestRenderLogDiagnosticsExpandsWhenLogsNeedAttention(t *testing.T) {
	oldVerbose := debugVerbose
	debugVerbose = true
	t.Cleanup(func() {
		debugVerbose = oldVerbose
	})

	lines := renderLogDiagnostics(logDiagnostics{
		KnownSize:      true,
		TotalBytes:     25 * 1024 * 1024,
		UnboundedCount: 1,
		Containers: []containerLogDiagnostics{
			{Service: "drupal", Driver: "json-file", SizeBytes: 25 * 1024 * 1024, HasSize: true, Rotated: false, RotationHint: "file-backed logs are not capped; set max-size and max-file"},
		},
	})

	rendered := strings.Join(lines, "\n")
	if !strings.Contains(rendered, "Total logs: 25.0MiB") {
		t.Fatalf("expected total log size, got:\n%s", rendered)
	}
	if !strings.Contains(rendered, "Best practice:") {
		t.Fatalf("expected best practice guidance, got:\n%s", rendered)
	}
	if !strings.Contains(rendered, "drupal: driver=json-file, size=25.0MiB, not rotated") {
		t.Fatalf("expected per-container detail, got:\n%s", rendered)
	}
}

func TestRenderLogDiagnosticsDefaultIsCompact(t *testing.T) {
	oldVerbose := debugVerbose
	debugVerbose = false
	t.Cleanup(func() {
		debugVerbose = oldVerbose
	})

	lines := renderLogDiagnostics(logDiagnostics{
		KnownSize:      true,
		TotalBytes:     25 * 1024 * 1024,
		UnboundedCount: 1,
		Containers: []containerLogDiagnostics{
			{Service: "drupal", Driver: "json-file", SizeBytes: 25 * 1024 * 1024, HasSize: true, Rotated: false},
		},
	})

	rendered := strings.Join(lines, "\n")
	if strings.Contains(rendered, "drupal: driver=") {
		t.Fatalf("expected compact output without per-container detail, got:\n%s", rendered)
	}
	if !strings.Contains(rendered, "Log handling: 1 container(s) are using unbounded file-backed logs") {
		t.Fatalf("expected compact log handling line, got:\n%s", rendered)
	}
}
