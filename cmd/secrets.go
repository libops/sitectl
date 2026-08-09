package cmd

import (
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"

	"github.com/libops/sitectl/pkg/config"
	"github.com/spf13/cobra"
	yaml "gopkg.in/yaml.v3"
)

type composeSecret struct {
	File string `json:"file"`
}
type composeVolume struct {
	Name       string            `json:"name"`
	Driver     string            `json:"driver"`
	DriverOpts map[string]string `json:"driver_opts"`
	External   bool              `json:"external"`
}
type composeServiceVolume struct {
	Type     string `json:"type"`
	Source   string `json:"source"`
	Target   string `json:"target"`
	ReadOnly bool   `json:"read_only"`
}
type composeService struct {
	Image   string                 `json:"image"`
	Volumes []composeServiceVolume `json:"volumes"`
}
type composeSecretConfig struct {
	Name     string                    `json:"name"`
	Secrets  map[string]composeSecret  `json:"secrets"`
	Volumes  map[string]composeVolume  `json:"volumes"`
	Services map[string]composeService `json:"services"`
}

type secretBackendConfig struct {
	Backend   string                    `yaml:"backend"`
	Directory string                    `yaml:"directory"`
	Secrets   map[string]secretOverride `yaml:"secrets"`
}
type secretOverride struct {
	Backend string `yaml:"backend"`
	Path    string `yaml:"path"`
	Field   string `yaml:"field"`
	Format  string `yaml:"format"`
}
type resolvedSecret struct{ Name, Backend, Path, Field, Format string }

func init() { RootCmd.AddCommand(secretsCommand()) }

func secretsCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use: "secrets", Short: "Inspect and manage secrets declared by Docker Compose", GroupID: "ops",
		Long: `Inspect and manage the active site's Compose-declared secrets.

Filesystem secrets default to ./secrets beneath the project directory. A site can map
individual names to Vault in .sitectl/secrets.yaml; Vault values are written through the
Vault CLI over stdin and are never materialized on the site filesystem by sitectl.`,
	}
	cmd.AddCommand(secretsListCommand(), secretsGenerateCommand(false), secretsGenerateCommand(true))
	return cmd
}

func secretsListCommand() *cobra.Command {
	return &cobra.Command{Use: "list", Short: "Show declared secrets and whether their backing values exist", Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx, secrets, err := resolveSecrets(cmd)
			if err != nil {
				return err
			}
			for _, secret := range secrets {
				state := "missing"
				switch secret.Backend {
				case "filesystem":
					data, readErr := ctx.ReadFile(secret.Path)
					if readErr == nil && len(strings.TrimSpace(string(data))) > 0 {
						state = "ready"
					} else if readErr != nil && !errors.Is(readErr, fs.ErrNotExist) {
						state = "error"
					}
				case "vault":
					state = "vault-reference"
				}
				if _, err := fmt.Fprintf(cmd.OutOrStdout(), "%s\t%s\t%s\t%s\n", secret.Name, secret.Backend, state, secret.Path); err != nil {
					return err
				}
			}
			return nil
		},
	}
}

func secretsGenerateCommand(rotate bool) *cobra.Command {
	use, short := "generate", "Generate values for missing Compose secrets"
	if rotate {
		use, short = "rotate NAME", "Replace one secret with a newly generated value"
	}
	return &cobra.Command{Use: use, Short: short, Args: func(cmd *cobra.Command, args []string) error {
		if rotate {
			return cobra.ExactArgs(1)(cmd, args)
		}
		return cobra.NoArgs(cmd, args)
	},
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx, secrets, err := resolveSecrets(cmd)
			if err != nil {
				return err
			}
			matched := !rotate
			for _, secret := range secrets {
				if rotate && !strings.EqualFold(secret.Name, args[0]) {
					continue
				}
				matched = true
				if rotate {
					if consequence := secretRotationConsequence(secret.Name); consequence != "" {
						_, _ = fmt.Fprintf(cmd.ErrOrStderr(), "Rotation consequence: %s\n", consequence)
					}
				}
				if !rotate && secret.Backend == "filesystem" {
					if data, readErr := ctx.ReadFile(secret.Path); readErr == nil && len(strings.TrimSpace(string(data))) > 0 {
						continue
					} else if readErr != nil && !errors.Is(readErr, fs.ErrNotExist) {
						return readErr
					}
				}
				if !rotate && secret.Backend == "vault" {
					exists, checkErr := vaultSecretExists(cmd, secret)
					if checkErr != nil {
						return checkErr
					}
					if exists {
						continue
					}
				}
				value, err := generateSecretValue(secret.Format)
				if err != nil {
					return fmt.Errorf("generate %s: %w", secret.Name, err)
				}
				switch secret.Backend {
				case "filesystem":
					err = ctx.WriteFile(secret.Path, []byte(value))
				case "vault":
					err = writeVaultSecret(cmd, secret, value)
				default:
					err = fmt.Errorf("unsupported backend %q for %s", secret.Backend, secret.Name)
				}
				if err != nil {
					return err
				}
				_, _ = fmt.Fprintf(cmd.OutOrStdout(), "%s: updated %s backend\n", secret.Name, secret.Backend)
			}
			if !matched {
				return fmt.Errorf("secret %q is not declared by Compose", args[0])
			}
			return nil
		},
	}
}

func resolveSecrets(cmd *cobra.Command) (*config.Context, []resolvedSecret, error) {
	ctx, err := resolveCurrentContext(cmd)
	if err != nil {
		return nil, nil, err
	}
	compose, err := inspectComposeSecrets(cmd, ctx)
	if err != nil {
		return nil, nil, err
	}
	settings := secretBackendConfig{Backend: "filesystem", Directory: "./secrets"}
	settingsPath := filepath.Join(ctx.ProjectDir, ".sitectl", "secrets.yaml")
	if data, readErr := ctx.ReadFile(settingsPath); readErr == nil {
		if err := yaml.Unmarshal(data, &settings); err != nil {
			return nil, nil, fmt.Errorf("parse %s: %w", settingsPath, err)
		}
	} else if !errors.Is(readErr, fs.ErrNotExist) {
		return nil, nil, readErr
	}
	if strings.TrimSpace(settings.Backend) == "" {
		settings.Backend = "filesystem"
	}
	if strings.TrimSpace(settings.Directory) == "" {
		settings.Directory = "./secrets"
	}
	result := make([]resolvedSecret, 0, len(compose.Secrets))
	for name, declared := range compose.Secrets {
		override := settings.Secrets[name]
		backend := firstNonBlank(override.Backend, settings.Backend)
		format := firstNonBlank(override.Format, defaultSecretFormat(name))
		field := firstNonBlank(override.Field, "value")
		path := override.Path
		if backend == "filesystem" {
			path = declared.File
			if path == "" {
				path = filepath.Join(settings.Directory, name)
			}
			if !filepath.IsAbs(path) {
				path = filepath.Join(ctx.ProjectDir, path)
			}
			path = filepath.Clean(path)
		} else if backend == "vault" && path == "" {
			return nil, nil, fmt.Errorf("vault secret %s requires path in .sitectl/secrets.yaml", name)
		}
		result = append(result, resolvedSecret{Name: name, Backend: backend, Path: path, Field: field, Format: format})
	}
	sort.Slice(result, func(i, j int) bool { return result[i].Name < result[j].Name })
	return ctx, result, nil
}

func inspectComposeSecrets(cmd *cobra.Command, ctx *config.Context) (composeSecretConfig, error) {
	args := []string{"compose"}
	args = append(args, ctx.DockerComposeGlobalArgsForCommand("config")...)
	args = append(args, "config", "--format", "json")
	c := exec.Command("docker", args...)
	c.Dir = ctx.ProjectDir
	output, err := ctx.RunQuietCommandContext(cmd.Context(), c)
	if err != nil {
		return composeSecretConfig{}, fmt.Errorf("inspect Compose secrets: %w", err)
	}
	var result composeSecretConfig
	if err := json.Unmarshal([]byte(output), &result); err != nil {
		return result, fmt.Errorf("decode Docker Compose config: %w", err)
	}
	return result, nil
}

func generateSecretValue(format string) (string, error) {
	buf := make([]byte, 32)
	if _, err := rand.Read(buf); err != nil {
		return "", err
	}
	switch format {
	case "hex32", "":
		return hex.EncodeToString(buf), nil
	case "base64-32":
		return base64.RawStdEncoding.EncodeToString(buf), nil
	case "laravel-base64":
		return "base64:" + base64.StdEncoding.EncodeToString(buf), nil
	case "salt74":
		extra := make([]byte, 56)
		if _, err := rand.Read(extra); err != nil {
			return "", err
		}
		value := base64.RawURLEncoding.EncodeToString(extra)
		return value[:74], nil
	default:
		return "", fmt.Errorf("unsupported format %q", format)
	}
}

func writeVaultSecret(cmd *cobra.Command, secret resolvedSecret, value string) error {
	if strings.HasPrefix(secret.Path, "-") || strings.HasPrefix(secret.Field, "-") || strings.ContainsAny(secret.Field, "=\r\n") {
		return fmt.Errorf("vault secret %s has an unsafe path or field", secret.Name)
	}
	c := exec.CommandContext(cmd.Context(), "vault", "kv", "put", secret.Path, secret.Field+"=-") // #nosec G204 -- validated values are separate argv entries and the secret travels over stdin.
	c.Stdin = strings.NewReader(value)
	output, err := c.CombinedOutput()
	if err != nil {
		return fmt.Errorf("write Vault secret %s: %w: %s", secret.Name, err, strings.TrimSpace(string(output)))
	}
	return nil
}

func vaultSecretExists(cmd *cobra.Command, secret resolvedSecret) (bool, error) {
	if strings.HasPrefix(secret.Path, "-") {
		return false, fmt.Errorf("vault secret %s has an unsafe path", secret.Name)
	}
	c := exec.CommandContext(cmd.Context(), "vault", "kv", "get", "-format=json", secret.Path) // #nosec G204 -- validated path is a distinct argv entry.
	output, err := c.CombinedOutput()
	if err == nil {
		return true, nil
	}
	message := strings.ToLower(string(output))
	if strings.Contains(message, "no value found") || strings.Contains(message, "not found") {
		return false, nil
	}
	return false, fmt.Errorf("inspect Vault secret %s: %w: %s", secret.Name, err, strings.TrimSpace(string(output)))
}

func defaultSecretFormat(name string) string {
	switch name {
	case "OJS_SECRET_KEY":
		return "laravel-base64"
	case "DRUPAL_DEFAULT_SALT":
		return "salt74"
	default:
		return "hex32"
	}
}

func secretRotationConsequence(name string) string {
	upper := strings.ToUpper(name)
	switch {
	case strings.Contains(upper, "DB_PASSWORD") || upper == "DB_ROOT_PASSWORD":
		return "recreate dependent application and database services together so credentials do not diverge"
	case strings.HasPrefix(upper, "WORDPRESS_") && (strings.HasSuffix(upper, "_KEY") || strings.HasSuffix(upper, "_SALT")):
		return "existing WordPress login cookies become invalid"
	case upper == "DRUPAL_DEFAULT_SALT":
		return "existing one-time links and other salt-derived values become invalid"
	case upper == "OJS_SECRET_KEY" || upper == "OJS_SALT":
		return "existing OJS encrypted or session-derived values may become invalid"
	default:
		return ""
	}
}
func firstNonBlank(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return strings.TrimSpace(value)
		}
	}
	return ""
}
