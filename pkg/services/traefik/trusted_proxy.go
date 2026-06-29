package traefik

import (
	"fmt"
	"net/netip"
	"path/filepath"
	"strings"

	corecomponent "github.com/libops/sitectl/pkg/component"
	"github.com/libops/sitectl/pkg/config"
	yaml "gopkg.in/yaml.v3"
)

const defaultTrustedIPLimit = 3

// IngressInspection summarizes Traefik forwarded-header trust and matching nginx real-IP settings.
type IngressInspection struct {
	ComposeFile   string
	Traefik       map[string][]string
	NginxServices map[string]NginxRealIPConfig
}

// NginxRealIPConfig describes nginx real-IP environment settings for one service.
type NginxRealIPConfig struct {
	Service   string
	TrustedIP []string
	Recursive string
}

// InspectIngress reads the active compose file and extracts ingress trust settings.
func InspectIngress(ctx *config.Context) (IngressInspection, error) {
	if ctx == nil {
		return IngressInspection{}, fmt.Errorf("context is nil")
	}
	path := composePathForContext(ctx)
	data, err := ctx.ReadFile(path)
	if err != nil {
		return IngressInspection{}, fmt.Errorf("read compose file %q: %w", path, err)
	}
	return inspectIngressCompose(path, data)
}

func inspectIngressCompose(path string, data []byte) (IngressInspection, error) {
	var root any
	if err := yaml.Unmarshal(data, &root); err != nil {
		return IngressInspection{}, fmt.Errorf("parse compose file %q: %w", path, err)
	}
	inspection := IngressInspection{
		ComposeFile:   path,
		Traefik:       map[string][]string{},
		NginxServices: map[string]NginxRealIPConfig{},
	}
	services := asStringMap(asStringMap(root)["services"])
	for serviceName, rawService := range services {
		service := asStringMap(rawService)
		for _, command := range composeCommandStrings(service["command"]) {
			entrypoint, trustedIPs, ok := parseTraefikTrustedIPCommand(command)
			if !ok {
				continue
			}
			inspection.Traefik[entrypoint] = trustedIPs
		}
		env := composeEnvironment(service["environment"])
		realIP := nginxRealIPConfig(serviceName, env)
		if len(realIP.TrustedIP) > 0 || strings.TrimSpace(realIP.Recursive) != "" {
			inspection.NginxServices[serviceName] = realIP
		}
	}
	return inspection, nil
}

func validateTrustedIP(value string) error {
	value = strings.TrimSpace(value)
	if value == "" {
		return fmt.Errorf("trusted IP value cannot be empty")
	}
	if _, err := netip.ParsePrefix(value); err == nil {
		return nil
	}
	if _, err := netip.ParseAddr(value); err == nil {
		return nil
	}
	return fmt.Errorf("trusted IP %q is not a valid IP address or CIDR prefix", value)
}

func parseTraefikTrustedIPCommand(value string) (string, []string, bool) {
	value = strings.TrimSpace(value)
	if value == "" || !strings.HasPrefix(value, "--") {
		return "", nil, false
	}
	value = strings.TrimPrefix(value, "--")
	key, rawTrustedIPs, ok := strings.Cut(value, "=")
	if !ok {
		return "", nil, false
	}
	parts := strings.Split(key, ".")
	if len(parts) != 4 || !strings.EqualFold(parts[0], "entrypoints") || !strings.EqualFold(parts[2], "forwardedHeaders") || !strings.EqualFold(parts[3], "trustedIPs") {
		return "", nil, false
	}
	entrypoint := strings.TrimSpace(parts[1])
	if entrypoint == "" {
		return "", nil, false
	}
	return entrypoint, corecomponent.SplitFollowUpValues(rawTrustedIPs), true
}

func composeCommandStrings(value any) []string {
	switch typed := value.(type) {
	case nil:
		return nil
	case string:
		return strings.Fields(typed)
	case []any:
		out := []string{}
		for _, item := range typed {
			out = append(out, composeCommandStrings(item)...)
		}
		return out
	case []string:
		out := []string{}
		for _, item := range typed {
			out = append(out, composeCommandStrings(item)...)
		}
		return out
	default:
		return strings.Fields(fmt.Sprint(value))
	}
}

func composeEnvironment(value any) map[string]string {
	switch typed := value.(type) {
	case nil:
		return nil
	case map[string]any:
		out := map[string]string{}
		for key, value := range typed {
			out[key] = fmt.Sprint(value)
		}
		return out
	case map[any]any:
		out := map[string]string{}
		for key, value := range typed {
			out[fmt.Sprint(key)] = fmt.Sprint(value)
		}
		return out
	case []any:
		out := map[string]string{}
		for _, item := range typed {
			mergeEnvironmentString(out, fmt.Sprint(item))
		}
		return out
	case []string:
		out := map[string]string{}
		for _, item := range typed {
			mergeEnvironmentString(out, item)
		}
		return out
	default:
		out := map[string]string{}
		mergeEnvironmentString(out, fmt.Sprint(value))
		return out
	}
}

func mergeEnvironmentString(out map[string]string, value string) {
	key, envValue, ok := strings.Cut(value, "=")
	key = strings.TrimSpace(key)
	if key == "" {
		return
	}
	if !ok {
		out[key] = ""
		return
	}
	out[key] = strings.TrimSpace(envValue)
}

func nginxRealIPConfig(service string, env map[string]string) NginxRealIPConfig {
	cfg := NginxRealIPConfig{Service: service}
	for i := 1; i <= defaultTrustedIPLimit; i++ {
		key := "NGINX_SET_REAL_IP_FROM"
		if i > 1 {
			key = fmt.Sprintf("NGINX_SET_REAL_IP_FROM%d", i)
		}
		if value := strings.TrimSpace(env[key]); value != "" {
			cfg.TrustedIP = append(cfg.TrustedIP, value)
		}
	}
	cfg.Recursive = strings.TrimSpace(env["NGINX_REAL_IP_RECURSIVE"])
	return cfg
}

func asStringMap(value any) map[string]any {
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

func composePathForContext(ctx *config.Context) string {
	if ctx != nil {
		for _, file := range ctx.ComposeFile {
			if strings.TrimSpace(file) != "" {
				return ctx.ResolveProjectPath(file)
			}
		}
		for _, candidate := range []string{"docker-compose.yml", "docker-compose.yaml", "compose.yml", "compose.yaml"} {
			path := ctx.ResolveProjectPath(candidate)
			if exists, err := ctx.FileExists(path); err == nil && exists {
				return path
			}
		}
		if strings.TrimSpace(ctx.ProjectDir) != "" {
			return filepath.Join(ctx.ProjectDir, "docker-compose.yml")
		}
	}
	return "docker-compose.yml"
}
