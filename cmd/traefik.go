package cmd

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"

	corecomponent "github.com/libops/sitectl/pkg/component"
	"github.com/libops/sitectl/pkg/config"
	"github.com/spf13/cobra"
)

const (
	traefikTLSHTTP        = "http"
	traefikTLSSelfManaged = "self-managed"
	traefikTLSMkcert      = "mkcert"
	traefikTLSLetsEncrypt = "letsencrypt"
)

type traefikTLSOptions struct {
	envFile        string
	tlsComposeFile string
	email          string
	acmeURL        string
	certFile       string
	keyFile        string
	domain         string
}

func traefikCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:     "traefik",
		Short:   "Operate on Traefik ingress in the active context",
		GroupID: "ops",
	}
	cmd.AddCommand(
		serviceStatusCommand("traefik"),
		traefikIngressStatusCommand(),
		traefikTLSCommand(),
		traefikBotMitigationCommand(),
	)
	return cmd
}

func traefikIngressStatusCommand() *cobra.Command {
	opts := traefikTLSOptions{}
	cmd := &cobra.Command{
		Use:   "ingress-status",
		Short: "Show Traefik ingress TLS and bot-mitigation settings",
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx, err := resolveCurrentContext(cmd)
			if err != nil {
				return err
			}
			envPath := traefikEnvPath(ctx, opts.envFile)
			env, _, err := readContextDotEnv(ctx, envPath)
			if err != nil {
				return err
			}
			scheme := firstNonEmptyString(env["URI_SCHEME"], ctx.ComposePublicScheme("http"))
			provider := firstNonEmptyString(env["TLS_PROVIDER"], "self-managed")
			bot := firstNonEmptyString(env["BOT_MITIGATION"], env["TRAEFIK_BOT_MITIGATION"], "off")
			_, _ = fmt.Fprintf(cmd.OutOrStdout(), "scheme=%s tls_provider=%s bot_mitigation=%s env=%s\n", scheme, provider, bot, envPath)
			return nil
		},
	}
	cmd.Flags().StringVar(&opts.envFile, "env-file", "", "Env file to update; defaults to the first context env-file or .env")
	return cmd
}

func traefikTLSCommand() *cobra.Command {
	opts := traefikTLSOptions{}
	cmd := &cobra.Command{
		Use:   "tls MODE",
		Short: "Switch Traefik TLS mode: http, mkcert, letsencrypt, or self-managed",
		Args: func(cmd *cobra.Command, args []string) error {
			if len(args) != 1 {
				return fmt.Errorf("expected TLS mode: http, mkcert, letsencrypt, or self-managed")
			}
			return validateTraefikTLSMode(args[0])
		},
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx, err := resolveCurrentContext(cmd)
			if err != nil {
				return err
			}
			return applyTraefikTLSMode(cmd, ctx, strings.TrimSpace(args[0]), opts)
		},
	}
	cmd.Flags().StringVar(&opts.envFile, "env-file", "", "Env file to update; defaults to the first context env-file or .env")
	cmd.Flags().StringVar(&opts.tlsComposeFile, "tls-compose-file", "docker-compose.tls.yml", "TLS compose override to add/remove from the context when it exists")
	cmd.Flags().StringVar(&opts.email, "email", "", "ACME email to set when using letsencrypt")
	cmd.Flags().StringVar(&opts.acmeURL, "acme-url", "", "ACME directory URL to set when using letsencrypt")
	cmd.Flags().StringVar(&opts.certFile, "cert-file", "", "Public certificate file to install for self-managed TLS")
	cmd.Flags().StringVar(&opts.keyFile, "key-file", "", "Private key file to install for self-managed TLS")
	cmd.Flags().StringVar(&opts.domain, "domain", "", "Domain to use for mkcert; defaults to DOMAIN from the env file or localhost")
	return cmd
}

func traefikBotMitigationCommand() *cobra.Command {
	opts := struct {
		envFile string
		siteKey string
		secret  string
	}{}
	cmd := &cobra.Command{
		Use:   "bot-mitigation STATE",
		Short: "Switch Traefik bot mitigation on or off",
		Args: func(cmd *cobra.Command, args []string) error {
			if len(args) != 1 {
				return fmt.Errorf("expected bot mitigation state: on or off")
			}
			switch strings.TrimSpace(args[0]) {
			case "on", "off", "turnstile":
				return nil
			default:
				return fmt.Errorf("invalid bot mitigation state %q: expected on or off", args[0])
			}
		},
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx, err := resolveCurrentContext(cmd)
			if err != nil {
				return err
			}
			state := strings.TrimSpace(args[0])
			if state == "turnstile" {
				state = "on"
			}
			envPath := traefikEnvPath(ctx, opts.envFile)
			env, raw, err := readContextDotEnv(ctx, envPath)
			if err != nil {
				return err
			}
			values := map[string]string{
				"BOT_MITIGATION":         state,
				"TRAEFIK_BOT_MITIGATION": state,
			}
			if strings.TrimSpace(opts.siteKey) != "" {
				values["TURNSTILE_SITE_KEY"] = strings.TrimSpace(opts.siteKey)
			}
			if strings.TrimSpace(opts.secret) != "" {
				values["TURNSTILE_SECRET_KEY"] = strings.TrimSpace(opts.secret)
			}
			if err := writeContextText(ctx, envPath, updateDotEnvText(raw, env, values)); err != nil {
				return err
			}
			_, _ = fmt.Fprintf(cmd.OutOrStdout(), "Traefik bot mitigation set to %s in %s\n", state, envPath)
			return nil
		},
	}
	cmd.Flags().StringVar(&opts.envFile, "env-file", "", "Env file to update; defaults to the first context env-file or .env")
	cmd.Flags().StringVar(&opts.siteKey, "turnstile-site-key", "", "Cloudflare Turnstile site key to write to the env file")
	cmd.Flags().StringVar(&opts.secret, "turnstile-secret-key", "", "Cloudflare Turnstile secret key to write to the env file")
	return cmd
}

func applyTraefikTLSMode(cmd *cobra.Command, ctx *config.Context, mode string, opts traefikTLSOptions) error {
	if err := validateTraefikTLSMode(mode); err != nil {
		return err
	}
	envPath := traefikEnvPath(ctx, opts.envFile)
	env, raw, err := readContextDotEnv(ctx, envPath)
	if err != nil {
		return err
	}

	enableTLS := mode != traefikTLSHTTP
	provider := traefikTLSSelfManaged
	if mode == traefikTLSLetsEncrypt {
		provider = traefikTLSLetsEncrypt
	}
	values := map[string]string{
		"URI_SCHEME":   ternaryString(enableTLS, "https", "http"),
		"TLS_PROVIDER": provider,
	}
	if mode == traefikTLSLetsEncrypt {
		if strings.TrimSpace(opts.email) != "" {
			values["ACME_EMAIL"] = strings.TrimSpace(opts.email)
		}
		if strings.TrimSpace(opts.acmeURL) != "" {
			values["ACME_URL"] = strings.TrimSpace(opts.acmeURL)
		}
	}
	if err := writeContextText(ctx, envPath, updateDotEnvText(raw, env, values)); err != nil {
		return err
	}

	tlsComposePath := contextPath(ctx, firstNonEmptyString(opts.tlsComposeFile, "docker-compose.tls.yml"))
	if err := persistTraefikTLSComposeSelection(ctx, firstNonEmptyString(opts.tlsComposeFile, "docker-compose.tls.yml"), tlsComposePath, enableTLS); err != nil {
		return err
	}
	for _, composePath := range []string{contextPath(ctx, "docker-compose.yml"), tlsComposePath} {
		if err := setTraefikLetsEncryptCommands(ctx, composePath, mode == traefikTLSLetsEncrypt); err != nil {
			return err
		}
	}

	switch mode {
	case traefikTLSMkcert:
		domain := firstNonEmptyString(opts.domain, env["DOMAIN"], "localhost")
		if err := generateAndInstallMkcertCertificates(ctx, domain); err != nil {
			return err
		}
	case traefikTLSSelfManaged:
		if strings.TrimSpace(opts.certFile) != "" || strings.TrimSpace(opts.keyFile) != "" {
			if strings.TrimSpace(opts.certFile) == "" || strings.TrimSpace(opts.keyFile) == "" {
				return fmt.Errorf("--cert-file and --key-file must be provided together")
			}
			if err := installTraefikCertificatePair(ctx, opts.certFile, opts.keyFile); err != nil {
				return err
			}
		}
	}

	_, _ = fmt.Fprintf(cmd.OutOrStdout(), "Traefik TLS mode set to %s in %s\n", mode, envPath)
	return nil
}

func validateTraefikTLSMode(mode string) error {
	switch strings.TrimSpace(mode) {
	case traefikTLSHTTP, traefikTLSSelfManaged, traefikTLSMkcert, traefikTLSLetsEncrypt:
		return nil
	default:
		return fmt.Errorf("invalid TLS mode %q: expected http, mkcert, letsencrypt, or self-managed", mode)
	}
}

func persistTraefikTLSComposeSelection(ctx *config.Context, composeValue, composePath string, enabled bool) error {
	if ctx == nil || ctx.Ephemeral || strings.TrimSpace(ctx.Name) == "" || strings.TrimSpace(ctx.Name) == "." {
		return nil
	}
	if !contextFileExists(ctx, composePath) {
		return nil
	}

	value := strings.TrimSpace(composeValue)
	if value == "" {
		value = "docker-compose.tls.yml"
	}
	current := append([]string{}, ctx.ComposeFile...)
	hasValue := false
	for _, candidate := range current {
		if candidate == value {
			hasValue = true
			break
		}
	}
	if enabled && !hasValue {
		current = append(current, value)
	}
	if !enabled && hasValue {
		next := current[:0]
		for _, candidate := range current {
			if candidate != value {
				next = append(next, candidate)
			}
		}
		current = next
	}
	if stringSlicesEqual(ctx.ComposeFile, current) {
		return nil
	}
	updated := *ctx
	updated.ComposeFile = current
	return config.SaveContext(&updated, false)
}

func generateAndInstallMkcertCertificates(ctx *config.Context, domain string) error {
	domain = strings.TrimSpace(domain)
	if domain == "" {
		domain = "localhost"
	}
	mkcert, err := exec.LookPath("mkcert")
	if err != nil {
		return fmt.Errorf("mkcert is required for TLS mode mkcert: %w", err)
	}
	workDir, err := os.MkdirTemp("", "sitectl-mkcert-*")
	if err != nil {
		return err
	}
	defer os.RemoveAll(workDir)

	certPath := filepath.Join(workDir, "cert.pem")
	keyPath := filepath.Join(workDir, "privkey.pem")
	args := []string{"-cert-file", certPath, "-key-file", keyPath, domain, "localhost", "127.0.0.1", "::1"}
	command := exec.Command(mkcert, args...) // #nosec G204 -- mkcert is resolved from a fixed executable name and args are passed without a shell.
	command.Stdout = os.Stdout
	command.Stderr = os.Stderr
	if err := command.Run(); err != nil {
		return fmt.Errorf("generate mkcert certificate: %w", err)
	}
	return uploadTraefikCertificatePair(ctx, certPath, keyPath)
}

func installTraefikCertificatePair(ctx *config.Context, certFile, keyFile string) error {
	if _, err := os.Stat(certFile); err != nil {
		return fmt.Errorf("read certificate file %s: %w", certFile, err)
	}
	if _, err := os.Stat(keyFile); err != nil {
		return fmt.Errorf("read private key file %s: %w", keyFile, err)
	}
	return uploadTraefikCertificatePair(ctx, certFile, keyFile)
}

func uploadTraefikCertificatePair(ctx *config.Context, certFile, keyFile string) error {
	if err := ctx.UploadFile(certFile, contextPath(ctx, filepath.Join("certs", "cert.pem"))); err != nil {
		return fmt.Errorf("install Traefik certificate: %w", err)
	}
	if err := ctx.UploadFile(keyFile, contextPath(ctx, filepath.Join("certs", "privkey.pem"))); err != nil {
		return fmt.Errorf("install Traefik private key: %w", err)
	}
	return nil
}

func setTraefikLetsEncryptCommands(ctx *config.Context, composePath string, enabled bool) error {
	if !contextFileExists(ctx, composePath) {
		return nil
	}
	raw, err := ctx.ReadSmallFile(composePath)
	if err != nil {
		return err
	}
	updated, err := setTraefikLetsEncryptCommandText(raw, enabled)
	if err != nil {
		return err
	}
	if updated == raw {
		return nil
	}
	return writeContextText(ctx, composePath, updated)
}

func setTraefikLetsEncryptCommandText(raw string, enabled bool) (string, error) {
	doc, err := corecomponent.LoadYAMLDocument([]byte(raw))
	if err != nil {
		return "", fmt.Errorf("load compose yaml: %w", err)
	}
	hasTraefik, err := doc.HasPath(".services.traefik")
	if err != nil {
		return "", fmt.Errorf("read compose services.traefik: %w", err)
	}
	if !hasTraefik {
		return raw, nil
	}

	commandPath := ".services.traefik.command"
	changed, err := doc.RemoveMatchingString(commandPath, isTraefikLetsEncryptLine)
	if err != nil {
		return "", fmt.Errorf("remove Traefik Let's Encrypt commands: %w", err)
	}
	if !enabled {
		if !changed {
			return raw, nil
		}
		out, err := doc.Bytes()
		if err != nil {
			return "", fmt.Errorf("marshal compose yaml: %w", err)
		}
		return string(out), nil
	}

	for _, line := range traefikLetsEncryptCommandLinesFor(raw) {
		if err := doc.AppendUniqueString(commandPath, line); err != nil {
			return "", fmt.Errorf("add Traefik Let's Encrypt command: %w", err)
		}
	}
	out, err := doc.Bytes()
	if err != nil {
		return "", fmt.Errorf("marshal compose yaml: %w", err)
	}
	return string(out), nil
}

func traefikLetsEncryptCommandLinesFor(raw string) []string {
	entrypoint := "web"
	storage := "/letsencrypt/acme.json"
	lower := strings.ToLower(raw)
	if strings.Contains(lower, "entrypoints.http.address") || strings.Contains(lower, "entrypoints.https.address") {
		entrypoint = "http"
	}
	if strings.Contains(raw, "/acme/") || strings.Contains(raw, "/acme:") {
		storage = "/acme/acme.json"
	}
	lines := []string{
		"--certificatesresolvers.letsencrypt.acme.email=${ACME_EMAIL:?set ACME_EMAIL}",
		"--certificatesresolvers.letsencrypt.acme.storage=" + storage,
		"--certificatesresolvers.letsencrypt.acme.httpchallenge=true",
		"--certificatesresolvers.letsencrypt.acme.httpchallenge.entrypoint=" + entrypoint,
	}
	if strings.Contains(raw, "ACME_URL") {
		lines = append(lines, "--certificatesresolvers.letsencrypt.acme.caserver=${ACME_URL}")
	}
	return lines
}

func isTraefikLetsEncryptLine(line string) bool {
	normalized := strings.ToLower(line)
	return strings.Contains(normalized, "certificatesresolvers.letsencrypt.acme")
}

func traefikEnvPath(ctx *config.Context, requested string) string {
	if strings.TrimSpace(requested) != "" {
		return contextPath(ctx, requested)
	}
	if ctx != nil && len(ctx.EnvFile) > 0 && strings.TrimSpace(ctx.EnvFile[0]) != "" {
		return contextPath(ctx, ctx.EnvFile[0])
	}
	return contextPath(ctx, ".env")
}

func readContextDotEnv(ctx *config.Context, path string) (map[string]string, string, error) {
	raw, err := ctx.ReadSmallFile(path)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return map[string]string{}, "", nil
		}
		return nil, "", err
	}
	return parseDotEnvText(raw), raw, nil
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

func updateDotEnvText(raw string, existing, values map[string]string) string {
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
		if existing != nil {
			existing[key] = value
		}
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

func writeContextText(ctx *config.Context, path string, text string) error {
	tempFile, err := os.CreateTemp("", "sitectl-context-write-*")
	if err != nil {
		return err
	}
	tempPath := tempFile.Name()
	defer os.Remove(tempPath)
	if _, err := tempFile.WriteString(text); err != nil {
		_ = tempFile.Close()
		return err
	}
	if err := tempFile.Close(); err != nil {
		return err
	}
	return ctx.UploadFile(tempPath, path)
}

func contextPath(ctx *config.Context, path string) string {
	path = strings.TrimSpace(path)
	if filepath.IsAbs(path) || ctx == nil || strings.TrimSpace(ctx.ProjectDir) == "" {
		return path
	}
	return filepath.Join(ctx.ProjectDir, path)
}

func contextFileExists(ctx *config.Context, path string) bool {
	if ctx == nil || strings.TrimSpace(path) == "" {
		return false
	}
	accessor, err := ctx.NewFileAccessor()
	if err != nil {
		return false
	}
	defer accessor.Close()
	_, err = accessor.Stat(path)
	return err == nil
}

func firstNonEmptyString(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return strings.TrimSpace(value)
		}
	}
	return ""
}

func ternaryString(condition bool, whenTrue, whenFalse string) string {
	if condition {
		return whenTrue
	}
	return whenFalse
}

func stringSlicesEqual(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
