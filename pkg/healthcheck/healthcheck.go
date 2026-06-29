package healthcheck

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	dockercontainer "github.com/docker/docker/api/types/container"
	"github.com/docker/docker/api/types/filters"
	"github.com/joho/godotenv"
	"github.com/libops/sitectl/pkg/config"
	sitectldocker "github.com/libops/sitectl/pkg/docker"
	sitevalidate "github.com/libops/sitectl/pkg/validate"
)

const (
	defaultHTTPTimeout = 10 * time.Second
)

var lookupIP = net.LookupIP

// DockerChecker runs health checks against the Docker Compose project attached
// to a sitectl context.
type DockerChecker struct {
	Context *config.Context
	Client  *sitectldocker.DockerClient
}

type OptionalHTTPServiceCheck struct {
	Service string
	Name    string
	URL     string
}

type composeDependencyConfigDocument struct {
	Services map[string]composeDependencyConfigService `json:"services"`
}

type composeDependencyConfigService struct {
	DependsOn json.RawMessage `json:"depends_on"`
}

// NewDockerChecker creates a Docker-backed checker for the given context.
func NewDockerChecker(ctx *config.Context) (*DockerChecker, error) {
	if ctx == nil {
		return nil, fmt.Errorf("context is nil")
	}
	cli, err := sitectldocker.GetDockerCli(ctx)
	if err != nil {
		return nil, fmt.Errorf("create docker client: %w", err)
	}
	return &DockerChecker{Context: ctx, Client: cli}, nil
}

// Close releases resources owned by the checker.
func (c *DockerChecker) Close() error {
	if c == nil || c.Client == nil {
		return nil
	}
	return c.Client.Close()
}

// CheckComposeServices verifies compose service containers are present,
// running, and either healthy or without a Docker healthcheck.
func (c *DockerChecker) CheckComposeServices(ctx context.Context, services ...string) ([]sitevalidate.Result, error) {
	if c == nil || c.Context == nil || c.Client == nil {
		return nil, fmt.Errorf("docker checker is not initialized")
	}
	containers, err := c.composeContainers(ctx)
	if err != nil {
		return nil, err
	}
	byService := containersByService(containers)
	if len(services) == 0 {
		services = sortedServiceNames(byService)
	}

	results := make([]sitevalidate.Result, 0, len(services))
	for _, service := range services {
		service = strings.TrimSpace(service)
		if service == "" {
			continue
		}
		results = append(results, c.checkComposeService(ctx, service, byService[service]))
	}
	return results, nil
}

// ServiceExists reports whether the current compose project has at least one
// container for service.
func (c *DockerChecker) ServiceExists(ctx context.Context, service string) (bool, error) {
	containers, err := c.composeContainers(ctx)
	if err != nil {
		return false, err
	}
	service = strings.TrimSpace(service)
	for _, container := range containers {
		if container.Labels["com.docker.compose.service"] == service {
			return true, nil
		}
	}
	return false, nil
}

// ServiceEnvironment returns the configured environment for a Compose service
// container. If a service has multiple containers, the first one returned by
// Docker is used, matching service exec behavior.
func (c *DockerChecker) ServiceEnvironment(ctx context.Context, service string) (map[string]string, error) {
	if c == nil || c.Client == nil || c.Context == nil {
		return nil, fmt.Errorf("docker checker is not initialized")
	}
	containerName, err := c.Client.GetContainerNameContext(ctx, c.Context, service)
	if err != nil {
		return nil, err
	}
	if strings.TrimSpace(containerName) == "" {
		return nil, fmt.Errorf("service %s is not running", service)
	}
	inspect, err := c.Client.CLI.ContainerInspect(ctx, containerName)
	if err != nil {
		return nil, fmt.Errorf("inspect service %s: %w", service, err)
	}
	env := map[string]string{}
	if inspect.Config == nil {
		return env, nil
	}
	for _, item := range inspect.Config.Env {
		key, value, ok := strings.Cut(item, "=")
		if !ok {
			continue
		}
		env[key] = value
	}
	return env, nil
}

// ServiceEnv returns one configured environment variable for a Compose service.
func (c *DockerChecker) ServiceEnv(ctx context.Context, service, key string) (string, bool, error) {
	env, err := c.ServiceEnvironment(ctx, service)
	if err != nil {
		return "", false, err
	}
	value, ok := env[key]
	return value, ok, nil
}

// CheckMariaDB verifies that a MariaDB/MySQL service is accepting local
// connections from inside its own container.
func (c *DockerChecker) CheckMariaDB(ctx context.Context, service string) sitevalidate.Result {
	service = firstNonEmpty(service, "mariadb")
	return c.checkMySQLCompatible(ctx, "mariadb:"+service, service)
}

// CheckMySQL verifies that a MySQL/MariaDB service is accepting local
// connections from inside its own container.
func (c *DockerChecker) CheckMySQL(ctx context.Context, service string) sitevalidate.Result {
	service = firstNonEmpty(service, "mysql")
	return c.checkMySQLCompatible(ctx, "mysql:"+service, service)
}

func (c *DockerChecker) checkMySQLCompatible(ctx context.Context, name, service string) sitevalidate.Result {
	return c.checkExec(ctx, name, service, []string{
		"sh",
		"-lc",
		`if command -v mariadb-admin >/dev/null 2>&1; then mariadb-admin ping -h 127.0.0.1 --silent; elif command -v mysqladmin >/dev/null 2>&1; then mysqladmin ping -h 127.0.0.1 --silent; else test -S /run/mysqld/mysqld.sock || test -S /var/run/mysqld/mysqld.sock; fi`,
	})
}

// CheckSolrCore verifies that a Solr core is loaded from inside the Solr
// container.
func (c *DockerChecker) CheckSolrCore(ctx context.Context, service, core string) sitevalidate.Result {
	service = firstNonEmpty(service, "solr")
	core = firstNonEmpty(core, "default")
	name := "solr:" + service
	if c == nil || c.Client == nil || c.Context == nil {
		return failed(name, "docker checker is not initialized")
	}
	containerName, err := c.Client.GetContainerNameContext(ctx, c.Context, service)
	if err != nil {
		return failed(name, err.Error())
	}
	if strings.TrimSpace(containerName) == "" {
		return failed(name, "service "+service+" is not running")
	}

	endpoint := fmt.Sprintf("http://127.0.0.1:8983/solr/admin/cores?action=STATUS&core=%s&wt=json", url.QueryEscape(core))
	command := fmt.Sprintf(`if command -v curl >/dev/null 2>&1; then curl -fsS --max-time 10 %q; elif command -v wget >/dev/null 2>&1; then wget -q --timeout=10 -O- %q; else echo "curl or wget is required"; exit 127; fi`, endpoint, endpoint)
	execCtx, cancel := context.WithTimeout(ctx, defaultHTTPTimeout)
	defer cancel()
	output, err := sitectldocker.ExecCapture(execCtx, c.Client, containerName, "", []string{"sh", "-lc", command})
	if err != nil {
		detail := strings.TrimSpace(output)
		if detail != "" {
			detail += ": "
		}
		return failed(name, detail+err.Error())
	}
	detail, err := solrCoreStatusDetail(output, core)
	if err != nil {
		return failed(name, err.Error())
	}
	return sitevalidate.Result{Name: name, Status: sitevalidate.StatusOK, Detail: detail}
}

// CheckHTTPFromContainer verifies that url is reachable from inside service.
func (c *DockerChecker) CheckHTTPFromContainer(ctx context.Context, name, service, targetURL string) sitevalidate.Result {
	return c.CheckHTTPFromContainerWithHostHeader(ctx, name, service, targetURL, "")
}

// CheckHTTPFromContainerWithHostHeader verifies that url is reachable from
// inside service while sending a specific HTTP Host header.
func (c *DockerChecker) CheckHTTPFromContainerWithHostHeader(ctx context.Context, name, service, targetURL, hostHeader string) sitevalidate.Result {
	targetURL = strings.TrimSpace(targetURL)
	if targetURL == "" {
		return failed(name, "URL is empty")
	}
	hostHeader = strings.TrimSpace(hostHeader)
	if hostHeader == "" {
		command := fmt.Sprintf(`if command -v curl >/dev/null 2>&1; then curl -fsS --max-time 10 %q >/dev/null; else wget -q --timeout=10 --spider %q; fi`, targetURL, targetURL)
		return c.checkExec(ctx, name, service, []string{"sh", "-lc", command})
	}
	header := "Host: " + hostHeader
	command := fmt.Sprintf(`if command -v curl >/dev/null 2>&1; then curl -fsS --max-time 10 -H %q %q >/dev/null; else wget -q --timeout=10 --header %q --spider %q; fi`, header, targetURL, header, targetURL)
	return c.checkExec(ctx, name, service, []string{"sh", "-lc", command})
}

// CheckHTTPRoute verifies an application route. Localhost-style URLs are tried
// from the host first; failures and non-local domains fall back to a
// container-side request with the public URL host sent as the HTTP Host header.
func (c *DockerChecker) CheckHTTPRoute(ctx context.Context, name, service, publicURL string) sitevalidate.Result {
	publicURL = strings.TrimSpace(publicURL)
	if publicURL == "" {
		return failed(name, "URL is empty")
	}
	parsed, err := url.Parse(publicURL)
	if err != nil {
		return failed(name, err.Error())
	}
	if isLocalHost(parsed.Hostname()) {
		if result := CheckHTTP(ctx, name, publicURL); result.Status == sitevalidate.StatusOK {
			return result
		}
	}
	containerURL := containerHTTPRouteURL(parsed)
	hostHeader := parsed.Host
	if hostHeader == "" {
		hostHeader = parsed.Hostname()
	}
	result := c.CheckHTTPFromContainerWithHostHeader(ctx, name, service, containerURL, hostHeader)
	if result.Status == sitevalidate.StatusOK {
		result.Detail = strings.TrimSpace(result.Detail + " via " + service)
	}
	return result
}

// CheckOptionalHTTPServices runs container-side HTTP checks for optional
// services only when the service exists and its compose container is healthy.
func (c *DockerChecker) CheckOptionalHTTPServices(ctx context.Context, checks ...OptionalHTTPServiceCheck) ([]sitevalidate.Result, error) {
	results := []sitevalidate.Result{}
	for _, check := range checks {
		service := strings.TrimSpace(check.Service)
		if service == "" {
			continue
		}
		ready, err := c.optionalHTTPServiceReady(ctx, service)
		if err != nil {
			return nil, err
		}
		if !ready {
			continue
		}
		name := firstNonEmpty(check.Name, "http:"+service)
		results = append(results, c.CheckHTTPFromContainer(ctx, name, service, check.URL))
	}
	return results, nil
}

func (c *DockerChecker) optionalHTTPServiceReady(ctx context.Context, service string) (bool, error) {
	exists, err := c.ServiceExists(ctx, service)
	if err != nil || !exists {
		return false, err
	}
	results, err := c.CheckComposeServices(ctx, service)
	if err != nil {
		return false, err
	}
	for _, result := range results {
		if result.Status != sitevalidate.StatusOK {
			return false, nil
		}
	}
	return len(results) > 0, nil
}

// CheckComposeServiceDependsOnHealthy verifies that a Compose service depends
// on another service with condition: service_healthy.
func (c *DockerChecker) CheckComposeServiceDependsOnHealthy(ctx context.Context, service, dependency string) sitevalidate.Result {
	service = strings.TrimSpace(service)
	dependency = strings.TrimSpace(dependency)
	name := fmt.Sprintf("compose-dependency:%s->%s", service, dependency)
	if c == nil || c.Context == nil {
		return failed(name, "docker checker is not initialized")
	}
	if service == "" || dependency == "" {
		return failed(name, "service and dependency are required")
	}
	configDoc, err := c.readComposeDependencyConfig(ctx)
	if err != nil {
		return failed(name, err.Error())
	}
	serviceDoc, ok := configDoc.Services[service]
	if !ok {
		return failed(name, "service "+service+" is not defined")
	}
	condition, ok := composeDependencyCondition(serviceDoc.DependsOn, dependency)
	if !ok {
		return failed(name, fmt.Sprintf("%s does not depend on %s", service, dependency))
	}
	if condition != "service_healthy" {
		return failed(name, fmt.Sprintf("%s depends on %s with condition %q", service, dependency, condition))
	}
	return sitevalidate.Result{Name: name, Status: sitevalidate.StatusOK, Detail: fmt.Sprintf("%s waits for %s service_healthy", service, dependency)}
}

func (c *DockerChecker) readComposeDependencyConfig(ctx context.Context) (composeDependencyConfigDocument, error) {
	args := []string{"compose"}
	args = append(args, c.Context.DockerComposeGlobalArgs()...)
	args = append(args, "config", "--format", "json")
	command := exec.CommandContext(ctx, "docker", args...) // #nosec G204 -- fixed docker compose command with context-owned compose/env file arguments.
	command.Dir = c.Context.ProjectDir
	var stdout bytes.Buffer
	var stderr bytes.Buffer
	command.Stdout = &stdout
	command.Stderr = &stderr
	if err := command.Run(); err != nil {
		return composeDependencyConfigDocument{}, fmt.Errorf("docker compose config: %w: %s", err, strings.TrimSpace(stderr.String()))
	}
	var document composeDependencyConfigDocument
	if err := json.Unmarshal(stdout.Bytes(), &document); err != nil {
		return composeDependencyConfigDocument{}, fmt.Errorf("parse docker compose config json: %w", err)
	}
	return document, nil
}

// CheckHTTP verifies that url returns a 2xx or 3xx response from the host
// running sitectl.
func CheckHTTP(ctx context.Context, name, targetURL string) sitevalidate.Result {
	targetURL = strings.TrimSpace(targetURL)
	if targetURL == "" {
		return failed(name, "URL is empty")
	}
	reqCtx, cancel := context.WithTimeout(ctx, defaultHTTPTimeout)
	defer cancel()
	if result, err := checkHTTPURL(reqCtx, name, targetURL, ""); err == nil {
		return result
	}
	if fallbackURL, hostHeader, ok := dockerHostFallbackURL(targetURL); ok && dockerHostFallbackReachable() {
		if result, err := checkHTTPURL(reqCtx, name, fallbackURL, hostHeader); err == nil {
			result.Detail += " via host.docker.internal"
			return result
		}
	}
	result, _ := checkHTTPURL(reqCtx, name, targetURL, "")
	return result
}

func checkHTTPURL(ctx context.Context, name, targetURL, hostHeader string) (sitevalidate.Result, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, targetURL, nil)
	if err != nil {
		result := failed(name, err.Error())
		return result, err
	}
	if strings.TrimSpace(hostHeader) != "" {
		req.Host = hostHeader
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		result := failed(name, err.Error())
		return result, err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 400 {
		err := fmt.Errorf("received %s", resp.Status)
		result := failed(name, err.Error())
		return result, err
	}
	return sitevalidate.Result{Name: name, Status: sitevalidate.StatusOK, Detail: resp.Status}, nil
}

func dockerHostFallbackURL(targetURL string) (string, string, bool) {
	parsed, err := url.Parse(targetURL)
	if err != nil || !isLocalHost(parsed.Hostname()) {
		return "", "", false
	}
	fallback := *parsed
	fallback.Host = "host.docker.internal"
	if port := parsed.Port(); port != "" {
		fallback.Host += ":" + port
	}
	return fallback.String(), parsed.Host, true
}

func dockerHostFallbackReachable() bool {
	_, err := net.LookupHost("host.docker.internal")
	return err == nil
}

func containerHTTPRouteURL(parsed *url.URL) string {
	if parsed == nil {
		return "http://127.0.0.1/"
	}
	route := *parsed
	route.Scheme = "http"
	route.Host = "127.0.0.1"
	if route.Path == "" {
		route.Path = "/"
	}
	return route.String()
}

func composeDependencyCondition(raw json.RawMessage, dependency string) (string, bool) {
	dependency = strings.TrimSpace(dependency)
	if len(bytes.TrimSpace(raw)) == 0 || dependency == "" {
		return "", false
	}
	var longForm map[string]struct {
		Condition string `json:"condition"`
	}
	if err := json.Unmarshal(raw, &longForm); err == nil && longForm != nil {
		if dep, ok := longForm[dependency]; ok {
			return firstNonEmpty(dep.Condition, "service_started"), true
		}
		return "", false
	}
	var shortForm []string
	if err := json.Unmarshal(raw, &shortForm); err == nil {
		for _, name := range shortForm {
			if strings.TrimSpace(name) == dependency {
				return "service_started", true
			}
		}
	}
	return "", false
}

func isLocalHost(host string) bool {
	host = strings.ToLower(strings.Trim(host, "[]"))
	switch host {
	case "localhost", "127.0.0.1", "::1":
		return true
	default:
		return resolvesToLoopback(host)
	}
}

func resolvesToLoopback(host string) bool {
	if strings.TrimSpace(host) == "" {
		return false
	}
	ips, err := lookupIP(host)
	if err != nil {
		return false
	}
	for _, ip := range ips {
		if ip.IsLoopback() {
			return true
		}
	}
	return false
}

// PublicURLFromEnv builds a public app URL from .env values and the context's
// Compose port bindings. SITE_URL wins when explicitly configured. Otherwise
// target port 443 implies https and target port 80 implies http.
func PublicURLFromEnv(ctx *config.Context, defaultScheme, defaultDomain string) string {
	env := ProjectEnv(ctx)
	if siteURL := strings.TrimSpace(env["SITE_URL"]); siteURL != "" {
		return siteURL
	}
	scheme := firstNonEmpty(defaultScheme, "http")
	if ctx != nil {
		scheme = ctx.ComposePublicScheme(scheme)
	}
	domain := firstNonEmpty(env["DOMAIN"], defaultDomain, "localhost")
	port := ""
	if ctx != nil {
		target := 80
		if scheme == "https" {
			target = 443
		}
		if hostPort, ok := ctx.ComposePublishedHostPort(target); ok && !isDefaultPort(scheme, strconv.Itoa(hostPort)) {
			port = strconv.Itoa(hostPort)
		}
	}
	host := domain
	if port != "" && !isDefaultPort(scheme, port) && !strings.Contains(domain, ":") {
		host = domain + ":" + port
	}
	return (&url.URL{Scheme: scheme, Host: host, Path: "/"}).String()
}

// ProjectEnv reads the project's .env file. Missing or unparsable files return
// an empty map so health checks can continue with stack defaults.
func ProjectEnv(ctx *config.Context) map[string]string {
	if ctx == nil || strings.TrimSpace(ctx.ProjectDir) == "" {
		return map[string]string{}
	}
	path := filepath.Join(ctx.ProjectDir, ".env")
	if len(ctx.EnvFile) > 0 && strings.TrimSpace(ctx.EnvFile[0]) != "" {
		envPath := strings.TrimSpace(ctx.EnvFile[0])
		if filepath.IsAbs(envPath) {
			path = envPath
		} else {
			path = filepath.Join(ctx.ProjectDir, envPath)
		}
	}
	data, err := ctx.ReadSmallFile(path)
	if err != nil {
		return map[string]string{}
	}
	env, err := godotenv.Parse(strings.NewReader(data))
	if err != nil {
		return map[string]string{}
	}
	return env
}

func (c *DockerChecker) composeContainers(ctx context.Context) ([]dockercontainer.Summary, error) {
	project := strings.TrimSpace(c.Context.EffectiveComposeProjectName())
	if project == "" {
		return nil, fmt.Errorf("compose project name is empty")
	}
	filterArgs := filters.NewArgs()
	filterArgs.Add("label", "com.docker.compose.project="+project)
	containers, err := c.Client.CLI.ContainerList(ctx, dockercontainer.ListOptions{
		All:     true,
		Filters: filterArgs,
	})
	if err != nil {
		return nil, fmt.Errorf("list compose containers: %w", err)
	}
	filtered := containers[:0]
	for _, container := range containers {
		if strings.EqualFold(container.Labels["com.docker.compose.oneoff"], "True") {
			continue
		}
		filtered = append(filtered, container)
	}
	return filtered, nil
}

func (c *DockerChecker) checkComposeService(ctx context.Context, service string, containers []dockercontainer.Summary) sitevalidate.Result {
	if len(containers) == 0 {
		return failed("service:"+service, "no compose container found")
	}
	details := make([]string, 0, len(containers))
	ok := true
	for _, container := range containers {
		inspect, err := c.Client.CLI.ContainerInspect(ctx, container.ID)
		if err != nil {
			ok = false
			details = append(details, containerName(container)+": inspect failed: "+err.Error())
			continue
		}
		state := inspect.State
		if state == nil {
			ok = false
			details = append(details, containerName(container)+": state unavailable")
			continue
		}
		if !state.Running {
			ok = false
			detail := fmt.Sprintf("%s: %s", containerName(container), state.Status)
			if state.ExitCode != 0 {
				detail += fmt.Sprintf(" exit=%d", state.ExitCode)
			}
			if strings.TrimSpace(state.Error) != "" {
				detail += " error=" + strings.TrimSpace(state.Error)
			}
			details = append(details, detail)
			continue
		}
		if state.Health != nil {
			health := strings.TrimSpace(string(state.Health.Status))
			if health != "" && health != "healthy" {
				ok = false
				details = append(details, fmt.Sprintf("%s: health=%s", containerName(container), health))
				continue
			}
			details = append(details, containerName(container)+": healthy")
			continue
		}
		details = append(details, containerName(container)+": running")
	}
	if !ok {
		return failed("service:"+service, strings.Join(details, "; "))
	}
	return sitevalidate.Result{Name: "service:" + service, Status: sitevalidate.StatusOK, Detail: strings.Join(details, "; ")}
}

func (c *DockerChecker) checkExec(ctx context.Context, name, service string, command []string) sitevalidate.Result {
	if c == nil || c.Client == nil || c.Context == nil {
		return failed(name, "docker checker is not initialized")
	}
	containerName, err := c.Client.GetContainerNameContext(ctx, c.Context, service)
	if err != nil {
		return failed(name, err.Error())
	}
	if strings.TrimSpace(containerName) == "" {
		return failed(name, "service "+service+" is not running")
	}
	execCtx, cancel := context.WithTimeout(ctx, defaultHTTPTimeout)
	defer cancel()
	output, err := sitectldocker.ExecCapture(execCtx, c.Client, containerName, "", command)
	if err != nil {
		detail := strings.TrimSpace(output)
		if detail != "" {
			detail += ": "
		}
		return failed(name, detail+err.Error())
	}
	return sitevalidate.Result{Name: name, Status: sitevalidate.StatusOK, Detail: trimOutput(output)}
}

func containersByService(containers []dockercontainer.Summary) map[string][]dockercontainer.Summary {
	byService := map[string][]dockercontainer.Summary{}
	for _, container := range containers {
		service := strings.TrimSpace(container.Labels["com.docker.compose.service"])
		if service == "" {
			continue
		}
		byService[service] = append(byService[service], container)
	}
	return byService
}

func sortedServiceNames(byService map[string][]dockercontainer.Summary) []string {
	names := make([]string, 0, len(byService))
	for name := range byService {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

func containerName(container dockercontainer.Summary) string {
	if len(container.Names) == 0 {
		return container.ID[:min(len(container.ID), 12)]
	}
	return strings.TrimPrefix(container.Names[0], "/")
}

func failed(name, detail string) sitevalidate.Result {
	return sitevalidate.Result{Name: name, Status: sitevalidate.StatusFailed, Detail: strings.TrimSpace(detail)}
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value != "" {
			return value
		}
	}
	return ""
}

func isDefaultPort(scheme, port string) bool {
	return scheme == "http" && port == "80" || scheme == "https" && port == "443"
}

func trimOutput(output string) string {
	output = strings.Join(strings.Fields(output), " ")
	if len(output) <= 200 {
		return output
	}
	return output[:200] + "..."
}

func solrCoreStatusDetail(output, core string) (string, error) {
	var payload struct {
		InitFailures map[string]json.RawMessage `json:"initFailures"`
		Status       map[string]json.RawMessage `json:"status"`
	}
	if err := json.Unmarshal([]byte(output), &payload); err != nil {
		return "", fmt.Errorf("parse Solr core status: %w: %s", err, trimOutput(output))
	}
	if raw, ok := payload.InitFailures[core]; ok {
		return "", fmt.Errorf("solr core %s has init failure: %s", core, trimOutput(string(raw)))
	}
	if _, ok := payload.Status[core]; !ok {
		return "", fmt.Errorf("solr core %s not found", core)
	}
	return "Solr core " + core + " is loaded", nil
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}
