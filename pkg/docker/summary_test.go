package docker

import (
	"context"
	"testing"

	dockercontainer "github.com/docker/docker/api/types/container"
	"github.com/libops/sitectl/pkg/config"
)

func TestSummarizeProjectWithClient(t *testing.T) {
	fake := &FakeDockerClient{
		ListFunc: func(ctx context.Context, options dockercontainer.ListOptions) ([]dockercontainer.Summary, error) {
			return []dockercontainer.Summary{
				{
					Names:  []string{"/museum-web-1"},
					State:  "running",
					Status: "Up 2 minutes (healthy)",
					Labels: map[string]string{"com.docker.compose.service": "web"},
				},
				{
					Names:  []string{"/museum-db-1"},
					State:  "exited",
					Status: "Exited (1) 10 seconds ago",
					Labels: map[string]string{"com.docker.compose.service": "db"},
				},
			}, nil
		},
	}

	summary, err := SummarizeProjectWithClient(context.Background(), fake, &config.Context{ProjectName: "museum"})
	if err != nil {
		t.Fatalf("SummarizeProjectWithClient() error = %v", err)
	}
	if summary.Total != 2 {
		t.Fatalf("expected 2 containers, got %d", summary.Total)
	}
	if summary.Running != 1 {
		t.Fatalf("expected 1 running container, got %d", summary.Running)
	}
	if summary.Status != "degraded" {
		t.Fatalf("expected degraded status, got %q", summary.Status)
	}
	if len(summary.Services) != 2 {
		t.Fatalf("expected 2 services, got %d", len(summary.Services))
	}
}

func TestParseComposePSOutput(t *testing.T) {
	output := `[
  {
    "Name": "lehigh-d10-drupal-1",
    "Service": "drupal",
    "State": "running",
    "Status": "Up 2 minutes",
    "Health": "healthy"
  },
  {
    "Name": "lehigh-d10-fcrepo-1",
    "Service": "fcrepo",
    "State": "running",
    "Status": "Up 2 minutes",
    "Health": "healthy"
  }
]`

	summary, err := parseComposePSOutput(output)
	if err != nil {
		t.Fatalf("parseComposePSOutput() error = %v", err)
	}
	if summary.Total != 2 {
		t.Fatalf("expected 2 containers, got %d", summary.Total)
	}
	if summary.Running != 2 {
		t.Fatalf("expected 2 running containers, got %d", summary.Running)
	}
	if summary.Healthy != 2 {
		t.Fatalf("expected 2 healthy containers, got %d", summary.Healthy)
	}
	if summary.Status != "running" {
		t.Fatalf("expected running status, got %q", summary.Status)
	}
}

func TestApplyDockerStatsUsesSingleEffectiveMemoryLimit(t *testing.T) {
	summary := ProjectSummary{
		Services: []ServiceSummary{
			{Name: "lehigh-d10-drupal-1", Service: "drupal"},
			{Name: "lehigh-d10-solr-1", Service: "solr"},
		},
	}

	output := `{"Name":"lehigh-d10-drupal-1","CPUPerc":"2.5%","MemUsage":"500MiB / 15.6GiB"}
{"Name":"lehigh-d10-solr-1","CPUPerc":"1.5%","MemUsage":"750MiB / 15.6GiB"}`

	applyDockerStats(&summary, output)

	if summary.CPUPercent != 4 {
		t.Fatalf("expected CPU percent 4, got %v", summary.CPUPercent)
	}
	if summary.MemoryBytes == 0 {
		t.Fatalf("expected memory usage to be aggregated")
	}
	if summary.MemoryLimitBytes == 0 {
		t.Fatalf("expected a memory limit to be detected")
	}
	if summary.MemoryLimitBytes > 20_000_000_000 {
		t.Fatalf("expected effective memory limit near host total, got %d", summary.MemoryLimitBytes)
	}
}
