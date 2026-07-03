package plugin

import (
	"fmt"
	"net/url"
	"strings"

	"github.com/libops/sitectl/pkg/config"
	"github.com/libops/sitectl/pkg/yamlnode"
	"github.com/spf13/cobra"
	yaml "gopkg.in/yaml.v3"
)

// IngressRoute describes one public route owned by a plugin.
type IngressRoute struct {
	Name           string `json:"name"`
	Service        string `json:"service"`
	Router         string `json:"router,omitempty"`
	TraefikService string `json:"traefik_service,omitempty"`
	DefaultScheme  string `json:"default_scheme,omitempty"`
	DefaultDomain  string `json:"default_domain,omitempty"`
	Path           string `json:"path,omitempty"`
	Primary        bool   `json:"primary,omitempty"`
}

// IngressRoutes is the plugin-owned route catalog for one context.
type IngressRoutes struct {
	Domain string         `json:"domain,omitempty"`
	Scheme string         `json:"scheme,omitempty"`
	Routes []IngressRoute `json:"routes,omitempty"`
}

// IngressRouteProvider returns plugin-owned public route descriptors.
type IngressRouteProvider interface {
	BindFlags(cmd *cobra.Command)
	Routes(cmd *cobra.Command, ctx *config.Context) (IngressRoutes, error)
}

// RegisterIngressRouteProvider registers plugin-owned public route discovery.
func (s *SDK) RegisterIngressRouteProvider(provider IngressRouteProvider) {
	if s == nil || provider == nil {
		return
	}
	cmd := &cobra.Command{
		Use:          "ingress-routes",
		Short:        "Internal ingress route discovery hook",
		Hidden:       true,
		SilenceUsage: true,
	}
	cmd.Flags().String("path", "", "Project path override")
	provider.BindFlags(cmd)
	s.ingressRoutesCmd = cmd
	s.ingressRouteProvider = provider
	s.hasIngressRoutes = true
}

// StaticIngressRoutes returns a route provider for plugins whose routes can be
// described without custom code. Optional routes are included only when their
// service is present in the active Compose project.
func StaticIngressRoutes(routes ...IngressRoute) IngressRouteProvider {
	return staticIngressRouteProvider{routes: append([]IngressRoute{}, routes...)}
}

// StandardComposeWebIngressRoutes returns a single primary app route provider
// for common Compose web applications whose router and service match.
func StandardComposeWebIngressRoutes(appService string) IngressRouteProvider {
	return StandardComposeWebIngressRoutesWithOptions(StandardComposeWebIngressOptions{
		AppService: appService,
	})
}

// StandardComposeWebIngressOptions configures route discovery for common
// Compose web applications.
type StandardComposeWebIngressOptions struct {
	AppService      string
	Router          string
	URLVariables    []string
	DomainVariables []string
	HTTPSVariables  []string
}

// StandardComposeWebIngressRoutesWithOptions returns a single primary app
// route provider that can use plugin-owned environment variables as fallback
// domain and scheme sources.
func StandardComposeWebIngressRoutesWithOptions(opts StandardComposeWebIngressOptions) IngressRouteProvider {
	return standardComposeWebIngressRouteProvider{opts: opts}
}

type standardComposeWebIngressRouteProvider struct {
	opts StandardComposeWebIngressOptions
}

func (p standardComposeWebIngressRouteProvider) BindFlags(cmd *cobra.Command) {}

func (p standardComposeWebIngressRouteProvider) Routes(cmd *cobra.Command, ctx *config.Context) (IngressRoutes, error) {
	appService := strings.TrimSpace(p.opts.AppService)
	if appService == "" {
		appService = "app"
	}
	router := strings.TrimSpace(p.opts.Router)
	if router == "" {
		router = appService
	}
	env, err := ContextServiceEnvironment(ctx, appService)
	if err != nil {
		return IngressRoutes{}, err
	}
	scheme := "http"
	domain := "localhost"
	usedIngressEnv := false
	if ingressScheme, ingressDomain, ok := ingressSchemeDomainFromEnv(env, scheme, domain); ok {
		scheme = ingressScheme
		domain = ingressDomain
		usedIngressEnv = true
	}
	if !usedIngressEnv {
		for _, key := range p.opts.URLVariables {
			parsedScheme, parsedDomain := schemeDomainFromURL(env[strings.TrimSpace(key)])
			if parsedDomain != "" {
				scheme = firstIngressValue(parsedScheme, scheme)
				domain = parsedDomain
				break
			}
		}
		if domain == "localhost" {
			for _, key := range p.opts.DomainVariables {
				if value := strings.TrimSpace(env[strings.TrimSpace(key)]); value != "" {
					domain = value
					break
				}
			}
		}
		for _, key := range p.opts.HTTPSVariables {
			if strings.EqualFold(strings.TrimSpace(env[strings.TrimSpace(key)]), "true") {
				scheme = "https"
				break
			}
		}
	}
	return IngressRoutes{
		Domain: domain,
		Scheme: scheme,
		Routes: []IngressRoute{{
			Name:          "app",
			Service:       appService,
			Router:        router,
			DefaultScheme: scheme,
			DefaultDomain: domain,
			Primary:       true,
		}},
	}, nil
}

type staticIngressRouteProvider struct {
	routes []IngressRoute
}

func (p staticIngressRouteProvider) BindFlags(cmd *cobra.Command) {}

func (p staticIngressRouteProvider) Routes(cmd *cobra.Command, ctx *config.Context) (IngressRoutes, error) {
	services, err := ContextComposeServices(ctx)
	if err != nil {
		return IngressRoutes{}, err
	}
	out := IngressRoutes{Routes: make([]IngressRoute, 0, len(p.routes))}
	for _, route := range p.routes {
		route.Name = strings.TrimSpace(route.Name)
		route.Service = strings.TrimSpace(route.Service)
		if route.Name == "" || route.Service == "" {
			continue
		}
		if len(services) > 0 && !services[route.Service] {
			continue
		}
		out.Routes = append(out.Routes, route)
	}
	return out, nil
}

// ContextComposeServices returns Compose service names visible from a context.
func ContextComposeServices(ctx *config.Context) (map[string]bool, error) {
	out := map[string]bool{}
	if ctx == nil {
		return out, nil
	}
	files := append([]string{}, ctx.ComposeFile...)
	if len(files) == 0 {
		files = []string{"docker-compose.yml", "docker-compose.yaml", "compose.yml", "compose.yaml"}
	}
	for _, file := range files {
		path := ctx.ResolveProjectPath(file)
		exists, err := ctx.FileExists(path)
		if err != nil {
			return nil, err
		}
		if !exists {
			continue
		}
		data, err := ctx.ReadSmallFile(path)
		if err != nil {
			return nil, fmt.Errorf("read compose file %q: %w", path, err)
		}
		mergeContextComposeServices(out, []byte(data))
	}
	return out, nil
}

// ContextServiceEnvironment returns the environment map for one Compose service.
func ContextServiceEnvironment(ctx *config.Context, serviceName string) (map[string]string, error) {
	out := map[string]string{}
	if ctx == nil {
		return out, nil
	}
	serviceName = strings.TrimSpace(serviceName)
	if serviceName == "" {
		return out, nil
	}
	files := append([]string{}, ctx.ComposeFile...)
	if len(files) == 0 {
		files = []string{"docker-compose.yml", "docker-compose.yaml", "compose.yml", "compose.yaml"}
	}
	for _, file := range files {
		path := ctx.ResolveProjectPath(file)
		exists, err := ctx.FileExists(path)
		if err != nil {
			return nil, err
		}
		if !exists {
			continue
		}
		data, err := ctx.ReadSmallFile(path)
		if err != nil {
			return nil, fmt.Errorf("read compose file %q: %w", path, err)
		}
		mergeContextServiceEnvironment(out, []byte(data), serviceName)
	}
	return out, nil
}

func mergeContextComposeServices(out map[string]bool, data []byte) {
	var doc yaml.Node
	if err := yaml.Unmarshal(data, &doc); err != nil {
		return
	}
	services := yamlnode.MappingValue(yamlnode.DocumentMapping(&doc), "services")
	if services == nil || services.Kind != yaml.MappingNode {
		return
	}
	for i := 0; i+1 < len(services.Content); i += 2 {
		name := strings.TrimSpace(services.Content[i].Value)
		if name != "" {
			out[name] = true
		}
	}
}

func mergeContextServiceEnvironment(out map[string]string, data []byte, serviceName string) {
	var doc yaml.Node
	if err := yaml.Unmarshal(data, &doc); err != nil {
		return
	}
	services := yamlnode.MappingValue(yamlnode.DocumentMapping(&doc), "services")
	service := yamlnode.MappingValue(services, serviceName)
	if service == nil || service.Kind != yaml.MappingNode {
		return
	}
	env := yamlnode.MappingValue(service, "environment")
	switch {
	case env == nil:
		return
	case env.Kind == yaml.MappingNode:
		for i := 0; i+1 < len(env.Content); i += 2 {
			key := strings.TrimSpace(env.Content[i].Value)
			value := strings.TrimSpace(env.Content[i+1].Value)
			if key != "" {
				out[key] = value
			}
		}
	case env.Kind == yaml.SequenceNode:
		for _, item := range env.Content {
			key, value, ok := strings.Cut(strings.TrimSpace(item.Value), "=")
			if ok && strings.TrimSpace(key) != "" {
				out[strings.TrimSpace(key)] = strings.TrimSpace(value)
			}
		}
	}
}

func schemeDomainFromURL(value string) (string, string) {
	parsed, err := url.Parse(strings.TrimSpace(value))
	if err != nil || strings.TrimSpace(parsed.Host) == "" {
		return "", ""
	}
	return strings.TrimSpace(parsed.Scheme), strings.TrimSpace(parsed.Host)
}

func ingressSchemeDomainFromEnv(env map[string]string, defaultScheme, defaultDomain string) (string, string, bool) {
	scheme := firstIngressValue(env["INGRESS_SCHEME"], defaultScheme, "http")
	domain := firstIngressHostname(env["INGRESS_HOSTNAMES"])
	if domain == "" {
		domain = firstIngressValue(defaultDomain, "localhost")
	}
	return scheme, domain, strings.TrimSpace(env["INGRESS_SCHEME"]) != "" || strings.TrimSpace(env["INGRESS_HOSTNAMES"]) != ""
}

func firstIngressHostname(value string) string {
	for _, hostname := range strings.Split(value, ",") {
		hostname = strings.TrimSpace(hostname)
		if hostname != "" {
			return hostname
		}
	}
	return ""
}

func firstIngressValue(values ...string) string {
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value != "" {
			return value
		}
	}
	return ""
}

func (s *SDK) runIngressRouteProvider(cmd *cobra.Command, provider IngressRouteProvider, params IngressRoutesParams) (IngressRoutes, error) {
	ctx, err := s.ingressRoutesContext(params)
	if err != nil {
		return IngressRoutes{}, err
	}
	return provider.Routes(cmd, ctx)
}

func (s *SDK) ingressRoutesContext(params IngressRoutesParams) (*config.Context, error) {
	if strings.TrimSpace(params.Path) != "" {
		return config.NewLocalProjectContext(params.Path, s.Metadata.Name)
	}
	return s.GetContext()
}
