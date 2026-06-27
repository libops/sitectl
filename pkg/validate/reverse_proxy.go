package validate

import (
	"fmt"
	"net/netip"
	"sort"
	"strings"

	corecomponent "github.com/libops/sitectl/pkg/component"
	"github.com/libops/sitectl/pkg/config"
	coretraefik "github.com/libops/sitectl/pkg/services/traefik"
)

func reverseProxyValidator(ctx *config.Context) ([]Result, error) {
	if ctx == nil {
		return nil, nil
	}
	inspection, err := coretraefik.InspectReverseProxy(ctx)
	if err != nil {
		return []Result{{
			Name:   "reverse-proxy",
			Status: StatusWarning,
			Detail: err.Error(),
		}}, nil
	}

	traefikValues, details, err := normalizedTraefikTrustedIPs(inspection.Traefik)
	if err != nil {
		return []Result{{
			Name:    "reverse-proxy",
			Status:  StatusFailed,
			Detail:  err.Error(),
			FixHint: "set reverse-proxy through sitectl component set or remove manual forwardedHeaders.trustedIPs flags",
		}}, nil
	}

	if len(traefikValues) == 0 && len(inspection.NginxServices) == 0 {
		return []Result{{
			Name:   "reverse-proxy",
			Status: StatusOK,
			Detail: "disabled",
		}}, nil
	}

	if len(traefikValues) == 0 {
		services := sortedNginxServiceNames(inspection.NginxServices)
		return []Result{{
			Name:    "reverse-proxy",
			Status:  StatusFailed,
			Detail:  fmt.Sprintf("nginx real-IP settings are present on %s but Traefik has no forwardedHeaders.trustedIPs flags", strings.Join(services, ", ")),
			FixHint: "set reverse-proxy through sitectl component set or remove the nginx real-IP overrides",
		}}, nil
	}

	if len(inspection.NginxServices) == 0 {
		return []Result{{
			Name:   "reverse-proxy",
			Status: StatusWarning,
			Detail: fmt.Sprintf("Traefik trusts %s; no nginx real-IP settings were present to compare", strings.Join(details, "; ")),
		}}, nil
	}

	for _, service := range sortedNginxServiceNames(inspection.NginxServices) {
		cfg := inspection.NginxServices[service]
		nginxValues := corecomponent.JoinFollowUpValues(cfg.TrustedIP)
		if err := validateTrustedIPs(cfg.TrustedIP); err != nil {
			return []Result{{
				Name:    "reverse-proxy",
				Status:  StatusFailed,
				Detail:  fmt.Sprintf("%s has invalid nginx real-IP settings: %v", service, err),
				FixHint: "set reverse-proxy through sitectl component set with valid IP or CIDR values",
			}}, nil
		}
		if !strings.EqualFold(strings.TrimSpace(cfg.Recursive), "on") {
			return []Result{{
				Name:    "reverse-proxy",
				Status:  StatusFailed,
				Detail:  fmt.Sprintf("%s has NGINX_REAL_IP_RECURSIVE=%q; expected \"on\"", service, cfg.Recursive),
				FixHint: "set reverse-proxy through sitectl component set so nginx and Traefik trust the same upstream proxies",
			}}, nil
		}
		if nginxValues != traefikValues {
			return []Result{{
				Name:    "reverse-proxy",
				Status:  StatusFailed,
				Detail:  fmt.Sprintf("%s trusts %s but Traefik trusts %s", service, nginxValues, traefikValues),
				FixHint: "set reverse-proxy through sitectl component set so nginx and Traefik trust the same upstream proxies",
			}}, nil
		}
	}

	return []Result{{
		Name:   "reverse-proxy",
		Status: StatusOK,
		Detail: fmt.Sprintf("enabled for %s", strings.Join(details, "; ")),
	}}, nil
}

func normalizedTraefikTrustedIPs(values map[string][]string) (string, []string, error) {
	entrypoints := make([]string, 0, len(values))
	for entrypoint := range values {
		entrypoints = append(entrypoints, entrypoint)
	}
	sort.Strings(entrypoints)

	var normalized string
	details := []string{}
	for _, entrypoint := range entrypoints {
		trustedIPs := values[entrypoint]
		if len(trustedIPs) == 0 {
			return "", nil, fmt.Errorf("traefik entrypoint %s has an empty trusted IP list", entrypoint)
		}
		if err := validateTrustedIPs(trustedIPs); err != nil {
			return "", nil, fmt.Errorf("traefik entrypoint %s has invalid trusted IPs: %w", entrypoint, err)
		}
		joined := corecomponent.JoinFollowUpValues(trustedIPs)
		if normalized == "" {
			normalized = joined
		} else if joined != normalized {
			return "", nil, fmt.Errorf("traefik trusted IPs differ across entrypoints: %s has %s, expected %s", entrypoint, joined, normalized)
		}
		details = append(details, fmt.Sprintf("%s=%s", entrypoint, joined))
	}
	return normalized, details, nil
}

func validateTrustedIPs(values []string) error {
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value == "" {
			return fmt.Errorf("empty trusted IP")
		}
		if _, err := netip.ParsePrefix(value); err == nil {
			continue
		}
		if _, err := netip.ParseAddr(value); err == nil {
			continue
		}
		return fmt.Errorf("%q is not a valid IP address or CIDR prefix", value)
	}
	return nil
}

func sortedNginxServiceNames(values map[string]coretraefik.NginxRealIPConfig) []string {
	out := make([]string, 0, len(values))
	for service := range values {
		out = append(out, service)
	}
	sort.Strings(out)
	return out
}
