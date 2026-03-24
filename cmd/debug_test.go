package cmd

import (
	"strings"
	"testing"

	"github.com/libops/sitectl/internal/debugreport"
	"github.com/libops/sitectl/pkg/plugin/debugui"
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
		UnboundedCount: 1,
		Containers: []containerLogDiagnostics{
			{Service: "drupal", Driver: "json-file", Rotated: false, RotationHint: "file-backed logs are not capped; set max-size and max-file"},
		},
	})

	rendered := debugui.FormatRows(rows)
	if strings.Contains(rendered, "Total logs") {
		t.Fatalf("expected log summary without total log size, got:\n%s", rendered)
	}
	if !strings.Contains(rendered, "Recommendation") {
		t.Fatalf("expected recommendation guidance, got:\n%s", rendered)
	}
}

func TestRenderLogDetailsBodyIncludesPerContainerRows(t *testing.T) {
	rendered := renderLogDetailsBody(logDiagnostics{
		UnboundedCount: 1,
		Containers: []containerLogDiagnostics{
			{Service: "drupal", Driver: "json-file", Rotated: false, RotationHint: "file-backed logs are not capped; set max-size and max-file"},
		},
	})

	if !strings.Contains(rendered, "drupal: driver=json-file, not rotated") {
		t.Fatalf("expected per-container detail, got:\n%s", rendered)
	}
}

func TestLogSummaryRowsStayCompact(t *testing.T) {
	rows := logSummaryRows(logDiagnostics{
		UnboundedCount: 1,
		Containers: []containerLogDiagnostics{
			{Service: "drupal", Driver: "json-file", Rotated: false},
		},
	})

	rendered := debugui.FormatRows(rows)
	if strings.Contains(rendered, "drupal: driver=") {
		t.Fatalf("expected compact output without per-container detail, got:\n%s", rendered)
	}
	if !strings.Contains(rendered, "Log handling") || !strings.Contains(rendered, "1 container(s) are using unbounded file-backed logs") {
		t.Fatalf("expected compact log handling line, got:\n%s", rendered)
	}
}

func TestImageSummaryRowsWarnWhenThresholdExceeded(t *testing.T) {
	rendered := debugui.FormatRows(imageSummaryRows(imageDiagnostics{
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

func TestHostSummaryRowsIncludeRequestedStats(t *testing.T) {
	rendered := debugui.FormatRows(hostSummaryRows(debugreport.HostDiagnostics{
		CPUCount:           8,
		MemoryBytes:        16 * 1024 * 1024 * 1024,
		SwapBytes:          2 * 1024 * 1024 * 1024,
		DiskAvailableBytes: 50 * 1024 * 1024 * 1024,
		OSVersion:          "Debian GNU/Linux 12 (bookworm)",
	}, "/srv/project"))

	for _, expected := range []string{"CPUs", "Memory", "Swap", "Available disk", "OS version", "Debian GNU/Linux 12 (bookworm)", "/srv/project"} {
		if !strings.Contains(rendered, expected) {
			t.Fatalf("expected %q in rendered rows, got:\n%s", expected, rendered)
		}
	}
}
