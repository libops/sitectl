package healthcheck

import (
	"bytes"
	"fmt"
	"net/url"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"

	"github.com/libops/sitectl/pkg/config"
	"github.com/libops/sitectl/pkg/yamlnode"
	yaml "gopkg.in/yaml.v3"
)

const (
	defaultTraefikService       = "traefik"
	defaultTraefikProviderDir   = "/etc/traefik/dynamic"
	defaultTraefikConfigDir     = "conf/traefik"
	defaultTraefikHTTPPort      = 80
	defaultTraefikHTTPSPort     = 443
	traefikDockerProviderSuffix = "@docker"
)

var (
	traefikHostCallPattern       = regexp.MustCompile(`\bHost\(([^)]*)\)`)
	traefikPathCallPattern       = regexp.MustCompile(`\bPath(?:Prefix)?\(([^)]*)\)`)
	traefikQuotedArgumentPattern = regexp.MustCompile("`([^`]*)`|'([^']*)'|\"([^\"]*)\"")
)

// TraefikRouteOptions describes the application route to resolve from Traefik
// dynamic configuration and the Compose Traefik service that owns it.
type TraefikRouteOptions struct {
	AppService     string
	Router         string
	TraefikService string
	DefaultScheme  string
	DefaultDomain  string
}

type traefikComposeRouteConfig struct {
	EntryPointPorts map[string]int
	ProviderTargets []traefikProviderTarget
	Mounts          map[string]string
}

type traefikProviderTarget struct {
	Target    string
	Directory bool
}

type traefikDynamicRouter struct {
	Name        string
	Rule        string
	Service     string
	EntryPoints []string
	TLS         bool
	Priority    int
	File        string
}

// PublicURLFromTraefik resolves a public application URL from Traefik router
// configuration. It reads the Traefik service's file provider mount, dynamic
// routers, entrypoints, and Compose-published ports from the supplied context.
func PublicURLFromTraefik(ctx *config.Context, opts TraefikRouteOptions) (string, bool, error) {
	if ctx == nil {
		return "", false, nil
	}
	appService := strings.TrimSpace(opts.AppService)
	if appService == "" {
		return "", false, nil
	}
	composeConfig, err := readTraefikComposeRouteConfig(ctx, firstNonEmpty(opts.TraefikService, defaultTraefikService))
	if err != nil {
		return "", false, err
	}
	files, err := traefikDynamicConfigFiles(ctx, composeConfig)
	if err != nil {
		return "", false, err
	}
	routers, err := readTraefikDynamicRouters(ctx, files)
	if err != nil {
		return "", false, err
	}
	router, ok := chooseTraefikRouter(routers, appService, opts.Router)
	if !ok {
		return "", false, nil
	}
	host := traefikRuleHost(router.Rule)
	if host == "" {
		host = strings.TrimSpace(opts.DefaultDomain)
	}
	if host == "" {
		return "", false, nil
	}
	scheme, targetPort := traefikRouterSchemeAndPort(router, composeConfig, opts.DefaultScheme)
	if targetPort != 0 {
		if hostPort, ok := ctx.ComposePublishedHostPort(targetPort); ok && !isDefaultPort(scheme, strconv.Itoa(hostPort)) && !strings.Contains(host, ":") {
			host = host + ":" + strconv.Itoa(hostPort)
		}
	}
	path := traefikRulePath(router.Rule)
	return (&url.URL{Scheme: scheme, Host: host, Path: path}).String(), true, nil
}

func readTraefikComposeRouteConfig(ctx *config.Context, traefikService string) (traefikComposeRouteConfig, error) {
	out := traefikComposeRouteConfig{
		EntryPointPorts: map[string]int{},
		Mounts:          map[string]string{},
	}
	for _, path := range traefikComposeFiles(ctx) {
		exists, err := ctx.FileExists(path)
		if err != nil {
			return traefikComposeRouteConfig{}, err
		}
		if !exists {
			continue
		}
		data, err := ctx.ReadFile(path)
		if err != nil {
			return traefikComposeRouteConfig{}, err
		}
		if err := mergeTraefikComposeRouteConfig(&out, data, traefikService); err != nil {
			return traefikComposeRouteConfig{}, fmt.Errorf("parse Traefik compose config %q: %w", path, err)
		}
	}
	if len(out.ProviderTargets) == 0 {
		out.ProviderTargets = append(out.ProviderTargets, traefikProviderTarget{Target: defaultTraefikProviderDir, Directory: true})
	}
	return out, nil
}

func traefikComposeFiles(ctx *config.Context) []string {
	if ctx == nil {
		return nil
	}
	seen := map[string]bool{}
	out := []string{}
	add := func(path string) {
		path = strings.TrimSpace(path)
		if path == "" {
			return
		}
		path = ctx.ResolveProjectPath(path)
		if seen[path] {
			return
		}
		seen[path] = true
		out = append(out, path)
	}
	if len(ctx.ComposeFile) > 0 {
		for _, file := range ctx.ComposeFile {
			add(file)
		}
		return out
	}
	for _, file := range []string{
		"docker-compose.yml",
		"docker-compose.yaml",
		"compose.yml",
		"compose.yaml",
		"compose.override.yml",
		"compose.override.yaml",
		"docker-compose.override.yml",
		"docker-compose.override.yaml",
	} {
		add(file)
	}
	return out
}

func mergeTraefikComposeRouteConfig(out *traefikComposeRouteConfig, data []byte, traefikService string) error {
	var doc yaml.Node
	if err := yaml.Unmarshal(data, &doc); err != nil {
		return err
	}
	root := yamlnode.DocumentMapping(&doc)
	services := yamlnode.MappingValue(root, "services")
	if services == nil || services.Kind != yaml.MappingNode {
		return nil
	}
	service := yamlnode.MappingValue(services, traefikService)
	if service == nil || service.Kind != yaml.MappingNode {
		return nil
	}
	for _, value := range yamlnode.StringFieldValues(yamlnode.MappingValue(service, "command")) {
		mergeTraefikCommandValue(out, value)
	}
	for target, source := range traefikVolumeMounts(yamlnode.MappingValue(service, "volumes")) {
		out.Mounts[target] = source
	}
	return nil
}

func mergeTraefikCommandValue(out *traefikComposeRouteConfig, value string) {
	for _, field := range strings.Fields(value) {
		lower := strings.ToLower(strings.TrimSpace(field))
		switch {
		case strings.HasPrefix(lower, "--entrypoints.") && strings.Contains(lower, ".address="):
			entrypoint, port := traefikCommandEntrypoint(field)
			if entrypoint != "" && port != 0 {
				out.EntryPointPorts[entrypoint] = port
			}
		case strings.HasPrefix(lower, "--providers.file.directory="):
			target := strings.TrimSpace(field[strings.Index(field, "=")+1:])
			if target != "" {
				out.ProviderTargets = appendUniqueTraefikProviderTarget(out.ProviderTargets, traefikProviderTarget{Target: target, Directory: true})
			}
		case strings.HasPrefix(lower, "--providers.file.filename="):
			target := strings.TrimSpace(field[strings.Index(field, "=")+1:])
			if target != "" {
				out.ProviderTargets = appendUniqueTraefikProviderTarget(out.ProviderTargets, traefikProviderTarget{Target: target})
			}
		}
	}
}

func traefikCommandEntrypoint(field string) (string, int) {
	lower := strings.ToLower(field)
	prefix := "--entrypoints."
	start := strings.Index(lower, prefix)
	if start < 0 {
		return "", 0
	}
	rest := field[start+len(prefix):]
	lowerRest := lower[start+len(prefix):]
	addressIdx := strings.Index(lowerRest, ".address=")
	if addressIdx < 0 {
		return "", 0
	}
	entrypoint := strings.TrimSpace(rest[:addressIdx])
	address := strings.TrimSpace(rest[addressIdx+len(".address="):])
	if idx := strings.LastIndex(address, ":"); idx >= 0 {
		address = address[idx+1:]
	}
	port, err := strconv.Atoi(strings.TrimSpace(address))
	if err != nil {
		return "", 0
	}
	return entrypoint, port
}

func appendUniqueTraefikProviderTarget(targets []traefikProviderTarget, target traefikProviderTarget) []traefikProviderTarget {
	target.Target = cleanTraefikContainerPath(target.Target)
	if target.Target == "" {
		return targets
	}
	for _, existing := range targets {
		if existing.Target == target.Target && existing.Directory == target.Directory {
			return targets
		}
	}
	return append(targets, target)
}

func traefikVolumeMounts(node *yaml.Node) map[string]string {
	out := map[string]string{}
	if node == nil || node.Kind != yaml.SequenceNode {
		return out
	}
	for _, item := range node.Content {
		source, target := traefikVolumeMount(item)
		if source == "" || target == "" {
			continue
		}
		out[cleanTraefikContainerPath(target)] = source
	}
	return out
}

func traefikVolumeMount(node *yaml.Node) (string, string) {
	if node == nil {
		return "", ""
	}
	switch node.Kind {
	case yaml.ScalarNode:
		parts := strings.Split(strings.TrimSpace(strings.Trim(node.Value, `"`)), ":")
		if len(parts) < 2 {
			return "", ""
		}
		return strings.TrimSpace(parts[0]), strings.TrimSpace(parts[1])
	case yaml.MappingNode:
		return yamlnode.ScalarValue(yamlnode.MappingValue(node, "source")), yamlnode.ScalarValue(yamlnode.MappingValue(node, "target"))
	default:
		return "", ""
	}
}

func traefikDynamicConfigFiles(ctx *config.Context, composeConfig traefikComposeRouteConfig) ([]string, error) {
	seen := map[string]bool{}
	out := []string{}
	addFile := func(path string) {
		path = strings.TrimSpace(path)
		if path == "" || seen[path] {
			return
		}
		seen[path] = true
		out = append(out, path)
	}
	for _, target := range composeConfig.ProviderTargets {
		hostPath := traefikProviderHostPath(ctx, composeConfig, target)
		if hostPath == "" {
			continue
		}
		if target.Directory {
			files, err := traefikDynamicConfigFilesInDir(ctx, hostPath)
			if err != nil {
				return nil, err
			}
			for _, file := range files {
				addFile(file)
			}
			continue
		}
		exists, err := ctx.FileExists(hostPath)
		if err != nil {
			return nil, err
		}
		if exists {
			addFile(hostPath)
		}
	}
	sort.Strings(out)
	return out, nil
}

func traefikProviderHostPath(ctx *config.Context, composeConfig traefikComposeRouteConfig, target traefikProviderTarget) string {
	containerTarget := cleanTraefikContainerPath(target.Target)
	if containerTarget == "" {
		return ""
	}
	mountTargets := make([]string, 0, len(composeConfig.Mounts))
	for mountTarget := range composeConfig.Mounts {
		mountTargets = append(mountTargets, mountTarget)
	}
	sort.Slice(mountTargets, func(i, j int) bool {
		return len(mountTargets[i]) > len(mountTargets[j])
	})
	for _, mountTarget := range mountTargets {
		if containerTarget != mountTarget && !strings.HasPrefix(containerTarget, mountTarget+"/") {
			continue
		}
		source := composeConfig.Mounts[mountTarget]
		suffix := strings.TrimPrefix(containerTarget, mountTarget)
		return resolveTraefikMountSource(ctx, source, suffix)
	}
	if containerTarget == defaultTraefikProviderDir {
		return ctx.ResolveProjectPath(filepath.FromSlash(defaultTraefikConfigDir))
	}
	return ""
}

func resolveTraefikMountSource(ctx *config.Context, source, suffix string) string {
	source = strings.TrimSpace(source)
	if source == "" || strings.HasPrefix(source, "$") {
		return ""
	}
	suffix = strings.TrimPrefix(cleanTraefikContainerPath(suffix), "/")
	if filepath.IsAbs(source) {
		return filepath.Join(source, filepath.FromSlash(suffix))
	}
	return ctx.ResolveProjectPath(filepath.Join(filepath.FromSlash(source), filepath.FromSlash(suffix)))
}

func traefikDynamicConfigFilesInDir(ctx *config.Context, dir string) ([]string, error) {
	exists, err := ctx.FileExists(dir)
	if err != nil {
		return nil, err
	}
	if !exists {
		return nil, nil
	}
	entries, err := ctx.ListFiles(dir)
	if err != nil {
		return nil, err
	}
	out := []string{}
	for _, entry := range entries {
		name := filepath.Base(entry)
		switch strings.ToLower(filepath.Ext(name)) {
		case ".yml", ".yaml":
			out = append(out, filepath.Join(dir, filepath.FromSlash(entry)))
		}
	}
	return out, nil
}

func readTraefikDynamicRouters(ctx *config.Context, files []string) ([]traefikDynamicRouter, error) {
	routers := []traefikDynamicRouter{}
	for _, file := range files {
		data, err := ctx.ReadFile(file)
		if err != nil {
			return nil, err
		}
		parsed, err := parseTraefikDynamicRouters(data, file)
		if err != nil {
			return nil, err
		}
		routers = append(routers, parsed...)
	}
	return routers, nil
}

func parseTraefikDynamicRouters(data []byte, file string) ([]traefikDynamicRouter, error) {
	if !bytes.Contains(data, []byte("routers:")) {
		return nil, nil
	}
	var doc yaml.Node
	if err := yaml.Unmarshal(data, &doc); err != nil {
		return nil, fmt.Errorf("parse Traefik dynamic config %q: %w", file, err)
	}
	root := yamlnode.DocumentMapping(&doc)
	httpConfig := yamlnode.MappingValue(root, "http")
	routersNode := yamlnode.MappingValue(httpConfig, "routers")
	if routersNode == nil || routersNode.Kind != yaml.MappingNode {
		return nil, nil
	}
	routers := []traefikDynamicRouter{}
	for i := 0; i+1 < len(routersNode.Content); i += 2 {
		name := strings.TrimSpace(routersNode.Content[i].Value)
		routerNode := routersNode.Content[i+1]
		if name == "" || routerNode.Kind != yaml.MappingNode {
			continue
		}
		routers = append(routers, traefikDynamicRouter{
			Name:        name,
			Rule:        yamlnode.ScalarValue(yamlnode.MappingValue(routerNode, "rule")),
			Service:     yamlnode.ScalarValue(yamlnode.MappingValue(routerNode, "service")),
			EntryPoints: yamlnode.StringValues(yamlnode.MappingValue(routerNode, "entryPoints")),
			TLS:         traefikRouterHasTLS(yamlnode.MappingValue(routerNode, "tls")),
			Priority:    yamlnode.IntValue(yamlnode.MappingValue(routerNode, "priority")),
			File:        file,
		})
	}
	return routers, nil
}

func chooseTraefikRouter(routers []traefikDynamicRouter, appService, preferredRouter string) (traefikDynamicRouter, bool) {
	type candidate struct {
		router   traefikDynamicRouter
		score    int
		pathless bool
	}
	candidates := []candidate{}
	for _, router := range routers {
		if traefikRuleHost(router.Rule) == "" {
			continue
		}
		score := traefikRouterScore(router, appService, preferredRouter)
		if score == 0 {
			continue
		}
		candidates = append(candidates, candidate{
			router:   router,
			score:    score,
			pathless: !traefikRuleHasPath(router.Rule),
		})
	}
	if len(candidates) == 0 {
		return traefikDynamicRouter{}, false
	}
	sort.SliceStable(candidates, func(i, j int) bool {
		if candidates[i].score != candidates[j].score {
			return candidates[i].score > candidates[j].score
		}
		if candidates[i].pathless != candidates[j].pathless {
			return candidates[i].pathless
		}
		if candidates[i].router.Priority != candidates[j].router.Priority {
			return candidates[i].router.Priority > candidates[j].router.Priority
		}
		return candidates[i].router.Name < candidates[j].router.Name
	})
	return candidates[0].router, true
}

func traefikRouterScore(router traefikDynamicRouter, appService, preferredRouter string) int {
	appService = strings.TrimSpace(appService)
	preferredRouter = strings.TrimSpace(preferredRouter)
	routerService := traefikRouterServiceName(router.Service)
	switch {
	case preferredRouter != "" && router.Name == preferredRouter:
		return 100
	case routerService == appService:
		return 90
	case router.Name == appService || router.Name == appService+"-web":
		return 85
	case hasTraefikRoutePrefix(routerService, appService) || hasTraefikRoutePrefix(router.Name, appService):
		return 70
	case !traefikRuleHasPath(router.Rule) && !strings.Contains(router.Service, "@"):
		return 40
	case !strings.Contains(router.Service, "@"):
		return 10
	default:
		return 0
	}
}

func traefikRouterServiceName(service string) string {
	service = strings.TrimSpace(service)
	service = strings.TrimSuffix(service, traefikDockerProviderSuffix)
	if before, _, ok := strings.Cut(service, "@"); ok {
		return before
	}
	return service
}

func hasTraefikRoutePrefix(value, prefix string) bool {
	value = strings.TrimSpace(value)
	prefix = strings.TrimSpace(prefix)
	if value == "" || prefix == "" || value == prefix {
		return false
	}
	return strings.HasPrefix(value, prefix+"-") || strings.HasPrefix(value, prefix+"_") || strings.HasPrefix(value, prefix+".")
}

func traefikRuleHost(rule string) string {
	return firstTraefikQuotedArgument(traefikHostCallPattern.FindStringSubmatch(rule))
}

func traefikRulePath(rule string) string {
	path := firstTraefikQuotedArgument(traefikPathCallPattern.FindStringSubmatch(rule))
	if !strings.HasPrefix(path, "/") {
		return "/"
	}
	return path
}

func traefikRuleHasPath(rule string) bool {
	return traefikRulePath(rule) != "/"
}

func firstTraefikQuotedArgument(match []string) string {
	if len(match) < 2 {
		return ""
	}
	args := traefikQuotedArgumentPattern.FindStringSubmatch(match[1])
	for _, arg := range args[1:] {
		if strings.TrimSpace(arg) != "" {
			return strings.TrimSpace(arg)
		}
	}
	return ""
}

func traefikRouterSchemeAndPort(router traefikDynamicRouter, composeConfig traefikComposeRouteConfig, defaultScheme string) (string, int) {
	if router.TLS {
		return "https", defaultTraefikHTTPSPort
	}
	for _, entrypoint := range router.EntryPoints {
		port := composeConfig.EntryPointPorts[entrypoint]
		switch port {
		case defaultTraefikHTTPSPort:
			return "https", port
		case defaultTraefikHTTPPort:
			return "http", port
		}
	}
	scheme := firstNonEmpty(defaultScheme, "http")
	if strings.EqualFold(scheme, "https") {
		return "https", defaultTraefikHTTPSPort
	}
	return "http", defaultTraefikHTTPPort
}

func cleanTraefikContainerPath(path string) string {
	path = strings.TrimSpace(path)
	if path == "" {
		return ""
	}
	return "/" + strings.Trim(strings.ReplaceAll(path, "\\", "/"), "/")
}

func traefikRouterHasTLS(node *yaml.Node) bool {
	if node == nil {
		return false
	}
	return node.Kind != yaml.ScalarNode || !strings.EqualFold(strings.TrimSpace(node.Value), "false")
}
