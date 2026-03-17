package docker

import (
	"context"
	"encoding/json"
	"fmt"
	"os/exec"
	"sort"
	"strconv"
	"strings"

	dockercontainer "github.com/docker/docker/api/types/container"
	"github.com/docker/docker/api/types/filters"
	"github.com/kballard/go-shellquote"
	"github.com/libops/sitectl/pkg/config"
	"golang.org/x/crypto/ssh"
)

type ServiceSummary struct {
	Service string
	Name    string
	State   string
	Status  string
	Healthy bool
}

type ProjectSummary struct {
	Running          int
	Total            int
	Healthy          int
	Stopped          int
	CPUPercent       float64
	MemoryBytes      uint64
	MemoryLimitBytes uint64
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
		service := firstNonEmpty(container.Labels["com.docker.compose.service"], trimContainerName(container.Names))
		item := ServiceSummary{
			Service: service,
			Name:    trimContainerName(container.Names),
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
		cmd := exec.Command("docker", args...)
		cmd.Dir = ctxCfg.ProjectDir
		output, err := cmd.CombinedOutput()
		return string(output), err
	}

	remoteCmd := fmt.Sprintf("cd %s && ", shellquote.Join(ctxCfg.ProjectDir))
	if ctxCfg.RunSudo {
		remoteCmd += "sudo "
	}
	remoteCmd += shellquote.Join(append([]string{"docker"}, args...)...)

	client, err := ctxCfg.DialSSH()
	if err != nil {
		return "", err
	}
	defer client.Close()

	session, err := client.NewSession()
	if err != nil {
		return "", err
	}
	defer session.Close()

	output, err := session.CombinedOutput(remoteCmd)
	if err != nil {
		if _, ok := err.(*ssh.ExitError); ok && len(output) > 0 {
			return string(output), nil
		}
		return string(output), err
	}
	return string(output), nil
}

func runDockerStats(ctxCfg *config.Context) (string, error) {
	args := []string{"stats", "--no-stream", "--format", "{{ json . }}"}
	if ctxCfg.DockerHostType == config.ContextLocal {
		cmd := exec.Command("docker", args...)
		cmd.Dir = ctxCfg.ProjectDir
		output, err := cmd.CombinedOutput()
		return string(output), err
	}

	remoteCmd := fmt.Sprintf("cd %s && ", shellquote.Join(ctxCfg.ProjectDir))
	if ctxCfg.RunSudo {
		remoteCmd += "sudo "
	}
	remoteCmd += shellquote.Join(append([]string{"docker"}, args...)...)

	client, err := ctxCfg.DialSSH()
	if err != nil {
		return "", err
	}
	defer client.Close()

	session, err := client.NewSession()
	if err != nil {
		return "", err
	}
	defer session.Close()

	output, err := session.CombinedOutput(remoteCmd)
	if err != nil {
		if _, ok := err.(*ssh.ExitError); ok && len(output) > 0 {
			return string(output), nil
		}
		return string(output), err
	}
	return string(output), nil
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
			Service: firstNonEmpty(composeField(row, "service")),
			Name:    firstNonEmpty(composeField(row, "name")),
			State:   strings.ToLower(firstNonEmpty(composeField(row, "state"), "unknown")),
			Status:  firstNonEmpty(composeField(row, "status")),
		}
		if item.Service == "" {
			item.Service = item.Name
		}
		item.Healthy = strings.Contains(strings.ToLower(firstNonEmpty(composeField(row, "health"), item.Status)), "healthy")
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
	serviceNames := map[string]struct{}{}
	containerNames := map[string]struct{}{}
	for _, service := range summary.Services {
		if strings.TrimSpace(service.Service) != "" {
			serviceNames[service.Service] = struct{}{}
		}
		if strings.TrimSpace(service.Name) != "" {
			containerNames[service.Name] = struct{}{}
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
	}
	summary.MemoryLimitBytes = maxLimit
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

func trimContainerName(names []string) string {
	for _, name := range names {
		name = strings.TrimPrefix(strings.TrimSpace(name), "/")
		if name != "" {
			return name
		}
	}
	return ""
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return value
		}
	}
	return ""
}
