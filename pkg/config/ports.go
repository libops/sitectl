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
	defaultHTTPPort              = 80
	defaultHTTPSPort             = 443
	defaultHTTPFallback          = 8080
	defaultHTTPSFallback         = 8443
	LocalDevComposeOverrideName  = "docker-compose.override.yml"
	siteURLEnv                   = "SITE_URL"
	uriSchemeEnv                 = "URI_SCHEME"
	domainEnv                    = "DOMAIN"
	sitectlDevPortsExtensionName = "x-sitectl-dev-ports"
	composeProjectLabel          = "com.docker.compose.project"
	maxPortSearchAttempts        = 200
)

type composePortPlan struct {
	Services    map[string]map[int]struct{}
	LetsEncrypt bool
}

func (p composePortPlan) empty() bool {
	return len(p.Services) == 0
}

func (p composePortPlan) usesHTTPS() bool {
	for _, targets := range p.Services {
		if _, ok := targets[defaultHTTPSPort]; ok {
			return true
		}
	}
	return false
}

func (p composePortPlan) usesPublicHTTPS() bool {
	for service, targets := range p.Services {
		if !strings.EqualFold(service, "traefik") {
			continue
		}
		if _, ok := targets[defaultHTTPSPort]; ok {
			return true
		}
		return false
	}
	return p.usesHTTPS()
}

func (p composePortPlan) tlsProvider(defaultProvider string) string {
	if p.LetsEncrypt {
		return "letsencrypt"
	}
	defaultProvider = strings.TrimSpace(defaultProvider)
	if defaultProvider != "" {
		return defaultProvider
	}
	return "self-managed"
}

func (p composePortPlan) targets() []int {
	seen := map[int]struct{}{}
	for _, targets := range p.Services {
		for target := range targets {
			seen[target] = struct{}{}
		}
	}
	out := make([]int, 0, len(seen))
	for target := range seen {
		out = append(out, target)
	}
	sort.Ints(out)
	return out
}

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

func (c Context) PrepareComposeUpPortOverride() (map[string]string, []string, error) {
	if !c.IsLocalDevelopment() {
		return nil, nil, nil
	}

	plan, err := c.inspectComposePortPlan()
	if err != nil {
		return nil, nil, err
	}
	if plan.empty() {
		return nil, nil, nil
	}

	project := c.EffectiveComposeProjectName()
	hostPorts := map[int]int{}
	messages := []string{}
	for _, target := range plan.targets() {
		fallback, ok := localDevFallbackPort(target)
		if !ok {
			continue
		}
		hostPort, targetMessages, err := c.resolveLocalDevPort(project, target, fallback)
		if err != nil {
			return nil, nil, fmt.Errorf("resolve host port for target %d: %w", target, err)
		}
		hostPorts[target] = hostPort
		messages = append(messages, targetMessages...)
	}

	writeMessages, err := c.writeComposePortOverride(plan, hostPorts)
	if err != nil {
		return nil, nil, err
	}
	messages = append(messages, writeMessages...)

	projectEnv := c.composeProjectEnv()
	scheme := "http"
	if plan.usesPublicHTTPS() {
		scheme = "https"
	}
	envValues := map[string]string{
		uriSchemeEnv: scheme,
		siteURLEnv:   localDevSiteURL(projectEnv, scheme, hostPorts),
	}
	return envValues, messages, nil
}

func (c Context) ComposePublicScheme(defaultScheme string) string {
	plan, err := c.inspectComposePortPlan()
	if err == nil && !plan.empty() {
		if plan.usesPublicHTTPS() {
			return "https"
		}
		return "http"
	}
	defaultScheme = strings.TrimSpace(defaultScheme)
	if defaultScheme != "" {
		return defaultScheme
	}
	return "http"
}

func (c Context) ComposeTLSProvider(defaultProvider string) string {
	plan, err := c.inspectComposePortPlan()
	if err == nil && !plan.empty() {
		return plan.tlsProvider(defaultProvider)
	}
	defaultProvider = strings.TrimSpace(defaultProvider)
	if defaultProvider != "" {
		return defaultProvider
	}
	return "self-managed"
}

func (c Context) ComposePublishedHostPort(target int) (int, bool) {
	plan, err := c.inspectComposePortPlan()
	if err != nil || plan.empty() {
		return 0, false
	}
	targets := map[int]struct{}{}
	for _, serviceTargets := range plan.Services {
		for serviceTarget := range serviceTargets {
			targets[serviceTarget] = struct{}{}
		}
	}
	if _, ok := targets[target]; !ok {
		return 0, false
	}
	hostPorts := map[int]int{}
	for serviceTarget := range targets {
		hostPorts[serviceTarget] = serviceTarget
	}
	for overrideTarget, hostPort := range c.composeOverrideHostPorts() {
		hostPorts[overrideTarget] = hostPort
	}
	hostPort, ok := hostPorts[target]
	return hostPort, ok
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

func (c Context) inspectComposePortPlan() (composePortPlan, error) {
	plan := composePortPlan{Services: map[string]map[int]struct{}{}}
	for _, file := range c.composeFilesForPortDetection() {
		path := c.ResolveProjectPath(file)
		exists, err := c.FileExists(path)
		if err != nil {
			return composePortPlan{}, err
		}
		if !exists {
			continue
		}
		data, err := c.ReadFile(path)
		if err != nil {
			return composePortPlan{}, err
		}
		mergeComposePortPlan(&plan, data)
	}
	return plan, nil
}

func (c Context) composeFilesForPortDetection() []string {
	if len(c.ComposeFile) > 0 {
		return append([]string{}, c.ComposeFile...)
	}
	return []string{"docker-compose.yml", "docker-compose.yaml", "compose.yml", "compose.yaml"}
}

func composeDataPublishesSecurePort(data []byte) bool {
	plan := composePortPlan{Services: map[string]map[int]struct{}{}}
	mergeComposePortPlan(&plan, data)
	return plan.usesHTTPS()
}

func mergeComposePortPlan(plan *composePortPlan, data []byte) {
	if plan == nil {
		return
	}
	var doc yaml.Node
	if err := yaml.Unmarshal(data, &doc); err != nil {
		return
	}
	root := documentMapping(&doc)
	if root == nil {
		return
	}
	services := mappingValue(root, "services")
	if services == nil || services.Kind != yaml.MappingNode {
		return
	}
	for i := 0; i+1 < len(services.Content); i += 2 {
		serviceName := strings.TrimSpace(services.Content[i].Value)
		service := services.Content[i+1]
		if serviceName == "" || service.Kind != yaml.MappingNode {
			continue
		}
		command := mappingValue(service, "command")
		targets := serviceTargetPorts(mappingValue(service, "ports"))
		for _, target := range serviceCommandTargetPorts(command) {
			targets[target] = struct{}{}
		}
		if strings.EqualFold(serviceName, "traefik") && serviceCommandUsesLetsEncrypt(command) {
			plan.LetsEncrypt = true
		}
		for target := range targets {
			if _, ok := localDevFallbackPort(target); !ok {
				continue
			}
			if plan.Services[serviceName] == nil {
				plan.Services[serviceName] = map[int]struct{}{}
			}
			plan.Services[serviceName][target] = struct{}{}
		}
	}
}

func serviceTargetPorts(node *yaml.Node) map[int]struct{} {
	targets := map[int]struct{}{}
	if node == nil {
		return targets
	}
	switch node.Kind {
	case yaml.SequenceNode:
		for _, item := range node.Content {
			for target := range serviceTargetPorts(item) {
				targets[target] = struct{}{}
			}
		}
	case yaml.MappingNode:
		if target := portMapTarget(node); target != 0 {
			targets[target] = struct{}{}
		}
	case yaml.ScalarNode:
		if target := portStringTarget(node.Value); target != 0 {
			targets[target] = struct{}{}
		}
	}
	return targets
}

func portStringTarget(value string) int {
	value = strings.TrimSpace(strings.Trim(value, `"'`))
	if value == "" {
		return 0
	}
	value = strings.Split(value, "/")[0]
	parts := strings.Split(value, ":")
	if len(parts) == 0 {
		return 0
	}
	target, err := strconv.Atoi(strings.TrimSpace(parts[len(parts)-1]))
	if err != nil {
		return 0
	}
	return target
}

func portMapTarget(node *yaml.Node) int {
	targetNode := mappingValue(node, "target")
	if targetNode == nil {
		return 0
	}
	target, err := strconv.Atoi(strings.TrimSpace(targetNode.Value))
	if err != nil {
		return 0
	}
	return target
}

func serviceCommandTargetPorts(node *yaml.Node) []int {
	targets := map[int]struct{}{}
	for _, value := range serviceCommandValues(node) {
		lower := strings.ToLower(strings.TrimSpace(value))
		if !strings.Contains(lower, "entrypoints.") || !strings.Contains(lower, "address=:") {
			continue
		}
		switch {
		case strings.Contains(lower, "address=:443"):
			targets[defaultHTTPSPort] = struct{}{}
		case strings.Contains(lower, "address=:80"):
			targets[defaultHTTPPort] = struct{}{}
		}
	}
	out := make([]int, 0, len(targets))
	for target := range targets {
		out = append(out, target)
	}
	sort.Ints(out)
	return out
}

func serviceCommandUsesLetsEncrypt(node *yaml.Node) bool {
	for _, value := range serviceCommandValues(node) {
		if strings.Contains(strings.ToLower(value), "certificatesresolvers.letsencrypt.acme") {
			return true
		}
	}
	return false
}

func serviceCommandValues(node *yaml.Node) []string {
	if node == nil {
		return nil
	}
	switch node.Kind {
	case yaml.ScalarNode:
		return strings.Fields(node.Value)
	case yaml.SequenceNode:
		values := []string{}
		for _, item := range node.Content {
			if item.Kind == yaml.ScalarNode {
				values = append(values, item.Value)
			}
		}
		return values
	default:
		return nil
	}
}

func (c Context) writeComposePortOverride(plan composePortPlan, hostPorts map[int]int) ([]string, error) {
	path := c.ResolveProjectPath(LocalDevComposeOverrideName)
	doc, existed, err := readComposeOverrideDocument(path)
	if err != nil {
		return nil, err
	}
	root := ensureDocumentMapping(doc)
	managed := managedPortOverrideServices(root)
	desired := desiredPortOverrides(plan, hostPorts)
	if len(desired) == 0 && len(managed) == 0 {
		return nil, nil
	}

	services := ensureMappingValue(root, "services")
	for service := range managed {
		if _, keep := desired[service]; !keep {
			if serviceNode := mappingValue(services, service); serviceNode != nil && serviceNode.Kind == yaml.MappingNode {
				deleteMappingKey(serviceNode, "ports")
				if len(serviceNode.Content) == 0 {
					deleteMappingKey(services, service)
				}
			}
		}
	}
	for service, ports := range desired {
		serviceNode := ensureMappingValue(services, service)
		setPortsOverride(serviceNode, ports)
	}
	if len(desired) == 0 {
		deleteMappingKey(root, sitectlDevPortsExtensionName)
		if len(services.Content) == 0 {
			deleteMappingKey(root, "services")
		}
	} else {
		setManagedPortExtension(root, desired)
	}

	if len(root.Content) == 0 {
		if existed {
			if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
				return nil, err
			}
			return []string{fmt.Sprintf("Removed %s local dev port override", LocalDevComposeOverrideName)}, nil
		}
		return nil, nil
	}

	data, err := yaml.Marshal(doc)
	if err != nil {
		return nil, fmt.Errorf("marshal %s: %w", path, err)
	}
	previous, readErr := os.ReadFile(path) // #nosec G304 -- local project override path is derived from the context.
	if readErr == nil && string(previous) == string(data) {
		return nil, nil
	}
	if err := os.WriteFile(path, data, 0o644); err != nil {
		return nil, fmt.Errorf("write %s: %w", path, err)
	}
	return []string{fmt.Sprintf("Wrote %s local dev port override", LocalDevComposeOverrideName)}, nil
}

func desiredPortOverrides(plan composePortPlan, hostPorts map[int]int) map[string][]string {
	desired := map[string][]string{}
	services := make([]string, 0, len(plan.Services))
	for service := range plan.Services {
		services = append(services, service)
	}
	sort.Strings(services)
	for _, service := range services {
		targets := make([]int, 0, len(plan.Services[service]))
		overrideNeeded := false
		for target := range plan.Services[service] {
			hostPort := hostPorts[target]
			if hostPort == 0 {
				hostPort = target
			}
			if hostPort != target {
				overrideNeeded = true
			}
			targets = append(targets, target)
		}
		if !overrideNeeded {
			continue
		}
		sort.Ints(targets)
		ports := make([]string, 0, len(targets))
		for _, target := range targets {
			hostPort := hostPorts[target]
			if hostPort == 0 {
				hostPort = target
			}
			ports = append(ports, fmt.Sprintf("%d:%d", hostPort, target))
		}
		desired[service] = ports
	}
	return desired
}

func readComposeOverrideDocument(path string) (*yaml.Node, bool, error) {
	data, err := os.ReadFile(path) // #nosec G304 -- local project override path is derived from the context.
	if err != nil {
		if os.IsNotExist(err) {
			return emptyYAMLDocument(), false, nil
		}
		return nil, false, fmt.Errorf("read %s: %w", path, err)
	}
	if len(strings.TrimSpace(string(data))) == 0 {
		return emptyYAMLDocument(), true, nil
	}
	var doc yaml.Node
	if err := yaml.Unmarshal(data, &doc); err != nil {
		return nil, true, fmt.Errorf("parse %s: %w", path, err)
	}
	if documentMapping(&doc) == nil {
		return emptyYAMLDocument(), true, nil
	}
	return &doc, true, nil
}

func emptyYAMLDocument() *yaml.Node {
	return &yaml.Node{
		Kind: yaml.DocumentNode,
		Content: []*yaml.Node{{
			Kind: yaml.MappingNode,
			Tag:  "!!map",
		}},
	}
}

func documentMapping(doc *yaml.Node) *yaml.Node {
	if doc == nil {
		return nil
	}
	if doc.Kind == yaml.DocumentNode {
		if len(doc.Content) == 0 {
			return nil
		}
		if doc.Content[0].Kind == yaml.MappingNode {
			return doc.Content[0]
		}
		return nil
	}
	if doc.Kind == yaml.MappingNode {
		return doc
	}
	return nil
}

func ensureDocumentMapping(doc *yaml.Node) *yaml.Node {
	if doc.Kind != yaml.DocumentNode {
		doc.Kind = yaml.DocumentNode
	}
	if len(doc.Content) == 0 || doc.Content[0].Kind != yaml.MappingNode {
		doc.Content = []*yaml.Node{{
			Kind: yaml.MappingNode,
			Tag:  "!!map",
		}}
	}
	return doc.Content[0]
}

func mappingValue(mapping *yaml.Node, key string) *yaml.Node {
	if mapping == nil || mapping.Kind != yaml.MappingNode {
		return nil
	}
	for i := 0; i+1 < len(mapping.Content); i += 2 {
		if mapping.Content[i].Value == key {
			return mapping.Content[i+1]
		}
	}
	return nil
}

func ensureMappingValue(mapping *yaml.Node, key string) *yaml.Node {
	if mapping.Kind != yaml.MappingNode {
		mapping.Kind = yaml.MappingNode
		mapping.Tag = "!!map"
		mapping.Content = nil
	}
	if value := mappingValue(mapping, key); value != nil {
		if value.Kind != yaml.MappingNode {
			value.Kind = yaml.MappingNode
			value.Tag = "!!map"
			value.Content = nil
		}
		return value
	}
	keyNode := &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: key}
	valueNode := &yaml.Node{Kind: yaml.MappingNode, Tag: "!!map"}
	mapping.Content = append(mapping.Content, keyNode, valueNode)
	return valueNode
}

func deleteMappingKey(mapping *yaml.Node, key string) {
	if mapping == nil || mapping.Kind != yaml.MappingNode {
		return
	}
	for i := 0; i+1 < len(mapping.Content); i += 2 {
		if mapping.Content[i].Value == key {
			mapping.Content = append(mapping.Content[:i], mapping.Content[i+2:]...)
			return
		}
	}
}

func managedPortOverrideServices(root *yaml.Node) map[string]struct{} {
	out := map[string]struct{}{}
	managed := mappingValue(root, sitectlDevPortsExtensionName)
	if managed == nil || managed.Kind != yaml.MappingNode {
		return out
	}
	for i := 0; i+1 < len(managed.Content); i += 2 {
		service := strings.TrimSpace(managed.Content[i].Value)
		if service != "" {
			out[service] = struct{}{}
		}
	}
	return out
}

func setManagedPortExtension(root *yaml.Node, desired map[string][]string) {
	managed := &yaml.Node{Kind: yaml.MappingNode, Tag: "!!map"}
	services := make([]string, 0, len(desired))
	for service := range desired {
		services = append(services, service)
	}
	sort.Strings(services)
	for _, service := range services {
		seq := &yaml.Node{Kind: yaml.SequenceNode, Tag: "!!seq"}
		for _, port := range desired[service] {
			seq.Content = append(seq.Content, quotedStringNode(port))
		}
		managed.Content = append(managed.Content, &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: service}, seq)
	}
	setMappingValue(root, sitectlDevPortsExtensionName, managed)
}

func setMappingValue(mapping *yaml.Node, key string, value *yaml.Node) {
	for i := 0; i+1 < len(mapping.Content); i += 2 {
		if mapping.Content[i].Value == key {
			mapping.Content[i+1] = value
			return
		}
	}
	mapping.Content = append(mapping.Content, &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: key}, value)
}

func setPortsOverride(serviceNode *yaml.Node, ports []string) {
	seq := &yaml.Node{Kind: yaml.SequenceNode, Tag: "!override"}
	for _, port := range ports {
		seq.Content = append(seq.Content, quotedStringNode(port))
	}
	setMappingValue(serviceNode, "ports", seq)
}

func quotedStringNode(value string) *yaml.Node {
	return &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: value, Style: yaml.DoubleQuotedStyle}
}

func (c Context) composeOverrideHostPorts() map[int]int {
	path := c.ResolveProjectPath(LocalDevComposeOverrideName)
	data, err := os.ReadFile(path) // #nosec G304 -- local project override path is derived from the context.
	if err != nil || len(strings.TrimSpace(string(data))) == 0 {
		return nil
	}
	var doc yaml.Node
	if err := yaml.Unmarshal(data, &doc); err != nil {
		return nil
	}
	root := documentMapping(&doc)
	services := mappingValue(root, "services")
	if services == nil || services.Kind != yaml.MappingNode {
		return nil
	}
	out := map[int]int{}
	for i := 0; i+1 < len(services.Content); i += 2 {
		service := services.Content[i+1]
		if service.Kind != yaml.MappingNode {
			continue
		}
		for target, host := range hostPortsFromPortsNode(mappingValue(service, "ports")) {
			out[target] = host
		}
	}
	return out
}

func hostPortsFromPortsNode(node *yaml.Node) map[int]int {
	out := map[int]int{}
	if node == nil || node.Kind != yaml.SequenceNode {
		return out
	}
	for _, item := range node.Content {
		if item.Kind != yaml.ScalarNode {
			continue
		}
		host, target := hostAndTargetPort(item.Value)
		if host != 0 && target != 0 {
			out[target] = host
		}
	}
	return out
}

func hostAndTargetPort(value string) (int, int) {
	value = strings.TrimSpace(strings.Trim(value, `"'`))
	if value == "" {
		return 0, 0
	}
	value = strings.Split(value, "/")[0]
	parts := strings.Split(value, ":")
	if len(parts) < 2 {
		target, err := strconv.Atoi(strings.TrimSpace(parts[0]))
		if err != nil {
			return 0, 0
		}
		return target, target
	}
	host, err := strconv.Atoi(strings.TrimSpace(parts[len(parts)-2]))
	if err != nil {
		return 0, 0
	}
	target, err := strconv.Atoi(strings.TrimSpace(parts[len(parts)-1]))
	if err != nil {
		return 0, 0
	}
	return host, target
}

func localDevFallbackPort(target int) (int, bool) {
	switch target {
	case defaultHTTPPort:
		return defaultHTTPFallback, true
	case defaultHTTPSPort:
		return defaultHTTPSFallback, true
	default:
		return 0, false
	}
}

func localDevSiteURL(projectEnv map[string]string, scheme string, hostPorts map[int]int) string {
	scheme = strings.TrimSpace(scheme)
	if scheme == "" {
		scheme = "http"
	}
	domain := firstNonEmptyEnv(projectEnv, domainEnv, "localhost")
	target := defaultHTTPPort
	if scheme == "https" {
		target = defaultHTTPSPort
	}
	port := strconv.Itoa(target)
	if hostPorts[target] != 0 {
		port = strconv.Itoa(hostPorts[target])
	}
	host := domain
	if !isDefaultPort(scheme, port) && !strings.Contains(domain, ":") {
		host = domain + ":" + port
	}
	return (&url.URL{Scheme: scheme, Host: host, Path: "/"}).String()
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
