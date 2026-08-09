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

	summary, err := SummarizeProjectWithClient(context.Background(), fake, &config.Context{ComposeProjectName: "museum"})
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

func TestParseHostMetricsOutput(t *testing.T) {
	output := `{"load1":"1.25","cpu_count":"8","disk_total_kb":"1000000","disk_avail_kb":"250000","net_rx_bytes":"123456","net_tx_bytes":"654321"}`

	load1, cpuCount, diskAvailable, diskTotal, netRX, netTX := parseHostMetricsOutput(output)

	if load1 != 1.25 {
		t.Fatalf("expected load1 1.25, got %v", load1)
	}
	if cpuCount != 8 {
		t.Fatalf("expected cpu count 8, got %d", cpuCount)
	}
	if diskAvailable != 250000000 {
		t.Fatalf("expected disk available 250000000, got %d", diskAvailable)
	}
	if diskTotal != 1000000000 {
		t.Fatalf("expected disk total 1000000000, got %d", diskTotal)
	}
	if netRX != 123456 {
		t.Fatalf("expected network rx 123456, got %d", netRX)
	}
	if netTX != 654321 {
		t.Fatalf("expected network tx 654321, got %d", netTX)
	}
}

func TestHostMetricParsersDoNotRequireShellPipelines(t *testing.T) {
	if got := parseProcLoadAverage("1.75 1.00 0.50 1/100 123\n"); got != "1.75" {
		t.Fatalf("parseProcLoadAverage() = %q, want 1.75", got)
	}
	if got := parseUptimeLoadAverage("10:30 up 4 days, load averages: 2.50 2.00 1.50"); got != "2.50" {
		t.Fatalf("parseUptimeLoadAverage() = %q, want 2.50", got)
	}
	if got := parseProcCPUCount("processor : 0\nmodel name : first\nprocessor : 1\n"); got != 2 {
		t.Fatalf("parseProcCPUCount() = %d, want 2", got)
	}
	total, available := parsePOSIXDiskUsage("Filesystem 1024-blocks Used Available Capacity Mounted on\n/dev/sda 1000 250 750 25% /\n")
	if total != "1000" || available != "750" {
		t.Fatalf("parsePOSIXDiskUsage() = (%q, %q), want (1000, 750)", total, available)
	}
}

func TestParseProcNetworkTotalsExcludesVirtualInterfaces(t *testing.T) {
	output := `Inter-| Receive | Transmit
 face |bytes packets errs drop fifo frame compressed multicast|bytes packets errs drop fifo colls carrier compressed
    lo: 100 1 0 0 0 0 0 0 200 2 0 0 0 0 0 0
  eth0: 300 3 0 0 0 0 0 0 400 4 0 0 0 0 0 0
docker0: 500 5 0 0 0 0 0 0 600 6 0 0 0 0 0 0
  wlan0: 700 7 0 0 0 0 0 0 800 8 0 0 0 0 0 0`

	rx, tx := parseProcNetworkTotals(output)
	if rx != 1000 || tx != 1200 {
		t.Fatalf("parseProcNetworkTotals() = (%d, %d), want (1000, 1200)", rx, tx)
	}
}
