package plugin

import (
	"errors"
	"fmt"
	"net/url"
	"path"
	"strings"

	"github.com/libops/sitectl/pkg/config"
	corehealthcheck "github.com/libops/sitectl/pkg/healthcheck"
	"github.com/spf13/cobra"
)

var (
	// ErrIngressRouteNotFound means the requested named route is not present in
	// the provider's route catalog.
	ErrIngressRouteNotFound = errors.New("ingress route not found")
	// ErrIngressRouteURLUnresolved means a route exists but neither Traefik nor
	// its catalog defaults provide a usable public URL.
	ErrIngressRouteURLUnresolved = errors.New("ingress route URL unresolved")
)

// IngressRouteResolution identifies the source used for a resolved route URL.
type IngressRouteResolution string

const (
	// IngressRouteResolutionTraefik indicates that the URL came from Traefik's
	// router configuration, including a local published port when configured.
	IngressRouteResolutionTraefik IngressRouteResolution = "traefik"
	// IngressRouteResolutionCatalog indicates that the URL came from the route
	// catalog's declared scheme, domain, and path.
	IngressRouteResolutionCatalog IngressRouteResolution = "catalog"
)

// ResolvedIngressRoute pairs a provider-owned route descriptor with its public
// URL. Route descriptors intentionally contain no authentication material.
type ResolvedIngressRoute struct {
	Route      IngressRoute
	URL        string
	Resolution IngressRouteResolution
}

// ResolveIngressRoute selects and resolves one route from a provider catalog.
// A non-empty name selects the first exact name match. An empty name selects
// the first explicitly primary route, or the first route when none is primary.
//
// Traefik configuration is authoritative when available. The catalog URL is
// returned as a fallback, including when Traefik inspection fails; in that
// case the fallback result is returned together with an error so callers can
// choose whether degraded resolution is acceptable.
func ResolveIngressRoute(ctx *config.Context, routes IngressRoutes, name string) (ResolvedIngressRoute, error) {
	route, err := selectIngressRoute(routes, name)
	if err != nil {
		return ResolvedIngressRoute{}, err
	}
	normalizeIngressRoute(&route, routes)

	fallbackURL, fallbackErr := ingressRouteCatalogURL(ctx, route)
	result := ResolvedIngressRoute{
		Route:      route,
		URL:        fallbackURL,
		Resolution: IngressRouteResolutionCatalog,
	}

	if ctx == nil {
		if fallbackErr != nil {
			return result, fallbackErr
		}
		return result, nil
	}

	resolvedURL, ok, resolveErr := corehealthcheck.PublicURLFromTraefik(ctx, corehealthcheck.TraefikRouteOptions{
		AppService:     route.Service,
		Router:         route.Router,
		TraefikService: route.TraefikService,
		DefaultScheme:  route.DefaultScheme,
		DefaultDomain:  route.DefaultDomain,
	})
	if resolveErr != nil {
		if fallbackErr != nil {
			return result, fmt.Errorf("%w for route %q: inspect Traefik: %w", ErrIngressRouteURLUnresolved, route.Name, resolveErr)
		}
		return result, fmt.Errorf("resolve ingress route %q from Traefik: %w", route.Name, resolveErr)
	}
	if ok {
		resolvedURL, err = composeIngressRoutePath(resolvedURL, route.Path)
		if err != nil {
			return result, fmt.Errorf("%w for route %q: %w", ErrIngressRouteURLUnresolved, route.Name, err)
		}
		result.URL = resolvedURL
		result.Resolution = IngressRouteResolutionTraefik
		return result, nil
	}
	if fallbackErr != nil {
		return result, fallbackErr
	}
	return result, nil
}

// ResolveIngressRouteFromProvider obtains a context-specific catalog from a
// provider and resolves its named or primary route.
func ResolveIngressRouteFromProvider(cmd *cobra.Command, ctx *config.Context, provider IngressRouteProvider, name string) (ResolvedIngressRoute, error) {
	if provider == nil {
		return ResolvedIngressRoute{}, fmt.Errorf("%w: ingress route provider is nil", ErrIngressRouteNotFound)
	}
	if cmd == nil {
		cmd = &cobra.Command{}
	}
	routes, err := provider.Routes(cmd, ctx)
	if err != nil {
		return ResolvedIngressRoute{}, fmt.Errorf("list ingress routes: %w", err)
	}
	return ResolveIngressRoute(ctx, routes, name)
}

func selectIngressRoute(routes IngressRoutes, name string) (IngressRoute, error) {
	name = strings.TrimSpace(name)
	if name != "" {
		for _, route := range routes.Routes {
			if strings.TrimSpace(route.Name) == name {
				return route, nil
			}
		}
		return IngressRoute{}, fmt.Errorf("%w: %q", ErrIngressRouteNotFound, name)
	}
	for _, route := range routes.Routes {
		if route.Primary {
			return route, nil
		}
	}
	if len(routes.Routes) == 0 {
		return IngressRoute{}, ErrIngressRouteNotFound
	}
	return routes.Routes[0], nil
}

func normalizeIngressRoute(route *IngressRoute, routes IngressRoutes) {
	route.Name = strings.TrimSpace(route.Name)
	route.Service = strings.TrimSpace(route.Service)
	route.Router = strings.TrimSpace(route.Router)
	route.TraefikService = strings.TrimSpace(route.TraefikService)
	route.DefaultScheme = firstIngressValue(route.DefaultScheme, routes.Scheme, "http")
	route.DefaultDomain = firstIngressValue(route.DefaultDomain, routes.Domain)
	route.Path = normalizeIngressPath(route.Path)
}

func ingressRouteCatalogURL(ctx *config.Context, route IngressRoute) (string, error) {
	domain := strings.TrimSpace(route.DefaultDomain)
	if domain == "" && ctx != nil && ctx.DockerHostType == config.ContextLocal {
		domain = "localhost"
	}
	if domain == "" {
		return "", fmt.Errorf("%w for route %q: catalog has no domain", ErrIngressRouteURLUnresolved, route.Name)
	}
	value := (&url.URL{
		Scheme: firstIngressValue(route.DefaultScheme, "http"),
		Host:   domain,
	}).String()
	resolved, err := composeIngressRoutePath(value, route.Path)
	if err != nil {
		return "", fmt.Errorf("%w for route %q: %w", ErrIngressRouteURLUnresolved, route.Name, err)
	}
	return resolved, nil
}

func composeIngressRoutePath(value, routePath string) (string, error) {
	parsed, err := url.Parse(strings.TrimSpace(value))
	if err != nil || parsed.Scheme == "" || parsed.Host == "" {
		return "", fmt.Errorf("invalid public URL %q", value)
	}
	routePath = normalizeIngressPath(routePath)
	if routePath == "" {
		return parsed.String(), nil
	}
	basePath := normalizeIngressPath(parsed.Path)
	switch {
	case basePath == "":
		parsed.Path = routePath
	case ingressPathContains(basePath, routePath):
		// The catalog path is already present in the discovered router path.
	case ingressPathContains(routePath, basePath):
		parsed.Path = routePath
	default:
		parsed.Path = path.Join(basePath, routePath)
	}
	parsed.RawPath = ""
	return parsed.String(), nil
}

func ingressPathContains(value, candidate string) bool {
	value = strings.Trim(value, "/")
	candidate = strings.Trim(candidate, "/")
	if value == "" || candidate == "" {
		return false
	}
	valueParts := strings.Split(value, "/")
	candidateParts := strings.Split(candidate, "/")
	if len(candidateParts) == 0 || len(candidateParts) > len(valueParts) {
		return false
	}
	for start := 0; start+len(candidateParts) <= len(valueParts); start++ {
		matched := true
		for i := range candidateParts {
			if valueParts[start+i] != candidateParts[i] {
				matched = false
				break
			}
		}
		if matched {
			return true
		}
	}
	return false
}

func normalizeIngressPath(value string) string {
	value = strings.TrimSpace(value)
	if value == "" || value == "/" {
		return ""
	}
	cleaned := path.Clean("/" + strings.TrimLeft(value, "/"))
	if cleaned == "/" {
		return ""
	}
	return cleaned
}
