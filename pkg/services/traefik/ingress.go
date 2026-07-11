package traefik

import (
	"context"
	_ "embed"
	"fmt"
	"net"
	"net/mail"
	"net/url"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	corecomponent "github.com/libops/sitectl/pkg/component"
	"github.com/libops/sitectl/pkg/config"
)

const (
	// IngressName is the component name for Traefik ingress settings.
	IngressName = "ingress"

	// IngressModeHTTP serves the stack over plain HTTP.
	IngressModeHTTP = "http"
	// IngressModeHTTPSCloudflareOrigin serves HTTPS using a Cloudflare Origin CA certificate mounted from ./certs.
	IngressModeHTTPSCloudflareOrigin = "https-cloudflare-origin"
	// IngressModeHTTPSLetsEncrypt serves HTTPS using Let's Encrypt ACME automation.
	IngressModeHTTPSLetsEncrypt = "https-letsencrypt"
	// IngressModeHTTPSCustom serves HTTPS using an operator-managed certificate mounted from ./certs.
	IngressModeHTTPSCustom = "https-custom"
	// IngressModeHTTPSMkcert serves HTTPS using mkcert-managed certificates for non-production contexts.
	IngressModeHTTPSMkcert = "https-mkcert"

	ingressModeName      = "mode"
	ingressDomainName    = "domain"
	ingressACMEEmailName = "acme-email"
	ingressTrustedIPName = "trusted-ip"
	uploadSizeName       = "max-upload-size"
	uploadTimeoutName    = "upload-timeout"
	ingressTLSModeEnv    = "SITECTL_TLS_MODE"

	// DefaultIngressDomain is the default local development domain.
	DefaultIngressDomain = "localhost"
	// DefaultMaxUploadSize is the upload size used when ingress upload settings are not explicitly set.
	DefaultMaxUploadSize = "128M"
	// DefaultUploadTimeout is the read timeout used when ingress upload settings are not explicitly set.
	DefaultUploadTimeout = "300s"
)

var (
	hostRuleExpr                        = `\bHost\s*\(\s*(?:` + "`" + `[^` + "`" + `]*` + "`" + `|"[^"]*"|'[^']*')\s*\)`
	hostRulePattern                     = regexp.MustCompile(hostRuleExpr)
	hostRuleWithTrailingOperatorPattern = regexp.MustCompile(hostRuleExpr + `\s*(?:&&|\|\|)\s*`)
	operatorWithTrailingHostRulePattern = regexp.MustCompile(`\s*(?:&&|\|\|)\s*` + hostRuleExpr)
	pathRulePattern                     = regexp.MustCompile(`\bPath(?:Prefix)?\s*\(`)
	ingressHostnameLabelPattern         = regexp.MustCompile(`^[A-Za-z0-9](?:[A-Za-z0-9-]{0,61}[A-Za-z0-9])?$`)
	ingressUploadSizePattern            = regexp.MustCompile(`^[0-9]+[kKmMgG]?$`)
	ingressTimeoutPattern               = regexp.MustCompile(`^[0-9]+(?:ms|s|m|h)$`)
)

//go:embed assets/ingress/default-tls.yml
var defaultTLSYAML string

//go:embed assets/ingress/redirect-http-to-https.yml
var redirectHTTPToHTTPSYAML string

// IngressOptions configures the reusable Traefik ingress component for a stack.
type IngressOptions struct {
	AppService          string
	NoAppService        bool
	TraefikService      string
	HTTPEntrypoint      string
	HTTPSEntrypoint     string
	Entrypoints         []string
	TraefikConfigDir    string
	RouterFiles         []string
	RouterHosts         map[string]string
	ServiceEnvTemplates map[string]map[string]string
	AppEnvDeletes       []string
	AppUpdate           IngressAppUpdateFunc
	TrustedIPLimit      int
}

// IngressAppUpdate describes a resolved ingress change for plugin-owned
// application wiring.
type IngressAppUpdate struct {
	Mode            string
	Domain          string
	Scheme          string
	BaseURL         string
	ACMEEmail       string
	TrustedProxyIPs []string
	UploadSize      string
	ReadTimeout     string
	HTTPS           bool
	LetsEncrypt     bool
	Mkcert          bool
}

// IngressAppUpdateFunc lets application plugins update app-specific config
// whenever the shared ingress component changes.
type IngressAppUpdateFunc func(context.Context, *config.Context, *corecomponent.ComposeFile, IngressAppUpdate) error

type ingressSettings struct {
	Mode        string
	Domain      string
	ACMEEmail   string
	TrustedIPs  []string
	UploadSize  string
	ReadTimeout string
	Scheme      string
	HTTPS       bool
	LetsEncrypt bool
	Mkcert      bool
}

type mkcertRunnerFunc func(context.Context, *config.Context, string, string, []string) error

var ingressMkcertRunner mkcertRunnerFunc = runIngressMkcert

// Ingress returns a reusable component that owns Traefik ingress, TLS, domain,
// proxy trust, upload, and read timeout configuration.
func Ingress(opts IngressOptions) (corecomponent.ComposeServiceComponent, error) {
	opts = normalizeIngressOptions(opts)
	return corecomponent.NewComposeServiceComponent(corecomponent.ComposeServiceComponentOptions{
		Name:                IngressName,
		DefaultState:        corecomponent.StateOn,
		DefaultDisposition:  corecomponent.DispositionEnabled,
		AllowedDispositions: []corecomponent.Disposition{corecomponent.DispositionEnabled},
		Guidance: corecomponent.StateGuidance{
			EnabledHelp:  "Traefik ingress is configured from Compose with an explicit domain, scheme, TLS mode, proxy trust, and upload/read timeout settings.",
			DisabledHelp: "Ingress is required for this stack.",
			Question:     "Configure the public ingress for this application.",
		},
		FollowUps: []corecomponent.FollowUpSpec{
			{
				Name:                 ingressModeName,
				Label:                "Ingress mode",
				FlagName:             "mode",
				FlagUsage:            "Ingress mode: http, https, https-letsencrypt, https-custom, or https-mkcert.",
				Question:             "Choose how Traefik should expose this application.",
				DefaultValue:         IngressModeHTTP,
				AppliesToDisposition: corecomponent.DispositionEnabled,
				Choices: []corecomponent.Choice{
					{Value: IngressModeHTTP, Label: IngressModeHTTP, Help: "Serve plain HTTP.", Aliases: []string{"1"}},
					{Value: IngressModeHTTPSCloudflareOrigin, Label: "https", Help: "Serve HTTPS with a Cloudflare Origin CA certificate mounted from ./certs.", Aliases: []string{"2", "cloudflare", "cloudflare-origin", "origin-ca"}},
					{Value: IngressModeHTTPSLetsEncrypt, Label: IngressModeHTTPSLetsEncrypt, Help: "Serve HTTPS with Let's Encrypt ACME automation.", Aliases: []string{"3", "letsencrypt", "le"}},
					{Value: IngressModeHTTPSCustom, Label: IngressModeHTTPSCustom, Help: "Serve HTTPS with operator-managed certs/cert.pem and certs/privkey.pem.", Aliases: []string{"4", "custom", "byo", "bring-your-own", "self-managed"}},
					{Value: IngressModeHTTPSMkcert, Label: IngressModeHTTPSMkcert, Help: "Serve HTTPS with mkcert-managed certificates for non-production environments.", Aliases: []string{"5", "mkcert", "self-signed", "dev"}},
				},
			},
			{
				Name:                 ingressDomainName,
				Label:                "Domain",
				FlagName:             "domain",
				FlagUsage:            "Public domain for Traefik host rules and app URLs.",
				Question:             "Enter the public domain.",
				DefaultValue:         DefaultIngressDomain,
				AppliesToDisposition: corecomponent.DispositionEnabled,
			},
			{
				Name:                 ingressACMEEmailName,
				Label:                "ACME email",
				FlagName:             "acme-email",
				FlagUsage:            "ACME account email; required with --mode https-letsencrypt.",
				Question:             "Enter the Let's Encrypt ACME account email.",
				AppliesToDisposition: corecomponent.DispositionEnabled,
			},
			{
				Name:                 ingressTrustedIPName,
				Label:                "Trusted proxy IPs",
				FlagName:             "trusted-ip",
				FlagUsage:            "Trusted upstream proxy IP/CIDR; may be passed more than once.",
				Question:             "Enter trusted upstream proxy IP/CIDR values, separated by commas.",
				MultiValue:           true,
				AppliesToDisposition: corecomponent.DispositionEnabled,
			},
			{
				Name:                 uploadSizeName,
				Label:                "Max upload size",
				FlagName:             uploadSizeName,
				FlagUsage:            "Maximum upload size, such as 128M or 2G.",
				Question:             "Enter the maximum upload size.",
				DefaultValue:         DefaultMaxUploadSize,
				AppliesToDisposition: corecomponent.DispositionEnabled,
			},
			{
				Name:                 uploadTimeoutName,
				Label:                "Upload timeout",
				FlagName:             uploadTimeoutName,
				FlagUsage:            "End-to-end upload/read timeout for Traefik, nginx, and PHP, such as 300s or 10m.",
				Question:             "Enter the upload/read timeout.",
				DefaultValue:         DefaultUploadTimeout,
				AppliesToDisposition: corecomponent.DispositionEnabled,
			},
		},
		DefinitionOnRules: []corecomponent.YAMLRule{{
			Files: []string{"docker-compose.yml", "docker-compose.yaml", "compose.yml", "compose.yaml"},
			Op:    corecomponent.OpRestore,
			Path:  ".services." + opts.TraefikService + ".command",
		}},
		DefinitionOffRules: []corecomponent.YAMLRule{{
			Files: []string{"docker-compose.yml", "docker-compose.yaml", "compose.yml", "compose.yaml"},
			Op:    corecomponent.OpDelete,
			Path:  ".services." + opts.TraefikService,
		}},
		AfterEnableOptions: func(values map[string]string) []corecomponent.Hook {
			return []corecomponent.Hook{func(ctx context.Context, runtime *corecomponent.Runtime) error {
				return applyIngressWithOptions(ctx, runtime.Context, opts, values, runtime.ApplyOptions)
			}}
		},
		Behavior: corecomponent.Behavior{
			Idempotent: true,
			Enable: corecomponent.TransitionBehavior{
				DataMigration: corecomponent.DataMigrationNone,
				Summary:       "Updates Traefik ingress, router TLS, app URLs, trusted proxy settings, upload limits, and read timeout together.",
			},
			Disable: corecomponent.TransitionBehavior{
				DataMigration: corecomponent.DataMigrationNone,
				Summary:       "Ingress is required and is not removed by this component.",
			},
		},
	})
}

func normalizeIngressOptions(opts IngressOptions) IngressOptions {
	if opts.NoAppService {
		opts.AppService = ""
	} else if strings.TrimSpace(opts.AppService) == "" {
		opts.AppService = "drupal"
	}
	if strings.TrimSpace(opts.TraefikService) == "" {
		opts.TraefikService = "traefik"
	}
	if strings.TrimSpace(opts.HTTPEntrypoint) == "" {
		opts.HTTPEntrypoint = "http"
	}
	if strings.TrimSpace(opts.HTTPSEntrypoint) == "" {
		if opts.HTTPEntrypoint == "web" {
			opts.HTTPSEntrypoint = "websecure"
		} else {
			opts.HTTPSEntrypoint = "https"
		}
	}
	if len(opts.Entrypoints) == 0 {
		opts.Entrypoints = []string{"http", "https", "web", "websecure"}
	}
	opts.Entrypoints = appendUniqueStrings(opts.Entrypoints, opts.HTTPEntrypoint, opts.HTTPSEntrypoint)
	if strings.TrimSpace(opts.TraefikConfigDir) == "" {
		opts.TraefikConfigDir = filepath.ToSlash(filepath.Join("conf", "traefik"))
	}
	if opts.TrustedIPLimit == 0 {
		opts.TrustedIPLimit = defaultTrustedIPLimit
	}
	if opts.ServiceEnvTemplates == nil {
		opts.ServiceEnvTemplates = map[string]map[string]string{}
	}
	if opts.AppService != "" {
		if _, ok := opts.ServiceEnvTemplates[opts.AppService]; !ok {
			opts.ServiceEnvTemplates[opts.AppService] = map[string]string{}
		}
	}
	return opts
}

// NormalizeIngressMode returns the canonical ingress mode for a user-provided
// mode or alias.
func NormalizeIngressMode(mode string) (string, bool) {
	switch strings.ToLower(strings.TrimSpace(mode)) {
	case "":
		return IngressModeHTTP, true
	case IngressModeHTTP:
		return IngressModeHTTP, true
	case "https", IngressModeHTTPSCloudflareOrigin, "cloudflare", "cloudflare-origin", "origin-ca":
		return IngressModeHTTPSCloudflareOrigin, true
	case IngressModeHTTPSLetsEncrypt, "letsencrypt", "le":
		return IngressModeHTTPSLetsEncrypt, true
	case IngressModeHTTPSCustom, "custom", "byo", "bring-your-own", "self-managed":
		return IngressModeHTTPSCustom, true
	case IngressModeHTTPSMkcert, "mkcert", "self-signed", "dev":
		return IngressModeHTTPSMkcert, true
	default:
		return strings.TrimSpace(mode), false
	}
}

// IngressModeUsesHTTPS reports whether a mode or alias enables HTTPS.
func IngressModeUsesHTTPS(mode string) bool {
	canonical, ok := NormalizeIngressMode(mode)
	if !ok {
		return false
	}
	return canonical != IngressModeHTTP
}

// IngressModeRequiresACMEEmail reports whether a mode or alias needs an ACME email.
func IngressModeRequiresACMEEmail(mode string) bool {
	canonical, ok := NormalizeIngressMode(mode)
	return ok && canonical == IngressModeHTTPSLetsEncrypt
}

// IngressModeTLSProvider reports the TLS provider label used for status output.
func IngressModeTLSProvider(mode string) string {
	canonical, ok := NormalizeIngressMode(mode)
	if !ok {
		return ""
	}
	switch canonical {
	case IngressModeHTTPSCloudflareOrigin:
		return "cloudflare-origin"
	case IngressModeHTTPSLetsEncrypt:
		return "letsencrypt"
	case IngressModeHTTPSCustom:
		return "custom"
	case IngressModeHTTPSMkcert:
		return "mkcert"
	default:
		return ""
	}
}

// SuggestedApplicationHosts returns hostnames that app-level host allowlists
// should commonly accept for this ingress update.
func SuggestedApplicationHosts(ctx *config.Context, update IngressAppUpdate) []string {
	hosts := appendUniqueHostnames(nil, update.Domain)
	hosts = appendUniqueHostnames(hosts, DefaultIngressDomain, "127.0.0.1", "::1")
	if ctx != nil && ctx.DockerHostType == config.ContextRemote {
		hosts = appendUniqueHostnames(hosts, ctx.SSHHostname)
	}
	return hosts
}

func appendUniqueHostnames(hosts []string, candidates ...string) []string {
	for _, candidate := range candidates {
		host := normalizeApplicationHostname(candidate)
		if host == "" {
			continue
		}
		exists := false
		for _, existing := range hosts {
			if strings.EqualFold(existing, host) {
				exists = true
				break
			}
		}
		if !exists {
			hosts = append(hosts, host)
		}
	}
	return hosts
}

func normalizeApplicationHostname(value string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return ""
	}
	if strings.Contains(value, "://") {
		parsed, err := url.Parse(value)
		if err == nil && parsed.Host != "" {
			value = parsed.Host
		}
	}
	value = strings.TrimSpace(strings.Split(value, "/")[0])
	if host, _, err := net.SplitHostPort(value); err == nil {
		value = host
	} else if strings.Count(value, ":") == 1 {
		if before, _, ok := strings.Cut(value, ":"); ok {
			value = before
		}
	}
	return strings.Trim(strings.TrimSpace(value), "[]")
}

func applyIngress(runCtx context.Context, ctx *config.Context, opts IngressOptions, values map[string]string) error {
	return applyIngressWithOptions(runCtx, ctx, opts, values, corecomponent.ApplyOptions{})
}

func applyIngressWithOptions(runCtx context.Context, ctx *config.Context, opts IngressOptions, values map[string]string, applyOpts corecomponent.ApplyOptions) error {
	settings, err := resolveIngressSettings(values)
	if err != nil {
		return err
	}
	opts = normalizeIngressOptions(opts)
	if err := validateIngressSettingsForContext(ctx, settings); err != nil {
		return err
	}
	if err := prepareIngressTLS(runCtx, ctx, settings, applyOpts); err != nil {
		return err
	}

	compose, err := corecomponent.LoadComposeFileForContext(ctx, composePathForContext(ctx))
	if err != nil {
		return err
	}
	if err := applyIngressCompose(ctx, compose, opts, settings); err != nil {
		return err
	}
	if opts.AppUpdate != nil {
		if err := opts.AppUpdate(runCtx, ctx, compose, newIngressAppUpdate(settings)); err != nil {
			return err
		}
	}
	if err := compose.Save(); err != nil {
		return err
	}
	if err := applyIngressRouterFiles(ctx, opts, settings); err != nil {
		return err
	}
	return writeIngressTLSFiles(ctx, opts, settings)
}

func newIngressAppUpdate(settings ingressSettings) IngressAppUpdate {
	return IngressAppUpdate{
		Mode:            settings.Mode,
		Domain:          settings.Domain,
		Scheme:          settings.Scheme,
		BaseURL:         settings.Scheme + "://" + settings.Domain,
		ACMEEmail:       settings.ACMEEmail,
		TrustedProxyIPs: append([]string{}, settings.TrustedIPs...),
		UploadSize:      settings.UploadSize,
		ReadTimeout:     settings.ReadTimeout,
		HTTPS:           settings.HTTPS,
		LetsEncrypt:     settings.LetsEncrypt,
		Mkcert:          settings.Mkcert,
	}
}

func resolveIngressSettings(values map[string]string) (ingressSettings, error) {
	rawMode := strings.TrimSpace(values[ingressModeName])
	mode, ok := NormalizeIngressMode(rawMode)
	if !ok {
		return ingressSettings{}, fmt.Errorf("invalid ingress mode %q: expected %s, https, %s, %s, or %s", rawMode, IngressModeHTTP, IngressModeHTTPSLetsEncrypt, IngressModeHTTPSCustom, IngressModeHTTPSMkcert)
	}
	settings := ingressSettings{
		Mode:        mode,
		Domain:      strings.TrimSpace(values[ingressDomainName]),
		ACMEEmail:   strings.TrimSpace(values[ingressACMEEmailName]),
		TrustedIPs:  corecomponent.SplitFollowUpValues(values[ingressTrustedIPName]),
		UploadSize:  strings.TrimSpace(values[uploadSizeName]),
		ReadTimeout: strings.TrimSpace(values[uploadTimeoutName]),
	}
	if settings.Domain == "" {
		settings.Domain = DefaultIngressDomain
	}
	if settings.UploadSize == "" {
		settings.UploadSize = DefaultMaxUploadSize
	}
	if settings.ReadTimeout == "" {
		settings.ReadTimeout = DefaultUploadTimeout
	}
	if err := validateIngressDomain(settings.Domain); err != nil {
		return ingressSettings{}, fmt.Errorf("invalid ingress domain %q: %w", settings.Domain, err)
	}
	if !ingressUploadSizePattern.MatchString(settings.UploadSize) {
		return ingressSettings{}, fmt.Errorf("invalid maximum upload size %q: expected digits with an optional K, M, or G suffix (case-insensitive)", settings.UploadSize)
	}
	if !ingressTimeoutPattern.MatchString(settings.ReadTimeout) {
		return ingressSettings{}, fmt.Errorf("invalid upload timeout %q: expected digits followed by ms, s, m, or h", settings.ReadTimeout)
	}
	switch settings.Mode {
	case IngressModeHTTP:
		settings.Scheme = "http"
	case IngressModeHTTPSCloudflareOrigin, IngressModeHTTPSCustom:
		settings.Scheme = "https"
		settings.HTTPS = true
	case IngressModeHTTPSLetsEncrypt:
		settings.Scheme = "https"
		settings.HTTPS = true
		settings.LetsEncrypt = true
		if settings.ACMEEmail == "" {
			return ingressSettings{}, fmt.Errorf("--%s is required with --%s %s", ingressACMEEmailName, ingressModeName, IngressModeHTTPSLetsEncrypt)
		}
		address, err := mail.ParseAddress(settings.ACMEEmail)
		if err != nil {
			return ingressSettings{}, fmt.Errorf("invalid ACME email %q: %w", settings.ACMEEmail, err)
		}
		if address.Name != "" || address.Address != settings.ACMEEmail {
			return ingressSettings{}, fmt.Errorf("invalid ACME email %q: expected a bare email address without a display name", settings.ACMEEmail)
		}
	case IngressModeHTTPSMkcert:
		settings.Scheme = "https"
		settings.HTTPS = true
		settings.Mkcert = true
	default:
		return ingressSettings{}, fmt.Errorf("invalid ingress mode %q: expected %s, https, %s, %s, or %s", settings.Mode, IngressModeHTTP, IngressModeHTTPSLetsEncrypt, IngressModeHTTPSCustom, IngressModeHTTPSMkcert)
	}
	for _, trustedIP := range settings.TrustedIPs {
		if err := validateTrustedIP(trustedIP); err != nil {
			return ingressSettings{}, err
		}
	}
	return settings, nil
}

func validateIngressDomain(domain string) error {
	domain = strings.TrimSpace(domain)
	if domain == "" {
		return fmt.Errorf("domain cannot be empty")
	}
	if len(domain) > 253 {
		return fmt.Errorf("domain exceeds 253 characters")
	}
	for _, r := range domain {
		if r < 0x20 || r == 0x7f {
			return fmt.Errorf("domain contains a control character")
		}
	}

	if strings.HasPrefix(domain, "[") || strings.HasSuffix(domain, "]") {
		if !strings.HasPrefix(domain, "[") || !strings.HasSuffix(domain, "]") || net.ParseIP(strings.TrimSuffix(strings.TrimPrefix(domain, "["), "]")) == nil {
			return fmt.Errorf("expected a bracketed IPv6 address")
		}
		return nil
	}
	if ip := net.ParseIP(domain); ip != nil {
		if strings.Contains(domain, ":") {
			return fmt.Errorf("IPv6 addresses must be enclosed in brackets")
		}
		return nil
	}

	for _, label := range strings.Split(domain, ".") {
		if !ingressHostnameLabelPattern.MatchString(label) {
			return fmt.Errorf("expected a DNS hostname or IP address")
		}
	}
	return nil
}

func validateIngressSettingsForContext(ctx *config.Context, settings ingressSettings) error {
	if !settings.Mkcert {
		return nil
	}
	if !mkcertAllowedForContext(ctx) {
		name := ""
		environment := ""
		if ctx != nil {
			name = ctx.Name
			environment = ctx.Environment
		}
		if name == "" {
			name = "current"
		}
		if environment == "" {
			environment = "-"
		}
		return fmt.Errorf("--%s %s is limited to local, dev, development, test, testing, qa, or sandbox contexts; context %q has environment %q", ingressModeName, IngressModeHTTPSMkcert, name, environment)
	}
	return nil
}

func mkcertAllowedForContext(ctx *config.Context) bool {
	if ctx == nil {
		return false
	}
	environment := strings.ToLower(strings.TrimSpace(ctx.Environment))
	switch environment {
	case "local", "dev", "development", "test", "testing", "qa", "sandbox":
		return true
	case "":
		return ctx.DockerHostType == config.ContextLocal
	default:
		return false
	}
}

func prepareIngressTLS(runCtx context.Context, ctx *config.Context, settings ingressSettings, applyOpts corecomponent.ApplyOptions) error {
	if !settings.Mkcert {
		return nil
	}
	return ensureIngressMkcertCertificates(runCtx, ctx, settings, applyOpts)
}

func ensureIngressMkcertCertificates(runCtx context.Context, ctx *config.Context, settings ingressSettings, applyOpts corecomponent.ApplyOptions) error {
	if ctx == nil {
		return fmt.Errorf("context is required for --%s %s", ingressModeName, IngressModeHTTPSMkcert)
	}
	targetCertPath := ctx.ResolveProjectPath(filepath.Join("certs", "cert.pem"))
	targetKeyPath := ctx.ResolveProjectPath(filepath.Join("certs", "privkey.pem"))
	hosts := mkcertHosts(settings)

	if err := ingressEnsureMkcertPrerequisites(runCtx, ctx, applyOpts); err != nil {
		return err
	}
	if err := ensureIngressCertDir(runCtx, ctx, targetCertPath); err != nil {
		return err
	}
	return ingressMkcertRunner(runCtx, ctx, targetCertPath, targetKeyPath, hosts)
}

func mkcertHosts(settings ingressSettings) []string {
	domain := settings.Domain
	if strings.HasPrefix(domain, "[") && strings.HasSuffix(domain, "]") {
		domain = strings.TrimSuffix(strings.TrimPrefix(domain, "["), "]")
	}
	hosts := appendUniqueStrings(nil, domain)
	if strings.EqualFold(settings.Domain, DefaultIngressDomain) {
		hosts = appendUniqueStrings(hosts, "127.0.0.1", "::1")
	} else {
		hosts = appendUniqueStrings(hosts, DefaultIngressDomain, "127.0.0.1", "::1")
	}
	return hosts
}

func runIngressMkcert(runCtx context.Context, ctx *config.Context, certPath, keyPath string, hosts []string) error {
	args := []string{"-cert-file", certPath, "-key-file", keyPath}
	args = append(args, hosts...)
	output, err := ingressRunHostCommand(runCtx, ctx, append([]string{"mkcert"}, args...))
	if err == nil {
		return nil
	}
	detail := strings.TrimSpace(output)
	if detail != "" {
		return fmt.Errorf("run mkcert: %w: %s", err, detail)
	}
	return fmt.Errorf("run mkcert: %w", err)
}

func applyIngressCompose(ctx *config.Context, compose *corecomponent.ComposeFile, opts IngressOptions, settings ingressSettings) error {
	if err := removeLegacyTraefikEnvironment(compose, opts); err != nil {
		return err
	}
	if err := normalizeTraefikFileProvider(compose, opts); err != nil {
		return err
	}
	if err := applyIngressTraefikCommands(compose, opts, settings); err != nil {
		return err
	}
	if err := applyIngressPortsAndVolumes(compose, opts, settings); err != nil {
		return err
	}
	if err := applyIngressTLSModeEnvironment(compose, opts, settings); err != nil {
		return err
	}
	if err := applyIngressServiceEnvironment(ctx, compose, opts, settings); err != nil {
		return err
	}
	if err := applyIngressUploadEnvironment(compose, opts, settings); err != nil {
		return err
	}
	return nil
}

func removeLegacyTraefikEnvironment(compose *corecomponent.ComposeFile, opts IngressOptions) error {
	for _, key := range []string{"DOMAIN", "URI_SCHEME", "TLS_PROVIDER", "ACME_EMAIL", "ACME_URL"} {
		if err := compose.DeleteServiceEnv(opts.TraefikService, key); err != nil {
			return err
		}
	}
	return nil
}

func applyIngressTLSModeEnvironment(compose *corecomponent.ComposeFile, opts IngressOptions, settings ingressSettings) error {
	if !settings.HTTPS {
		return compose.DeleteServiceEnv(opts.TraefikService, ingressTLSModeEnv)
	}
	return compose.SetServiceEnv(opts.TraefikService, ingressTLSModeEnv, settings.Mode)
}

func normalizeTraefikFileProvider(compose *corecomponent.ComposeFile, opts IngressOptions) error {
	for _, prefix := range []string{
		"--providers.file.filename=",
		"--providers.file.directory=",
		"--providers.docker",
	} {
		if err := compose.RemoveServiceStringsByPrefix(opts.TraefikService, "command", prefix); err != nil {
			return err
		}
	}
	if err := compose.AppendUniqueServiceString(opts.TraefikService, "command", "--providers.file.directory=/etc/traefik/dynamic"); err != nil {
		return err
	}
	if err := compose.AppendUniqueServiceString(opts.TraefikService, "command", "--providers.file.watch=true"); err != nil {
		return err
	}
	if err := compose.RemoveServiceVolumesBySource(opts.TraefikService, "/var/run/docker.sock"); err != nil {
		return err
	}
	for _, value := range []string{
		"./conf/traefik:/etc/traefik/dynamic:ro",
		"./conf/traefik:/etc/traefik/dynamic:ro,z",
		"./conf/traefik:/etc/traefik/dynamic:ro,Z",
	} {
		removeServiceStringVariants(compose, opts.TraefikService, "volumes", value)
	}
	return compose.AppendUniqueServiceString(opts.TraefikService, "volumes", "./conf/traefik:/etc/traefik/dynamic:ro,z")
}

func applyIngressTraefikCommands(compose *corecomponent.ComposeFile, opts IngressOptions, settings ingressSettings) error {
	if err := removeIngressCommandPrefixes(compose, opts); err != nil {
		return err
	}
	httpEntry := strings.TrimSpace(opts.HTTPEntrypoint)
	httpsEntry := strings.TrimSpace(opts.HTTPSEntrypoint)
	if err := compose.AppendUniqueServiceString(opts.TraefikService, "command", fmt.Sprintf("--entryPoints.%s.address=:80", httpEntry)); err != nil {
		return err
	}
	if err := compose.AppendUniqueServiceString(opts.TraefikService, "command", fmt.Sprintf("--entryPoints.%s.transport.respondingTimeouts.readTimeout=%s", httpEntry, settings.ReadTimeout)); err != nil {
		return err
	}
	activeEntries := []string{httpEntry}
	if settings.HTTPS {
		if err := compose.AppendUniqueServiceString(opts.TraefikService, "command", fmt.Sprintf("--entryPoints.%s.address=:443", httpsEntry)); err != nil {
			return err
		}
		if err := compose.AppendUniqueServiceString(opts.TraefikService, "command", fmt.Sprintf("--entryPoints.%s.transport.respondingTimeouts.readTimeout=%s", httpsEntry, settings.ReadTimeout)); err != nil {
			return err
		}
		activeEntries = append(activeEntries, httpsEntry)
	}
	if len(settings.TrustedIPs) > 0 {
		if opts.TrustedIPLimit > 0 && len(settings.TrustedIPs) > opts.TrustedIPLimit {
			return fmt.Errorf("%s supports at most %d trusted IP values", IngressName, opts.TrustedIPLimit)
		}
		joined := strings.Join(settings.TrustedIPs, ",")
		for _, entrypoint := range activeEntries {
			if err := compose.AppendUniqueServiceString(opts.TraefikService, "command", fmt.Sprintf("--entryPoints.%s.forwardedHeaders.trustedIPs=%s", entrypoint, joined)); err != nil {
				return err
			}
		}
	}
	if settings.LetsEncrypt {
		for _, value := range []string{
			"--certificatesResolvers.letsencrypt.acme.storage=/acme/acme.json",
			"--certificatesResolvers.letsencrypt.acme.httpChallenge.entryPoint=" + httpEntry,
			"--certificatesResolvers.letsencrypt.acme.email=" + settings.ACMEEmail,
		} {
			if err := compose.AppendUniqueServiceString(opts.TraefikService, "command", value); err != nil {
				return err
			}
		}
	}
	return nil
}

func removeIngressCommandPrefixes(compose *corecomponent.ComposeFile, opts IngressOptions) error {
	for _, entrypoint := range appendUniqueStrings(opts.Entrypoints, opts.HTTPEntrypoint, opts.HTTPSEntrypoint) {
		for _, prefix := range []string{
			fmt.Sprintf("--entryPoints.%s.address=", entrypoint),
			fmt.Sprintf("--entrypoints.%s.address=", entrypoint),
			fmt.Sprintf("--entryPoints.%s.transport.respondingTimeouts.readTimeout=", entrypoint),
			fmt.Sprintf("--entrypoints.%s.transport.respondingTimeouts.readTimeout=", entrypoint),
			fmt.Sprintf("--entryPoints.%s.forwardedHeaders.trustedIPs=", entrypoint),
			fmt.Sprintf("--entrypoints.%s.forwardedHeaders.trustedIPs=", entrypoint),
		} {
			if err := compose.RemoveServiceStringsByPrefix(opts.TraefikService, "command", prefix); err != nil {
				return err
			}
		}
	}
	for _, entrypoint := range []string{"https", "websecure", opts.HTTPSEntrypoint} {
		for _, prefix := range []string{
			fmt.Sprintf("--entryPoints.%s.http.tls.certResolver=", entrypoint),
			fmt.Sprintf("--entrypoints.%s.http.tls.certResolver=", entrypoint),
		} {
			if err := compose.RemoveServiceStringsByPrefix(opts.TraefikService, "command", prefix); err != nil {
				return err
			}
		}
	}
	for _, prefix := range []string{
		"--certificatesResolvers.letsencrypt.",
		"--certificatesresolvers.letsencrypt.",
	} {
		if err := compose.RemoveServiceStringsByPrefix(opts.TraefikService, "command", prefix); err != nil {
			return err
		}
	}
	return nil
}

func applyIngressPortsAndVolumes(compose *corecomponent.ComposeFile, opts IngressOptions, settings ingressSettings) error {
	for _, port := range []string{"80:80", "443:443"} {
		removeServiceStringVariants(compose, opts.TraefikService, "ports", port)
	}
	if err := compose.AppendUniqueServiceString(opts.TraefikService, "ports", `"80:80"`); err != nil {
		return err
	}
	for _, mount := range []string{
		"./certs:/certs",
		"./certs/cert.pem:/certs/cert.pem",
		"./certs/privkey.pem:/certs/privkey.pem",
	} {
		removeServiceStringVariants(compose, opts.TraefikService, "volumes", mount)
		for _, mode := range []string{"rw", "ro", "z", "Z", "rw,z", "ro,z", "rw,Z", "ro,Z"} {
			removeServiceStringVariants(compose, opts.TraefikService, "volumes", mount+":"+mode)
		}
	}
	removeServiceStringVariants(compose, opts.TraefikService, "volumes", "acme-data:/acme:rw")
	if settings.HTTPS {
		if err := compose.AppendUniqueServiceString(opts.TraefikService, "ports", `"443:443"`); err != nil {
			return err
		}
	}
	if settings.LetsEncrypt {
		if err := compose.AppendUniqueServiceString(opts.TraefikService, "volumes", "acme-data:/acme:rw"); err != nil {
			return err
		}
		return compose.AddVolumeBlock("acme-data", "  acme-data: {}")
	}
	if err := compose.DeleteVolume("acme-data"); err != nil {
		return err
	}
	if settings.HTTPS {
		if err := compose.AppendUniqueServiceString(opts.TraefikService, "volumes", "./certs/cert.pem:/certs/cert.pem:ro,z"); err != nil {
			return err
		}
		return compose.AppendUniqueServiceString(opts.TraefikService, "volumes", "./certs/privkey.pem:/certs/privkey.pem:ro,z")
	}
	return nil
}

func applyIngressServiceEnvironment(ctx *config.Context, compose *corecomponent.ComposeFile, opts IngressOptions, settings ingressSettings) error {
	if strings.TrimSpace(opts.AppService) != "" && compose.HasService(opts.AppService) {
		update := newIngressAppUpdate(settings)
		standardEnv := map[string]string{
			"INGRESS_HOSTNAMES": strings.Join(SuggestedApplicationHosts(ctx, update), ","),
			"INGRESS_SCHEME":    settings.Scheme,
		}
		keys := make([]string, 0, len(standardEnv))
		for key := range standardEnv {
			keys = append(keys, key)
		}
		sort.Strings(keys)
		for _, key := range keys {
			if err := compose.SetServiceEnv(opts.AppService, key, standardEnv[key]); err != nil {
				return err
			}
		}
		for _, key := range opts.AppEnvDeletes {
			if err := compose.DeleteServiceEnv(opts.AppService, key); err != nil {
				return err
			}
		}
	}

	services := make([]string, 0, len(opts.ServiceEnvTemplates))
	for service := range opts.ServiceEnvTemplates {
		if strings.TrimSpace(service) != "" {
			services = append(services, service)
		}
	}
	sort.Strings(services)
	for _, service := range services {
		if !compose.HasService(service) {
			continue
		}
		keys := make([]string, 0, len(opts.ServiceEnvTemplates[service]))
		for key := range opts.ServiceEnvTemplates[service] {
			if strings.TrimSpace(key) != "" {
				keys = append(keys, key)
			}
		}
		sort.Strings(keys)
		for _, key := range keys {
			value := renderIngressTemplate(opts.ServiceEnvTemplates[service][key], settings)
			if err := compose.SetServiceEnv(service, key, value); err != nil {
				return err
			}
		}
	}
	return nil
}

func applyIngressUploadEnvironment(compose *corecomponent.ComposeFile, opts IngressOptions, settings ingressSettings) error {
	if opts.AppService == "" {
		return nil
	}
	phpTimeoutSeconds, err := ingressPHPTimeoutSeconds(settings.ReadTimeout)
	if err != nil {
		return err
	}
	env := map[string]string{
		"PHP_UPLOAD_MAX_FILESIZE":       settings.UploadSize,
		"PHP_POST_MAX_SIZE":             settings.UploadSize,
		"PHP_MAX_INPUT_TIME":            phpTimeoutSeconds,
		"PHP_MAX_EXECUTION_TIME":        phpTimeoutSeconds,
		"PHP_REQUEST_TERMINATE_TIMEOUT": phpTimeoutSeconds,
		"NGINX_CLIENT_MAX_BODY_SIZE":    settings.UploadSize,
		"NGINX_CLIENT_BODY_TIMEOUT":     settings.ReadTimeout,
		"NGINX_FASTCGI_READ_TIMEOUT":    settings.ReadTimeout,
		"NGINX_FASTCGI_SEND_TIMEOUT":    settings.ReadTimeout,
	}
	keys := make([]string, 0, len(env))
	for key := range env {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	for _, key := range keys {
		if err := compose.SetServiceEnv(opts.AppService, key, env[key]); err != nil {
			return err
		}
	}
	if len(settings.TrustedIPs) == 0 {
		for i := 1; i <= opts.TrustedIPLimit; i++ {
			key := "NGINX_SET_REAL_IP_FROM"
			if i > 1 {
				key = fmt.Sprintf("NGINX_SET_REAL_IP_FROM%d", i)
			}
			if err := compose.DeleteServiceEnv(opts.AppService, key); err != nil {
				return err
			}
		}
		return compose.DeleteServiceEnv(opts.AppService, "NGINX_REAL_IP_RECURSIVE")
	}
	for i := 1; i <= opts.TrustedIPLimit; i++ {
		key := "NGINX_SET_REAL_IP_FROM"
		if i > 1 {
			key = fmt.Sprintf("NGINX_SET_REAL_IP_FROM%d", i)
		}
		if i <= len(settings.TrustedIPs) {
			if err := compose.SetServiceEnv(opts.AppService, key, settings.TrustedIPs[i-1]); err != nil {
				return err
			}
			continue
		}
		if err := compose.DeleteServiceEnv(opts.AppService, key); err != nil {
			return err
		}
	}
	return compose.SetServiceEnv(opts.AppService, "NGINX_REAL_IP_RECURSIVE", "on")
}

func ingressPHPTimeoutSeconds(value string) (string, error) {
	duration, err := time.ParseDuration(strings.TrimSpace(value))
	if err != nil {
		return "", fmt.Errorf("invalid upload timeout %q: %w", value, err)
	}
	if duration < 0 {
		return "", fmt.Errorf("invalid upload timeout %q: duration must not be negative", value)
	}
	seconds := duration / time.Second
	if duration%time.Second != 0 {
		seconds++
	}
	return strconv.FormatInt(int64(seconds), 10), nil
}

func applyIngressRouterFiles(ctx *config.Context, opts IngressOptions, settings ingressSettings) error {
	if ctx == nil {
		return nil
	}
	files, err := ingressRouterFiles(ctx, opts)
	if err != nil {
		return err
	}
	for _, path := range files {
		data, err := ctx.ReadFile(path)
		if err != nil {
			return fmt.Errorf("read Traefik router file %q: %w", path, err)
		}
		updated := rewriteIngressRouterText(string(data), opts, settings)
		if updated == string(data) {
			continue
		}
		if err := ctx.WriteFile(path, []byte(updated)); err != nil {
			return fmt.Errorf("write Traefik router file %q: %w", path, err)
		}
	}
	return nil
}

func ingressRouterFiles(ctx *config.Context, opts IngressOptions) ([]string, error) {
	if len(opts.RouterFiles) > 0 {
		out := make([]string, 0, len(opts.RouterFiles))
		for _, rel := range opts.RouterFiles {
			if strings.TrimSpace(rel) != "" {
				out = append(out, ctx.ResolveProjectPath(filepath.FromSlash(rel)))
			}
		}
		return out, nil
	}
	root := ctx.ResolveProjectPath(filepath.FromSlash(opts.TraefikConfigDir))
	exists, err := ctx.FileExists(root)
	if err != nil {
		return nil, err
	}
	if !exists {
		return nil, nil
	}
	files, err := ctx.ListFiles(root)
	if err != nil {
		return nil, err
	}
	out := []string{}
	for _, rel := range files {
		if strings.Contains(filepath.ToSlash(rel), "/") {
			continue
		}
		name := filepath.Base(rel)
		if strings.HasPrefix(name, "00-") {
			continue
		}
		switch filepath.Ext(name) {
		case ".yml", ".yaml", ".tmpl":
			out = append(out, filepath.Join(root, filepath.FromSlash(rel)))
		}
	}
	sort.Strings(out)
	return out, nil
}

func rewriteIngressRouterText(text string, opts IngressOptions, settings ingressSettings) string {
	lines := strings.Split(text, "\n")
	lines = stripLegacyTLSGoTemplates(lines)
	lines = rewriteRouterHostRules(lines, opts, settings)
	lines = rewriteRouterEntryPoints(lines, opts, settings)
	lines = rewriteRouterTLS(lines, settings)
	return strings.Join(lines, "\n")
}

func stripLegacyTLSGoTemplates(lines []string) []string {
	out := make([]string, 0, len(lines))
	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		switch {
		case strings.Contains(trimmed, `env "TLS_PROVIDER"`):
			continue
		case strings.Contains(trimmed, `env "URI_SCHEME"`):
			continue
		case trimmed == "{{- end }}" || trimmed == "{{ end }}":
			continue
		default:
			out = append(out, line)
		}
	}
	return out
}

func rewriteRouterHostRules(lines []string, opts IngressOptions, settings ingressSettings) []string {
	out := append([]string{}, lines...)
	inRouters := false
	router := ""
	for i, line := range out {
		trimmed := strings.TrimSpace(line)
		indent := leadingSpaces(line)
		if indent == 2 && trimmed == "routers:" {
			inRouters = true
			router = ""
			continue
		}
		if inRouters && indent <= 2 && trimmed != "" && trimmed != "routers:" {
			inRouters = false
			router = ""
		}
		if !inRouters {
			continue
		}
		if indent == 4 && strings.HasSuffix(trimmed, ":") {
			router = strings.TrimSuffix(trimmed, ":")
			continue
		}
		if router == "" || !strings.Contains(trimmed, "rule:") {
			continue
		}
		host := ingressRouterHost(opts, router, settings.Domain)
		out[i] = rewriteRouterRuleLine(line, router, host, settings.Domain, opts, settings.HTTPS)
	}
	return out
}

func rewriteRouterRuleLine(line, router, host, domain string, opts IngressOptions, https bool) string {
	idx := strings.Index(line, "rule:")
	if idx < 0 {
		return line
	}
	prefix := line[:idx+len("rule:")]
	after := line[idx+len("rule:"):]
	space := after[:len(after)-len(strings.TrimLeft(after, " \t"))]
	raw := strings.TrimSpace(after)
	if raw == "" {
		return line
	}
	quote := ""
	rule := raw
	if len(raw) >= 2 {
		first := raw[0]
		last := raw[len(raw)-1]
		if (first == '\'' || first == '"') && first == last {
			quote = raw[:1]
			rule = raw[1 : len(raw)-1]
		}
	}
	rule = replaceLegacyDomainTemplates(rule, domain)
	if https {
		rule = ensureRouterRuleHost(rule, host)
	} else if shouldPreserveHTTPHostGate(rule, router, host, domain, opts) {
		rule = strings.TrimSpace(rule)
	} else {
		rule = removeRouterRuleHostGate(rule)
	}
	return prefix + space + quote + rule + quote
}

func ensureRouterRuleHost(rule, host string) string {
	hostRule := "Host(`" + host + "`)"
	if hostRulePattern.MatchString(rule) {
		return hostRulePattern.ReplaceAllStringFunc(rule, func(string) string {
			return hostRule
		})
	}
	rule = strings.TrimSpace(rule)
	if isEmptyRouterRule(rule) || isCatchAllRouterRule(rule) {
		return hostRule
	}
	return hostRule + " && " + rule
}

func removeRouterRuleHostGate(rule string) string {
	current := strings.TrimSpace(rule)
	for {
		next := hostRuleWithTrailingOperatorPattern.ReplaceAllString(current, "")
		next = operatorWithTrailingHostRulePattern.ReplaceAllString(next, "")
		next = hostRulePattern.ReplaceAllString(next, "")
		next = strings.TrimSpace(next)
		if next == current {
			break
		}
		current = next
	}
	if isEmptyRouterRule(current) {
		return "PathPrefix(`/`)"
	}
	return current
}

func shouldPreserveHTTPHostGate(rule, router, renderedHost, domain string, opts IngressOptions) bool {
	if !hostRulePattern.MatchString(rule) || pathRulePattern.MatchString(rule) {
		return false
	}
	if opts.RouterHosts != nil {
		if template := strings.TrimSpace(opts.RouterHosts[router]); template != "" && renderDomainTemplate(template, domain) != domain {
			return true
		}
	}
	for _, host := range routerRuleHosts(rule) {
		switch host {
		case "", "localhost", domain, renderedHost:
			continue
		default:
			return true
		}
	}
	return false
}

func routerRuleHosts(rule string) []string {
	matches := hostRulePattern.FindAllString(rule, -1)
	hosts := make([]string, 0, len(matches))
	for _, match := range matches {
		start := strings.Index(match, "(")
		end := strings.LastIndex(match, ")")
		if start < 0 || end <= start {
			continue
		}
		arg := strings.TrimSpace(match[start+1 : end])
		arg = strings.TrimSpace(strings.Trim(arg, "`\"'"))
		hosts = append(hosts, arg)
	}
	return hosts
}

func isEmptyRouterRule(rule string) bool {
	rule = strings.TrimSpace(rule)
	for rule != "" {
		next := strings.TrimSpace(strings.Trim(rule, "()"))
		if next == rule {
			break
		}
		rule = next
	}
	return rule == "" || rule == "&&" || rule == "||"
}

func isCatchAllRouterRule(rule string) bool {
	normalized := strings.TrimSpace(rule)
	normalized = strings.ReplaceAll(normalized, `"`, "`")
	normalized = strings.ReplaceAll(normalized, `'`, "`")
	return normalized == "PathPrefix(`/`)" || normalized == "Path(`/`)"
}

func rewriteRouterEntryPoints(lines []string, opts IngressOptions, settings ingressSettings) []string {
	target := strings.TrimSpace(opts.HTTPEntrypoint)
	if settings.HTTPS {
		target = strings.TrimSpace(opts.HTTPSEntrypoint)
	}
	if target == "" {
		return lines
	}
	out := make([]string, 0, len(lines))
	inRouters := false
	for i := 0; i < len(lines); {
		line := lines[i]
		trimmed := strings.TrimSpace(line)
		indent := leadingSpaces(line)
		if indent == 2 && trimmed == "routers:" {
			inRouters = true
			out = append(out, line)
			i++
			continue
		}
		if inRouters && indent <= 2 && trimmed != "" && trimmed != "routers:" {
			inRouters = false
		}
		if inRouters && indent == 4 && strings.HasSuffix(trimmed, ":") {
			end := routerBlockEnd(lines, i)
			block := lines[i:end]
			if routerBlockHasRule(block) {
				block = setRouterEntryPointsBlock(block, target)
			}
			out = append(out, block...)
			i = end
			continue
		}
		out = append(out, line)
		i++
	}
	return out
}

func setRouterEntryPointsBlock(lines []string, entrypoint string) []string {
	cleaned := make([]string, 0, len(lines))
	for i := 0; i < len(lines); {
		line := lines[i]
		if leadingSpaces(line) == 6 && strings.HasPrefix(strings.TrimSpace(line), "entryPoints:") {
			i++
			for i < len(lines) {
				trimmed := strings.TrimSpace(lines[i])
				if trimmed != "" && leadingSpaces(lines[i]) <= 6 {
					break
				}
				i++
			}
			continue
		}
		cleaned = append(cleaned, line)
		i++
	}

	insertAt := len(cleaned)
	for i, line := range cleaned {
		if leadingSpaces(line) == 6 && strings.HasPrefix(strings.TrimSpace(line), "rule:") {
			insertAt = i + 1
			break
		}
	}
	block := []string{
		"      entryPoints:",
		"        - " + entrypoint,
	}
	out := make([]string, 0, len(cleaned)+len(block))
	out = append(out, cleaned[:insertAt]...)
	out = append(out, block...)
	out = append(out, cleaned[insertAt:]...)
	return out
}

func rewriteRouterTLS(lines []string, settings ingressSettings) []string {
	out := make([]string, 0, len(lines))
	inRouters := false
	for i := 0; i < len(lines); {
		line := lines[i]
		trimmed := strings.TrimSpace(line)
		indent := leadingSpaces(line)
		if indent == 2 && trimmed == "routers:" {
			inRouters = true
			out = append(out, line)
			i++
			continue
		}
		if inRouters && indent <= 2 && trimmed != "" && trimmed != "routers:" {
			inRouters = false
		}
		if inRouters && indent == 4 && strings.HasSuffix(trimmed, ":") {
			end := routerBlockEnd(lines, i)
			block := removeRouterTLSBlock(lines[i:end])
			if routerBlockHasRule(block) && settings.HTTPS {
				block = appendRouterTLSBlock(block, settings)
			}
			out = append(out, block...)
			i = end
			continue
		}
		out = append(out, line)
		i++
	}
	return out
}

func routerBlockEnd(lines []string, start int) int {
	for i := start + 1; i < len(lines); i++ {
		trimmed := strings.TrimSpace(lines[i])
		if trimmed == "" {
			continue
		}
		if leadingSpaces(lines[i]) <= 4 {
			return i
		}
	}
	return len(lines)
}

func removeRouterTLSBlock(lines []string) []string {
	out := make([]string, 0, len(lines))
	for i := 0; i < len(lines); {
		line := lines[i]
		if leadingSpaces(line) == 6 && strings.HasPrefix(strings.TrimSpace(line), "tls:") {
			i++
			for i < len(lines) {
				trimmed := strings.TrimSpace(lines[i])
				if trimmed != "" && leadingSpaces(lines[i]) <= 6 {
					break
				}
				i++
			}
			continue
		}
		out = append(out, line)
		i++
	}
	return out
}

func routerBlockHasRule(lines []string) bool {
	for _, line := range lines {
		if leadingSpaces(line) == 6 && strings.Contains(strings.TrimSpace(line), "rule:") {
			return true
		}
	}
	return false
}

func appendRouterTLSBlock(lines []string, settings ingressSettings) []string {
	insertAt := len(lines)
	for insertAt > 0 && strings.TrimSpace(lines[insertAt-1]) == "" {
		insertAt--
	}
	block := []string{"      tls: {}"}
	if settings.LetsEncrypt {
		block = []string{
			"      tls:",
			"        certResolver: letsencrypt",
		}
	}
	out := make([]string, 0, len(lines)+len(block))
	out = append(out, lines[:insertAt]...)
	out = append(out, block...)
	out = append(out, lines[insertAt:]...)
	return out
}

func writeIngressTLSFiles(ctx *config.Context, opts IngressOptions, settings ingressSettings) error {
	if ctx == nil {
		return nil
	}
	dir := ctx.ResolveProjectPath(filepath.FromSlash(opts.TraefikConfigDir))
	tlsPath := filepath.Join(dir, "00-tls.yml")
	redirectPath := filepath.Join(dir, "00-redirect.yml")
	if !settings.HTTPS || settings.LetsEncrypt {
		if err := removeIfExists(ctx, tlsPath); err != nil {
			return err
		}
	} else {
		if err := ctx.WriteFile(tlsPath, []byte(defaultTLSFile())); err != nil {
			return err
		}
	}
	if !settings.HTTPS {
		return removeIfExists(ctx, redirectPath)
	}
	return ctx.WriteFile(redirectPath, []byte(redirectTLSFile(opts.HTTPEntrypoint)))
}

func defaultTLSFile() string {
	return ensureTrailingNewline(defaultTLSYAML)
}

func redirectTLSFile(httpEntry string) string {
	return ensureTrailingNewline(strings.ReplaceAll(redirectHTTPToHTTPSYAML, "{http_entrypoint}", httpEntry))
}

func ensureTrailingNewline(value string) string {
	if strings.HasSuffix(value, "\n") {
		return value
	}
	return value + "\n"
}

func removeIfExists(ctx *config.Context, path string) error {
	if ctx == nil {
		return nil
	}
	return ctx.RemoveFile(path)
}

func ingressRouterHost(opts IngressOptions, router, domain string) string {
	if opts.RouterHosts != nil {
		if template := strings.TrimSpace(opts.RouterHosts[router]); template != "" {
			return renderDomainTemplate(template, domain)
		}
	}
	return domain
}

func renderIngressTemplate(template string, settings ingressSettings) string {
	value := strings.TrimSpace(template)
	if value == "" {
		return ""
	}
	replacer := strings.NewReplacer(
		"{scheme}", settings.Scheme,
		"{domain}", settings.Domain,
		"{base_url}", settings.Scheme+"://"+settings.Domain,
		"{https_enabled}", fmt.Sprintf("%t", settings.HTTPS),
		"{mode}", settings.Mode,
	)
	return replacer.Replace(value)
}

func renderDomainTemplate(template, domain string) string {
	return strings.NewReplacer("{domain}", domain).Replace(template)
}

func replaceLegacyDomainTemplates(value, domain string) string {
	replacements := map[string]string{
		`{{ env "DOMAIN" }}`:                                           domain,
		`{{ env "DOMAIN" | default "localhost" }}`:                     domain,
		`{{ trimPrefix "www." (env "DOMAIN") }}`:                       strings.TrimPrefix(domain, "www."),
		`{{ trimPrefix "www." (env "DOMAIN" | default "localhost") }}`: strings.TrimPrefix(domain, "www."),
	}
	out := value
	for old, replacement := range replacements {
		out = strings.ReplaceAll(out, old, replacement)
	}
	return out
}

func removeServiceStringVariants(compose *corecomponent.ComposeFile, service, key, value string) {
	for _, candidate := range []string{value, `"` + value + `"`, `'` + value + `'`} {
		_ = compose.RemoveServiceString(service, key, candidate)
	}
}

func appendUniqueStrings(values []string, extras ...string) []string {
	seen := map[string]bool{}
	out := []string{}
	for _, value := range append(values, extras...) {
		value = strings.TrimSpace(value)
		if value == "" || seen[value] {
			continue
		}
		seen[value] = true
		out = append(out, value)
	}
	return out
}
