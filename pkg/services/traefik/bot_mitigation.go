package traefik

import (
	"archive/zip"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	corecomponent "github.com/libops/sitectl/pkg/component"
	yaml "gopkg.in/yaml.v3"
)

const (
	// BotMitigationName is the component name for Traefik captcha-protect bot mitigation.
	BotMitigationName = "bot-mitigation"
	// BotMitigationStateOn enables captcha-protect bot mitigation.
	BotMitigationStateOn = "on"
	// BotMitigationStateOff disables captcha-protect bot mitigation.
	BotMitigationStateOff = "off"

	captchaProtectCommand       = "--experimental.localPlugins.captcha-protect.modulename=github.com/libops/captcha-protect"
	captchaProtectPluginVolume  = "./conf/traefik/plugins/captcha-protect:/plugins-local/src/github.com/libops/captcha-protect:r"
	captchaProtectTemplateMount = "./conf/traefik/challenge.tmpl.html:/challenge.tmpl.html:ro"
	turnstileSiteKeyDefault     = "${TURNSTILE_SITE_KEY:-1x00000000000000000000AA}"
	turnstileSecretKeyDefault   = "${TURNSTILE_SECRET_KEY:-1x0000000000000000000000000000000AA}" // #nosec G101 -- documented Cloudflare Turnstile test key fallback; runtime warning tells users to configure real keys.
	captchaProtectSourceURL     = "https://github.com/libops/captcha-protect/archive/refs/tags/v2.0.0.zip"
	captchaProtectSourceSHA256  = "eed10b2f3deb816971cb93cec5f95bf208b9cff527517834d82c2ea51cf76f87"
	// maxCaptchaProtectArchiveBytes bounds memory use before the archive hash is verified.
	maxCaptchaProtectArchiveBytes = 8 << 20
	captchaProtectInstallMarker   = ".sitectl-source"
	// Hash of the extracted, post-filter plugin tree from captchaProtectSourceURL.
	// Run `make bump-captcha-protect` to refresh captchaProtectSourceURL,
	// captchaProtectSourceSHA256, and this tree hash from the latest GitHub
	// release. Use CAPTCHA_PROTECT_TAG=vX.Y.Z for a specific release.
	// When bumping captchaProtectSourceURL, regenerate by downloading the zip,
	// verifying captchaProtectSourceSHA256, extracting it through
	// extractCaptchaProtectArchive, and running hashCaptchaProtectSourceTree
	// before writeCaptchaProtectInstallMarker adds captchaProtectInstallMarker.
	// The extraction filter intentionally drops ci/, .github/, renovate.json5,
	// and *_test.go files before this hash is computed.
	captchaProtectExtractedTreeSHA256 = "de7856faaea3e0c6f029f3e02975b9ae096f6f4316895a4d521b3fc9b3229bca"
)

var captchaProtectVolumes = []string{
	captchaProtectPluginVolume,
	captchaProtectTemplateMount,
}

var defaultCaptchaProtectMiddleware = CaptchaProtectMiddlewareOptions{ // #nosec G101 -- Turnstile fields are environment-template names, not embedded credential values.
	Window:              864000,
	Mode:                "regex",
	ProtectRoutes:       "^/",
	ExcludeRoutes:       []string{`\/oai\/request`, `\/node\/\d+\/manifest`},
	ProtectParameters:   "true",
	ChallengeTemplate:   "/challenge.tmpl.html",
	ChallengeStatusCode: 429,
	CaptchaProvider:     "turnstile",
	SiteKey:             `{{ env "TURNSTILE_SITE_KEY" }}`,
	SecretKey:           `{{ env "TURNSTILE_SECRET_KEY" }}`,
	IPForwardedHeader:   "X-Forwarded-For",
	GoodBots: []string{
		"apple.com",
		"archive.org",
		"commoncrawl.org",
		"duckduckgo.com",
		"iframely.com",
		"facebook.com",
		"instagram.com",
		"kagibot.org",
		"linkedin.com",
		"msn.com",
		"openalex.org",
		"twitter.com",
		"x.com",
	},
	PersistentStateFile:     "/acme/state.json",
	ProtectFileExtensions:   "php,html,jp2,tif,tiff",
	PeriodSeconds:           30,
	FailureThreshold:        3,
	EnableGooglebotIPCheck:  "true",
	EnableUptimeRobotBypass: "true",
}

var (
	fetchCaptchaProtectArchive       = fetchVerifiedCaptchaProtectArchive
	captchaProtectExpectedTreeSHA256 = captchaProtectExtractedTreeSHA256
)

// CaptchaProtectMiddlewareOptions configures the generated captcha-protect
// middleware block.
type CaptchaProtectMiddlewareOptions struct {
	Window                  int
	Mode                    string
	ProtectRoutes           string
	ExcludeRoutes           []string
	ProtectParameters       string
	ChallengeTemplate       string
	ChallengeURL            string
	ChallengeStatusCode     int
	CaptchaProvider         string
	SiteKey                 string
	SecretKey               string
	IPForwardedHeader       string
	GoodBots                []string
	PersistentStateFile     string
	ProtectFileExtensions   string
	PeriodSeconds           int
	FailureThreshold        int
	EnableGooglebotIPCheck  string
	EnableUptimeRobotBypass string
}

// BotMitigationOptions configures a reusable Traefik bot-mitigation component
// for an application router.
type BotMitigationOptions struct {
	Name             string
	RouterName       string
	RouterConfigPath string
	MiddlewareName   string
	Middleware       CaptchaProtectMiddlewareOptions
}

// BotMitigation returns reusable Traefik captcha-protect component metadata.
//
// ApplyBotMitigation is the sole mutating entrypoint. The returned definition
// is intentionally metadata-only so component review/create prompts cannot
// drift from the command, volume, router, and plugin-source mutations.
func BotMitigation(opts BotMitigationOptions) corecomponent.Definition {
	opts = NormalizeBotMitigationOptions(opts)
	return corecomponent.Definition{
		Name:                opts.Name,
		DefaultState:        corecomponent.StateOff,
		DefaultDisposition:  corecomponent.DispositionDisabled,
		AllowedDispositions: []corecomponent.Disposition{corecomponent.DispositionDisabled, corecomponent.DispositionEnabled},
		PromptOnCreate:      true,
		Guidance: corecomponent.StateGuidance{
			Question:     fmt.Sprintf("Control whether Traefik protects %s routes with the captcha-protect Turnstile middleware.", opts.RouterName),
			EnabledHelp:  fmt.Sprintf("Enable captcha-protect as a local Traefik plugin on the %s router.", opts.RouterName),
			DisabledHelp: fmt.Sprintf("Leave %s routes without the captcha-protect middleware.", opts.RouterName),
		},
		Gates: corecomponent.GateSpec{
			LocalOnly: true,
		},
		Behavior: corecomponent.Behavior{
			Idempotent: true,
			Enable: corecomponent.TransitionBehavior{
				DataMigration: corecomponent.DataMigrationNone,
				Summary:       fmt.Sprintf("Enabling bot mitigation configures Traefik to load captcha-protect and challenge %s traffic with Turnstile.", opts.RouterName),
			},
			Disable: corecomponent.TransitionBehavior{
				DataMigration: corecomponent.DataMigrationNone,
				Summary:       fmt.Sprintf("Disabling bot mitigation removes captcha-protect Traefik command, mounts, environment, and %s router middleware.", opts.RouterName),
			},
		},
	}
}

// ApplyBotMitigation applies or removes Traefik captcha-protect configuration.
func ApplyBotMitigation(projectDir, state string, opts BotMitigationOptions) error {
	return ApplyBotMitigationContext(context.Background(), projectDir, state, opts)
}

// ApplyBotMitigationContext applies or removes Traefik captcha-protect
// configuration, using ctx for network work performed while enabling.
func ApplyBotMitigationContext(ctx context.Context, projectDir, state string, opts BotMitigationOptions) error {
	if ctx == nil {
		ctx = context.Background()
	}
	if projectDir == "" {
		projectDir = "."
	}
	opts = NormalizeBotMitigationOptions(opts)
	switch state {
	case BotMitigationStateOn:
		return enableBotMitigation(ctx, projectDir, opts)
	case BotMitigationStateOff:
		return disableBotMitigation(projectDir, opts)
	default:
		return fmt.Errorf("invalid bot mitigation state %q: expected on or off", state)
	}
}

// NormalizeBotMitigationOptions applies defaults for reusable bot mitigation.
func NormalizeBotMitigationOptions(opts BotMitigationOptions) BotMitigationOptions {
	if strings.TrimSpace(opts.Name) == "" {
		opts.Name = BotMitigationName
	}
	if strings.TrimSpace(opts.RouterName) == "" {
		opts.RouterName = "app"
	}
	if strings.TrimSpace(opts.RouterConfigPath) == "" {
		opts.RouterConfigPath = filepath.ToSlash(filepath.Join("conf", "traefik", opts.RouterName+".yml"))
	}
	if strings.TrimSpace(opts.MiddlewareName) == "" {
		opts.MiddlewareName = "captcha-protect"
	}
	opts.Middleware = normalizeCaptchaProtectMiddlewareOptions(opts.Middleware)
	return opts
}

func normalizeCaptchaProtectMiddlewareOptions(opts CaptchaProtectMiddlewareOptions) CaptchaProtectMiddlewareOptions {
	defaults := defaultCaptchaProtectMiddlewareOptions()
	if opts.Window == 0 {
		opts.Window = defaults.Window
	}
	if strings.TrimSpace(opts.Mode) == "" {
		opts.Mode = defaults.Mode
	}
	if strings.TrimSpace(opts.ProtectRoutes) == "" {
		opts.ProtectRoutes = defaults.ProtectRoutes
	}
	if len(opts.ExcludeRoutes) == 0 {
		opts.ExcludeRoutes = defaults.ExcludeRoutes
	}
	if strings.TrimSpace(opts.ProtectParameters) == "" {
		opts.ProtectParameters = defaults.ProtectParameters
	}
	if strings.TrimSpace(opts.ChallengeTemplate) == "" {
		opts.ChallengeTemplate = defaults.ChallengeTemplate
	}
	if opts.ChallengeStatusCode == 0 {
		opts.ChallengeStatusCode = defaults.ChallengeStatusCode
	}
	if strings.TrimSpace(opts.CaptchaProvider) == "" {
		opts.CaptchaProvider = defaults.CaptchaProvider
	}
	if strings.TrimSpace(opts.SiteKey) == "" {
		opts.SiteKey = defaults.SiteKey
	}
	if strings.TrimSpace(opts.SecretKey) == "" {
		opts.SecretKey = defaults.SecretKey
	}
	if strings.TrimSpace(opts.IPForwardedHeader) == "" {
		opts.IPForwardedHeader = defaults.IPForwardedHeader
	}
	if len(opts.GoodBots) == 0 {
		opts.GoodBots = defaults.GoodBots
	}
	if strings.TrimSpace(opts.PersistentStateFile) == "" {
		opts.PersistentStateFile = defaults.PersistentStateFile
	}
	if strings.TrimSpace(opts.ProtectFileExtensions) == "" {
		opts.ProtectFileExtensions = defaults.ProtectFileExtensions
	}
	if opts.PeriodSeconds == 0 {
		opts.PeriodSeconds = defaults.PeriodSeconds
	}
	if opts.FailureThreshold == 0 {
		opts.FailureThreshold = defaults.FailureThreshold
	}
	if strings.TrimSpace(opts.EnableGooglebotIPCheck) == "" {
		opts.EnableGooglebotIPCheck = defaults.EnableGooglebotIPCheck
	}
	if strings.TrimSpace(opts.EnableUptimeRobotBypass) == "" {
		opts.EnableUptimeRobotBypass = defaults.EnableUptimeRobotBypass
	}
	return opts
}

func defaultCaptchaProtectMiddlewareOptions() CaptchaProtectMiddlewareOptions {
	defaults := defaultCaptchaProtectMiddleware
	defaults.ExcludeRoutes = append([]string{}, defaultCaptchaProtectMiddleware.ExcludeRoutes...)
	defaults.GoodBots = append([]string{}, defaultCaptchaProtectMiddleware.GoodBots...)
	return defaults
}

func enableBotMitigation(ctx context.Context, projectDir string, opts BotMitigationOptions) error {
	if err := updateComposeForBotMitigation(projectDir, true); err != nil {
		return err
	}
	if err := updateRouterConfigForBotMitigation(filepath.Join(projectDir, filepath.FromSlash(opts.RouterConfigPath)), opts, true); err != nil {
		return err
	}
	return ensureBotMitigationFiles(ctx, projectDir)
}

func disableBotMitigation(projectDir string, opts BotMitigationOptions) error {
	if err := updateComposeForBotMitigation(projectDir, false); err != nil {
		return err
	}
	if err := updateRouterConfigForBotMitigation(filepath.Join(projectDir, filepath.FromSlash(opts.RouterConfigPath)), opts, false); err != nil {
		return err
	}
	return removeBotMitigationFiles(projectDir)
}

func updateComposeForBotMitigation(projectDir string, enabled bool) error {
	path := filepath.Join(projectDir, "docker-compose.yml")
	data, err := os.ReadFile(path) // #nosec G304 -- compose file path is an explicit project configuration path.
	if err != nil {
		return fmt.Errorf("read compose file: %w", err)
	}
	defs, err := corecomponent.ParseComposeDefinitions(data)
	if err != nil {
		return fmt.Errorf("parse compose file: %w", err)
	}
	if defs == nil || defs.Services == nil || defs.Services["traefik"] == nil {
		return fmt.Errorf("docker-compose.yml does not define services.traefik")
	}
	compose, err := corecomponent.LoadComposeFile(path)
	if err != nil {
		return err
	}

	if enabled {
		if err := compose.AppendUniqueServiceString("traefik", "command", captchaProtectCommand); err != nil {
			return fmt.Errorf("add captcha-protect Traefik command: %w", err)
		}
		for _, volume := range captchaProtectVolumes {
			if err := compose.AppendUniqueServiceString("traefik", "volumes", volume); err != nil {
				return fmt.Errorf("add captcha-protect Traefik volume: %w", err)
			}
		}
		if err := compose.SetServiceEnv("traefik", "TURNSTILE_SITE_KEY", turnstileSiteKeyDefault); err != nil {
			return fmt.Errorf("set TURNSTILE_SITE_KEY: %w", err)
		}
		if err := compose.SetServiceEnv("traefik", "TURNSTILE_SECRET_KEY", turnstileSecretKeyDefault); err != nil {
			return fmt.Errorf("set TURNSTILE_SECRET_KEY: %w", err)
		}
	} else {
		if err := compose.RemoveServiceString("traefik", "command", captchaProtectCommand); err != nil {
			return fmt.Errorf("remove captcha-protect Traefik command: %w", err)
		}
		for _, volume := range captchaProtectVolumes {
			if err := compose.RemoveServiceString("traefik", "volumes", volume); err != nil {
				return fmt.Errorf("remove captcha-protect Traefik volume: %w", err)
			}
		}
		if err := compose.DeleteServiceEnv("traefik", "TURNSTILE_SITE_KEY"); err != nil {
			return fmt.Errorf("remove TURNSTILE_SITE_KEY: %w", err)
		}
		if err := compose.DeleteServiceEnv("traefik", "TURNSTILE_SECRET_KEY"); err != nil {
			return fmt.Errorf("remove TURNSTILE_SECRET_KEY: %w", err)
		}
	}

	return compose.Save()
}

func updateRouterConfigForBotMitigation(path string, opts BotMitigationOptions, enabled bool) error {
	data, err := os.ReadFile(path) // #nosec G304 -- traefik config path is an explicit project configuration path.
	if err != nil {
		return fmt.Errorf("read traefik router config: %w", err)
	}
	if hasTraefikTemplateControlLines(data) {
		return updateTemplatedRouterConfigForBotMitigation(path, data, opts, enabled)
	}

	doc, err := corecomponent.LoadYAMLDocument(quoteTraefikTemplateScalars(data))
	if err != nil {
		return fmt.Errorf("load traefik router yaml: %w", err)
	}

	hasRouters, err := doc.HasPath(".http.routers")
	if err != nil {
		return fmt.Errorf("read traefik routers: %w", err)
	}
	if !hasRouters {
		return fmt.Errorf("traefik router config does not define http.routers")
	}
	routerPath := ".http.routers." + opts.RouterName
	hasRouter, err := doc.HasPath(routerPath)
	if err != nil {
		return fmt.Errorf("read traefik router %s: %w", opts.RouterName, err)
	}
	if !hasRouter {
		return fmt.Errorf("traefik router config does not define http.routers.%s", opts.RouterName)
	}

	routerMiddlewarePath := routerPath + ".middlewares"
	if err := doc.RemoveString(routerMiddlewarePath, opts.MiddlewareName); err != nil {
		return fmt.Errorf("remove %s middleware reference: %w", opts.MiddlewareName, err)
	}
	middlewarePath := ".http.middlewares." + opts.MiddlewareName
	if err := doc.DeletePath(middlewarePath); err != nil {
		return fmt.Errorf("remove %s middleware block: %w", opts.MiddlewareName, err)
	}

	if enabled {
		if err := doc.AppendUniqueString(routerMiddlewarePath, opts.MiddlewareName); err != nil {
			return fmt.Errorf("add %s middleware reference: %w", opts.MiddlewareName, err)
		}
		if err := doc.SetValue(middlewarePath, captchaProtectMiddlewareDefinition(opts.Middleware)); err != nil {
			return fmt.Errorf("write %s middleware block: %w", opts.MiddlewareName, err)
		}
	}

	out, err := doc.Bytes()
	if err != nil {
		return fmt.Errorf("marshal traefik router config: %w", err)
	}
	return os.WriteFile(path, out, 0o600) // #nosec G703 -- path is the selected project's traefik router config.
}

func updateTemplatedRouterConfigForBotMitigation(path string, data []byte, opts BotMitigationOptions, enabled bool) error {
	lines := strings.Split(string(data), "\n")
	var err error
	lines = removeTemplatedHTTPMiddlewareBlock(lines, opts.MiddlewareName)
	if _, ok := findTemplatedRouter(lines, opts.RouterName); !ok {
		return fmt.Errorf("traefik router config does not define http.routers.%s", opts.RouterName)
	}
	lines, err = removeTemplatedRouterMiddleware(lines, opts.RouterName, opts.MiddlewareName)
	if err != nil {
		return err
	}
	if enabled {
		lines, err = appendTemplatedRouterMiddleware(lines, opts.RouterName, opts.MiddlewareName)
		if err != nil {
			return err
		}
		lines, err = appendTemplatedHTTPMiddlewareBlock(lines, opts)
		if err != nil {
			return err
		}
	}
	return os.WriteFile(path, []byte(strings.Join(lines, "\n")), 0o600) // #nosec G703 -- path is the selected project's traefik router config.
}

func hasTraefikTemplateControlLines(data []byte) bool {
	for _, line := range strings.Split(string(data), "\n") {
		if isTraefikTemplateControlLine(line) {
			return true
		}
	}
	return false
}

func isTraefikTemplateControlLine(line string) bool {
	trimmed := strings.TrimSpace(line)
	if !strings.HasPrefix(trimmed, "{{") || !strings.HasSuffix(trimmed, "}}") {
		return false
	}
	return strings.Contains(trimmed, " if ") ||
		strings.Contains(trimmed, " if(") ||
		strings.Contains(trimmed, " else") ||
		strings.Contains(trimmed, " end ")
}

func findTemplatedRouter(lines []string, routerName string) (int, bool) {
	routersIdx, ok := findTemplatedHTTPChild(lines, "routers")
	if !ok {
		return 0, false
	}
	routersEnd := findTemplatedBlockEnd(lines, routersIdx, 2)
	return findTemplatedMapKey(lines, routersIdx+1, routersEnd, routerName, 4)
}

func removeTemplatedRouterMiddleware(lines []string, routerName, middlewareName string) ([]string, error) {
	routerIdx, ok := findTemplatedRouter(lines, routerName)
	if !ok {
		return lines, fmt.Errorf("traefik router config does not define http.routers.%s", routerName)
	}
	middlewaresIdx, ok := findTemplatedRouterChild(lines, routerIdx, "middlewares")
	if !ok {
		return lines, nil
	}
	middlewaresEnd := findTemplatedBlockEnd(lines, middlewaresIdx, 6)
	filtered := make([]string, 0, middlewaresEnd-middlewaresIdx)
	filtered = append(filtered, lines[middlewaresIdx])
	for _, line := range lines[middlewaresIdx+1 : middlewaresEnd] {
		if strings.TrimSpace(line) == "- "+middlewareName {
			continue
		}
		filtered = append(filtered, line)
	}
	if !hasIndentedTemplatedContent(filtered[1:]) {
		return append(lines[:middlewaresIdx], lines[middlewaresEnd:]...), nil
	}
	return append(lines[:middlewaresIdx], append(filtered, lines[middlewaresEnd:]...)...), nil
}

func appendTemplatedRouterMiddleware(lines []string, routerName, middlewareName string) ([]string, error) {
	routerIdx, ok := findTemplatedRouter(lines, routerName)
	if !ok {
		return lines, fmt.Errorf("traefik router config does not define http.routers.%s", routerName)
	}
	middlewaresIdx, ok := findTemplatedRouterChild(lines, routerIdx, "middlewares")
	if ok {
		middlewaresEnd := findTemplatedBlockEnd(lines, middlewaresIdx, 6)
		for _, line := range lines[middlewaresIdx+1 : middlewaresEnd] {
			if strings.TrimSpace(line) == "- "+middlewareName {
				return lines, nil
			}
		}
		insertAt := insertionIndexBeforeTrailingBlanks(lines, middlewaresEnd)
		return insertLines(lines, insertAt, []string{"        - " + middlewareName}), nil
	}
	routerEnd := findTemplatedRouterEditableEnd(lines, routerIdx)
	return insertLines(lines, routerEnd, []string{
		"      middlewares:",
		"        - " + middlewareName,
	}), nil
}

func removeTemplatedHTTPMiddlewareBlock(lines []string, middlewareName string) []string {
	middlewaresIdx, ok := findTemplatedHTTPChild(lines, "middlewares")
	if !ok {
		return lines
	}
	middlewaresEnd := findTemplatedBlockEnd(lines, middlewaresIdx, 2)
	childIdx, ok := findTemplatedMapKey(lines, middlewaresIdx+1, middlewaresEnd, middlewareName, 4)
	if !ok {
		return lines
	}
	childEnd := findTemplatedBlockEnd(lines, childIdx, 4)
	lines = append(lines[:childIdx], lines[childEnd:]...)

	middlewaresIdx, ok = findTemplatedHTTPChild(lines, "middlewares")
	if !ok {
		return lines
	}
	middlewaresEnd = findTemplatedBlockEnd(lines, middlewaresIdx, 2)
	if !hasIndentedTemplatedContent(lines[middlewaresIdx+1 : middlewaresEnd]) {
		return append(lines[:middlewaresIdx], lines[middlewaresEnd:]...)
	}
	return lines
}

func appendTemplatedHTTPMiddlewareBlock(lines []string, opts BotMitigationOptions) ([]string, error) {
	entry, err := renderTemplatedHTTPMiddlewareEntry(opts)
	if err != nil {
		return nil, err
	}
	middlewaresIdx, ok := findTemplatedHTTPChild(lines, "middlewares")
	if ok {
		middlewaresEnd := findTemplatedBlockEnd(lines, middlewaresIdx, 2)
		insertAt := insertionIndexBeforeTrailingBlanks(lines, middlewaresEnd)
		return insertLines(lines, insertAt, entry), nil
	}
	insertAt := insertionIndexBeforeTrailingBlanks(lines, len(lines))
	block := append([]string{"  middlewares:"}, entry...)
	return insertLines(lines, insertAt, block), nil
}

func renderTemplatedHTTPMiddlewareEntry(opts BotMitigationOptions) ([]string, error) {
	var buf bytes.Buffer
	encoder := yaml.NewEncoder(&buf)
	encoder.SetIndent(2)
	if err := encoder.Encode(map[string]traefikPluginMiddleware{
		opts.MiddlewareName: captchaProtectMiddlewareDefinition(opts.Middleware),
	}); err != nil {
		_ = encoder.Close()
		return nil, fmt.Errorf("marshal %s middleware block: %w", opts.MiddlewareName, err)
	}
	if err := encoder.Close(); err != nil {
		return nil, fmt.Errorf("close %s middleware encoder: %w", opts.MiddlewareName, err)
	}
	raw := strings.Split(strings.TrimRight(buf.String(), "\n"), "\n")
	for i := range raw {
		raw[i] = "    " + raw[i]
	}
	return raw, nil
}

func findTemplatedRouterChild(lines []string, routerIdx int, key string) (int, bool) {
	end := findTemplatedRouterEditableEnd(lines, routerIdx)
	return findTemplatedMapKey(lines, routerIdx+1, end, key, 6)
}

func findTemplatedRouterEditableEnd(lines []string, routerIdx int) int {
	for i := routerIdx + 1; i < len(lines); i++ {
		line := lines[i]
		if strings.TrimSpace(line) == "" {
			continue
		}
		if isTraefikTemplateControlLine(line) {
			return i
		}
		if leadingSpaces(line) <= 4 {
			return i
		}
	}
	return len(lines)
}

func findTemplatedHTTPChild(lines []string, key string) (int, bool) {
	httpIdx, ok := findTemplatedMapKey(lines, 0, len(lines), "http", 0)
	if !ok {
		return 0, false
	}
	return findTemplatedMapKey(lines, httpIdx+1, len(lines), key, 2)
}

func findTemplatedMapKey(lines []string, start, end int, key string, indent int) (int, bool) {
	prefix := strings.Repeat(" ", indent) + key + ":"
	for i := start; i < end && i < len(lines); i++ {
		line := lines[i]
		if strings.TrimSpace(line) == "" || isTraefikTemplateControlLine(line) {
			continue
		}
		currentIndent := leadingSpaces(line)
		if currentIndent < indent {
			continue
		}
		if currentIndent == indent && strings.HasPrefix(line, prefix) {
			return i, true
		}
	}
	return 0, false
}

func findTemplatedBlockEnd(lines []string, start int, indent int) int {
	for i := start + 1; i < len(lines); i++ {
		line := lines[i]
		if strings.TrimSpace(line) == "" {
			continue
		}
		if isTraefikTemplateControlLine(line) {
			return i
		}
		if leadingSpaces(line) <= indent {
			return i
		}
	}
	return len(lines)
}

func hasIndentedTemplatedContent(lines []string) bool {
	for _, line := range lines {
		if strings.TrimSpace(line) != "" && !isTraefikTemplateControlLine(line) {
			return true
		}
	}
	return false
}

func insertLines(lines []string, index int, inserted []string) []string {
	result := make([]string, 0, len(lines)+len(inserted))
	result = append(result, lines[:index]...)
	result = append(result, inserted...)
	result = append(result, lines[index:]...)
	return result
}

func insertionIndexBeforeTrailingBlanks(lines []string, index int) int {
	for index > 0 && strings.TrimSpace(lines[index-1]) == "" {
		index--
	}
	return index
}

func leadingSpaces(line string) int {
	return len(line) - len(strings.TrimLeft(line, " "))
}

func quoteTraefikTemplateScalars(data []byte) []byte {
	lines := strings.SplitAfter(string(data), "\n")
	for i, line := range lines {
		if !strings.Contains(line, "{{") || !strings.Contains(line, "}}") {
			continue
		}
		lineEnd := ""
		body := line
		if strings.HasSuffix(body, "\n") {
			lineEnd = "\n"
			body = strings.TrimSuffix(body, "\n")
		}
		prefix, value, ok := splitInlineYAMLValue(body)
		if !ok {
			continue
		}
		trimmed := strings.TrimSpace(value)
		if strings.HasPrefix(trimmed, "|") || strings.HasPrefix(trimmed, ">") {
			continue
		}
		lines[i] = prefix + singleQuoteYAMLScalar(unwrapYAMLScalarQuotes(trimmed)) + lineEnd
	}
	return []byte(strings.Join(lines, ""))
}

func splitInlineYAMLValue(line string) (string, string, bool) {
	for i := 0; i < len(line); i++ {
		if line[i] != ':' {
			continue
		}
		if i+1 < len(line) && line[i+1] != ' ' && line[i+1] != '\t' {
			continue
		}
		valueStart := i + 1
		for valueStart < len(line) && (line[valueStart] == ' ' || line[valueStart] == '\t') {
			valueStart++
		}
		if valueStart >= len(line) {
			return "", "", false
		}
		return line[:valueStart], line[valueStart:], true
	}

	trimmed := strings.TrimLeft(line, " \t")
	indentLen := len(line) - len(trimmed)
	if !strings.HasPrefix(trimmed, "- ") {
		return "", "", false
	}
	valueStart := indentLen + len("- ")
	if valueStart >= len(line) {
		return "", "", false
	}
	return line[:valueStart], line[valueStart:], true
}

func unwrapYAMLScalarQuotes(value string) string {
	if len(value) < 2 {
		return value
	}
	quote := value[0]
	if (quote != '\'' && quote != '"') || value[len(value)-1] != quote {
		return value
	}
	if quote == '"' {
		if unquoted, err := strconv.Unquote(value); err == nil {
			return unquoted
		}
		return value[1 : len(value)-1]
	}
	return strings.ReplaceAll(value[1:len(value)-1], "''", "'")
}

func singleQuoteYAMLScalar(value string) string {
	return "'" + strings.ReplaceAll(value, "'", "''") + "'"
}

func ensureBotMitigationFiles(ctx context.Context, projectDir string) error {
	pluginDir := filepath.Join(projectDir, "conf", "traefik", "plugins", "captcha-protect")
	if err := installCaptchaProtectPlugin(ctx, pluginDir); err != nil {
		return err
	}

	templatePath := filepath.Join(projectDir, "conf", "traefik", "challenge.tmpl.html")
	if _, err := os.Stat(templatePath); err == nil {
		return nil
	} else if !os.IsNotExist(err) {
		return fmt.Errorf("stat challenge template: %w", err)
	}
	templateData, err := os.ReadFile(filepath.Join(pluginDir, "challenge.tmpl.html")) // #nosec G304 -- plugin source path is controlled by this installer.
	if err != nil {
		return fmt.Errorf("read captcha-protect default challenge template: %w", err)
	}
	return os.WriteFile(templatePath, templateData, 0o644) // #nosec G306,G703 -- generated web template must be project-readable and is scoped under the selected project.
}

func removeBotMitigationFiles(projectDir string) error {
	pluginDir := filepath.Join(projectDir, "conf", "traefik", "plugins", "captcha-protect")
	if err := os.RemoveAll(pluginDir); err != nil {
		return fmt.Errorf("remove captcha-protect plugin source: %w", err)
	}
	templatePath := filepath.Join(projectDir, "conf", "traefik", "challenge.tmpl.html")
	if err := os.Remove(templatePath); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("remove captcha-protect challenge template: %w", err)
	}
	return nil
}

func installCaptchaProtectPlugin(ctx context.Context, targetDir string) error {
	current, err := captchaProtectPluginCurrent(targetDir)
	if err != nil {
		return err
	}
	if current {
		return nil
	}

	archiveData, err := fetchCaptchaProtectArchive(ctx)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(targetDir), 0o755); err != nil { // #nosec G301 -- generated project plugin directory must remain readable by the compose stack.
		return fmt.Errorf("create captcha-protect plugin parent directory: %w", err)
	}
	tmpDir, err := os.MkdirTemp(filepath.Dir(targetDir), ".captcha-protect-*")
	if err != nil {
		return fmt.Errorf("create temporary captcha-protect extraction directory: %w", err)
	}
	cleanupTemp := true
	defer func() {
		if cleanupTemp {
			_ = os.RemoveAll(tmpDir)
		}
	}()

	if err := extractCaptchaProtectArchive(archiveData, tmpDir); err != nil {
		return err
	}
	if err := verifyCaptchaProtectSourceTree(tmpDir, captchaProtectExpectedTreeSHA256); err != nil {
		return err
	}
	if err := writeCaptchaProtectInstallMarker(tmpDir); err != nil {
		return err
	}
	if err := os.RemoveAll(targetDir); err != nil {
		return fmt.Errorf("replace captcha-protect plugin directory: %w", err)
	}
	if err := os.Rename(tmpDir, targetDir); err != nil {
		return fmt.Errorf("install captcha-protect plugin source: %w", err)
	}
	cleanupTemp = false
	return nil
}

func captchaProtectPluginCurrent(targetDir string) (bool, error) {
	return captchaProtectPluginCurrentForTreeSHA(targetDir, captchaProtectExpectedTreeSHA256)
}

func captchaProtectPluginCurrentForTreeSHA(targetDir, expectedTreeSHA string) (bool, error) {
	info, err := os.Stat(targetDir)
	if err != nil {
		if os.IsNotExist(err) {
			return false, nil
		}
		return false, fmt.Errorf("stat captcha-protect plugin source: %w", err)
	}
	if !info.IsDir() {
		return false, nil
	}

	treeSHA, err := hashCaptchaProtectSourceTree(targetDir)
	if err != nil {
		return false, err
	}
	return treeSHA == expectedTreeSHA, nil
}

func verifyCaptchaProtectSourceTree(root, expectedTreeSHA string) error {
	treeSHA, err := hashCaptchaProtectSourceTree(root)
	if err != nil {
		return err
	}
	if treeSHA != expectedTreeSHA {
		return fmt.Errorf("captcha-protect extracted tree sha256 mismatch: expected %s, got %s", expectedTreeSHA, treeSHA)
	}
	return nil
}

func readCaptchaProtectInstallMarker(targetDir string) (map[string]string, error) {
	data, err := os.ReadFile(filepath.Join(targetDir, captchaProtectInstallMarker)) // #nosec G304 -- plugin source path is controlled by this installer.
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("read captcha-protect install marker: %w", err)
	}
	marker := map[string]string{}
	for _, line := range strings.Split(string(data), "\n") {
		key, value, ok := strings.Cut(strings.TrimSpace(line), "=")
		if ok && key != "" {
			marker[key] = value
		}
	}
	return marker, nil
}

func writeCaptchaProtectInstallMarker(targetDir string) error {
	treeSHA, err := hashCaptchaProtectSourceTree(targetDir)
	if err != nil {
		return err
	}
	data := fmt.Sprintf("source_url=%s\narchive_sha256=%s\ntree_sha256=%s\n", captchaProtectSourceURL, captchaProtectSourceSHA256, treeSHA)
	if err := os.WriteFile(filepath.Join(targetDir, captchaProtectInstallMarker), []byte(data), 0o644); err != nil { // #nosec G306,G703 -- marker is project-readable metadata under generated plugin source.
		return fmt.Errorf("write captcha-protect install marker: %w", err)
	}
	return nil
}

func hashCaptchaProtectSourceTree(root string) (string, error) {
	hash := sha256.New()
	if err := filepath.WalkDir(root, func(path string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.IsDir() {
			return nil
		}
		rel, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		rel = filepath.ToSlash(rel)
		if rel == captchaProtectInstallMarker {
			return nil
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		if !info.Mode().IsRegular() {
			return fmt.Errorf("captcha-protect source contains non-regular file %s", rel)
		}

		_, _ = hash.Write([]byte("file\x00" + rel + "\x00"))
		source, err := os.Open(path) // #nosec G304,G122 -- walked regular file is under the controlled plugin source directory.
		if err != nil {
			return err
		}
		if _, err := io.Copy(hash, source); err != nil {
			_ = source.Close()
			return err
		}
		if err := source.Close(); err != nil {
			return err
		}
		_, _ = hash.Write([]byte{0})
		return nil
	}); err != nil {
		return "", fmt.Errorf("hash captcha-protect plugin source: %w", err)
	}
	return hex.EncodeToString(hash.Sum(nil)), nil
}

func fetchVerifiedCaptchaProtectArchive(ctx context.Context) ([]byte, error) {
	archiveData, err := downloadCaptchaProtectArchive(ctx, captchaProtectSourceURL)
	if err != nil {
		return nil, err
	}
	sum := sha256.Sum256(archiveData)
	if got := hex.EncodeToString(sum[:]); got != captchaProtectSourceSHA256 {
		return nil, fmt.Errorf("captcha-protect archive sha256 mismatch: expected %s, got %s", captchaProtectSourceSHA256, got)
	}
	return archiveData, nil
}

func downloadCaptchaProtectArchive(ctx context.Context, sourceURL string) ([]byte, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	client := &http.Client{Timeout: 60 * time.Second}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, sourceURL, nil) // #nosec G107 -- source URL is a pinned constant with hash verification.
	if err != nil {
		return nil, fmt.Errorf("create captcha-protect archive request: %w", err)
	}
	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("download captcha-protect archive: %w", err)
	}
	defer func() {
		_ = resp.Body.Close()
	}()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("download captcha-protect archive: unexpected HTTP status %s", resp.Status)
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, maxCaptchaProtectArchiveBytes+1))
	if err != nil {
		return nil, fmt.Errorf("read captcha-protect archive: %w", err)
	}
	if len(data) > maxCaptchaProtectArchiveBytes {
		return nil, fmt.Errorf("read captcha-protect archive: response exceeds %d bytes", maxCaptchaProtectArchiveBytes)
	}
	return data, nil
}

func extractCaptchaProtectArchive(archiveData []byte, targetDir string) error {
	reader, err := zip.NewReader(bytes.NewReader(archiveData), int64(len(archiveData)))
	if err != nil {
		return fmt.Errorf("open captcha-protect archive: %w", err)
	}

	for _, file := range reader.File {
		rel, ok := stripZipRoot(file.Name)
		if !ok {
			continue
		}
		if shouldSkipCaptchaProtectArchivePath(rel) {
			continue
		}
		targetPath := filepath.Join(targetDir, filepath.FromSlash(rel))
		cleanTarget := filepath.Clean(targetPath)
		cleanRoot := filepath.Clean(targetDir)
		if cleanTarget != cleanRoot && !strings.HasPrefix(cleanTarget, cleanRoot+string(os.PathSeparator)) {
			return fmt.Errorf("captcha-protect archive contains unsafe path %q", file.Name)
		}

		if file.FileInfo().IsDir() {
			if err := os.MkdirAll(cleanTarget, 0o755); err != nil { // #nosec G301 -- generated project plugin directory must remain readable by the compose stack.
				return fmt.Errorf("create captcha-protect directory %s: %w", rel, err)
			}
			continue
		}
		if err := os.MkdirAll(filepath.Dir(cleanTarget), 0o755); err != nil { // #nosec G301 -- generated project plugin directory must remain readable by the compose stack.
			return fmt.Errorf("create captcha-protect parent directory %s: %w", filepath.Dir(rel), err)
		}
		source, err := file.Open()
		if err != nil {
			return fmt.Errorf("open captcha-protect archive file %s: %w", rel, err)
		}
		if err := writeExtractedFile(cleanTarget, source); err != nil {
			_ = source.Close()
			return err
		}
		if err := source.Close(); err != nil {
			return fmt.Errorf("close captcha-protect archive file %s: %w", rel, err)
		}
	}
	return nil
}

func shouldSkipCaptchaProtectArchivePath(rel string) bool {
	clean := pathCleanSlash(rel)
	if clean == "ci" || strings.HasPrefix(clean, "ci/") {
		return true
	}
	if clean == ".github" || strings.HasPrefix(clean, ".github/") {
		return true
	}
	if clean == "renovate.json5" {
		return true
	}
	return strings.HasSuffix(clean, "_test.go")
}

func stripZipRoot(name string) (string, bool) {
	clean := pathCleanSlash(name)
	if clean == "." || clean == "" {
		return "", false
	}
	parts := strings.SplitN(clean, "/", 2)
	if len(parts) != 2 || strings.TrimSpace(parts[1]) == "" {
		return "", false
	}
	return parts[1], true
}

func pathCleanSlash(name string) string {
	return strings.TrimPrefix(filepath.ToSlash(filepath.Clean(filepath.FromSlash(name))), "/")
}

func writeExtractedFile(path string, source io.Reader) error {
	target, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o644) // #nosec G304,G302 -- caller validates archive paths stay under the extraction root, and generated plugin files must be project-readable.
	if err != nil {
		return fmt.Errorf("create captcha-protect archive file %s: %w", path, err)
	}
	_, copyErr := io.Copy(target, source)
	closeErr := target.Close()
	if copyErr != nil {
		return fmt.Errorf("write captcha-protect archive file %s: %w", path, copyErr)
	}
	if closeErr != nil {
		return fmt.Errorf("close captcha-protect archive file %s: %w", path, closeErr)
	}
	return nil
}

type traefikPluginMiddleware struct {
	Plugin map[string]captchaProtectMiddlewareConfig `yaml:"plugin"`
}

type captchaProtectMiddlewareConfig struct {
	Window                  int      `yaml:"window"`
	Mode                    string   `yaml:"mode"`
	ProtectRoutes           string   `yaml:"protectRoutes"`
	ExcludeRoutes           []string `yaml:"excludeRoutes"`
	ProtectParameters       string   `yaml:"protectParameters"`
	ChallengeTemplate       string   `yaml:"challengeTmpl"`
	ChallengeURL            string   `yaml:"challengeURL"`
	ChallengeStatusCode     int      `yaml:"challengeStatusCode"`
	CaptchaProvider         string   `yaml:"captchaProvider"`
	SiteKey                 string   `yaml:"siteKey"`
	SecretKey               string   `yaml:"secretKey"`
	IPForwardedHeader       string   `yaml:"ipForwardedHeader"`
	GoodBots                []string `yaml:"goodBots"`
	PersistentStateFile     string   `yaml:"persistentStateFile"`
	ProtectFileExtensions   string   `yaml:"protectFileExtensions"`
	PeriodSeconds           int      `yaml:"periodSeconds"`
	FailureThreshold        int      `yaml:"failureThreshold"`
	EnableGooglebotIPCheck  string   `yaml:"enableGooglebotIPCheck"`
	EnableUptimeRobotBypass string   `yaml:"enableUptimeRobotBypass"`
}

func captchaProtectMiddlewareDefinition(opts CaptchaProtectMiddlewareOptions) traefikPluginMiddleware {
	return traefikPluginMiddleware{
		Plugin: map[string]captchaProtectMiddlewareConfig{
			"captcha-protect": {
				Window:                  opts.Window,
				Mode:                    opts.Mode,
				ProtectRoutes:           opts.ProtectRoutes,
				ExcludeRoutes:           append([]string{}, opts.ExcludeRoutes...),
				ProtectParameters:       opts.ProtectParameters,
				ChallengeTemplate:       opts.ChallengeTemplate,
				ChallengeURL:            opts.ChallengeURL,
				ChallengeStatusCode:     opts.ChallengeStatusCode,
				CaptchaProvider:         opts.CaptchaProvider,
				SiteKey:                 opts.SiteKey,
				SecretKey:               opts.SecretKey,
				IPForwardedHeader:       opts.IPForwardedHeader,
				GoodBots:                append([]string{}, opts.GoodBots...),
				PersistentStateFile:     opts.PersistentStateFile,
				ProtectFileExtensions:   opts.ProtectFileExtensions,
				PeriodSeconds:           opts.PeriodSeconds,
				FailureThreshold:        opts.FailureThreshold,
				EnableGooglebotIPCheck:  opts.EnableGooglebotIPCheck,
				EnableUptimeRobotBypass: opts.EnableUptimeRobotBypass,
			},
		},
	}
}
