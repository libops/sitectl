package healthcheck

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
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

// DockerChecker runs health checks against the Docker Compose project attached
// to a sitectl context.
type DockerChecker struct {
	Context *config.Context
	Client  *sitectldocker.DockerClient
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
	command := fmt.Sprintf(`if command -v curl >/dev/null 2>&1; then curl -fsS --max-time 10 %q >/dev/null; else wget -q --timeout=10 --spider %q; fi`, targetURL, targetURL)
	return c.checkExec(ctx, name, service, []string{"sh", "-lc", command})
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
	req, err := http.NewRequestWithContext(reqCtx, http.MethodGet, targetURL, nil)
	if err != nil {
		return failed(name, err.Error())
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return failed(name, err.Error())
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 400 {
		return failed(name, "received "+resp.Status)
	}
	return sitevalidate.Result{Name: name, Status: sitevalidate.StatusOK, Detail: resp.Status}
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
