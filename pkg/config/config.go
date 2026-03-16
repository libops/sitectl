package config

import (
	"log/slog"
	"os"
	"path/filepath"

	yaml "gopkg.in/yaml.v3"
)

type Config struct {
	CurrentContext string    `yaml:"current-context"`
	Contexts       []Context `yaml:"contexts"`
}

func ConfigFilePath() string {
	home, err := os.UserHomeDir()
	if err != nil {
		slog.Error("Unable to detect home directory", "err", err)
		os.Exit(1)
	}

	baseDir := filepath.Join(home, ".sitectl")
	_, err = os.Stat(baseDir)
	if os.IsNotExist(err) {
		err = os.Mkdir(baseDir, 0700)
		if err != nil {
			slog.Error("Unable to create ~/.sitectl directory", "err", err)
			os.Exit(1)
		}
	}

	return filepath.Join(baseDir, "config.yaml")
}

func Load() (*Config, error) {
	data, err := os.ReadFile(ConfigFilePath())
	if err != nil {
		return &Config{}, nil
	}
	var cfg Config
	err = yaml.Unmarshal(data, &cfg)
	return &cfg, err
}

func Save(cfg *Config) error {
	data, err := yaml.Marshal(cfg)
	if err != nil {
		return err
	}
	return os.WriteFile(ConfigFilePath(), data, 0600)
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
