package devmode

import (
	"context"
	"fmt"
	"path/filepath"
	"sort"
	"strings"

	corecomponent "github.com/libops/sitectl/pkg/component"
	"github.com/libops/sitectl/pkg/config"
	yaml "gopkg.in/yaml.v3"
)

const (
	Name                = "dev-mode"
	defaultOverrideFile = "docker-compose.override.yml"
)

type Options struct {
	AppService   string
	OverrideFile string
	Environment  map[string]string
	Volumes      []string
}

func Component(opts Options) (corecomponent.ComposeServiceComponent, error) {
	opts = normalizeOptions(opts)
	return corecomponent.NewComposeServiceComponent(corecomponent.ComposeServiceComponentOptions{
		Name:                Name,
		DefaultState:        corecomponent.StateOff,
		DefaultDisposition:  corecomponent.DispositionDisabled,
		AllowedDispositions: []corecomponent.Disposition{corecomponent.DispositionDisabled, corecomponent.DispositionEnabled},
		Guidance: corecomponent.StateGuidance{
			EnabledHelp:  "A local docker-compose.override.yml mounts the host codebase and sets UID for writable files.",
			DisabledHelp: "No local development compose override is present.",
			Question:     "Enable local development bind mounts for this application?",
		},
		DefinitionOnRules: []corecomponent.YAMLRule{{
			Files: []string{opts.OverrideFile},
			Op:    corecomponent.OpRestore,
			Path:  ".services." + opts.AppService + ".environment.UID",
		}},
		DefinitionOffRules: []corecomponent.YAMLRule{{
			Files: []string{opts.OverrideFile},
			Op:    corecomponent.OpDelete,
			Path:  ".",
		}},
		AfterEnable: []corecomponent.Hook{func(_ context.Context, runtime *corecomponent.Runtime) error {
			return writeOverride(runtime.Context, opts)
		}},
		AfterDisable: []corecomponent.Hook{func(_ context.Context, runtime *corecomponent.Runtime) error {
			return removeOverride(runtime.Context, opts)
		}},
		Behavior: corecomponent.Behavior{
			Idempotent: true,
			Enable: corecomponent.TransitionBehavior{
				DataMigration: corecomponent.DataMigrationNone,
				Summary:       "Writes docker-compose.override.yml for local bind mounts and UID propagation.",
			},
			Disable: corecomponent.TransitionBehavior{
				DataMigration: corecomponent.DataMigrationNone,
				Summary:       "Removes docker-compose.override.yml.",
			},
		},
	})
}

func normalizeOptions(opts Options) Options {
	if strings.TrimSpace(opts.AppService) == "" {
		opts.AppService = "drupal"
	}
	if strings.TrimSpace(opts.OverrideFile) == "" {
		opts.OverrideFile = defaultOverrideFile
	}
	if opts.Environment == nil {
		opts.Environment = map[string]string{}
	}
	if strings.TrimSpace(opts.Environment["UID"]) == "" {
		opts.Environment["UID"] = "${UID:-1000}"
	}
	opts.Volumes = normalizeVolumes(opts.Volumes)
	return opts
}

func writeOverride(ctx *config.Context, opts Options) error {
	if ctx == nil {
		return fmt.Errorf("context is nil")
	}
	service := map[string]any{
		"environment": sortedStringMap(opts.Environment),
	}
	if len(opts.Volumes) > 0 {
		service["volumes"] = opts.Volumes
	}
	root := map[string]any{
		"services": map[string]any{
			opts.AppService: service,
		},
	}
	data, err := yaml.Marshal(root)
	if err != nil {
		return fmt.Errorf("marshal dev mode override: %w", err)
	}
	return ctx.WriteFile(ctx.ResolveProjectPath(opts.OverrideFile), data)
}

func removeOverride(ctx *config.Context, opts Options) error {
	if ctx == nil {
		return fmt.Errorf("context is nil")
	}
	return ctx.RemoveFile(ctx.ResolveProjectPath(opts.OverrideFile))
}

func sortedStringMap(values map[string]string) map[string]string {
	out := map[string]string{}
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	for _, key := range keys {
		value := strings.TrimSpace(values[key])
		if value == "" {
			continue
		}
		out[key] = value
	}
	return out
}

func normalizeVolumes(values []string) []string {
	out := []string{}
	seen := map[string]bool{}
	for _, value := range values {
		value = filepath.ToSlash(strings.TrimSpace(value))
		if value == "" || seen[value] {
			continue
		}
		seen[value] = true
		out = append(out, value)
	}
	return out
}
