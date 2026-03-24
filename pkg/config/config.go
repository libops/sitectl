package config

import (
	"fmt"
	"os"
	"path/filepath"

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
	data, err := os.ReadFile(path)
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
	cfg, err := Load()
	if err != nil {
		return "", err
	}
	current := cfg.CurrentContext

	if discovered, err := DiscoverCurrentContext(); err == nil && discovered != nil {
		return discovered.Name, nil
	} else if err != nil {
		return "", err
	}

	if current == "" {
		return "", nil
	}

	return current, nil
}
