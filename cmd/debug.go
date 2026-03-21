package cmd

import (
	"context"
	"fmt"
	"os"
	"sort"
	"strconv"
	"strings"
	"time"

	dockercontainer "github.com/docker/docker/api/types/container"
	"github.com/docker/docker/api/types/filters"
	"github.com/kballard/go-shellquote"
	corecomponent "github.com/libops/sitectl/pkg/component"
	"github.com/libops/sitectl/pkg/config"
	"github.com/libops/sitectl/pkg/docker"
	"github.com/libops/sitectl/pkg/plugin"
	"github.com/spf13/cobra"
)

var debugOutputPath string
var debugVerbose bool

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

		if strings.TrimSpace(ctx.Plugin) != "" {
			output, err := pluginSDK.InvokePluginCommand(ctx.Plugin, []string{"__debug"}, plugin.CommandExecOptions{Capture: true})
			if err != nil {
				return err
			}
			if trimmed := strings.TrimSpace(output); trimmed != "" {
				body.WriteString("\n\n")
				body.WriteString(trimmed)
			}
		}

		if strings.TrimSpace(debugOutputPath) != "" {
			if err := os.WriteFile(debugOutputPath, []byte(body.String()+"\n"), 0o644); err != nil {
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
	lines := []string{
		fmt.Sprintf("Generated: %s", time.Now().UTC().Format(time.RFC3339)),
		fmt.Sprintf("Context: %s", ctx.Name),
		fmt.Sprintf("Plugin owner: %s", firstNonEmpty(ctx.Plugin, "core")),
		fmt.Sprintf("Docker host type: %s", ctx.DockerHostType),
		fmt.Sprintf("Project dir: %s", ctx.ProjectDir),
	}
	if strings.TrimSpace(ctx.ProjectName) != "" {
		lines = append(lines, fmt.Sprintf("Project name: %s", ctx.ProjectName))
	}
	if strings.TrimSpace(ctx.ComposeProjectName) != "" {
		lines = append(lines, fmt.Sprintf("Compose project: %s", ctx.ComposeProjectName))
	}
	if strings.TrimSpace(ctx.DockerSocket) != "" {
		lines = append(lines, fmt.Sprintf("Docker socket: %s", ctx.DockerSocket))
	}
	if diagnostics, err := collectLogDiagnostics(&ctx); err == nil {
		lines = append(lines, renderLogDiagnostics(diagnostics)...)
	} else {
		lines = append(lines, fmt.Sprintf("Log diagnostics: unavailable (%v)", err))
	}
	return corecomponent.RenderSection("sitectl", strings.Join(lines, "\n"))
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

func collectLogDiagnostics(ctxCfg *config.Context) (logDiagnostics, error) {
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

	diagnostics := logDiagnostics{
		KnownSize:  true,
		Containers: make([]containerLogDiagnostics, 0, len(containers)),
	}

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
		if item.LogPath != "" {
			size, hasSize, err := logFileSize(ctxCfg, item.LogPath)
			if err != nil {
				item.RotationHint = appendHint(item.RotationHint, fmt.Sprintf("unable to stat log file: %v", err))
				diagnostics.KnownSize = false
			} else {
				item.SizeBytes = size
				item.HasSize = hasSize
				if hasSize {
					diagnostics.TotalBytes += size
				} else {
					diagnostics.KnownSize = false
				}
			}
		}
		diagnostics.Containers = append(diagnostics.Containers, item)
	}

	sort.Slice(diagnostics.Containers, func(i, j int) bool {
		return diagnostics.Containers[i].Service < diagnostics.Containers[j].Service
	})

	return diagnostics, nil
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

func logFileSize(ctxCfg *config.Context, path string) (int64, bool, error) {
	if strings.TrimSpace(path) == "" {
		return 0, false, nil
	}
	if ctxCfg.DockerHostType == config.ContextLocal {
		info, err := os.Stat(path)
		if err != nil {
			if os.IsNotExist(err) {
				return 0, false, nil
			}
			return 0, false, err
		}
		return info.Size(), true, nil
	}

	client, err := ctxCfg.DialSSH()
	if err != nil {
		return 0, false, err
	}
	defer client.Close()

	session, err := client.NewSession()
	if err != nil {
		return 0, false, err
	}
	defer session.Close()

	cmd := fmt.Sprintf("test -f %s && wc -c < %s || true", shellquote.Join(path), shellquote.Join(path))
	output, err := session.CombinedOutput(cmd)
	if err != nil {
		return 0, false, err
	}
	text := strings.TrimSpace(string(output))
	if text == "" {
		return 0, false, nil
	}
	size, err := strconv.ParseInt(text, 10, 64)
	if err != nil {
		return 0, false, err
	}
	return size, true, nil
}

func renderLogDiagnostics(diagnostics logDiagnostics) []string {
	lines := []string{}
	totalLine := "Total logs: unknown"
	if diagnostics.KnownSize {
		totalLine = fmt.Sprintf("Total logs: %s", humanBytes(diagnostics.TotalBytes))
	}
	lines = append(lines, totalLine)

	if diagnostics.UnboundedCount == 0 {
		if diagnostics.ExternalDriverCount > 0 {
			lines = append(lines, "Log handling: logs are capped or shipped to an external driver")
		} else {
			lines = append(lines, "Log handling: file-backed container logs appear capped")
		}
	} else {
		lines = append(lines, fmt.Sprintf("Log handling: %d container(s) are using unbounded file-backed logs", diagnostics.UnboundedCount))
		lines = append(lines, "Best practice: set Docker log rotation with max-size and max-file, or ship logs to syslog, journald, or another central driver.")
	}

	if debugVerbose {
		lines = append(lines, "", "Log details:")
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
	}

	return lines
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
