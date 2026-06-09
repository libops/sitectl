package memcached

import (
	_ "embed"

	corecomponent "github.com/libops/sitectl/pkg/component"
)

//go:embed docker-compose.yml
var composeYAML string

// ServiceComponent is the Memcached compose-backed service component.
type ServiceComponent = corecomponent.ComposeServiceComponent

// TargetOptions configures how the Memcached service component is wired into a
// target compose project.
type TargetOptions struct {
	AppService         string
	AppDependencies    map[string]any
	AppEnvironment     map[string]string
	DefaultState       corecomponent.State
	DefaultDisposition corecomponent.Disposition
	Dependencies       corecomponent.Dependencies
	Behavior           corecomponent.Behavior
	ExtraOnRules       []corecomponent.YAMLRule
	ExtraOffRules      []corecomponent.YAMLRule
	FileOnRules        []corecomponent.FileRule
	FileOffRules       []corecomponent.FileRule
}

// ComposeYAML returns the embedded Memcached compose service definition.
func ComposeYAML() []byte {
	return []byte(composeYAML)
}

// New builds a Memcached service component from embedded compose definitions.
func New(opts TargetOptions) (ServiceComponent, error) {
	return corecomponent.NewComposeServiceComponent(corecomponent.ComposeServiceComponentOptions{
		Name:               "memcached",
		ComposeYAML:        ComposeYAML(),
		ServiceNames:       []string{"memcached"},
		AppService:         opts.AppService,
		AppDependencies:    opts.AppDependencies,
		AppEnvironment:     opts.AppEnvironment,
		DefaultState:       opts.DefaultState,
		DefaultDisposition: opts.DefaultDisposition,
		AllowedDispositions: []corecomponent.Disposition{
			corecomponent.DispositionEnabled,
			corecomponent.DispositionDisabled,
		},
		Dependencies: opts.Dependencies,
		Guidance: corecomponent.StateGuidance{
			EnabledHelp:  "Run Memcached in this compose project.",
			DisabledHelp: "Remove the local Memcached service from this compose project.",
		},
		Behavior:      opts.Behavior,
		ExtraOnRules:  opts.ExtraOnRules,
		ExtraOffRules: opts.ExtraOffRules,
		FileOnRules:   opts.FileOnRules,
		FileOffRules:  opts.FileOffRules,
	})
}
