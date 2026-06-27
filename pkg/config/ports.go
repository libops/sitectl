package config

import (
	"errors"
	"fmt"
	"io/fs"
	"net"
	"net/url"
	"os"
	"os/exec"
	"sort"
	"strconv"
	"strings"

	"github.com/joho/godotenv"
	yaml "gopkg.in/yaml.v3"
)

const (
	defaultHTTPPort       = 80
	defaultHTTPSPort      = 443
	defaultHTTPFallback   = 8080
	defaultHTTPSFallback  = 8443
	hostInsecurePortEnv   = "HOST_INSECURE_PORT"
	hostSecurePortEnv     = "HOST_SECURE_PORT"
	siteURLEnv            = "SITE_URL"
	composeProjectLabel   = "com.docker.compose.project"
	maxPortSearchAttempts = 200
)

func (c Context) IsLocalDevelopment() bool {
	if c.DockerHostType != ContextLocal {
		return false
	}
	switch strings.ToLower(strings.TrimSpace(c.Environment)) {
	case "", "local", "dev", "development":
		return true
	default:
		return false
	}
}

func (c Context) ComposeUpPortEnv() (map[string]string, []string, error) {
	if !c.IsLocalDevelopment() {
		return nil, nil, nil
	}
	project := c.EffectiveComposeProjectName()
	projectEnv := c.composeProjectEnv()
	httpStart := envPort(projectEnv, hostInsecurePortEnv, defaultHTTPPort)

	httpPort, httpMessages, err := c.resolveLocalDevPort(project, httpStart, defaultHTTPFallback)
	if err != nil {
		return nil, nil, fmt.Errorf("resolve HTTP host port: %w", err)
	}
	values := map[string]string{
		hostInsecurePortEnv: strconv.Itoa(httpPort),
	}
	messages := append([]string{}, httpMessages...)

	usesHTTPS, err := c.composePublishesSecurePort()
	if err != nil {
		return nil, nil, err
	}
	if usesHTTPS {
		httpsStart := envPort(projectEnv, hostSecurePortEnv, defaultHTTPSPort)
		httpsPort, httpsMessages, err := c.resolveLocalDevPort(project, httpsStart, defaultHTTPSFallback)
		if err != nil {
			return nil, nil, fmt.Errorf("resolve HTTPS host port: %w", err)
		}
		values[hostSecurePortEnv] = strconv.Itoa(httpsPort)
		messages = append(messages, httpsMessages...)
	}
	values[siteURLEnv] = localDevSiteURL(projectEnv, values)
	return values, messages, nil
}

func (c Context) PersistComposeUpPortEnv(values map[string]string) ([]string, error) {
	if !c.IsLocalDevelopment() || len(values) == 0 {
		return nil, nil
	}
	envPath := c.composeEnvFilePath()
	raw, existing, err := c.readComposeEnvFile(envPath)
	if err != nil {
		return nil, err
	}

	updates := map[string]string{}
	maybePersist := func(name string, defaultPort int) {
		value := strings.TrimSpace(values[name])
		if value == "" {
			return
		}
		if value == strconv.Itoa(defaultPort) && strings.TrimSpace(existing[name]) == "" {
			return
		}
		if existing[name] != value {
			updates[name] = value
		}
	}
	maybePersist(hostInsecurePortEnv, defaultHTTPPort)
	maybePersist(hostSecurePortEnv, defaultHTTPSPort)
	if nonDefaultLocalDevPort(values) {
		if value := strings.TrimSpace(values[siteURLEnv]); value != "" && existing[siteURLEnv] != value {
			updates[siteURLEnv] = value
		}
	}
	if len(updates) == 0 {
		return nil, nil
	}
	if err := c.WriteFile(envPath, []byte(updateDotEnvText(raw, updates))); err != nil {
		return nil, err
	}
	keys := make([]string, 0, len(updates))
	for key := range updates {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return []string{fmt.Sprintf("Wrote %s to %s", strings.Join(keys, ", "), envPath)}, nil
}

func (c Context) composeProjectEnv() map[string]string {
	raw, env, err := c.readComposeEnvFile(c.composeEnvFilePath())
	if err != nil || strings.TrimSpace(raw) == "" {
		return map[string]string{}
	}
	return env
}

func (c Context) readComposeEnvFile(path string) (string, map[string]string, error) {
	raw, err := (&c).ReadSmallFile(path)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return "", map[string]string{}, nil
		}
		return "", nil, err
	}
	env, err := godotenv.Parse(strings.NewReader(raw))
	if err != nil {
		env = parseDotEnvText(raw)
	}
	return raw, env, nil
}

func (c Context) composeEnvFilePath() string {
	if len(c.EnvFile) > 0 && strings.TrimSpace(c.EnvFile[0]) != "" {
		return c.ResolveProjectPath(c.EnvFile[0])
	}
	return c.ResolveProjectPath(".env")
}

func (c Context) composePublishesSecurePort() (bool, error) {
	for _, file := range c.composeFilesForPortDetection() {
		path := c.ResolveProjectPath(file)
		exists, err := c.FileExists(path)
		if err != nil {
			return false, err
		}
		if !exists {
			continue
		}
		data, err := c.ReadFile(path)
		if err != nil {
			return false, err
		}
		if composeDataPublishesSecurePort(data) {
			return true, nil
		}
	}
	return false, nil
}

func (c Context) composeFilesForPortDetection() []string {
	if len(c.ComposeFile) > 0 {
		return append([]string{}, c.ComposeFile...)
	}
	return []string{"docker-compose.yml", "docker-compose.yaml", "compose.yml", "compose.yaml"}
}

func composeDataPublishesSecurePort(data []byte) bool {
	if strings.Contains(string(data), hostSecurePortEnv) {
		return true
	}
	var root any
	if err := yaml.Unmarshal(data, &root); err != nil {
		return false
	}
	services := stringMap(stringMap(root)["services"])
	for _, rawService := range services {
		service := stringMap(rawService)
		if servicePublishesPort(service["ports"], defaultHTTPSPort) {
			return true
		}
	}
	return false
}

func servicePublishesPort(value any, target int) bool {
	switch typed := value.(type) {
	case nil:
		return false
	case string:
		return portStringTargets(typed, target)
	case []any:
		for _, item := range typed {
			if servicePublishesPort(item, target) {
				return true
			}
		}
	case []string:
		for _, item := range typed {
			if portStringTargets(item, target) {
				return true
			}
		}
	case map[string]any:
		return portMapTargets(typed, target)
	case map[any]any:
		return portMapTargets(stringMap(typed), target)
	}
	return false
}

func portStringTargets(value string, target int) bool {
	value = strings.TrimSpace(strings.Trim(value, `"'`))
	if value == "" {
		return false
	}
	if strings.Contains(value, hostSecurePortEnv) {
		return true
	}
	value = strings.Split(value, "/")[0]
	parts := strings.Split(value, ":")
	if len(parts) == 0 {
		return false
	}
	return strings.TrimSpace(parts[len(parts)-1]) == strconv.Itoa(target)
}

func portMapTargets(value map[string]any, target int) bool {
	if len(value) == 0 {
		return false
	}
	if strings.Contains(fmt.Sprint(value["published"]), hostSecurePortEnv) {
		return true
	}
	return strings.TrimSpace(fmt.Sprint(value["target"])) == strconv.Itoa(target)
}

func stringMap(value any) map[string]any {
	switch typed := value.(type) {
	case map[string]any:
		return typed
	case map[any]any:
		out := map[string]any{}
		for key, value := range typed {
			out[fmt.Sprint(key)] = value
		}
		return out
	default:
		return nil
	}
}

func (c Context) resolveLocalDevPort(project string, start, fallback int) (int, []string, error) {
	port := start
	messages := []string{}
	for attempts := 0; attempts < maxPortSearchAttempts; attempts++ {
		inUse := tcpPortInUse(port)
		if !inUse {
			return port, messages, nil
		}
		owned, err := dockerPublishedPortBelongsToProject(project, port)
		if err == nil && owned {
			return port, messages, nil
		}
		next := port + 1
		switch port {
		case defaultHTTPPort, defaultHTTPSPort:
			next = fallback
		}
		messages = append(messages, fmt.Sprintf("Port %d is already in use; trying %d", port, next))
		port = next
	}
	return 0, messages, fmt.Errorf("no available port found after %d attempts starting at %d", maxPortSearchAttempts, start)
}

func envPort(projectEnv map[string]string, name string, fallback int) int {
	value := strings.TrimSpace(os.Getenv(name))
	if value == "" && projectEnv != nil {
		value = strings.TrimSpace(projectEnv[name])
	}
	if value == "" {
		return fallback
	}
	port, err := strconv.Atoi(value)
	if err != nil || port <= 0 || port > 65535 {
		return fallback
	}
	return port
}

func localDevSiteURL(projectEnv, values map[string]string) string {
	scheme := firstNonEmptyEnv(projectEnv, "URI_SCHEME", "http")
	if strings.EqualFold(firstNonEmptyEnv(projectEnv, "TRAEFIK_TLS_ENABLED", ""), "true") && strings.TrimSpace(firstNonEmptyEnv(projectEnv, "URI_SCHEME", "")) == "" {
		scheme = "https"
	}
	domain := firstNonEmptyEnv(projectEnv, "DOMAIN", "localhost")
	port := ""
	if scheme == "https" {
		port = strings.TrimSpace(values[hostSecurePortEnv])
	} else {
		port = strings.TrimSpace(values[hostInsecurePortEnv])
	}
	host := domain
	if port != "" && !isDefaultPort(scheme, port) && !strings.Contains(domain, ":") {
		host = domain + ":" + port
	}
	return (&url.URL{Scheme: scheme, Host: host, Path: "/"}).String()
}

func nonDefaultLocalDevPort(values map[string]string) bool {
	return strings.TrimSpace(values[hostInsecurePortEnv]) != "" && values[hostInsecurePortEnv] != strconv.Itoa(defaultHTTPPort) ||
		strings.TrimSpace(values[hostSecurePortEnv]) != "" && values[hostSecurePortEnv] != strconv.Itoa(defaultHTTPSPort)
}

func firstNonEmptyEnv(env map[string]string, key, fallback string) string {
	if env != nil && strings.TrimSpace(env[key]) != "" {
		return strings.TrimSpace(env[key])
	}
	return fallback
}

func isDefaultPort(scheme, port string) bool {
	switch strings.ToLower(strings.TrimSpace(scheme)) {
	case "https":
		return strings.TrimSpace(port) == strconv.Itoa(defaultHTTPSPort)
	default:
		return strings.TrimSpace(port) == strconv.Itoa(defaultHTTPPort)
	}
}

func parseDotEnvText(raw string) map[string]string {
	values := map[string]string{}
	for _, line := range strings.Split(raw, "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		parts := strings.SplitN(line, "=", 2)
		if len(parts) != 2 {
			continue
		}
		values[strings.TrimSpace(parts[0])] = strings.Trim(strings.TrimSpace(parts[1]), `"`)
	}
	return values
}

func updateDotEnvText(raw string, values map[string]string) string {
	lines := []string{}
	if strings.TrimSpace(raw) != "" {
		lines = strings.Split(strings.TrimRight(raw, "\n"), "\n")
	}
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	for _, key := range keys {
		value := values[key]
		lineValue := fmt.Sprintf(`%s="%s"`, key, strings.ReplaceAll(value, `"`, `\"`))
		updated := false
		for i, line := range lines {
			if strings.HasPrefix(strings.TrimSpace(line), key+"=") {
				lines[i] = lineValue
				updated = true
				break
			}
		}
		if !updated {
			lines = append(lines, lineValue)
		}
	}
	return strings.Join(lines, "\n") + "\n"
}

func tcpPortInUse(port int) bool {
	listener, err := net.Listen("tcp", fmt.Sprintf(":%d", port))
	if err != nil {
		return true
	}
	_ = listener.Close()
	return false
}

func dockerPublishedPortBelongsToProject(project string, port int) (bool, error) {
	project = strings.TrimSpace(project)
	if project == "" {
		return false, nil
	}
	out, err := exec.Command("docker", "ps", "-q", "--filter", fmt.Sprintf("publish=%d", port)).Output()
	if err != nil {
		return false, err
	}
	ids := strings.Fields(string(out))
	for _, id := range ids {
		labelOut, err := exec.Command("docker", "inspect", id, "--format", "{{ index .Config.Labels \""+composeProjectLabel+"\" }}").Output()
		if err != nil {
			continue
		}
		if strings.TrimSpace(string(labelOut)) == project {
			return true, nil
		}
	}
	return false, nil
}

func AppendEnvOverrides(base []string, values map[string]string) []string {
	if len(values) == 0 {
		return base
	}
	out := append([]string{}, base...)
	for key, value := range values {
		key = strings.TrimSpace(key)
		if key == "" {
			continue
		}
		out = append(out, key+"="+value)
	}
	return out
}
