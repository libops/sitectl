package component

import (
	"fmt"
	"sort"
	"strings"
)

var defaultComposeRuleFiles = []string{
	"docker-compose.yml",
	"docker-compose.yaml",
	"compose.yml",
	"compose.yaml",
}

// ComposeServiceComponentOptions describes a service component backed by
// Docker Compose definitions and optional target application wiring.
type ComposeServiceComponentOptions struct {
	Name                 string
	Description          string
	ComposeYAML          []byte
	Definitions          *ComposeDefinitions
	ServiceNames         []string
	AppService           string
	AppDependencies      map[string]any
	AppEnvironment       map[string]string
	DefaultState         State
	DefaultDisposition   Disposition
	AllowedDispositions  []Disposition
	Dependencies         Dependencies
	Guidance             StateGuidance
	PromptOnCreate       bool
	FollowUps            []FollowUpSpec
	Gates                GateSpec
	Behavior             Behavior
	ExtraOnRules         []YAMLRule
	ExtraOffRules        []YAMLRule
	DefinitionOnRules    []YAMLRule
	DefinitionOffRules   []YAMLRule
	FileOnRules          []FileRule
	FileOffRules         []FileRule
	ApplyFollowUps       func(map[string]string) []YAMLRule
	BeforeDisable        []Hook
	AfterDisable         []Hook
	BeforeEnable         []Hook
	AfterEnable          []Hook
	BeforeDisableOptions func(map[string]string) []Hook
	AfterDisableOptions  func(map[string]string) []Hook
	BeforeEnableOptions  func(map[string]string) []Hook
	AfterEnableOptions   func(map[string]string) []Hook
}

// ComposeServiceComponent is a component implementation that applies or
// removes Docker Compose service definitions.
type ComposeServiceComponent struct {
	definition           Definition
	component            StaticComponent
	applyFollowUps       func(map[string]string) []YAMLRule
	beforeDisableOptions func(map[string]string) []Hook
	afterDisableOptions  func(map[string]string) []Hook
	beforeEnableOptions  func(map[string]string) []Hook
	afterEnableOptions   func(map[string]string) []Hook
}

// NewComposeServiceComponent builds a service component from compose
// definitions and component metadata.
func NewComposeServiceComponent(opts ComposeServiceComponentOptions) (ComposeServiceComponent, error) {
	name := strings.TrimSpace(opts.Name)
	if name == "" {
		return ComposeServiceComponent{}, fmt.Errorf("compose service component name cannot be empty")
	}

	defs := opts.Definitions
	if defs == nil {
		if len(opts.ComposeYAML) > 0 {
			parsed, err := ParseComposeDefinitions(opts.ComposeYAML)
			if err != nil {
				return ComposeServiceComponent{}, fmt.Errorf("parse %s compose definitions: %w", name, err)
			}
			defs = parsed
		} else {
			defs = &ComposeDefinitions{}
		}
	}

	serviceNames := normalizeServiceNames(opts.ServiceNames, defs)
	if len(serviceNames) == 0 && len(opts.ExtraOnRules) == 0 && len(opts.ExtraOffRules) == 0 && len(opts.DefinitionOnRules) == 0 && len(opts.DefinitionOffRules) == 0 && len(opts.FileOnRules) == 0 && len(opts.FileOffRules) == 0 && opts.ApplyFollowUps == nil && len(opts.BeforeDisable) == 0 && len(opts.AfterDisable) == 0 && len(opts.BeforeEnable) == 0 && len(opts.AfterEnable) == 0 && opts.BeforeDisableOptions == nil && opts.AfterDisableOptions == nil && opts.BeforeEnableOptions == nil && opts.AfterEnableOptions == nil {
		return ComposeServiceComponent{}, fmt.Errorf("compose service component %q has no services or mutations", name)
	}

	onRules := composeDefinitionRules(defs, OpRestore)
	offRules := composeDefinitionRules(defs, OpDelete)
	targetOn, targetOff := targetServiceRules(opts.AppService, opts.AppDependencies, opts.AppEnvironment)
	applyOnRules := append([]YAMLRule{}, targetOn...)
	applyOnRules = append(applyOnRules, opts.ExtraOnRules...)
	applyOffRules := append([]YAMLRule{}, targetOff...)
	applyOffRules = append(applyOffRules, opts.ExtraOffRules...)
	onRules = append(onRules, targetOn...)
	onRules = append(onRules, opts.ExtraOnRules...)
	onRules = append(onRules, opts.DefinitionOnRules...)
	offRules = append(offRules, targetOff...)
	offRules = append(offRules, opts.ExtraOffRules...)
	offRules = append(offRules, opts.DefinitionOffRules...)

	defaultState := normalizeState(opts.DefaultState)
	if defaultState == "" {
		defaultState = StateOn
	}
	defaultDisposition := normalizeDisposition(opts.DefaultDisposition)
	if defaultDisposition == "" {
		defaultDisposition = StateToDisposition(defaultState)
	}
	allowed := opts.AllowedDispositions
	if len(allowed) == 0 {
		allowed = []Disposition{DispositionEnabled, DispositionDisabled}
	}

	definition := Definition{
		Name:                name,
		DefaultState:        defaultState,
		DefaultDisposition:  defaultDisposition,
		AllowedDispositions: allowed,
		Guidance:            opts.Guidance,
		PromptOnCreate:      opts.PromptOnCreate,
		FollowUps:           opts.FollowUps,
		Gates:               opts.Gates,
		Dependencies:        opts.Dependencies,
		Behavior:            serviceComponentBehavior(opts.Behavior, name),
		On: DomainSpec{
			Compose: YAMLStateSpec{Rules: onRules},
			Files:   FileStateSpec{Rules: opts.FileOnRules},
		},
		Off: DomainSpec{
			Compose: YAMLStateSpec{Rules: offRules},
			Files:   FileStateSpec{Rules: opts.FileOffRules},
		},
	}

	on := ComponentSpec{
		Name:         name,
		Gates:        opts.Gates,
		BeforeEnable: append([]Hook{}, opts.BeforeEnable...),
		AfterEnable:  append([]Hook{}, opts.AfterEnable...),
		Compose: ComposeSpec{
			Definitions: nonEmptyComposeDefinitions(defs),
			Rules:       applyOnRules,
		},
		Files: FileStateSpec{Rules: opts.FileOnRules},
	}
	off := ComponentSpec{
		Name:          name,
		Gates:         opts.Gates,
		BeforeDisable: append([]Hook{}, opts.BeforeDisable...),
		AfterDisable:  append([]Hook{}, opts.AfterDisable...),
		Compose: ComposeSpec{
			RemoveServices:      serviceNames,
			PruneUnusedResource: true,
			Rules:               applyOffRules,
		},
		Files: FileStateSpec{Rules: opts.FileOffRules},
	}

	return ComposeServiceComponent{
		definition:           definition,
		component:            NewStaticComponent(name, defaultState, on, off),
		applyFollowUps:       opts.ApplyFollowUps,
		beforeDisableOptions: opts.BeforeDisableOptions,
		afterDisableOptions:  opts.AfterDisableOptions,
		beforeEnableOptions:  opts.BeforeEnableOptions,
		afterEnableOptions:   opts.AfterEnableOptions,
	}, nil
}

func nonEmptyComposeDefinitions(defs *ComposeDefinitions) *ComposeDefinitions {
	if defs == nil {
		return nil
	}
	if len(defs.Services) == 0 && len(defs.Networks) == 0 && len(defs.Volumes) == 0 && len(defs.Secrets) == 0 && len(defs.Configs) == 0 {
		return nil
	}
	return defs
}

// Definition returns the user-facing component definition.
func (c ComposeServiceComponent) Definition() Definition {
	return c.definition
}

// Name returns the component name.
func (c ComposeServiceComponent) Name() string {
	return c.component.Name()
}

// DefaultState returns the component state reconciled by default.
func (c ComposeServiceComponent) DefaultState() State {
	return c.component.DefaultState()
}

// SpecFor returns the compose apply spec for the requested state.
func (c ComposeServiceComponent) SpecFor(state State) ComponentSpec {
	return c.component.SpecFor(state)
}

// SpecForWithOptions returns the compose apply spec for the requested state
// after applying option-derived follow-up rules.
func (c ComposeServiceComponent) SpecForWithOptions(state State, options map[string]string) ComponentSpec {
	spec := c.SpecFor(state)
	switch normalizeState(state) {
	case StateOn:
		if c.applyFollowUps != nil {
			spec.Compose.Rules = append(spec.Compose.Rules, c.applyFollowUps(options)...)
		}
		if c.beforeEnableOptions != nil {
			spec.BeforeEnable = append(spec.BeforeEnable, c.beforeEnableOptions(options)...)
		}
		if c.afterEnableOptions != nil {
			spec.AfterEnable = append(spec.AfterEnable, c.afterEnableOptions(options)...)
		}
	case StateOff:
		if c.beforeDisableOptions != nil {
			spec.BeforeDisable = append(spec.BeforeDisable, c.beforeDisableOptions(options)...)
		}
		if c.afterDisableOptions != nil {
			spec.AfterDisable = append(spec.AfterDisable, c.afterDisableOptions(options)...)
		}
	}
	return spec
}

func normalizeServiceNames(names []string, defs *ComposeDefinitions) []string {
	seen := map[string]bool{}
	out := []string{}
	for _, name := range names {
		name = strings.TrimSpace(name)
		if name == "" || seen[name] {
			continue
		}
		seen[name] = true
		out = append(out, name)
	}
	if len(out) == 0 && defs != nil {
		for name := range defs.Services {
			if strings.TrimSpace(name) == "" || seen[name] {
				continue
			}
			seen[name] = true
			out = append(out, name)
		}
	}
	sort.Strings(out)
	return out
}

func composeDefinitionRules(defs *ComposeDefinitions, op RuleOp) []YAMLRule {
	if defs == nil {
		return nil
	}
	rules := []YAMLRule{}
	appendSectionRules := func(section string, entries map[string]any) {
		names := make([]string, 0, len(entries))
		for name := range entries {
			if strings.TrimSpace(name) != "" {
				names = append(names, name)
			}
		}
		sort.Strings(names)
		for _, name := range names {
			rules = append(rules, YAMLRule{
				Files: defaultComposeRuleFiles,
				Op:    op,
				Path:  fmt.Sprintf(".%s.%s", section, name),
			})
		}
	}
	appendSectionRules("services", defs.Services)
	appendSectionRules("networks", defs.Networks)
	appendSectionRules("volumes", defs.Volumes)
	appendSectionRules("secrets", defs.Secrets)
	appendSectionRules("configs", defs.Configs)
	return rules
}

func targetServiceRules(appService string, dependencies map[string]any, environment map[string]string) ([]YAMLRule, []YAMLRule) {
	appService = strings.TrimSpace(appService)
	if appService == "" {
		return nil, nil
	}

	onRules := []YAMLRule{}
	offRules := []YAMLRule{}
	dependencyNames := make([]string, 0, len(dependencies))
	for name := range dependencies {
		if strings.TrimSpace(name) != "" {
			dependencyNames = append(dependencyNames, name)
		}
	}
	sort.Strings(dependencyNames)
	for _, name := range dependencyNames {
		value := dependencies[name]
		if value == nil {
			value = map[string]any{"condition": "service_started"}
		}
		path := fmt.Sprintf(".services.%s.depends_on.%s", appService, name)
		onRules = append(onRules, YAMLRule{
			Files: defaultComposeRuleFiles,
			Op:    OpSet,
			Path:  path,
			Value: value,
		})
		offRules = append(offRules, YAMLRule{
			Files: defaultComposeRuleFiles,
			Op:    OpDelete,
			Path:  path,
		})
	}

	envNames := make([]string, 0, len(environment))
	for name := range environment {
		if strings.TrimSpace(name) != "" {
			envNames = append(envNames, name)
		}
	}
	sort.Strings(envNames)
	for _, name := range envNames {
		path := fmt.Sprintf(".services.%s.environment.%s", appService, name)
		onRules = append(onRules, YAMLRule{
			Files: defaultComposeRuleFiles,
			Op:    OpSet,
			Path:  path,
			Value: environment[name],
		})
		offRules = append(offRules, YAMLRule{
			Files: defaultComposeRuleFiles,
			Op:    OpDelete,
			Path:  path,
		})
	}

	return onRules, offRules
}

func serviceComponentBehavior(behavior Behavior, name string) Behavior {
	if behavior.Idempotent || behavior.Enable.Summary != "" || behavior.Disable.Summary != "" {
		return behavior
	}
	return Behavior{
		Idempotent: true,
		Enable: TransitionBehavior{
			DataMigration: DataMigrationNone,
			Summary:       fmt.Sprintf("Restores the %s compose service and target app wiring.", name),
		},
		Disable: TransitionBehavior{
			DataMigration: DataMigrationBackfill,
			Summary:       fmt.Sprintf("Removes the local %s compose service and target app wiring; data may remain in Docker volumes.", name),
		},
	}
}
