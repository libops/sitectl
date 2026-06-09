package cmd

import (
	"bytes"
	"strings"
	"testing"
	"time"

	sitevalidate "github.com/libops/sitectl/pkg/validate"
)

func TestShouldRetryHealthcheckDefaultRetriesStartingServices(t *testing.T) {
	report := sitevalidate.Report{
		Valid: false,
		Results: []sitevalidate.Result{
			{Name: "service:solr", Status: sitevalidate.StatusFailed, Detail: "drupal-solr-1: health=starting"},
			{Name: "http:drupal", Status: sitevalidate.StatusFailed, Detail: "connection refused"},
		},
	}

	if !shouldRetryHealthcheck(report, healthcheckHostParams{}) {
		t.Fatal("expected default healthcheck to retry while a compose service is starting")
	}
}

func TestShouldRetryHealthcheckDefaultDoesNotRetryStableFailures(t *testing.T) {
	tests := []struct {
		name   string
		report sitevalidate.Report
	}{
		{
			name: "unhealthy service",
			report: sitevalidate.Report{
				Valid: false,
				Results: []sitevalidate.Result{
					{Name: "service:solr", Status: sitevalidate.StatusFailed, Detail: "drupal-solr-1: health=unhealthy"},
				},
			},
		},
		{
			name: "plugin check failure",
			report: sitevalidate.Report{
				Valid: false,
				Results: []sitevalidate.Result{
					{Name: "solr:solr", Status: sitevalidate.StatusFailed, Detail: "curl failed"},
				},
			},
		},
		{
			name: "valid report",
			report: sitevalidate.Report{
				Valid: true,
				Results: []sitevalidate.Result{
					{Name: "service:solr", Status: sitevalidate.StatusOK, Detail: "drupal-solr-1: healthy"},
				},
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if shouldRetryHealthcheck(tt.report, healthcheckHostParams{}) {
				t.Fatal("expected default healthcheck not to retry")
			}
		})
	}
}

func TestShouldRetryHealthcheckPersistRetriesStableFailures(t *testing.T) {
	report := sitevalidate.Report{
		Valid: false,
		Results: []sitevalidate.Result{
			{Name: "http:drupal", Status: sitevalidate.StatusFailed, Detail: "connection refused"},
		},
	}

	if !shouldRetryHealthcheck(report, healthcheckHostParams{Persist: true}) {
		t.Fatal("expected persistent healthcheck to retry stable failures")
	}
}

func TestHealthcheckRetryMessageNamesStartingServices(t *testing.T) {
	report := sitevalidate.Report{
		Valid: false,
		Results: []sitevalidate.Result{
			{Name: "service:solr", Status: sitevalidate.StatusFailed, Detail: "drupal-solr-1: health=starting"},
		},
	}

	message := healthcheckRetryMessage(report, healthcheckHostParams{Interval: 3 * time.Second}, 2)
	for _, want := range []string{"retry 2", "solr starting", "3s"} {
		if !strings.Contains(message, want) {
			t.Fatalf("message %q missing %q", message, want)
		}
	}
}

func TestHealthcheckProgressDoesNotWriteControlCharactersToNonTTY(t *testing.T) {
	var stderr bytes.Buffer

	progress := startHealthcheckProgress(&stderr, "Waiting for healthcheck retry 1: solr starting; next check in 10s")
	progress.Update("Waiting for healthcheck retry 2: solr starting; next check in 10s")
	progress.Stop()

	got := stderr.String()
	for _, control := range []string{"\r", "\x1b"} {
		if strings.Contains(got, control) {
			t.Fatalf("expected non-terminal progress without terminal control characters, got %q", got)
		}
	}
}
