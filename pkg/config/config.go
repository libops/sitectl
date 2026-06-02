package config

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	yaml "gopkg.in/yaml.v3"
)

type Config struct {
	CurrentContext string     `yaml:"current-context"`
	Contexts       []Context  `yaml:"contexts"`
	CronSpecs      []CronSpec `yaml:"cron-specs,omitempty"`
}

func ConfigFilePath() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("unable to detect home directory: %w", err)
	}

	baseDir := filepath.Join(home, ".sitectl")
	if _, err := os.Stat(baseDir); os.IsNotExist(err) {
		if err := os.Mkdir(baseDir, 0700); err != nil {
			return "", fmt.Errorf("unable to create ~/.sitectl directory: %w", err)
		}
	}

	return filepath.Join(baseDir, "config.yaml"), nil
}

func Load() (*Config, error) {
	path, err := ConfigFilePath()
	if err != nil {
		return nil, err
	}
	data, err := os.ReadFile(path) // #nosec G304 -- path is produced by ConfigFilePath under ~/.sitectl.
	if err != nil {
		return &Config{}, nil
	}
	var cfg Config
	err = yaml.Unmarshal(data, &cfg)
	return &cfg, err
}

func Save(cfg *Config) error {
	path, err := ConfigFilePath()
	if err != nil {
		return err
	}
	data, err := yaml.Marshal(cfg)
	if err != nil {
		return err
	}
	return os.WriteFile(path, data, 0600)
}

func Current() (string, error) {
	return CurrentForPlugin("")
}

func CurrentForPlugin(pluginName string) (string, error) {
	return currentForPlugin(pluginName, nil)
}

func CurrentForPluginWithDiagnostics(pluginName string, diagnostics io.Writer) (string, error) {
	return currentForPlugin(pluginName, diagnostics)
}

func currentForPlugin(pluginName string, diagnostics io.Writer) (string, error) {
	cfg, err := Load()
	if err != nil {
		return "", err
	}
	current := cfg.CurrentContext

	discovery, err := DiscoverCurrentContextForPlugin(pluginName)
	if err != nil {
		return "", err
	}
	if discovery.Context != nil {
		printContextDiscovery(diagnostics, fmt.Sprintf("using context %q from compose project %s claimed by plugin %q%s", discovery.Context.Name, discovery.ComposeProjectDir, discovery.Claim.Plugin, discoveryReason(discovery.Claim)))
		return discovery.Context.Name, nil
	}
	printFallbackDiscovery(diagnostics, discovery, current)

	if current == "" {
		return "", nil
	}

	return current, nil
}

func printFallbackDiscovery(w io.Writer, discovery CurrentContextDiscovery, current string) {
	if w == nil {
		return
	}
	defaultText := "no default context is set"
	if strings.TrimSpace(current) != "" {
		defaultText = fmt.Sprintf("using default context %q", current)
	}
	switch {
	case strings.TrimSpace(discovery.ComposeProjectDir) == "":
		printContextDiscovery(w, fmt.Sprintf("no compose project detected from %s; %s", discovery.CWD, defaultText))
	case discovery.Claim == nil:
		printContextDiscovery(w, fmt.Sprintf("no plugin claimed compose project %s; %s", discovery.ComposeProjectDir, defaultText))
	default:
		printContextDiscovery(w, fmt.Sprintf("plugin %q claimed compose project %s%s, but no matching local context exists; %s", discovery.Claim.Plugin, discovery.ComposeProjectDir, discoveryReason(discovery.Claim), defaultText))
	}
}

func printContextDiscovery(w io.Writer, message string) {
	if w == nil || strings.TrimSpace(message) == "" {
		return
	}
	fmt.Fprintf(w, "sitectl: %s\n", message)
}

func discoveryReason(claim *ProjectClaim) string {
	if claim == nil || strings.TrimSpace(claim.Reason) == "" {
		return ""
	}
	return " (" + strings.TrimSpace(claim.Reason) + ")"
}
