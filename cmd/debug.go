package cmd

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"charm.land/lipgloss/v2"
	dockercontainer "github.com/docker/docker/api/types/container"
	"github.com/docker/docker/api/types/filters"
	dockerimage "github.com/docker/docker/api/types/image"
	"github.com/docker/docker/client"
	"github.com/kballard/go-shellquote"
	"github.com/libops/sitectl/pkg/config"
	"github.com/libops/sitectl/pkg/docker"
	"github.com/libops/sitectl/pkg/plugin"
	"github.com/spf13/cobra"
	"golang.org/x/term"
)

var debugOutputPath string
var debugVerbose bool

var ansiPattern = regexp.MustCompile(`\x1b\[[0-9;]*m`)

const (
	imageSizeWarningThreshold = int64(20 * 1024 * 1024 * 1024)
	dockerPruneDocsURL        = "https://docs.docker.com/engine/manage-resources/pruning/"
)

var (
	debugPanelStyle = lipgloss.NewStyle().
			Background(lipgloss.Color("#112235")).
			Padding(1, 2)
	debugTitleStyle = lipgloss.NewStyle().
			Bold(true).
			Foreground(lipgloss.Color("#98C1D9"))
	debugMutedStyle = lipgloss.NewStyle().
			Foreground(lipgloss.Color("#9FB3C8"))
	debugSectionDividerStyle = lipgloss.NewStyle().
					Foreground(lipgloss.Color("#29425E"))
	debugStatusOKStyle = lipgloss.NewStyle().
				Bold(true).
				Foreground(lipgloss.Color("#7BD389"))
	debugStatusWarningStyle = lipgloss.NewStyle().
				Bold(true).
				Foreground(lipgloss.Color("#F4C95D"))
	debugStatusFailedStyle = lipgloss.NewStyle().
				Bold(true).
				Foreground(lipgloss.Color("#F28482"))
	debugRowStyle = lipgloss.NewStyle().
			Background(lipgloss.Color("#112235"))
)

var debugCmd = &cobra.Command{
	Use:   "debug",
	Short: "Collect a text support bundle for the active context",
	RunE: func(cmd *cobra.Command, args []string) error {
		contextName, err := config.ResolveCurrentContextName(cmd.Flags())
		if err != nil {
			return err
		}
		ctx, err := config.GetContext(contextName)
		if err != nil {
			return err
		}

		var body strings.Builder
		body.WriteString(renderCoreDebug(ctx))

		if pluginName := strings.TrimSpace(ctx.Plugin); pluginName != "" && pluginName != "core" {
			pluginArgs := []string{"--context", contextName, "__debug"}
			if debugVerbose {
				pluginArgs = append(pluginArgs, "--verbose")
			}
			slog.Debug("handing off debug to plugin", "context", contextName, "plugin", pluginName, "args", pluginArgs)
			output, err := pluginSDK.InvokePluginCommand(pluginName, pluginArgs, plugin.CommandExecOptions{Capture: true})
			if err != nil {
				return err
			}
			slog.Debug("plugin debug completed", "context", contextName, "plugin", pluginName)
			if trimmed := strings.TrimSpace(output); trimmed != "" {
				body.WriteString("\n\n")
				body.WriteString(trimmed)
			}
		}

		if strings.TrimSpace(debugOutputPath) != "" {
			report := renderPlainDebugReport(body.String())
			if err := os.WriteFile(debugOutputPath, []byte(report+"\n"), 0o644); err != nil {
				return err
			}
			_, err = fmt.Fprintf(cmd.OutOrStdout(), "wrote debug bundle to %s\n", debugOutputPath)
			return err
		}

		_, err = fmt.Fprintln(cmd.OutOrStdout(), body.String())
		return err
	},
}

func init() {
	debugCmd.Flags().StringVarP(&debugOutputPath, "output", "o", "", "Write the debug report to a file instead of stdout")
	debugCmd.Flags().BoolVarP(&debugVerbose, "verbose", "v", false, "Include verbose diagnostic details")
	RootCmd.AddCommand(debugCmd)
}

func renderCoreDebug(ctx config.Context) string {
	slog.Debug("starting core debug", "context", ctx.Name, "docker_host_type", ctx.DockerHostType)
	meta := []debugRow{
		{Label: "Generated", Value: time.Now().UTC().Format(time.RFC3339)},
		{Label: "Context", Value: ctx.Name},
		{Label: "Plugin owner", Value: firstNonEmpty(ctx.Plugin, "core")},
		{Label: "Docker host type", Value: string(ctx.DockerHostType)},
		{Label: "Project dir", Value: ctx.ProjectDir},
	}
	if strings.TrimSpace(ctx.ProjectName) != "" {
		meta = append(meta, debugRow{Label: "Project name", Value: ctx.ProjectName})
	}
	if strings.TrimSpace(ctx.ComposeProjectName) != "" {
		meta = append(meta, debugRow{Label: "Compose project", Value: ctx.ComposeProjectName})
	}
	if strings.TrimSpace(ctx.DockerSocket) != "" {
		meta = append(meta, debugRow{Label: "Docker socket", Value: ctx.DockerSocket})
	}

	coreBody := []string{
		debugMutedStyle.Render("General Docker configuration and host-level diagnostics for this context."),
		"",
		debugDivider(),
		"",
		debugTitleStyle.Render("General"),
		"",
		formatDebugRows(meta),
	}
	slog.Debug("collecting log diagnostics", "context", ctx.Name)
	if diagnostics, err := collectLogDiagnostics(&ctx); err == nil {
		slog.Debug("collected log diagnostics", "context", ctx.Name, "containers", len(diagnostics.Containers), "known_size", diagnostics.KnownSize)
		coreBody = append(coreBody, "", debugDivider(), "", debugTitleStyle.Render("Log Summary"), "", formatDebugRows(logSummaryRows(diagnostics)))
		if debugVerbose {
			coreBody = append(coreBody, "", debugDivider(), "", debugTitleStyle.Render("Log Details"), "", renderLogDetailsBody(diagnostics))
		}
	} else {
		slog.Debug("log diagnostics failed", "context", ctx.Name, "error", err)
		coreBody = append(coreBody, "", debugDivider(), "", debugTitleStyle.Render("Log Summary"), "", formatDebugRows([]debugRow{
			{Label: "Log status", Value: renderStatus("warning")},
			{Label: "Log diagnostics", Value: err.Error()},
		}))
	}
	slog.Debug("collecting image diagnostics", "context", ctx.Name)
	if diagnostics, err := collectImageDiagnostics(&ctx); err == nil {
		slog.Debug("collected image diagnostics", "context", ctx.Name, "images", diagnostics.ImageCount, "total_bytes", diagnostics.TotalBytes)
		coreBody = append(coreBody, "", debugDivider(), "", debugTitleStyle.Render("Image Summary"), "", formatDebugRows(imageSummaryRows(diagnostics)))
	} else {
		slog.Debug("image diagnostics failed", "context", ctx.Name, "error", err)
		coreBody = append(coreBody, "", debugDivider(), "", debugTitleStyle.Render("Image Summary"), "", formatDebugRows([]debugRow{
			{Label: "Image status", Value: renderStatus("warning")},
			{Label: "Image diagnostics", Value: err.Error()},
		}))
	}
	slog.Debug("finished core debug", "context", ctx.Name)
	return renderDebugPanel("sitectl", strings.Join(coreBody, "\n"))
}

type logDiagnostics struct {
	TotalBytes          int64
	KnownSize           bool
	Containers          []containerLogDiagnostics
	UnboundedCount      int
	ExternalDriverCount int
}

type containerLogDiagnostics struct {
	Service      string
	Container    string
	Driver       string
	LogPath      string
	SizeBytes    int64
	HasSize      bool
	Rotated      bool
	External     bool
	RotationHint string
}

type imageDiagnostics struct {
	TotalBytes int64
	ImageCount int
}

func collectLogDiagnostics(ctxCfg *config.Context) (logDiagnostics, error) {
	slog.Debug("opening docker client for log diagnostics", "context", ctxCfg.Name)
	cli, err := docker.GetDockerCli(ctxCfg)
	if err != nil {
		return logDiagnostics{}, err
	}
	defer cli.Close()

	filterArgs := filters.NewArgs()
	filterArgs.Add("label", "com.docker.compose.project="+ctxCfg.EffectiveComposeProjectName())
	containers, err := cli.CLI.ContainerList(context.Background(), dockercontainer.ListOptions{
		All:     true,
		Filters: filterArgs,
	})
	if err != nil {
		return logDiagnostics{}, err
	}
	slog.Debug("listed containers for log diagnostics", "context", ctxCfg.Name, "count", len(containers))

	diagnostics := logDiagnostics{
		KnownSize:  true,
		Containers: make([]containerLogDiagnostics, 0, len(containers)),
	}
	remotePaths := make([]string, 0, len(containers))

	for _, summary := range containers {
		name := trimContainerName(summary.Names)
		service := firstNonEmpty(summary.Labels["com.docker.compose.service"], name)
		inspect, err := cli.CLI.ContainerInspect(context.Background(), name)
		if err != nil {
			return logDiagnostics{}, err
		}

		item := describeContainerLogs(service, name, inspect)
		if item.External {
			diagnostics.ExternalDriverCount++
		}
		if !item.Rotated && !item.External {
			diagnostics.UnboundedCount++
		}
		if item.LogPath != "" && ctxCfg.DockerHostType != config.ContextLocal {
			remotePaths = append(remotePaths, item.LogPath)
		}
		diagnostics.Containers = append(diagnostics.Containers, item)
	}

	if ctxCfg.DockerHostType == config.ContextLocal {
		for i := range diagnostics.Containers {
			item := &diagnostics.Containers[i]
			if item.LogPath == "" {
				diagnostics.KnownSize = false
				continue
			}
			size, hasSize, err := logFileSizeLocal(item.LogPath)
			if err != nil {
				item.RotationHint = appendHint(item.RotationHint, fmt.Sprintf("unable to stat log file: %v", err))
				diagnostics.KnownSize = false
				continue
			}
			item.SizeBytes = size
			item.HasSize = hasSize
			if hasSize {
				diagnostics.TotalBytes += size
			} else {
				diagnostics.KnownSize = false
			}
		}
	} else if len(remotePaths) > 0 {
		slog.Debug("collecting remote log file sizes", "context", ctxCfg.Name, "paths", len(remotePaths))
		sizes, err := logFileSizesRemote(ctxCfg, remotePaths)
		if err != nil {
			diagnostics.KnownSize = false
			for i := range diagnostics.Containers {
				if diagnostics.Containers[i].LogPath == "" {
					continue
				}
				diagnostics.Containers[i].RotationHint = appendHint(diagnostics.Containers[i].RotationHint, fmt.Sprintf("unable to stat log file: %v", err))
			}
		} else {
			for i := range diagnostics.Containers {
				item := &diagnostics.Containers[i]
				if item.LogPath != "" {
					size, ok := sizes[item.LogPath]
					if ok {
						item.SizeBytes = size
						item.HasSize = true
						diagnostics.TotalBytes += size
						continue
					}
				}
				diagnostics.KnownSize = false
			}
		}
	}

	sort.Slice(diagnostics.Containers, func(i, j int) bool {
		return diagnostics.Containers[i].Service < diagnostics.Containers[j].Service
	})

	return diagnostics, nil
}

func collectImageDiagnostics(ctxCfg *config.Context) (imageDiagnostics, error) {
	slog.Debug("opening docker client for image diagnostics", "context", ctxCfg.Name)
	cli, err := docker.GetDockerCli(ctxCfg)
	if err != nil {
		return imageDiagnostics{}, err
	}
	defer cli.Close()

	apiClient, ok := cli.CLI.(*client.Client)
	if !ok {
		return imageDiagnostics{}, fmt.Errorf("docker client does not support image listing")
	}

	images, err := apiClient.ImageList(context.Background(), dockerimage.ListOptions{All: true})
	if err != nil {
		return imageDiagnostics{}, err
	}
	slog.Debug("listed images", "context", ctxCfg.Name, "count", len(images))

	diagnostics := imageDiagnostics{ImageCount: len(images)}
	for _, image := range images {
		if image.Size > 0 {
			diagnostics.TotalBytes += image.Size
		}
	}

	return diagnostics, nil
}

func imageSummaryRows(diagnostics imageDiagnostics) []debugRow {
	state := "ok"
	rows := []debugRow{
		{Label: "Image status", Value: renderStatus(state)},
		{Label: "Total images", Value: humanBytes(diagnostics.TotalBytes)},
		{Label: "Image count", Value: strconv.Itoa(diagnostics.ImageCount)},
	}
	if diagnostics.TotalBytes >= imageSizeWarningThreshold {
		state = "warning"
		rows[0].Value = renderStatus(state)
		rows = append(rows,
			debugRow{Label: "Recommendation", Value: "run docker system prune -af periodically on development hosts"},
			debugRow{Label: "Docs", Value: dockerPruneDocsURL},
		)
	}
	return rows
}

func describeContainerLogs(service, containerName string, inspect dockercontainer.InspectResponse) containerLogDiagnostics {
	item := containerLogDiagnostics{
		Service:   service,
		Container: containerName,
		LogPath:   strings.TrimSpace(inspect.LogPath),
	}
	if inspect.HostConfig != nil {
		item.Driver = strings.TrimSpace(inspect.HostConfig.LogConfig.Type)
		item.Rotated, item.External, item.RotationHint = evaluateLogConfig(inspect.HostConfig.LogConfig.Type, inspect.HostConfig.LogConfig.Config)
	}
	if item.Driver == "" {
		item.Driver = "default"
	}
	return item
}

func evaluateLogConfig(driver string, options map[string]string) (rotated bool, external bool, hint string) {
	switch strings.TrimSpace(driver) {
	case "", "json-file", "local":
		maxSize := strings.TrimSpace(options["max-size"])
		maxFile := strings.TrimSpace(options["max-file"])
		if maxSize != "" && maxFile != "" {
			return true, false, fmt.Sprintf("rotation configured: max-size=%s max-file=%s", maxSize, maxFile)
		}
		if maxSize != "" {
			return true, false, fmt.Sprintf("rotation configured: max-size=%s", maxSize)
		}
		return false, false, "file-backed logs are not capped; set max-size and max-file"
	case "syslog", "journald", "gelf", "fluentd", "awslogs", "splunk", "gcplogs":
		return true, true, "logs ship to an external logging driver"
	default:
		if len(options) == 0 {
			return false, false, "custom log driver has no explicit rotation settings"
		}
		return true, true, "custom log driver configured"
	}
}

func logFileSizeLocal(path string) (int64, bool, error) {
	if strings.TrimSpace(path) == "" {
		return 0, false, nil
	}
	slog.Debug("logFileSizeLocal", "path", path)

	info, err := os.Stat(path)
	if err != nil {
		if os.IsNotExist(err) {
			return 0, false, nil
		}
		return 0, false, err
	}
	return info.Size(), true, nil
}

func logFileSizesRemote(ctxCfg *config.Context, paths []string) (map[string]int64, error) {
	uniquePaths := make([]string, 0, len(paths))
	seen := map[string]bool{}
	for _, path := range paths {
		path = strings.TrimSpace(path)
		if path == "" || seen[path] {
			continue
		}
		seen[path] = true
		uniquePaths = append(uniquePaths, path)
	}
	if len(uniquePaths) == 0 {
		return map[string]int64{}, nil
	}

	slog.Debug("dialing ssh for remote log sizes", "context", ctxCfg.Name, "paths", len(uniquePaths))
	client, err := ctxCfg.DialSSH()
	if err != nil {
		return nil, err
	}
	defer client.Close()

	session, err := client.NewSession()
	if err != nil {
		return nil, err
	}
	defer session.Close()

	parts := make([]string, 0, len(uniquePaths))
	for _, path := range uniquePaths {
		quoted := shellquote.Join(path)
		parts = append(parts, fmt.Sprintf("if test -f %s; then printf '%%s\\t' %s; stat -c %%s %s; fi", quoted, quoted, quoted))
	}
	cmd := strings.Join(parts, "; ")
	slog.Debug("running remote log size command", "context", ctxCfg.Name, "command", cmd)
	output, err := session.CombinedOutput(cmd)
	if err != nil {
		return nil, err
	}
	slog.Debug("completed remote log size command", "context", ctxCfg.Name)

	sizes := map[string]int64{}
	for _, line := range strings.Split(strings.TrimSpace(string(output)), "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		path, rawSize, ok := strings.Cut(line, "\t")
		if !ok {
			continue
		}
		size, err := strconv.ParseInt(strings.TrimSpace(rawSize), 10, 64)
		if err != nil {
			return nil, err
		}
		sizes[strings.TrimSpace(path)] = size
	}
	return sizes, nil
}

func logSummaryRows(diagnostics logDiagnostics) []debugRow {
	totalLine := "unknown"
	if diagnostics.KnownSize {
		totalLine = humanBytes(diagnostics.TotalBytes)
	}
	totalState := "ok"
	if !diagnostics.KnownSize {
		totalState = "warning"
	} else if diagnostics.TotalBytes >= 1<<30 {
		totalState = "warning"
	}

	logHandling := "file-backed container logs appear capped"
	if diagnostics.UnboundedCount == 0 {
		if diagnostics.ExternalDriverCount > 0 {
			logHandling = "logs are capped or shipped to an external driver"
		}
	} else {
		totalState = "failed"
		if diagnostics.UnboundedCount <= 2 {
			totalState = "warning"
		}
		logHandling = fmt.Sprintf("%d container(s) are using unbounded file-backed logs", diagnostics.UnboundedCount)
	}

	rows := []debugRow{
		{Label: "Log status", Value: renderStatus(totalState)},
		{Label: "Total logs", Value: totalLine},
		{Label: "Log handling", Value: logHandling},
	}
	if !diagnostics.KnownSize {
		rows = append(rows, debugRow{Label: "Note", Value: "unable to determine one or more container log file sizes"})
	} else if diagnostics.TotalBytes >= 1<<30 {
		rows = append(rows, debugRow{Label: "Note", Value: "aggregate container logs exceed 1 GiB"})
	}
	if diagnostics.UnboundedCount > 0 {
		rows = append(rows, debugRow{
			Label: "Recommendation",
			Value: `configure Docker log rotation with max-size and max-file, or ship logs to syslog, journald, or another central driver

https://docs.docker.com/engine/logging/configure/`})
	}
	return rows
}

func renderLogDetailsBody(diagnostics logDiagnostics) string {
	lines := []string{"Log details:"}
	for _, item := range diagnostics.Containers {
		line := fmt.Sprintf("  %s: driver=%s", item.Service, item.Driver)
		if item.HasSize {
			line += fmt.Sprintf(", size=%s", humanBytes(item.SizeBytes))
		}
		if item.External {
			line += ", external"
		} else if item.Rotated {
			line += ", rotated"
		} else {
			line += ", not rotated"
		}
		if item.RotationHint != "" {
			line += fmt.Sprintf(" (%s)", item.RotationHint)
		}
		lines = append(lines, line)
	}
	return strings.Join(lines, "\n")
}

type debugRow struct {
	Label string
	Value string
}

func renderDebugPanel(title, body string) string {
	header := debugTitleStyle.Render(strings.TrimSpace(title))
	content := header
	if strings.TrimSpace(body) != "" {
		content += "\n\n" + body
	}
	return debugPanelStyle.Width(debugPanelWidth()).Render(content)
}

func formatDebugRows(rows []debugRow) string {
	labelWidth := 0
	for _, row := range rows {
		if len(strings.TrimSpace(row.Label)) > labelWidth {
			labelWidth = len(strings.TrimSpace(row.Label))
		}
	}

	lines := make([]string, 0, len(rows))
	rowWidth := debugContentWidth()
	for _, row := range rows {
		label := strings.TrimSpace(row.Label)
		value := strings.TrimSpace(row.Value)
		if label == "" {
			lines = append(lines, renderDebugRow(rowWidth, "", value))
			continue
		}
		lines = append(lines, renderDebugRow(rowWidth, fmt.Sprintf("%-*s", labelWidth, label), value))
	}
	return strings.Join(lines, "\n")
}

func renderDebugRow(width int, label, value string) string {
	valueWidth := max(0, width-lipgloss.Width(label)-2)
	row := label
	if strings.TrimSpace(label) != "" {
		row += "  "
	}
	row += lipgloss.NewStyle().
		Width(valueWidth).
		Background(lipgloss.Color("#112235")).
		Render(value)
	return debugRowStyle.Width(width).Render(row)
}

func debugPanelWidth() int {
	if columns, err := strconv.Atoi(strings.TrimSpace(os.Getenv("COLUMNS"))); err == nil && columns > 0 {
		return max(40, columns)
	}
	if width, _, err := term.GetSize(int(os.Stdout.Fd())); err == nil && width > 0 {
		return max(40, width)
	}
	return 100
}

func debugContentWidth() int {
	return max(20, debugPanelWidth()-4)
}

func debugDivider() string {
	return debugSectionDividerStyle.Width(debugContentWidth()).Render(strings.Repeat("─", debugContentWidth()))
}

func renderStatus(state string) string {
	switch strings.ToLower(strings.TrimSpace(state)) {
	case "ok":
		return debugStatusOKStyle.Render("OK")
	case "warning":
		return debugStatusWarningStyle.Render("WARNING")
	case "failed":
		return debugStatusFailedStyle.Render("FAILED")
	default:
		return debugMutedStyle.Render(strings.ToUpper(strings.TrimSpace(state)))
	}
}

func humanBytes(size int64) string {
	const unit = 1024
	if size < unit {
		return fmt.Sprintf("%dB", size)
	}
	div, exp := int64(unit), 0
	for n := size / unit; n >= unit; n /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.1f%ciB", float64(size)/float64(div), "KMGTPE"[exp])
}

func trimContainerName(names []string) string {
	for _, name := range names {
		trimmed := strings.TrimPrefix(strings.TrimSpace(name), "/")
		if trimmed != "" {
			return trimmed
		}
	}
	return ""
}

func renderPlainDebugReport(value string) string {
	lines := strings.Split(ansiPattern.ReplaceAllString(value, ""), "\n")
	for i := range lines {
		lines[i] = strings.TrimRight(lines[i], " \t")
	}
	return strings.TrimSpace(strings.Join(lines, "\n"))
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return value
		}
	}
	return ""
}

func appendHint(current, next string) string {
	current = strings.TrimSpace(current)
	next = strings.TrimSpace(next)
	switch {
	case current == "":
		return next
	case next == "":
		return current
	default:
		return current + "; " + next
	}
}
