package cmd

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"os"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"charm.land/lipgloss/v2"
	dockercontainer "github.com/docker/docker/api/types/container"
	"github.com/docker/docker/api/types/filters"
	dockerimage "github.com/docker/docker/api/types/image"
	"github.com/docker/docker/client"
	"github.com/libops/sitectl/internal/debugreport"
	"github.com/libops/sitectl/pkg/config"
	"github.com/libops/sitectl/pkg/docker"
	"github.com/libops/sitectl/pkg/helpers"
	"github.com/libops/sitectl/pkg/plugin"
	"github.com/spf13/cobra"
	"golang.org/x/term"
)

var debugOutputPath string
var debugVerbose bool
var debugProgressUIActive bool

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
		reporter := debugProgressReporter(nil)
		if stderrFile, ok := cmd.ErrOrStderr().(*os.File); ok && term.IsTerminal(int(stderrFile.Fd())) {
			report, err := runDebugCollectionWithProgress(cmd, contextName, ctx)
			if err != nil {
				return err
			}
			return writeDebugReport(cmd, report)
		}

		report, err := collectDebugReport(cmd.Context(), contextName, ctx, reporter)
		if err != nil {
			return err
		}
		return writeDebugReport(cmd, report)
	},
}

func writeDebugReport(cmd *cobra.Command, report string) error {
	if strings.TrimSpace(debugOutputPath) != "" {
		report = renderPlainDebugReport(report)
		if err := os.WriteFile(debugOutputPath, []byte(report+"\n"), 0o644); err != nil {
			return err
		}
		_, err := fmt.Fprintf(cmd.OutOrStdout(), "wrote debug bundle to %s\n", debugOutputPath)
		return err
	}

	_, err := fmt.Fprintln(cmd.OutOrStdout(), report)
	return err
}

func collectDebugReport(runCtx context.Context, contextName string, ctx config.Context, reporter debugProgressReporter) (string, error) {
	if err := runCtx.Err(); err != nil {
		return "", err
	}
	var body strings.Builder
	reportProgress(reporter, "Collecting Core Diagnostics", "Inspecting Docker configuration, logs, and images")
	body.WriteString(renderCoreDebug(runCtx, ctx))

	if pluginName := strings.TrimSpace(ctx.Plugin); pluginName != "" && pluginName != "core" {
		if err := runCtx.Err(); err != nil {
			return "", err
		}
		pluginArgs := []string{"--context", contextName, "__debug"}
		if debugVerbose {
			pluginArgs = append(pluginArgs, "--verbose")
		}
		reportProgress(reporter, "Collecting Plugin Diagnostics", fmt.Sprintf("Running %s debug collectors", pluginName))
		slog.Debug("handing off debug to plugin", "context", contextName, "plugin", pluginName, "args", pluginArgs)
		output, err := pluginSDK.InvokePluginCommand(pluginName, pluginArgs, plugin.CommandExecOptions{Context: runCtx, Capture: true, LiveStderr: !progressEnabled()})
		if err != nil {
			return "", err
		}
		slog.Debug("plugin debug completed", "context", contextName, "plugin", pluginName)
		if trimmed := strings.TrimSpace(output); trimmed != "" {
			body.WriteString("\n\n")
			body.WriteString(trimmed)
		}
	}

	return body.String(), nil
}

func init() {
	debugCmd.Flags().StringVarP(&debugOutputPath, "output", "o", "", "Write the debug report to a file instead of stdout")
	debugCmd.Flags().BoolVarP(&debugVerbose, "verbose", "v", false, "Include verbose diagnostic details")
	RootCmd.AddCommand(debugCmd)
}

func runDebugCollectionWithProgress(cmd *cobra.Command, contextName string, ctx config.Context) (string, error) {
	debugProgressUIActive = true
	defer func() { debugProgressUIActive = false }()
	progress := newDebugProgressLine(cmd.ErrOrStderr())
	defer progress.Close()
	return collectDebugReport(cmd.Context(), contextName, ctx, progress.Report)
}

func reportProgress(reporter debugProgressReporter, title, detail string) {
	if reporter != nil {
		reporter(title, detail)
	}
}

func progressEnabled() bool {
	return debugProgressUIActive
}

func renderCoreDebug(runCtx context.Context, ctx config.Context) string {
	slog.Debug("starting core debug", "context", ctx.Name, "docker_host_type", ctx.DockerHostType)
	meta := []debugRow{
		{Label: "Generated", Value: time.Now().UTC().Format(time.RFC3339)},
		{Label: "Context", Value: ctx.Name},
		{Label: "Plugin owner", Value: helpers.FirstNonEmpty(ctx.Plugin, "core")},
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
	var sharedSession *debugreport.Session
	if ctx.DockerHostType == config.ContextRemote {
		if session, err := debugreport.NewSession(&ctx); err != nil {
			slog.Debug("shared debug session setup failed", "context", ctx.Name, "error", err)
		} else {
			sharedSession = session
			defer sharedSession.Close()
		}
	}

	var hostDiagnostics debugreport.HostDiagnostics
	var composeDiagnostics debugreport.ComposeDiagnostics
	var logDiagnostics logDiagnostics
	var imageDiagnostics imageDiagnostics
	var logErr error
	var imageErr error

	if sharedSession != nil {
		hostDiagnostics = debugreport.CollectHostDiagnosticsWithSession(runCtx, &ctx, sharedSession)
		composeDiagnostics = debugreport.CollectComposeDiagnosticsWithSession(runCtx, &ctx, sharedSession)
		if cli, err := sharedSession.DockerClient(); err != nil {
			logErr = err
			imageErr = err
		} else {
			logDiagnostics, logErr = collectLogDiagnosticsWithClient(runCtx, &ctx, cli)
			imageDiagnostics, imageErr = collectImageDiagnosticsWithClient(runCtx, &ctx, cli)
		}
	} else {
		hostDiagnostics = debugreport.CollectHostDiagnostics(runCtx, &ctx)
		composeDiagnostics = debugreport.CollectComposeDiagnostics(runCtx, &ctx)
		logDiagnostics, logErr, imageDiagnostics, imageErr = collectCoreDockerDiagnostics(runCtx, &ctx)
	}
	coreBody = append(coreBody, "", debugDivider(), "", debugTitleStyle.Render("Host Resources"), "", formatDebugRows(hostSummaryRows(hostDiagnostics, ctx.ProjectDir)))
	coreBody = append(coreBody, "", debugDivider(), "", debugTitleStyle.Render("Compose Services"), "", formatDebugRows(composeSummaryRows(composeDiagnostics)))
	if logErr == nil {
		slog.Debug("collected log diagnostics", "context", ctx.Name, "containers", len(logDiagnostics.Containers))
		coreBody = append(coreBody, "", debugDivider(), "", debugTitleStyle.Render("Log Summary"), "", formatDebugRows(logSummaryRows(logDiagnostics)))
		if debugVerbose {
			coreBody = append(coreBody, "", debugDivider(), "", debugTitleStyle.Render("Log Details"), "", renderLogDetailsBody(logDiagnostics))
		}
	} else {
		slog.Debug("log diagnostics failed", "context", ctx.Name, "error", logErr)
		coreBody = append(coreBody, "", debugDivider(), "", debugTitleStyle.Render("Log Summary"), "", formatDebugRows([]debugRow{
			{Label: "Log status", Value: renderStatus("warning")},
			{Label: "Log diagnostics", Value: logErr.Error()},
		}))
	}
	if imageErr == nil {
		slog.Debug("collected image diagnostics", "context", ctx.Name, "images", imageDiagnostics.ImageCount, "total_bytes", imageDiagnostics.TotalBytes)
		coreBody = append(coreBody, "", debugDivider(), "", debugTitleStyle.Render("Image Summary"), "", formatDebugRows(imageSummaryRows(imageDiagnostics)))
	} else {
		slog.Debug("image diagnostics failed", "context", ctx.Name, "error", imageErr)
		coreBody = append(coreBody, "", debugDivider(), "", debugTitleStyle.Render("Image Summary"), "", formatDebugRows([]debugRow{
			{Label: "Image status", Value: renderStatus("warning")},
			{Label: "Image diagnostics", Value: imageErr.Error()},
		}))
	}
	slog.Debug("finished core debug", "context", ctx.Name)
	return renderDebugPanel("sitectl", strings.Join(coreBody, "\n"))
}

type logDiagnostics struct {
	Containers          []containerLogDiagnostics
	UnboundedCount      int
	ExternalDriverCount int
}

type containerLogDiagnostics struct {
	Service      string
	Container    string
	Driver       string
	Rotated      bool
	External     bool
	RotationHint string
}

type imageDiagnostics struct {
	TotalBytes int64
	ImageCount int
}

type debugProgressReporter func(title, detail string)

type debugProgressLine struct {
	out    *os.File
	frames []string
	index  int
	title  string
	detail string
	mu     sync.Mutex
	done   chan struct{}
	once   sync.Once
}

func newDebugProgressLine(w io.Writer) *debugProgressLine {
	file, ok := w.(*os.File)
	if !ok {
		return &debugProgressLine{frames: []string{".", "o", "O", "o"}}
	}
	progress := &debugProgressLine{
		out:    file,
		frames: []string{"-", "\\", "|", "/"},
		title:  "Preparing Debug Bundle",
		detail: "Starting diagnostic collection",
		done:   make(chan struct{}),
	}
	go progress.animate(120 * time.Millisecond)
	return progress
}

func (p *debugProgressLine) Report(title, detail string) {
	if p == nil {
		return
	}
	p.mu.Lock()
	p.title = strings.TrimSpace(title)
	p.detail = strings.TrimSpace(detail)
	p.renderLocked()
	p.mu.Unlock()
}

func (p *debugProgressLine) Close() {
	if p == nil || p.out == nil {
		return
	}
	p.once.Do(func() {
		close(p.done)
		p.mu.Lock()
		defer p.mu.Unlock()
		fmt.Fprint(p.out, "\r\033[2K")
	})
}

func (p *debugProgressLine) animate(interval time.Duration) {
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ticker.C:
			p.mu.Lock()
			p.renderLocked()
			p.mu.Unlock()
		case <-p.done:
			return
		}
	}
}

func (p *debugProgressLine) renderLocked() {
	if p.out == nil {
		return
	}
	frame := p.frames[p.index%len(p.frames)]
	p.index++
	line := fmt.Sprintf("\r%s %s", frame, strings.TrimSpace(strings.Join([]string{p.title, p.detail}, " - ")))
	fmt.Fprint(p.out, truncateDebugProgress(line))
}

func truncateDebugProgress(line string) string {
	width := debugPanelWidth()
	if width <= 0 {
		return line
	}
	plain := ansiPattern.ReplaceAllString(line, "")
	if lipgloss.Width(plain) <= width {
		return line
	}
	runes := []rune(plain)
	if len(runes) <= width {
		return string(runes)
	}
	if width <= 1 {
		return string(runes[:width])
	}
	return string(runes[:width-1]) + "…"
}

func collectLogDiagnosticsWithClient(runCtx context.Context, ctxCfg *config.Context, cli *docker.DockerClient) (logDiagnostics, error) {
	filterArgs := filters.NewArgs()
	filterArgs.Add("label", "com.docker.compose.project="+ctxCfg.EffectiveComposeProjectName())
	containers, err := cli.CLI.ContainerList(runCtx, dockercontainer.ListOptions{
		All:     true,
		Filters: filterArgs,
	})
	if err != nil {
		return logDiagnostics{}, err
	}
	slog.Debug("listed containers for log diagnostics", "context", ctxCfg.Name, "count", len(containers))

	diagnostics := logDiagnostics{
		Containers: make([]containerLogDiagnostics, 0, len(containers)),
	}

	for _, summary := range containers {
		if err := runCtx.Err(); err != nil {
			return logDiagnostics{}, err
		}
		name := docker.TrimContainerName(summary.Names)
		service := helpers.FirstNonEmpty(summary.Labels["com.docker.compose.service"], name)
		inspect, err := cli.CLI.ContainerInspect(runCtx, name)
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
		diagnostics.Containers = append(diagnostics.Containers, item)
	}

	sort.Slice(diagnostics.Containers, func(i, j int) bool {
		return diagnostics.Containers[i].Service < diagnostics.Containers[j].Service
	})

	return diagnostics, nil
}

func collectImageDiagnosticsWithClient(runCtx context.Context, ctxCfg *config.Context, cli *docker.DockerClient) (imageDiagnostics, error) {
	apiClient, ok := cli.CLI.(*client.Client)
	if !ok {
		return imageDiagnostics{}, fmt.Errorf("docker client does not support image listing")
	}

	images, err := apiClient.ImageList(runCtx, dockerimage.ListOptions{All: true})
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

func collectCoreDockerDiagnostics(runCtx context.Context, ctxCfg *config.Context) (logDiagnostics, error, imageDiagnostics, error) {
	slog.Debug("opening shared docker client for core diagnostics", "context", ctxCfg.Name)
	cli, err := docker.GetDockerCli(ctxCfg)
	if err != nil {
		return logDiagnostics{}, err, imageDiagnostics{}, err
	}
	defer cli.Close()

	slog.Debug("collecting log diagnostics", "context", ctxCfg.Name)
	logs, logErr := collectLogDiagnosticsWithClient(runCtx, ctxCfg, cli)
	slog.Debug("collecting image diagnostics", "context", ctxCfg.Name)
	images, imageErr := collectImageDiagnosticsWithClient(runCtx, ctxCfg, cli)
	return logs, logErr, images, imageErr
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

func hostSummaryRows(diagnostics debugreport.HostDiagnostics, projectDir string) []debugRow {
	status := "ok"
	if len(diagnostics.Issues) > 0 {
		status = "warning"
	}

	rows := []debugRow{
		{Label: "Host status", Value: renderStatus(status)},
		{Label: "CPUs", Value: renderDebugValue(intValueOrUnknown(diagnostics.CPUCount))},
		{Label: "Memory", Value: renderDebugValue(bytesValueOrUnknown(diagnostics.MemoryBytes))},
		{Label: "Swap", Value: renderDebugValue(bytesValueOrUnknown(diagnostics.SwapBytes))},
		{Label: "Available disk", Value: renderDebugValue(diskValueOrUnknown(diagnostics.DiskAvailableBytes, projectDir))},
		{Label: "OS version", Value: renderDebugValue(diagnostics.OSVersion)},
	}
	if len(diagnostics.Issues) > 0 {
		rows = append(rows, debugRow{Label: "Diagnostics", Value: strings.Join(diagnostics.Issues, "\n")})
	}
	return rows
}

func composeSummaryRows(diagnostics debugreport.ComposeDiagnostics) []debugRow {
	status := "ok"
	if len(diagnostics.Issues) > 0 {
		status = "warning"
	}

	rows := []debugRow{
		{Label: "Compose status", Value: renderStatus(status)},
		{Label: "Compose file", Value: renderDebugValue(diagnostics.ComposePath)},
	}
	if len(diagnostics.Services) == 0 {
		rows = append(rows, debugRow{Label: "Services", Value: "none found"})
	} else {
		for _, service := range diagnostics.Services {
			rows = append(rows, debugRow{Label: service.Service, Value: renderDebugValue(service.Image)})
		}
	}
	if len(diagnostics.BindMounts) > 0 {
		for _, mount := range diagnostics.BindMounts {
			value := mount.Target + " <- " + mount.Source
			if mount.Issue != "" {
				value += " (" + mount.Issue + ")"
			} else {
				value += ": " + humanBytes(mount.AvailableBytes) + " available"
			}
			rows = append(rows, debugRow{Label: "Bind mount", Value: value})
		}
	}
	if len(diagnostics.Issues) > 0 {
		rows = append(rows, debugRow{Label: "Diagnostics", Value: strings.Join(diagnostics.Issues, "\n")})
	}
	return rows
}

func renderDebugValue(value string) string {
	if strings.TrimSpace(value) == "" {
		return "unknown"
	}
	return value
}

func intValueOrUnknown(value int) string {
	if value <= 0 {
		return ""
	}
	return strconv.Itoa(value)
}

func bytesValueOrUnknown(value int64) string {
	if value < 0 {
		return ""
	}
	return humanBytes(value)
}

func diskValueOrUnknown(value int64, projectDir string) string {
	if value < 0 {
		return ""
	}
	if strings.TrimSpace(projectDir) == "" {
		return humanBytes(value)
	}
	return fmt.Sprintf("%s at %s", humanBytes(value), projectDir)
}

func describeContainerLogs(service, containerName string, inspect dockercontainer.InspectResponse) containerLogDiagnostics {
	item := containerLogDiagnostics{
		Service:   service,
		Container: containerName,
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

func logSummaryRows(diagnostics logDiagnostics) []debugRow {
	totalState := "ok"

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
		{Label: "Log handling", Value: logHandling},
	}
	if diagnostics.UnboundedCount > 0 {
		rows = append(rows, debugRow{
			Label: "Recommendation",
			Value: `for non-local environments, configure Docker log rotation with max-size and max-file, or ship logs to syslog, journald, or another central driver

https://docs.docker.com/engine/logging/configure/`})
	}
	return rows
}

func renderLogDetailsBody(diagnostics logDiagnostics) string {
	lines := []string{"Log details:"}
	for _, item := range diagnostics.Containers {
		line := fmt.Sprintf("  %s: driver=%s", item.Service, item.Driver)
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

func renderPlainDebugReport(value string) string {
	lines := strings.Split(ansiPattern.ReplaceAllString(value, ""), "\n")
	for i := range lines {
		lines[i] = strings.TrimRight(lines[i], " \t")
	}
	return strings.TrimSpace(strings.Join(lines, "\n"))
}
