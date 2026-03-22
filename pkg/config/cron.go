package config

import (
	"fmt"
	"slices"
	"strings"
)

type CronSpec struct {
	Name                 string   `yaml:"name"`
	Context              string   `yaml:"context"`
	Schedule             string   `yaml:"schedule"`
	OutputDir            string   `yaml:"output-dir"`
	Components           []string `yaml:"components,omitempty"`
	RetentionDays        int      `yaml:"retention-days,omitempty"`
	PreserveFirstOfMonth bool     `yaml:"preserve-first-of-month,omitempty"`
	DockerPrune          bool     `yaml:"docker-prune,omitempty"`
}

func GetCronSpec(name string) (CronSpec, error) {
	cfg, err := Load()
	if err != nil {
		return CronSpec{}, err
	}
	for _, spec := range cfg.CronSpecs {
		if strings.EqualFold(spec.Name, name) {
			return spec, nil
		}
	}
	return CronSpec{}, fmt.Errorf("cron spec %q not found", name)
}

func SaveCronSpec(spec CronSpec) error {
	cfg, err := Load()
	if err != nil {
		return err
	}
	updated := false
	for i := range cfg.CronSpecs {
		if strings.EqualFold(cfg.CronSpecs[i].Name, spec.Name) {
			cfg.CronSpecs[i] = spec
			updated = true
			break
		}
	}
	if !updated {
		cfg.CronSpecs = append(cfg.CronSpecs, spec)
		slices.SortFunc(cfg.CronSpecs, func(a, b CronSpec) int {
			return strings.Compare(strings.ToLower(a.Name), strings.ToLower(b.Name))
		})
	}
	return Save(cfg)
}

func DeleteCronSpec(name string) error {
	cfg, err := Load()
	if err != nil {
		return err
	}
	filtered := cfg.CronSpecs[:0]
	found := false
	for _, spec := range cfg.CronSpecs {
		if strings.EqualFold(spec.Name, name) {
			found = true
			continue
		}
		filtered = append(filtered, spec)
	}
	if !found {
		return fmt.Errorf("cron spec %q not found", name)
	}
	cfg.CronSpecs = filtered
	return Save(cfg)
}
