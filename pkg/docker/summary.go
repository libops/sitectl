package docker

import (
	"context"
	"encoding/json"
	"fmt"
	"os/exec"
	"sort"
	"strconv"
	"strings"
	"time"

	dockercontainer "github.com/docker/docker/api/types/container"
	"github.com/docker/docker/api/types/filters"
	"github.com/libops/sitectl/pkg/config"
	"github.com/libops/sitectl/pkg/helpers"
)

type ServiceSummary struct {
	Service          string
	Name             string
	State            string
	Status           string
	Healthy          bool
	CPUPercent       float64
	MemoryBytes      uint64
	MemoryLimitBytes uint64
}

type ProjectSummary struct {
	Running          int
	Total            int
	Healthy          int
	Stopped          int
	CPUPercent       float64
	MemoryBytes      uint64
	MemoryLimitBytes uint64
	HostLoad1        float64
	HostCPUCount     int
	DiskAvailable    uint64
	DiskTotal        uint64
	NetworkRXBytes   uint64
	NetworkTXBytes   uint64
	CollectedAt      time.Time
	Services         []ServiceSummary
	Status           string
}

func SummarizeProject(ctxCfg *config.Context) (ProjectSummary, error) {
	if ctxCfg == nil {
		return ProjectSummary{}, fmt.Errorf("context cannot be nil")
	}

	output, err := runComposePS(ctxCfg)
	if err == nil {
		summary, parseErr := parseComposePSOutput(output)
		if parseErr != nil {
			return ProjectSummary{}, parseErr
		}
		if statsOutput, statsErr := runDockerStats(ctxCfg); statsErr == nil {
			applyDockerStats(&summary, statsOutput)
		}
		if hostOutput, hostErr := runHostMetrics(ctxCfg); hostErr == nil {
			applyHostMetrics(&summary, hostOutput)
		}
		return summary, nil
	}

	cli, cliErr := GetDockerCli(ctxCfg)
	if cliErr != nil {
		return ProjectSummary{}, err
	}
	defer cli.Close()

	summary, fallbackErr := SummarizeProjectWithClient(context.Background(), cli.CLI, ctxCfg)
	if fallbackErr != nil {
		return ProjectSummary{}, err
	}
	if hostOutput, hostErr := runHostMetrics(ctxCfg); hostErr == nil {
		applyHostMetrics(&summary, hostOutput)
	}
	return summary, nil
}

func SummarizeProjectWithClient(ctx context.Context, cli DockerAPI, ctxCfg *config.Context) (ProjectSummary, error) {
	if ctxCfg == nil {
		return ProjectSummary{}, fmt.Errorf("context cannot be nil")
	}

	filterArgs := filters.NewArgs()
	filterArgs.Add("label", "com.docker.compose.project="+ctxCfg.EffectiveComposeProjectName())

	containers, err := cli.ContainerList(ctx, dockercontainer.ListOptions{
		All:     true,
		Filters: filterArgs,
	})
	if err != nil {
		return ProjectSummary{}, err
	}

	summary := ProjectSummary{
		Services: make([]ServiceSummary, 0, len(containers)),
	}

	for _, container := range containers {
		service := helpers.FirstNonEmpty(container.Labels["com.docker.compose.service"], TrimContainerName(container.Names))
		item := ServiceSummary{
			Service: service,
			Name:    TrimContainerName(container.Names),
			State:   container.State,
			Status:  container.Status,
			Healthy: strings.Contains(strings.ToLower(container.Status), "healthy"),
		}
		summary.Total++
		if container.State == "running" {
			summary.Running++
		} else {
			summary.Stopped++
		}
		if item.Healthy {
			summary.Healthy++
		}
		summary.Services = append(summary.Services, item)
	}

	finalizeSummary(&summary)
	return summary, nil
}

func runComposePS(ctxCfg *config.Context) (string, error) {
	args := composePSArgs(*ctxCfg)
	if ctxCfg.DockerHostType == config.ContextLocal {
		cmd := exec.Command("docker", args...) // #nosec G204 -- docker arguments are assembled by sitectl from context configuration without a shell.
		cmd.Dir = ctxCfg.ProjectDir
		output, err := cmd.CombinedOutput()
		return string(output), err
	}
	return ctxCfg.RunQuietCommand(exec.Command("docker", args...)) // #nosec G204 -- docker arguments are assembled by sitectl from context configuration without a shell.
}

func runDockerStats(ctxCfg *config.Context) (string, error) {
	args := []string{"stats", "--no-stream", "--format", "{{ json . }}"}
	if ctxCfg.DockerHostType == config.ContextLocal {
		cmd := exec.Command("docker", args...) // #nosec G204 -- docker stats arguments are fixed by sitectl without a shell.
		cmd.Dir = ctxCfg.ProjectDir
		output, err := cmd.CombinedOutput()
		return string(output), err
	}
	return ctxCfg.RunQuietCommand(exec.Command("docker", args...)) // #nosec G204 -- docker stats arguments are fixed by sitectl without a shell.
}

func runHostMetrics(ctxCfg *config.Context) (string, error) {
	if ctxCfg == nil {
		return "", fmt.Errorf("context cannot be nil")
	}

	var load1 string
	var cpuCount string
	var netRX uint64
	var netTX uint64
	collected := false
	accessor, accessorErr := ctxCfg.NewFileAccessor()
	if accessorErr == nil {
		defer accessor.Close()
		if data, err := accessor.ReadFile("/proc/loadavg"); err == nil {
			collected = true
			load1 = parseProcLoadAverage(string(data))
		}
		if data, err := accessor.ReadFile("/proc/cpuinfo"); err == nil {
			collected = true
			cpuCount = strconv.Itoa(parseProcCPUCount(string(data)))
			if cpuCount == "0" {
				cpuCount = ""
			}
		}
		if data, err := accessor.ReadFile("/proc/net/dev"); err == nil {
			collected = true
			netRX, netTX = parseProcNetworkTotals(string(data))
		}
	}

	if load1 == "" {
		if output, err := runHostCommand(ctxCfg, "uptime"); err == nil {
			collected = true
			load1 = parseUptimeLoadAverage(output)
		}
	}
	if cpuCount == "" {
		if output, err := runHostCommand(ctxCfg, "getconf", "_NPROCESSORS_ONLN"); err == nil {
			collected = true
			cpuCount = firstPositiveInteger(output)
		}
	}
	if cpuCount == "" {
		if output, err := runHostCommand(ctxCfg, "nproc"); err == nil {
			collected = true
			cpuCount = firstPositiveInteger(output)
		}
	}

	var diskTotalKB string
	var diskAvailKB string
	if output, err := runHostCommand(ctxCfg, "df", "-kP", "."); err == nil {
		collected = true
		diskTotalKB, diskAvailKB = parsePOSIXDiskUsage(output)
	}
	if !collected {
		return "", fmt.Errorf("host metric probes were unavailable")
	}

	payload, err := json.Marshal(hostMetricsPayload{
		Load1:       load1,
		CPUCount:    cpuCount,
		DiskTotalKB: diskTotalKB,
		DiskAvailKB: diskAvailKB,
		NetRXBytes:  strconv.FormatUint(netRX, 10),
		NetTXBytes:  strconv.FormatUint(netTX, 10),
	})
	if err != nil {
		return "", fmt.Errorf("encode host metrics: %w", err)
	}
	return string(payload), nil
}

func runHostCommand(ctxCfg *config.Context, name string, args ...string) (string, error) {
	command := exec.Command(name, args...) // #nosec G204 -- executable and arguments are fixed host metric probes selected by sitectl.
	command.Dir = ctxCfg.ProjectDir
	if ctxCfg.DockerHostType == config.ContextLocal {
		output, err := command.CombinedOutput()
		return string(output), err
	}
	return ctxCfg.RunQuietCommand(command)
}

func parseProcLoadAverage(output string) string {
	fields := strings.Fields(output)
	if len(fields) == 0 {
		return ""
	}
	if _, err := strconv.ParseFloat(fields[0], 64); err != nil {
		return ""
	}
	return fields[0]
}

func parseUptimeLoadAverage(output string) string {
	lower := strings.ToLower(output)
	marker := "load average:"
	index := strings.LastIndex(lower, marker)
	if index < 0 {
		marker = "load averages:"
		index = strings.LastIndex(lower, marker)
	}
	if index < 0 {
		return ""
	}
	values := strings.Fields(strings.NewReplacer(",", " ", ":", " ").Replace(output[index+len(marker):]))
	if len(values) == 0 {
		return ""
	}
	if _, err := strconv.ParseFloat(values[0], 64); err != nil {
		return ""
	}
	return values[0]
}

func parseProcCPUCount(output string) int {
	count := 0
	for _, line := range strings.Split(output, "\n") {
		parts := strings.SplitN(line, ":", 2)
		if len(parts) == 2 && strings.TrimSpace(parts[0]) == "processor" {
			count++
		}
	}
	return count
}

func firstPositiveInteger(output string) string {
	fields := strings.Fields(output)
	if len(fields) == 0 {
		return ""
	}
	value, err := strconv.Atoi(fields[0])
	if err != nil || value <= 0 {
		return ""
	}
	return strconv.Itoa(value)
}

func parsePOSIXDiskUsage(output string) (string, string) {
	lines := strings.Split(strings.TrimSpace(output), "\n")
	for index := len(lines) - 1; index >= 1; index-- {
		fields := strings.Fields(lines[index])
		if len(fields) < 4 {
			continue
		}
		if _, err := strconv.ParseUint(fields[1], 10, 64); err != nil {
			continue
		}
		if _, err := strconv.ParseUint(fields[3], 10, 64); err != nil {
			continue
		}
		return fields[1], fields[3]
	}
	return "", ""
}

func parseProcNetworkTotals(output string) (uint64, uint64) {
	var rxTotal uint64
	var txTotal uint64
	for _, line := range strings.Split(output, "\n") {
		parts := strings.SplitN(line, ":", 2)
		if len(parts) != 2 {
			continue
		}
		iface := strings.TrimSpace(parts[0])
		if ignoredHostNetworkInterface(iface) {
			continue
		}
		fields := strings.Fields(parts[1])
		if len(fields) < 9 {
			continue
		}
		rx, rxErr := strconv.ParseUint(fields[0], 10, 64)
		tx, txErr := strconv.ParseUint(fields[8], 10, 64)
		if rxErr == nil {
			rxTotal += rx
		}
		if txErr == nil {
			txTotal += tx
		}
	}
	return rxTotal, txTotal
}

func ignoredHostNetworkInterface(iface string) bool {
	if iface == "" || iface == "lo" {
		return true
	}
	for _, prefix := range []string{"docker", "veth", "br-", "virbr", "tailscale", "tun", "tap"} {
		if strings.HasPrefix(iface, prefix) {
			return true
		}
	}
	return false
}

func composePSArgs(ctxCfg config.Context) []string {
	args := []string{"compose"}
	for _, file := range ctxCfg.ComposeFile {
		args = append(args, "-f", file)
	}
	for _, env := range ctxCfg.EnvFile {
		args = append(args, "--env-file", env)
	}
	return append(args, "ps", "--all", "--format", "json")
}

func parseComposePSOutput(output string) (ProjectSummary, error) {
	trimmed := strings.TrimSpace(output)
	if trimmed == "" {
		return ProjectSummary{Status: "not running"}, nil
	}

	var payload any
	if err := json.Unmarshal([]byte(trimmed), &payload); err != nil {
		lines := strings.Split(trimmed, "\n")
		items := make([]any, 0, len(lines))
		for _, line := range lines {
			line = strings.TrimSpace(line)
			if line == "" {
				continue
			}
			var row any
			if lineErr := json.Unmarshal([]byte(line), &row); lineErr != nil {
				return ProjectSummary{}, err
			}
			items = append(items, row)
		}
		payload = items
	}

	rows, ok := payload.([]any)
	if !ok {
		return ProjectSummary{}, fmt.Errorf("unexpected docker compose ps payload")
	}

	summary := ProjectSummary{
		Services: make([]ServiceSummary, 0, len(rows)),
	}
	for _, raw := range rows {
		row, ok := raw.(map[string]any)
		if !ok {
			continue
		}
		item := ServiceSummary{
			Service: helpers.FirstNonEmpty(composeField(row, "service")),
			Name:    helpers.FirstNonEmpty(composeField(row, "name")),
			State:   strings.ToLower(helpers.FirstNonEmpty(composeField(row, "state"), "unknown")),
			Status:  helpers.FirstNonEmpty(composeField(row, "status")),
		}
		if item.Service == "" {
			item.Service = item.Name
		}
		item.Healthy = strings.Contains(strings.ToLower(helpers.FirstNonEmpty(composeField(row, "health"), item.Status)), "healthy")
		summary.Total++
		if item.State == "running" {
			summary.Running++
		} else {
			summary.Stopped++
		}
		if item.Healthy {
			summary.Healthy++
		}
		summary.Services = append(summary.Services, item)
	}

	finalizeSummary(&summary)
	return summary, nil
}

type dockerStatsRow struct {
	Name     string `json:"Name"`
	CPUPerc  string `json:"CPUPerc"`
	MemUsage string `json:"MemUsage"`
}

func applyDockerStats(summary *ProjectSummary, output string) {
	if summary == nil {
		return
	}
	serviceIndex := map[string]int{}
	containerIndex := map[string]int{}
	serviceNames := map[string]struct{}{}
	containerNames := map[string]struct{}{}
	for i, service := range summary.Services {
		if strings.TrimSpace(service.Service) != "" {
			serviceNames[service.Service] = struct{}{}
			serviceIndex[service.Service] = i
		}
		if strings.TrimSpace(service.Name) != "" {
			containerNames[service.Name] = struct{}{}
			containerIndex[service.Name] = i
		}
	}

	var maxLimit uint64
	for _, line := range strings.Split(strings.TrimSpace(output), "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		var row dockerStatsRow
		if err := json.Unmarshal([]byte(line), &row); err != nil {
			continue
		}
		if _, ok := containerNames[row.Name]; !ok {
			if _, ok := serviceNames[row.Name]; !ok {
				continue
			}
		}
		summary.CPUPercent += parsePercent(row.CPUPerc)
		used, limit := parseMemUsage(row.MemUsage)
		summary.MemoryBytes += used
		if limit > maxLimit {
			maxLimit = limit
		}
		if idx, ok := containerIndex[row.Name]; ok {
			summary.Services[idx].CPUPercent = parsePercent(row.CPUPerc)
			summary.Services[idx].MemoryBytes = used
			summary.Services[idx].MemoryLimitBytes = limit
		} else if idx, ok := serviceIndex[row.Name]; ok {
			summary.Services[idx].CPUPercent = parsePercent(row.CPUPerc)
			summary.Services[idx].MemoryBytes = used
			summary.Services[idx].MemoryLimitBytes = limit
		}
	}
	summary.MemoryLimitBytes = maxLimit
}

type hostMetricsPayload struct {
	Load1       string `json:"load1"`
	CPUCount    string `json:"cpu_count"`
	DiskTotalKB string `json:"disk_total_kb"`
	DiskAvailKB string `json:"disk_avail_kb"`
	NetRXBytes  string `json:"net_rx_bytes"`
	NetTXBytes  string `json:"net_tx_bytes"`
}

func applyHostMetrics(summary *ProjectSummary, output string) {
	if summary == nil {
		return
	}
	load1, cpuCount, diskAvailable, diskTotal, netRX, netTX := parseHostMetricsOutput(output)
	summary.HostLoad1 = load1
	summary.HostCPUCount = cpuCount
	summary.DiskAvailable = diskAvailable
	summary.DiskTotal = diskTotal
	summary.NetworkRXBytes = netRX
	summary.NetworkTXBytes = netTX
	summary.CollectedAt = time.Now()
}

func parseHostMetricsOutput(output string) (float64, int, uint64, uint64, uint64, uint64) {
	var payload hostMetricsPayload
	if err := json.Unmarshal([]byte(strings.TrimSpace(output)), &payload); err != nil {
		return 0, 0, 0, 0, 0, 0
	}
	load1, _ := strconv.ParseFloat(strings.TrimSpace(payload.Load1), 64)
	cpuCount, _ := strconv.Atoi(strings.TrimSpace(payload.CPUCount))
	diskTotal := parseUint(strings.TrimSpace(payload.DiskTotalKB)) * 1000
	diskAvailable := parseUint(strings.TrimSpace(payload.DiskAvailKB)) * 1000
	netRX := parseUint(strings.TrimSpace(payload.NetRXBytes))
	netTX := parseUint(strings.TrimSpace(payload.NetTXBytes))
	return load1, cpuCount, diskAvailable, diskTotal, netRX, netTX
}

func parseUint(value string) uint64 {
	if value == "" {
		return 0
	}
	parsed, err := strconv.ParseUint(value, 10, 64)
	if err != nil {
		return 0
	}
	return parsed
}

func parsePercent(value string) float64 {
	value = strings.TrimSpace(strings.TrimSuffix(value, "%"))
	if value == "" {
		return 0
	}
	parsed, err := strconv.ParseFloat(value, 64)
	if err != nil {
		return 0
	}
	return parsed
}

func parseMemUsage(value string) (uint64, uint64) {
	parts := strings.Split(value, "/")
	if len(parts) != 2 {
		return 0, 0
	}
	return parseHumanBytes(parts[0]), parseHumanBytes(parts[1])
}

func parseHumanBytes(value string) uint64 {
	value = strings.TrimSpace(strings.ToUpper(value))
	replacer := strings.NewReplacer("IB", "B", "I", "")
	value = replacer.Replace(value)
	multiplier := float64(1)
	switch {
	case strings.HasSuffix(value, "KB"):
		multiplier = 1000
		value = strings.TrimSuffix(value, "KB")
	case strings.HasSuffix(value, "MB"):
		multiplier = 1000 * 1000
		value = strings.TrimSuffix(value, "MB")
	case strings.HasSuffix(value, "GB"):
		multiplier = 1000 * 1000 * 1000
		value = strings.TrimSuffix(value, "GB")
	case strings.HasSuffix(value, "TB"):
		multiplier = 1000 * 1000 * 1000 * 1000
		value = strings.TrimSuffix(value, "TB")
	case strings.HasSuffix(value, "B"):
		value = strings.TrimSuffix(value, "B")
	}
	parsed, err := strconv.ParseFloat(strings.TrimSpace(value), 64)
	if err != nil {
		return 0
	}
	return uint64(parsed * multiplier)
}

func composeField(row map[string]any, key string) string {
	for candidate, value := range row {
		if strings.EqualFold(candidate, key) {
			if str, ok := value.(string); ok {
				return strings.TrimSpace(str)
			}
			return strings.TrimSpace(fmt.Sprint(value))
		}
	}
	return ""
}

func finalizeSummary(summary *ProjectSummary) {
	sort.Slice(summary.Services, func(i, j int) bool {
		if summary.Services[i].Service != summary.Services[j].Service {
			return summary.Services[i].Service < summary.Services[j].Service
		}
		return summary.Services[i].Name < summary.Services[j].Name
	})

	switch {
	case summary.Total == 0:
		summary.Status = "not running"
	case summary.Running == summary.Total:
		summary.Status = "running"
	case summary.Running > 0:
		summary.Status = "degraded"
	default:
		summary.Status = "stopped"
	}
}

func TrimContainerName(names []string) string {
	for _, name := range names {
		name = strings.TrimPrefix(strings.TrimSpace(name), "/")
		if name != "" {
			return name
		}
	}
	return ""
}
