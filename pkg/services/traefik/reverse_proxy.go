package traefik

import (
	"context"
	"fmt"
	"net/netip"
	"path/filepath"
	"strings"

	corecomponent "github.com/libops/sitectl/pkg/component"
	"github.com/libops/sitectl/pkg/config"
	yaml "gopkg.in/yaml.v3"
)

const (
	ReverseProxyName          = "reverse-proxy"
	reverseProxyTrustedIPName = "trusted-ip"
	defaultTrustedIPLimit     = 3
)

type ReverseProxyOptions struct {
	AppService     string
	NoAppService   bool
	TraefikService string
	Entrypoints    []string
	TrustedIPLimit int
}

type ReverseProxyInspection struct {
	ComposeFile   string
	Traefik       map[string][]string
	NginxServices map[string]NginxRealIPConfig
}

type NginxRealIPConfig struct {
	Service   string
	TrustedIP []string
	Recursive string
}

func ReverseProxy(opts ReverseProxyOptions) (corecomponent.ComposeServiceComponent, error) {
	opts = normalizeReverseProxyOptions(opts)
	definitionOnRules := []corecomponent.YAMLRule{{
		Files: []string{"docker-compose.yml", "docker-compose.yaml", "compose.yml", "compose.yaml"},
		Op:    corecomponent.OpContains,
		Path:  ".services." + opts.TraefikService + ".command",
		Value: "forwardedHeaders.trustedIPs=",
	}}
	definitionOffRules := []corecomponent.YAMLRule{{
		Files: []string{"docker-compose.yml", "docker-compose.yaml", "compose.yml", "compose.yaml"},
		Op:    corecomponent.OpNotContains,
		Path:  ".services." + opts.TraefikService + ".command",
		Value: "forwardedHeaders.trustedIPs=",
	}}
	if strings.TrimSpace(opts.AppService) != "" {
		definitionOnRules = append(definitionOnRules, corecomponent.YAMLRule{
			Files: []string{"docker-compose.yml", "docker-compose.yaml", "compose.yml", "compose.yaml"},
			Op:    corecomponent.OpRestore,
			Path:  ".services." + opts.AppService + ".environment.NGINX_SET_REAL_IP_FROM",
		})
		definitionOffRules = append(definitionOffRules, corecomponent.YAMLRule{
			Files: []string{"docker-compose.yml", "docker-compose.yaml", "compose.yml", "compose.yaml"},
			Op:    corecomponent.OpDelete,
			Path:  ".services." + opts.AppService + ".environment.NGINX_SET_REAL_IP_FROM",
		})
	}
	return corecomponent.NewComposeServiceComponent(corecomponent.ComposeServiceComponentOptions{
		Name:                ReverseProxyName,
		DefaultState:        corecomponent.StateOff,
		DefaultDisposition:  corecomponent.DispositionDisabled,
		AllowedDispositions: []corecomponent.Disposition{corecomponent.DispositionDisabled, corecomponent.DispositionEnabled},
		Guidance: corecomponent.StateGuidance{
			EnabledHelp:  "Traefik trusts X-Forwarded-For from an upstream load balancer, and the app nginx service trusts the same source IPs.",
			DisabledHelp: "Traefik does not trust upstream forwarded headers from a load balancer.",
			Question:     "Is this application behind a trusted reverse proxy or load balancer?",
		},
		FollowUps: []corecomponent.FollowUpSpec{{
			Name:                 reverseProxyTrustedIPName,
			Label:                "Trusted proxy IPs",
			FlagName:             "trusted-ip",
			FlagUsage:            "Trusted upstream proxy IP/CIDR; may be passed more than once.",
			Question:             "Enter the trusted upstream proxy IP/CIDR values, separated by commas.",
			MultiValue:           true,
			Required:             true,
			PromptOnCreate:       true,
			AppliesToDisposition: corecomponent.DispositionEnabled,
		}},
		DefinitionOnRules:  definitionOnRules,
		DefinitionOffRules: definitionOffRules,
		AfterEnableOptions: func(values map[string]string) []corecomponent.Hook {
			return []corecomponent.Hook{func(_ context.Context, runtime *corecomponent.Runtime) error {
				return applyReverseProxy(runtime.Context, opts, values)
			}}
		},
		AfterDisable: []corecomponent.Hook{func(_ context.Context, runtime *corecomponent.Runtime) error {
			return removeReverseProxy(runtime.Context, opts)
		}},
		Behavior: corecomponent.Behavior{
			Idempotent: true,
			Enable: corecomponent.TransitionBehavior{
				DataMigration: corecomponent.DataMigrationNone,
				Summary:       "Adds Traefik forwarded-header trust and matching nginx real-IP settings.",
			},
			Disable: corecomponent.TransitionBehavior{
				DataMigration: corecomponent.DataMigrationNone,
				Summary:       "Removes Traefik forwarded-header trust and nginx real-IP overrides.",
			},
		},
	})
}

func normalizeReverseProxyOptions(opts ReverseProxyOptions) ReverseProxyOptions {
	if opts.NoAppService {
		opts.AppService = ""
	} else if strings.TrimSpace(opts.AppService) == "" {
		opts.AppService = "drupal"
	}
	if strings.TrimSpace(opts.TraefikService) == "" {
		opts.TraefikService = "traefik"
	}
	if len(opts.Entrypoints) == 0 {
		opts.Entrypoints = []string{"http", "https", "web", "websecure"}
	}
	if opts.TrustedIPLimit == 0 {
		opts.TrustedIPLimit = defaultTrustedIPLimit
	}
	return opts
}

func applyReverseProxy(ctx *config.Context, opts ReverseProxyOptions, values map[string]string) error {
	trustedIPs := corecomponent.SplitFollowUpValues(values[reverseProxyTrustedIPName])
	if len(trustedIPs) == 0 {
		return fmt.Errorf("--trusted-ip is required when enabling %s", ReverseProxyName)
	}
	if opts.TrustedIPLimit > 0 && len(trustedIPs) > opts.TrustedIPLimit {
		return fmt.Errorf("%s supports at most %d trusted IP values because nginx exposes NGINX_SET_REAL_IP_FROM through NGINX_SET_REAL_IP_FROM%d", ReverseProxyName, opts.TrustedIPLimit, opts.TrustedIPLimit)
	}
	for _, value := range trustedIPs {
		if err := validateTrustedIP(value); err != nil {
			return err
		}
	}

	compose, err := corecomponent.LoadComposeFile(composePathForContext(ctx))
	if err != nil {
		return err
	}
	if err := removeTraefikTrustedIPCommands(compose, opts); err != nil {
		return err
	}
	entrypoints := activeEntrypoints(compose, opts)
	if len(entrypoints) == 0 {
		return fmt.Errorf("service %q does not declare a Traefik entrypoint address", opts.TraefikService)
	}
	joined := strings.Join(trustedIPs, ",")
	for _, entrypoint := range entrypoints {
		if err := compose.AppendUniqueServiceString(opts.TraefikService, "command", fmt.Sprintf("--entryPoints.%s.forwardedHeaders.trustedIPs=%s", entrypoint, joined)); err != nil {
			return err
		}
	}
	if opts.AppService != "" {
		for i := 1; i <= opts.TrustedIPLimit; i++ {
			key := "NGINX_SET_REAL_IP_FROM"
			if i > 1 {
				key = fmt.Sprintf("NGINX_SET_REAL_IP_FROM%d", i)
			}
			if i <= len(trustedIPs) {
				if err := compose.SetServiceEnv(opts.AppService, key, trustedIPs[i-1]); err != nil {
					return err
				}
			} else if err := compose.DeleteServiceEnv(opts.AppService, key); err != nil {
				return err
			}
		}
		if err := compose.SetServiceEnv(opts.AppService, "NGINX_REAL_IP_RECURSIVE", "on"); err != nil {
			return err
		}
	}
	return compose.Save()
}

func removeReverseProxy(ctx *config.Context, opts ReverseProxyOptions) error {
	compose, err := corecomponent.LoadComposeFile(composePathForContext(ctx))
	if err != nil {
		return err
	}
	if err := removeTraefikTrustedIPCommands(compose, opts); err != nil {
		return err
	}
	if opts.AppService != "" {
		for i := 1; i <= opts.TrustedIPLimit; i++ {
			key := "NGINX_SET_REAL_IP_FROM"
			if i > 1 {
				key = fmt.Sprintf("NGINX_SET_REAL_IP_FROM%d", i)
			}
			if err := compose.DeleteServiceEnv(opts.AppService, key); err != nil {
				return err
			}
		}
		if err := compose.DeleteServiceEnv(opts.AppService, "NGINX_REAL_IP_RECURSIVE"); err != nil {
			return err
		}
	}
	return compose.Save()
}

func removeTraefikTrustedIPCommands(compose *corecomponent.ComposeFile, opts ReverseProxyOptions) error {
	for _, entrypoint := range opts.Entrypoints {
		for _, prefix := range []string{
			fmt.Sprintf("--entryPoints.%s.forwardedHeaders.trustedIPs=", entrypoint),
			fmt.Sprintf("--entrypoints.%s.forwardedHeaders.trustedIPs=", entrypoint),
		} {
			if err := compose.RemoveServiceStringsByPrefix(opts.TraefikService, "command", prefix); err != nil {
				return err
			}
		}
	}
	return nil
}

func activeEntrypoints(compose *corecomponent.ComposeFile, opts ReverseProxyOptions) []string {
	block, ok := compose.ServiceBlock(opts.TraefikService)
	if !ok {
		return nil
	}
	lower := strings.ToLower(block)
	out := []string{}
	for _, entrypoint := range opts.Entrypoints {
		entrypoint = strings.TrimSpace(entrypoint)
		if entrypoint == "" {
			continue
		}
		needle := fmt.Sprintf("--entrypoints.%s.address=", strings.ToLower(entrypoint))
		if strings.Contains(lower, needle) {
			out = append(out, entrypoint)
		}
	}
	return out
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

func InspectReverseProxy(ctx *config.Context) (ReverseProxyInspection, error) {
	if ctx == nil {
		return ReverseProxyInspection{}, fmt.Errorf("context is nil")
	}
	path := composePathForContext(ctx)
	data, err := ctx.ReadFile(path)
	if err != nil {
		return ReverseProxyInspection{}, fmt.Errorf("read compose file %q: %w", path, err)
	}
	return inspectReverseProxyCompose(path, data)
}

func inspectReverseProxyCompose(path string, data []byte) (ReverseProxyInspection, error) {
	var root any
	if err := yaml.Unmarshal(data, &root); err != nil {
		return ReverseProxyInspection{}, fmt.Errorf("parse compose file %q: %w", path, err)
	}
	inspection := ReverseProxyInspection{
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
