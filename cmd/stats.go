package cmd

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/libops/sitectl/pkg/config"
	"github.com/libops/sitectl/pkg/docker"
	"github.com/libops/sitectl/pkg/healthcheck"
	"github.com/libops/sitectl/pkg/plugin"
	coretraefik "github.com/libops/sitectl/pkg/services/traefik"
	"github.com/spf13/cobra"
)

type statsOptions struct {
	Path   string
	Format string
}

type statsResolvedContext struct {
	Context     *config.Context
	ContextName string
	FromPath    bool
}

type statsReport struct {
	Context    statsContextReport    `json:"context"`
	Ingress    statsIngressReport    `json:"ingress"`
	Containers statsContainersReport `json:"containers"`
}

type statsContextReport struct {
	Name           string `json:"name,omitempty"`
	Plugin         string `json:"plugin,omitempty"`
	Environment    string `json:"environment,omitempty"`
	ProjectDir     string `json:"project_dir,omitempty"`
	ComposeProject string `json:"compose_project,omitempty"`
}

type statsIngressReport struct {
	Status              string              `json:"status"`
	PublicURL           string              `json:"public_url,omitempty"`
	Domain              string              `json:"domain,omitempty"`
	Routes              []statsIngressRoute `json:"routes,omitempty"`
	Ports               []statsIngressPort  `json:"ports,omitempty"`
	TrustedProxies      string              `json:"trusted_proxies,omitempty"`
	TrustedProxiesError string              `json:"trusted_proxies_error,omitempty"`
	RoutesError         string              `json:"routes_error,omitempty"`
}

type statsIngressRoute struct {
	Name           string `json:"name"`
	URL            string `json:"url,omitempty"`
	Scheme         string `json:"scheme,omitempty"`
	Domain         string `json:"domain,omitempty"`
	Service        string `json:"service,omitempty"`
	Router         string `json:"router,omitempty"`
	TraefikService string `json:"traefik_service,omitempty"`
	Status         string `json:"status"`
	Error          string `json:"error,omitempty"`
	Primary        bool   `json:"primary,omitempty"`
}

type statsIngressPort struct {
	Name          string `json:"name"`
	ContainerPort int    `json:"container_port"`
	HostPort      int    `json:"host_port,omitempty"`
	State         string `json:"state"`
	Running       bool   `json:"running,omitempty"`
	WouldPublish  bool   `json:"would_publish,omitempty"`
}

type statsContainersReport struct {
	Status           string                  `json:"status"`
	Running          int                     `json:"running"`
	Total            int                     `json:"total"`
	Healthy          int                     `json:"healthy"`
	Stopped          int                     `json:"stopped"`
	CPUPercent       float64                 `json:"cpu_percent"`
	MemoryBytes      uint64                  `json:"memory_bytes"`
	MemoryLimitBytes uint64                  `json:"memory_limit_bytes,omitempty"`
	HostLoad1        float64                 `json:"host_load_1,omitempty"`
	HostCPUCount     int                     `json:"host_cpu_count,omitempty"`
	DiskAvailable    uint64                  `json:"disk_available_bytes,omitempty"`
	DiskTotal        uint64                  `json:"disk_total_bytes,omitempty"`
	NetworkRXBytes   uint64                  `json:"network_rx_bytes,omitempty"`
	NetworkTXBytes   uint64                  `json:"network_tx_bytes,omitempty"`
	CollectedAt      time.Time               `json:"collected_at,omitempty"`
	Services         []statsContainerService `json:"services,omitempty"`
}

type statsContainerService struct {
	Service          string  `json:"service"`
	Name             string  `json:"name,omitempty"`
	State            string  `json:"state"`
	Status           string  `json:"status,omitempty"`
	Healthy          bool    `json:"healthy,omitempty"`
	CPUPercent       float64 `json:"cpu_percent"`
	MemoryBytes      uint64  `json:"memory_bytes"`
	MemoryLimitBytes uint64  `json:"memory_limit_bytes,omitempty"`
}

var statsFlags statsOptions

var statsCmd = &cobra.Command{
	Use:   "stats",
	Short: "Show ingress and container runtime stats for the active context",
	RunE: func(cmd *cobra.Command, args []string) error {
		if format := strings.TrimSpace(statsFlags.Format); format != "" && !strings.EqualFold(format, "json") {
			return fmt.Errorf("unsupported stats format %q; only json is supported", statsFlags.Format)
		}
		resolved, err := resolveStatsContext(cmd, statsFlags.Path)
		if err != nil {
			return err
		}
		summary, err := docker.SummarizeProject(resolved.Context)
		if err != nil {
			return err
		}
		return writeStatsReport(cmd, buildStatsReport(cmd.Context(), resolved, summary))
	},
}

func init() {
	statsCmd.Flags().StringVar(&statsFlags.Path, "path", "", "Project path override")
	statsCmd.Flags().StringVar(&statsFlags.Format, "format", "json", "Output format. Only json is supported.")
	statsCmd.GroupID = "ops"
	RootCmd.AddCommand(statsCmd)
}

func resolveStatsContext(cmd *cobra.Command, path string) (statsResolvedContext, error) {
	path = strings.TrimSpace(path)
	if path != "" {
		abs, err := filepath.Abs(path)
		if err != nil {
			return statsResolvedContext{}, err
		}
		pluginName := ""
		if claim, err := plugin.DetectProjectOwner(abs, ""); err == nil && claim != nil {
			pluginName = strings.TrimSpace(claim.Plugin)
		}
		ctx, err := config.NewLocalProjectContext(abs, pluginName)
		if err != nil {
			return statsResolvedContext{}, err
		}
		return statsResolvedContext{Context: ctx, ContextName: ctx.Name, FromPath: true}, nil
	}
	contextName, err := resolveContextName(cmd)
	if err != nil {
		return statsResolvedContext{}, err
	}
	ctx, err := config.GetContext(contextName)
	if err != nil {
		return statsResolvedContext{}, err
	}
	return statsResolvedContext{Context: &ctx, ContextName: contextName}, nil
}

func writeStatsReport(cmd *cobra.Command, report statsReport) error {
	data, err := json.MarshalIndent(report, "", "  ")
	if err != nil {
		return err
	}
	_, err = fmt.Fprintln(cmd.OutOrStdout(), string(data))
	return err
}

func buildStatsReport(runCtx context.Context, resolved statsResolvedContext, summary docker.ProjectSummary) statsReport {
	ctx := resolved.Context
	return statsReport{
		Context: statsContextFromContext(ctx),
		Ingress: buildIngressStats(runCtx, resolved),
		Containers: statsContainersReport{
			Status:           summary.Status,
			Running:          summary.Running,
			Total:            summary.Total,
			Healthy:          summary.Healthy,
			Stopped:          summary.Stopped,
			CPUPercent:       summary.CPUPercent,
			MemoryBytes:      summary.MemoryBytes,
			MemoryLimitBytes: summary.MemoryLimitBytes,
			HostLoad1:        summary.HostLoad1,
			HostCPUCount:     summary.HostCPUCount,
			DiskAvailable:    summary.DiskAvailable,
			DiskTotal:        summary.DiskTotal,
			NetworkRXBytes:   summary.NetworkRXBytes,
			NetworkTXBytes:   summary.NetworkTXBytes,
			CollectedAt:      summary.CollectedAt,
			Services:         statsContainerServices(summary.Services),
		},
	}
}

func statsContextFromContext(ctx *config.Context) statsContextReport {
	if ctx == nil {
		return statsContextReport{}
	}
	return statsContextReport{
		Name:           ctx.Name,
		Plugin:         ctx.Plugin,
		Environment:    ctx.Environment,
		ProjectDir:     ctx.ProjectDir,
		ComposeProject: ctx.EffectiveComposeProjectName(),
	}
}

func statsContainerServices(services []docker.ServiceSummary) []statsContainerService {
	out := make([]statsContainerService, 0, len(services))
	for _, service := range services {
		out = append(out, statsContainerService{
			Service:          service.Service,
			Name:             service.Name,
			State:            service.State,
			Status:           service.Status,
			Healthy:          service.Healthy,
			CPUPercent:       service.CPUPercent,
			MemoryBytes:      service.MemoryBytes,
			MemoryLimitBytes: service.MemoryLimitBytes,
		})
	}
	return out
}

func buildIngressStats(runCtx context.Context, resolved statsResolvedContext) statsIngressReport {
	ctx := resolved.Context
	report := statsIngressReport{
		Status: "ok",
		Ports: []statsIngressPort{
			ingressPortStatus(runCtx, ctx, "http", 80),
			ingressPortStatus(runCtx, ctx, "https", 443),
		},
	}
	if ctx == nil {
		report.Status = "unknown"
		return report
	}

	routes, routeErr := pluginIngressRoutes(runCtx, resolved)
	if routeErr != nil {
		report.Status = "warning"
		report.RoutesError = routeErr.Error()
	}
	if len(routes.Routes) == 0 {
		routes = defaultStatsIngressRoutes(ctx)
	}
	report.Routes = make([]statsIngressRoute, 0, len(routes.Routes))
	for i, route := range routes.Routes {
		route.DefaultScheme = firstStatsValue(route.DefaultScheme, routes.Scheme, "http")
		route.DefaultDomain = firstStatsValue(route.DefaultDomain, routes.Domain, "localhost")
		route.Primary = route.Primary || i == 0
		resolvedRoute := resolveStatsIngressRoute(runCtx, ctx, route)
		if resolvedRoute.Error != "" {
			report.Status = "warning"
		}
		if resolvedRoute.Primary && report.PublicURL == "" {
			report.PublicURL = resolvedRoute.URL
			report.Domain = resolvedRoute.Domain
		}
		report.Routes = append(report.Routes, resolvedRoute)
	}

	if inspection, err := coretraefik.InspectIngress(ctx); err == nil {
		report.TrustedProxies = formatStatsTrustedProxies(inspection.Traefik)
	} else {
		report.Status = "warning"
		report.TrustedProxiesError = err.Error()
	}

	return report
}

func formatStatsTrustedProxies(values map[string][]string) string {
	if len(values) == 0 {
		return "none"
	}
	entrypoints := make([]string, 0, len(values))
	for entrypoint := range values {
		entrypoint = strings.TrimSpace(entrypoint)
		if entrypoint != "" {
			entrypoints = append(entrypoints, entrypoint)
		}
	}
	sort.Strings(entrypoints)
	parts := make([]string, 0, len(entrypoints))
	for _, entrypoint := range entrypoints {
		trusted := strings.Join(values[entrypoint], ",")
		if strings.TrimSpace(trusted) == "" {
			continue
		}
		parts = append(parts, entrypoint+"="+trusted)
	}
	if len(parts) == 0 {
		return "none"
	}
	return strings.Join(parts, "; ")
}

func pluginIngressRoutes(runCtx context.Context, resolved statsResolvedContext) (plugin.IngressRoutes, error) {
	ctx := resolved.Context
	if ctx == nil {
		return plugin.IngressRoutes{}, nil
	}
	pluginName := strings.TrimSpace(ctx.Plugin)
	if pluginName == "" || pluginName == "core" {
		return plugin.IngressRoutes{}, nil
	}
	supported, err := pluginSupportsIngressRoutes(pluginName)
	if err != nil {
		return plugin.IngressRoutes{}, err
	}
	if !supported {
		return plugin.IngressRoutes{}, nil
	}
	params := plugin.IngressRoutesParams{}
	if resolved.FromPath && ctx.DockerHostType == config.ContextLocal {
		params.Path = ctx.ProjectDir
	}
	req, err := plugin.NewIngressRoutesRequest(params)
	if err != nil {
		return plugin.IngressRoutes{}, err
	}
	if !resolved.FromPath {
		req.Context = resolved.ContextName
	}
	resp, err := pluginSDK.InvokePluginRPC(pluginName, req, plugin.CommandExecOptions{Context: runCtx})
	if err != nil {
		if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
			return plugin.IngressRoutes{}, err
		}
		if plugin.IsRPCErrorCode(err, plugin.RPCErrorCodeUnsupportedMethod) || plugin.IsRPCErrorCode(err, plugin.RPCErrorCodeNotRegistered) {
			return plugin.IngressRoutes{}, nil
		}
		return plugin.IngressRoutes{}, err
	}
	if len(resp.Result) == 0 {
		return plugin.IngressRoutes{}, nil
	}
	return plugin.DecodeRPCResult[plugin.IngressRoutes](resp)
}

func defaultStatsIngressRoutes(ctx *config.Context) plugin.IngressRoutes {
	return plugin.IngressRoutes{Routes: []plugin.IngressRoute{{
		Name:          "app",
		Service:       firstStatsValue(defaultIngressAppService(ctx), "app"),
		DefaultScheme: "http",
		DefaultDomain: "localhost",
		Primary:       true,
	}}}
}

func resolveStatsIngressRoute(runCtx context.Context, ctx *config.Context, route plugin.IngressRoute) statsIngressRoute {
	out := statsIngressRoute{
		Name:           firstStatsValue(route.Name, "app"),
		Service:        route.Service,
		Router:         route.Router,
		TraefikService: route.TraefikService,
		Primary:        route.Primary,
		Status:         "resolved",
	}
	resolved, ok, err := healthcheck.PublicURLFromTraefik(ctx, healthcheck.TraefikRouteOptions{
		AppService:     route.Service,
		Router:         route.Router,
		TraefikService: route.TraefikService,
		DefaultScheme:  route.DefaultScheme,
		DefaultDomain:  route.DefaultDomain,
	})
	if err != nil {
		out.Status = "error"
		out.Error = err.Error()
		resolved = fallbackStatsRouteURL(route)
	} else if !ok {
		out.Status = "fallback"
		resolved = fallbackStatsRouteURL(route)
	} else {
		resolved = withStatsRoutePath(resolved, route.Path)
	}
	if withRuntimePort := publicURLWithRunningHostPort(runCtx, ctx, resolved); withRuntimePort != "" {
		resolved = withRuntimePort
	}
	out.URL = statsPublicURL(resolved)
	out.Scheme, out.Domain = splitStatsURL(out.URL)
	return out
}

func withStatsRoutePath(value, routePath string) string {
	routePath = strings.TrimSpace(routePath)
	if routePath == "" {
		return value
	}
	parsed, err := url.Parse(strings.TrimSpace(value))
	if err != nil {
		return value
	}
	if !strings.HasPrefix(routePath, "/") {
		routePath = "/" + strings.TrimLeft(routePath, "/")
	}
	if parsed.Path == "" || parsed.Path == "/" {
		parsed.Path = routePath
	}
	return parsed.String()
}

func fallbackStatsRouteURL(route plugin.IngressRoute) string {
	scheme := firstStatsValue(route.DefaultScheme, "http")
	domain := firstStatsValue(route.DefaultDomain, "localhost")
	path := strings.TrimSpace(route.Path)
	if !strings.HasPrefix(path, "/") {
		path = "/" + strings.TrimLeft(path, "/")
	}
	if path == "/" {
		path = ""
	}
	return (&url.URL{Scheme: scheme, Host: domain, Path: path}).String()
}

func splitStatsURL(value string) (string, string) {
	parsed, err := url.Parse(strings.TrimSpace(value))
	if err != nil {
		return "", ""
	}
	return parsed.Scheme, parsed.Hostname()
}

func statsPublicURL(value string) string {
	parsed, err := url.Parse(strings.TrimSpace(value))
	if err != nil || parsed.Path != "/" || parsed.RawQuery != "" || parsed.Fragment != "" {
		return strings.TrimSpace(value)
	}
	parsed.Path = ""
	return parsed.String()
}

func publicURLWithRunningHostPort(runCtx context.Context, ctx *config.Context, value string) string {
	parsed, err := url.Parse(strings.TrimSpace(value))
	if err != nil || parsed.Scheme == "" || parsed.Host == "" {
		return ""
	}
	target := 80
	if parsed.Scheme == "https" {
		target = 443
	}
	port, ok, err := runningTraefikHostPort(runCtx, ctx, target)
	if err != nil || !ok {
		return ""
	}
	if isDefaultPublicURLPort(parsed.Scheme, port) {
		parsed.Host = parsed.Hostname()
		return parsed.String()
	}
	parsed.Host = parsed.Hostname() + ":" + strconv.Itoa(port)
	return parsed.String()
}

func isDefaultPublicURLPort(scheme string, port int) bool {
	return (scheme == "http" && port == 80) || (scheme == "https" && port == 443)
}

func ingressPortStatus(runCtx context.Context, ctx *config.Context, name string, target int) statsIngressPort {
	out := statsIngressPort{
		Name:          name,
		ContainerPort: target,
		State:         "unpublished",
	}
	if port, ok, err := runningTraefikHostPort(runCtx, ctx, target); err == nil && ok {
		out.HostPort = port
		out.State = "running"
		out.Running = true
		return out
	}
	if ctx != nil {
		if port, ok := ctx.ComposePublishedHostPort(target); ok {
			out.HostPort = port
			out.State = "would_publish"
			out.WouldPublish = true
		}
	}
	return out
}

func runningTraefikHostPort(runCtx context.Context, ctx *config.Context, target int) (int, bool, error) {
	if ctx == nil {
		return 0, false, nil
	}
	cli, err := docker.GetDockerCli(ctx)
	if err != nil {
		return 0, false, err
	}
	defer func() { _ = cli.Close() }()

	containerName, err := cli.GetContainerNameContext(runCtx, ctx, "traefik")
	if err != nil {
		return 0, false, err
	}
	if strings.TrimSpace(containerName) == "" {
		return 0, false, nil
	}
	inspect, err := cli.CLI.ContainerInspect(runCtx, containerName)
	if err != nil {
		return 0, false, err
	}
	if inspect.NetworkSettings == nil {
		return 0, false, nil
	}
	for port, bindings := range inspect.NetworkSettings.Ports {
		containerPort, _, ok := strings.Cut(string(port), "/")
		if !ok || containerPort != strconv.Itoa(target) || len(bindings) == 0 {
			continue
		}
		for _, binding := range bindings {
			hostPort := strings.TrimSpace(binding.HostPort)
			if hostPort == "" {
				continue
			}
			parsed, err := strconv.Atoi(hostPort)
			if err == nil {
				return parsed, true, nil
			}
		}
	}
	return 0, false, nil
}

func defaultIngressAppService(ctx *config.Context) string {
	if ctx == nil {
		return ""
	}
	switch strings.TrimSpace(ctx.Plugin) {
	case "archivesspace":
		return "archivesspace"
	case "drupal", "isle":
		return "drupal"
	case "omeka-classic":
		return "omeka-classic"
	case "omeka-s":
		return "omeka-s"
	case "ojs":
		return "ojs"
	case "wp":
		return "wp"
	default:
		return "app"
	}
}

func firstStatsValue(values ...string) string {
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value != "" {
			return value
		}
	}
	return ""
}
