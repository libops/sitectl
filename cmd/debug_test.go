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

func TestLogSummaryRowsIncludeRecommendationWhenLogsNeedAttention(t *testing.T) {
	rows := logSummaryRows(logDiagnostics{
		KnownSize:      true,
		TotalBytes:     25 * 1024 * 1024,
		UnboundedCount: 1,
		Containers: []containerLogDiagnostics{
			{Service: "drupal", Driver: "json-file", SizeBytes: 25 * 1024 * 1024, HasSize: true, Rotated: false, RotationHint: "file-backed logs are not capped; set max-size and max-file"},
		},
	})

	rendered := formatDebugRows(rows)
	if !strings.Contains(rendered, "Total logs") || !strings.Contains(rendered, "25.0MiB") {
		t.Fatalf("expected total log size, got:\n%s", rendered)
	}
	if !strings.Contains(rendered, "Recommendation") {
		t.Fatalf("expected recommendation guidance, got:\n%s", rendered)
	}
}

func TestRenderLogDetailsBodyIncludesPerContainerRows(t *testing.T) {
	rendered := renderLogDetailsBody(logDiagnostics{
		KnownSize:      true,
		TotalBytes:     25 * 1024 * 1024,
		UnboundedCount: 1,
		Containers: []containerLogDiagnostics{
			{Service: "drupal", Driver: "json-file", SizeBytes: 25 * 1024 * 1024, HasSize: true, Rotated: false, RotationHint: "file-backed logs are not capped; set max-size and max-file"},
		},
	})

	if !strings.Contains(rendered, "drupal: driver=json-file, size=25.0MiB, not rotated") {
		t.Fatalf("expected per-container detail, got:\n%s", rendered)
	}
}

func TestLogSummaryRowsStayCompact(t *testing.T) {
	rows := logSummaryRows(logDiagnostics{
		KnownSize:      true,
		TotalBytes:     25 * 1024 * 1024,
		UnboundedCount: 1,
		Containers: []containerLogDiagnostics{
			{Service: "drupal", Driver: "json-file", SizeBytes: 25 * 1024 * 1024, HasSize: true, Rotated: false},
		},
	})

	rendered := formatDebugRows(rows)
	if strings.Contains(rendered, "drupal: driver=") {
		t.Fatalf("expected compact output without per-container detail, got:\n%s", rendered)
	}
	if !strings.Contains(rendered, "Log handling") || !strings.Contains(rendered, "1 container(s) are using unbounded file-backed logs") {
		t.Fatalf("expected compact log handling line, got:\n%s", rendered)
	}
}

func TestImageSummaryRowsWarnWhenThresholdExceeded(t *testing.T) {
	rendered := formatDebugRows(imageSummaryRows(imageDiagnostics{
		TotalBytes: imageSizeWarningThreshold + 1,
		ImageCount: 42,
	}))

	if !strings.Contains(rendered, "docker system prune -af") {
		t.Fatalf("expected prune recommendation, got:\n%s", rendered)
	}
	if !strings.Contains(rendered, dockerPruneDocsURL) {
		t.Fatalf("expected prune docs link, got:\n%s", rendered)
	}
}
